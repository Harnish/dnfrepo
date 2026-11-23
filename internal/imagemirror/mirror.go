package imagemirror

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"dnfrepo/internal/registry"
	"dnfrepo/internal/secretbox"
	"dnfrepo/internal/store"
)

const (
	StatusUpToDate   = "up to date"
	StatusDownloaded = "downloaded"
)

var ErrRunning = errors.New("already running")

// Mirrors runs image mirrors: check the upstream tag, stage what changed on disk, then import it
// into the embedded registry under its write gate.
type Mirrors struct {
	Store      *store.Store
	Reg        *registry.Registry
	Box        *secretbox.Box // nil: mirrors with stored credentials cannot run
	HTTP       *http.Client
	StagingDir string
	Scheme     string           // "" = https
	Now        func() time.Time // nil = time.Now

	RunTimeout time.Duration // bound for one run (0 = 1 h)

	mu           sync.Mutex
	running      map[int64]bool
	beforeImport func() // test hook
}

func (m *Mirrors) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}

func (m *Mirrors) claim(id int64) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.running == nil {
		m.running = map[int64]bool{}
	}
	if m.running[id] {
		return false
	}
	m.running[id] = true
	return true
}

func (m *Mirrors) release(id int64) { m.mu.Lock(); delete(m.running, id); m.mu.Unlock() }

// Trigger starts a run that outlives ctx; ErrRunning if one is already going.
func (m *Mirrors) Trigger(ctx context.Context, id int64) error {
	if !m.claim(id) {
		return ErrRunning
	}
	go func() {
		defer m.release(id)
		m.runClaimed(context.WithoutCancel(ctx), id) // outcome is recorded on the mirror row
	}()
	return nil
}

// RunOne runs one mirror and records the outcome on it.
func (m *Mirrors) RunOne(ctx context.Context, id int64) error {
	if !m.claim(id) {
		return ErrRunning
	}
	defer m.release(id)
	return m.runClaimed(ctx, id)
}

func (m *Mirrors) runClaimed(ctx context.Context, id int64) error {
	t := m.RunTimeout
	if t <= 0 {
		t = time.Hour
	}
	ctx, cancel := context.WithTimeout(ctx, t)
	defer cancel()
	for attempt := 0; ; attempt++ {
		mr, err := m.Store.ImageMirrorByID(ctx, id)
		if err != nil {
			return err
		}
		status, err := m.run(ctx, mr)
		msg := ""
		if err != nil {
			status, msg = "error", err.Error()
		}
		if rerr := m.Store.RecordImageMirrorRun(context.WithoutCancel(ctx), id, status, msg); rerr != nil && err == nil {
			err = rerr
		}
		// Platforms edited while this run was copying: its copy used the old set. Force another.
		now, gerr := m.Store.ImageMirrorByID(ctx, id)
		if err != nil || gerr != nil || samePlatforms(now.Platforms, mr.Platforms) || attempt >= 2 {
			return err
		}
		if err := m.Store.UpdateImageMirror(ctx, id, now.Platforms, now.Keep, nil, nil, true); err != nil {
			return err
		}
	}
}

func samePlatforms(a, b []string) bool {
	a, b = slices.Clone(a), slices.Clone(b)
	slices.Sort(a)
	slices.Sort(b)
	return slices.Equal(slices.Compact(a), slices.Compact(b))
}

func (m *Mirrors) client(username string, secret []byte) (*Client, error) {
	c := &Client{HTTP: m.HTTP, Scheme: m.Scheme, Username: username}
	if secret != nil {
		if m.Box == nil {
			return nil, errors.New("SECRET_KEY is not set: stored upstream credentials cannot be opened")
		}
		tok, err := m.Box.Open(secret)
		if err != nil {
			return nil, errors.New("stored upstream credentials cannot be opened (SECRET_KEY changed?)")
		}
		c.Token = tok
	}
	return c, nil
}

// entry is one element of an index's "manifests" array.
type entry struct {
	MediaType   string            `json:"mediaType"`
	Digest      string            `json:"digest"`
	Platform    *platformJSON     `json:"platform"`
	Annotations map[string]string `json:"annotations"`
}

type platformJSON struct {
	OS           string `json:"os"`
	Architecture string `json:"architecture"`
	Variant      string `json:"variant"`
}

func (e entry) platform() string {
	if e.Platform == nil {
		return ""
	}
	s := e.Platform.OS + "/" + e.Platform.Architecture
	if e.Platform.Variant != "" {
		s += "/" + e.Platform.Variant
	}
	return s
}

func (e entry) attestation() bool {
	return e.Annotations["vnd.docker.reference.type"] != "" || e.platform() == "unknown/unknown"
}

func isIndex(mt string) bool { return mt == mtOCIIndex || mt == mtDockerList }

func parseIndex(b []byte) ([]entry, error) {
	var idx struct{ Manifests []entry }
	if err := json.Unmarshal(b, &idx); err != nil {
		return nil, fmt.Errorf("bad index: %w", err)
	}
	return idx.Manifests, nil
}

// platforms lists an index's image platforms in order, attestations excluded.
func platforms(es []entry) []string {
	out := []string{}
	for _, e := range es {
		if p := e.platform(); p != "" && !e.attestation() && !slices.Contains(out, p) {
			out = append(out, p)
		}
	}
	return out
}

// Inspect reports the upstream tag's digest and platforms ([] for a single-platform image).
func (m *Mirrors) Inspect(ctx context.Context, upstream, username, token string) (string, []string, error) {
	ref, err := ParseRef(upstream)
	if err != nil {
		return "", nil, err
	}
	return m.inspect(ctx, ref, &Client{HTTP: m.HTTP, Scheme: m.Scheme, Username: username, Token: token})
}

// InspectMirror is Inspect with a stored mirror's upstream and credentials.
func (m *Mirrors) InspectMirror(ctx context.Context, id int64) (string, []string, error) {
	mr, err := m.Store.ImageMirrorByID(ctx, id)
	if err != nil {
		return "", nil, err
	}
	ref, err := ParseRef(mr.Upstream + ":" + mr.Tag)
	if err != nil {
		return "", nil, err
	}
	c, err := m.client(mr.Username, mr.Secret)
	if err != nil {
		return "", nil, err
	}
	return m.inspect(ctx, ref, c)
}

func (m *Mirrors) inspect(ctx context.Context, ref Ref, c *Client) (string, []string, error) {
	dg, err := c.Head(ctx, ref)
	if err != nil {
		return "", nil, err
	}
	mt, b, err := c.Manifest(ctx, ref, dg)
	if err != nil {
		return "", nil, err
	}
	if !isIndex(mt) {
		return dg, []string{}, nil
	}
	es, err := parseIndex(b)
	if err != nil {
		return "", nil, err
	}
	return dg, platforms(es), nil
}

// image manifest fields we need to find its blobs.
type imageJSON struct {
	Config struct {
		MediaType string `json:"mediaType"`
		Digest    string `json:"digest"`
		Size      int64  `json:"size"`
	} `json:"config"`
	Layers []struct {
		MediaType string `json:"mediaType"`
		Digest    string `json:"digest"`
		Size      int64  `json:"size"`
	} `json:"layers"`
}

func (m *Mirrors) run(ctx context.Context, mr store.ImageMirror) (string, error) {
	ref, err := ParseRef(mr.Upstream + ":" + mr.Tag)
	if err != nil {
		return "", err
	}
	c, err := m.client(mr.Username, mr.Secret)
	if err != nil {
		return "", err
	}
	up, err := c.Head(ctx, ref)
	if err != nil {
		return "", err
	}
	if up == mr.UpstreamDigest {
		if vs, err := m.Store.ImageVersions(ctx, mr.ID); err == nil && len(vs) > 0 {
			if local, err := m.Reg.TagDigest(ctx, mr.LocalName, mr.Tag); err == nil && local == vs[0].Digest {
				return StatusUpToDate, nil
			}
		}
	}
	if err := os.MkdirAll(m.StagingDir, 0o755); err != nil {
		return "", err
	}
	dir, err := os.MkdirTemp(m.StagingDir, "run-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)

	mt, payload, err := c.Manifest(ctx, ref, up)
	if err != nil {
		return "", err
	}
	var children []registry.ImportManifest
	top := registry.ImportManifest{MediaType: mt, Payload: payload}
	images := []registry.ImportManifest{top}
	if isIndex(mt) {
		es, err := parseIndex(payload)
		if err != nil {
			return "", err
		}
		keep, all, err := selectEntries(es, mr.Platforms)
		if err != nil {
			return "", err
		}
		if !all {
			if top.Payload, err = subsetIndex(payload, keep); err != nil {
				return "", err
			}
		}
		for _, i := range keep {
			cmt, cb, err := c.Manifest(ctx, ref, es[i].Digest)
			if err != nil {
				return "", err
			}
			if isIndex(cmt) {
				return "", fmt.Errorf("nested index %s is not supported", es[i].Digest)
			}
			children = append(children, registry.ImportManifest{MediaType: cmt, Payload: cb})
		}
		images = children
	}
	blobs, err := m.stage(ctx, c, ref, mr.LocalName, dir, images)
	if err != nil {
		return "", err
	}
	dated, err := m.datedTag(ctx, mr.LocalName, mr.Tag)
	if err != nil {
		return "", err
	}
	mans := append(children, top)
	if !isIndex(mt) {
		mans = []registry.ImportManifest{top}
	}
	// Settings row first: a name without one is public while it exists, so a private upstream's
	// image must never be in storage without its users-mode row.
	mode := store.ModePublic
	if mr.Secret != nil {
		mode = store.ModeUsers // private upstream: nobody can pull until an admin opens it
	}
	if err := m.Store.EnsureRegistryRepoMode(ctx, mr.LocalName, mode); err != nil {
		return "", err
	}
	if m.beforeImport != nil {
		m.beforeImport()
	}
	dg, err := m.Reg.Import(ctx, mr.LocalName, blobs, mans, []string{mr.Tag, dated})
	if errors.Is(err, registry.ErrReadOnly) {
		return "", errors.New("registry busy (garbage collection); retried next run")
	}
	if err != nil {
		return "", err
	}
	if err := m.Store.AddImageVersion(ctx, store.ImageVersion{MirrorID: mr.ID, Tag: dated, Digest: dg}, up); err != nil {
		return "", err
	}
	if err := m.prune(ctx, mr); err != nil {
		return "", err
	}
	return StatusDownloaded, nil
}

// selectEntries picks the index entries to copy. No selection, or every upstream platform
// selected, keeps everything (attestations included) so the index can be stored unchanged.
func selectEntries(es []entry, sel []string) (keep []int, all bool, err error) {
	have := platforms(es)
	var missing []string
	for _, p := range sel {
		if !slices.Contains(have, p) {
			missing = append(missing, p)
		}
	}
	if len(missing) > 0 {
		return nil, false, fmt.Errorf("selected platform(s) not published upstream: %s (upstream has: %s)",
			strings.Join(missing, ", "), strings.Join(have, ", "))
	}
	all = len(sel) == 0 || len(sel) >= len(have)
	for i, e := range es {
		if all || (!e.attestation() && slices.Contains(sel, e.platform())) {
			keep = append(keep, i)
		}
	}
	return keep, all, nil
}

// subsetIndex rewrites an index keeping only the given entries (other fields untouched).
func subsetIndex(payload []byte, keep []int) ([]byte, error) {
	var idx map[string]json.RawMessage
	if err := json.Unmarshal(payload, &idx); err != nil {
		return nil, err
	}
	var es []json.RawMessage
	if err := json.Unmarshal(idx["manifests"], &es); err != nil {
		return nil, err
	}
	out := make([]json.RawMessage, 0, len(keep))
	for _, i := range keep {
		out = append(out, es[i])
	}
	b, err := json.Marshal(out)
	if err != nil {
		return nil, err
	}
	idx["manifests"] = b
	return json.Marshal(idx)
}

// stage downloads every blob the image manifests name that the local image lacks.
func (m *Mirrors) stage(ctx context.Context, c *Client, ref Ref, local, dir string, images []registry.ImportManifest) ([]registry.ImportBlob, error) {
	var out []registry.ImportBlob
	seen := map[string]bool{}
	add := func(mt, dg string, size int64) error {
		if seen[dg] || m.Reg.HasBlob(ctx, local, dg) {
			return nil
		}
		seen[dg] = true
		p := filepath.Join(dir, strings.ReplaceAll(dg, ":", "-"))
		n, err := c.Blob(ctx, ref, dg, p, size)
		if err != nil {
			return err
		}
		if size > 0 && n != size {
			return fmt.Errorf("blob %s: size %d, manifest says %d", dg, n, size)
		}
		out = append(out, registry.ImportBlob{Digest: dg, Size: n, MediaType: mt, Path: p})
		return nil
	}
	for _, im := range images {
		var j imageJSON
		if err := json.Unmarshal(im.Payload, &j); err != nil {
			return nil, fmt.Errorf("bad image manifest: %w", err)
		}
		if err := add(j.Config.MediaType, j.Config.Digest, j.Config.Size); err != nil {
			return nil, err
		}
		for _, l := range j.Layers {
			if err := add(l.MediaType, l.Digest, l.Size); err != nil {
				return nil, err
			}
		}
	}
	return out, nil
}

// datedTag is the first free <tag>-YYYYMMDD[-n] (UTC) in the local image.
func (m *Mirrors) datedTag(ctx context.Context, local, tag string) (string, error) {
	names, err := m.Reg.TagNames(ctx, local)
	if err != nil && !errors.Is(err, registry.ErrNoSuchRepo) {
		return "", err
	}
	base := tag + "-" + m.now().UTC().Format("20060102")
	for n := 1; ; n++ {
		t := base
		if n > 1 {
			t += "-" + strconv.Itoa(n)
		}
		if !slices.Contains(names, t) {
			return t, nil
		}
	}
}

// prune drops this mirror's dated tags beyond Keep, oldest first. Only ledger tags are touched.
func (m *Mirrors) prune(ctx context.Context, mr store.ImageMirror) error {
	vs, err := m.Store.ImageVersions(ctx, mr.ID)
	if err != nil {
		return err
	}
	for i := mr.Keep; i < len(vs); i++ {
		// A dated tag someone pushed over since is theirs now: drop it from the ledger, keep the tag.
		if cur, err := m.Reg.TagDigest(ctx, mr.LocalName, vs[i].Tag); err == nil && cur == vs[i].Digest {
			if err := m.Reg.DeleteTag(ctx, mr.LocalName, vs[i].Tag); err != nil && !errors.Is(err, registry.ErrNoSuchTag) {
				return err
			}
		}
		if err := m.Store.DeleteImageVersion(ctx, mr.ID, vs[i].Tag); err != nil {
			return err
		}
	}
	return nil
}
