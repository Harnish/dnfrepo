package mirror

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sync/errgroup"

	"dnfrepo/internal/pipeline"
	"dnfrepo/internal/rpmmeta"
	"dnfrepo/internal/secretbox"
	"dnfrepo/internal/store"
)

// Syncer runs mirror syncs: stage and verify every wanted upstream rpm outside
// the repo, then publish them as one batch through pipeline.IngestBatch.
type Syncer struct {
	Store       *store.Store
	Pipeline    *pipeline.Service
	Client      *http.Client
	Box         *secretbox.Box // nil allowed when no mirror uses credentials
	StagingDir  string
	Concurrency int           // parallel downloads; default 4
	MaxRPMBytes int64         // per-rpm cap; default 4 GiB
	Timeout     time.Duration // applied by the caller; default 6h
}

// Result.Status is "synced" or "up to date".
type Result struct {
	Status  string
	BatchID int64
	Added   int
}

const (
	defaultMaxRPM      = 4 << 30
	defaultConcurrency = 4
	bookkeepingTimeout = 30 * time.Second
)

// scrubErr keeps the wrapped error for errors.Is while its text has the token masked.
type scrubErr struct {
	msg string
	err error
}

func (e *scrubErr) Error() string { return e.msg }
func (e *scrubErr) Unwrap() error { return e.err }

func scrub(s, token string) string {
	if token == "" {
		return s
	}
	return strings.ReplaceAll(s, token, "***")
}

// Run syncs one mirror that the caller has already claimed (state running). On
// every return path it finishes the mirror row, and the batch when one was
// created, even if ctx is cancelled. Any failure before ingest leaves the repo
// unchanged and keeps verified staged files for the retry.
func (s *Syncer) Run(ctx context.Context, repoID int64, actor string) (Result, error) {
	var (
		token     string
		batchID   int64
		committed bool
	)
	res, err := s.run(ctx, repoID, actor, &token, &batchID, &committed)

	bctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), bookkeepingTimeout)
	defer cancel()
	status, msg := res.Status, ""
	if err != nil {
		err = &scrubErr{msg: scrub(err.Error(), token), err: err}
		msg, status = err.Error(), "error"
		if committed {
			msg = "published but post-processing failed: " + msg
		}
		log.Printf("mirror: repo %d: %s", repoID, msg)
	}
	if batchID != 0 {
		bstatus, bmsg := "synced", ""
		if !committed {
			bstatus, bmsg = "error", msg
		}
		if ferr := s.Store.FinishBatch(bctx, batchID, bstatus, res.Added, bmsg); ferr != nil && err == nil {
			err = ferr
		}
	}
	if ferr := s.Store.FinishMirror(bctx, repoID, status, msg, nil); ferr != nil && err == nil {
		err = ferr
	}
	if err != nil && !committed {
		return Result{}, err
	}
	return res, err
}

func (s *Syncer) run(ctx context.Context, repoID int64, actor string, token *string, batchID *int64, committed *bool) (Result, error) {
	m, err := s.Store.MirrorByRepo(ctx, repoID)
	if err != nil {
		return Result{}, err
	}
	repo, err := s.Store.RepoByID(ctx, repoID)
	if err != nil {
		return Result{}, err
	}
	if len(m.TokenEnc) > 0 {
		if s.Box == nil {
			return Result{}, errors.New("mirror has stored credentials but no secret key is configured")
		}
		if *token, err = s.Box.Open(m.TokenEnc); err != nil {
			return Result{}, errors.New("cannot decrypt stored mirror credentials")
		}
	}
	maxRPM := s.MaxRPMBytes
	if maxRPM <= 0 {
		maxRPM = defaultMaxRPM
	}
	up := &Upstream{Base: m.UpstreamURL, Client: s.Client, Username: m.Username, Token: *token,
		GPGKey: m.GPGKey, RequireSig: m.RequireSig, MaxRPMBytes: maxRPM}

	all, err := up.Packages(ctx)
	if err != nil {
		return Result{}, err
	}
	all, err = dedupe(all)
	if err != nil {
		return Result{}, err
	}
	keep, overrides, err := s.Store.RepoKeep(ctx, repoID)
	if err != nil {
		return Result{}, err
	}
	have, err := s.Store.ListPackages(ctx, repoID)
	if err != nil {
		return Result{}, err
	}
	want, err := Plan(all, have, Filter{Arches: m.Arches, Include: m.Include, Exclude: m.Exclude, KeepLatest: m.KeepLatest}, keep, overrides)
	if err != nil {
		return Result{}, err
	}
	if len(want) == 0 {
		return Result{Status: "up to date"}, nil
	}

	if s.StagingDir == "" {
		return Result{}, errors.New("mirror staging dir is not configured")
	}
	dir := filepath.Join(s.StagingDir, strconv.FormatInt(repoID, 10))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return Result{}, fmt.Errorf("staging dir: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return Result{}, fmt.Errorf("staging dir: %w", err)
	}
	files, err := s.stageAll(ctx, up, dir, want)
	if err != nil {
		return Result{}, err
	}
	if err := checkHeaders(files, want); err != nil {
		return Result{}, err
	}

	if *batchID, err = s.Store.CreateBatch(ctx, repoID); err != nil {
		return Result{}, err
	}
	pkgs, err := s.Pipeline.IngestBatch(ctx, repo.Slug, *batchID, files, actor)
	if pkgs == nil && err != nil {
		return Result{}, err // nothing published; staged files kept for the retry
	}
	*committed = true
	for _, f := range files {
		os.Remove(f.Path) // the published copy is a separate link (or copy)
	}
	return Result{Status: "synced", BatchID: *batchID, Added: len(pkgs)}, err
}

// dedupe drops repeated filenames with the same content; differing content fails.
func dedupe(in []UpstreamPkg) ([]UpstreamPkg, error) {
	seen := map[string]UpstreamPkg{}
	out := in[:0:0]
	for _, p := range in {
		if q, ok := seen[p.Filename]; ok {
			if q.SHA256 != p.SHA256 || q.Size != p.Size {
				return nil, fmt.Errorf("upstream lists %s twice with different checksums", p.Filename)
			}
			continue
		}
		seen[p.Filename] = p
		out = append(out, p)
	}
	return out, nil
}

var shaRe = regexp.MustCompile(`^[0-9a-f]{64}$`)

// stageAll downloads want into dir with at most Concurrency in flight. A
// failure does not cancel sibling downloads, so whatever verifies is kept for
// the retry; ctx cancellation stops them all.
func (s *Syncer) stageAll(ctx context.Context, up *Upstream, dir string, want []UpstreamPkg) ([]pipeline.StagedFile, error) {
	n := s.Concurrency
	if n <= 0 {
		n = defaultConcurrency
	}
	for _, p := range want { // the checksum names the staged file: never let it leave dir
		if !shaRe.MatchString(p.SHA256) || filepath.Dir(filepath.Join(dir, p.SHA256)) != dir {
			return nil, fmt.Errorf("package %q: invalid sha256", p.Filename)
		}
	}
	var g errgroup.Group
	g.SetLimit(n)
	started := map[string]bool{} // one download per sha256, even under two filenames
	files := make([]pipeline.StagedFile, len(want))
	for i, p := range want {
		path := filepath.Join(dir, p.SHA256)
		files[i] = pipeline.StagedFile{Path: path, Filename: p.Filename, SHA256: p.SHA256, Size: p.Size}
		if started[p.SHA256] {
			continue
		}
		started[p.SHA256] = true
		g.Go(func() error { return stage(ctx, up, p, path) })
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	return files, nil
}

// stage makes path a verified copy of p. An existing file is re-hashed before
// it is trusted. A new body goes to path.part and is renamed into place only
// after Download verified it; path itself is never opened for writing, since
// after ingest it may share an inode with the published rpm.
func stage(ctx context.Context, up *Upstream, p UpstreamPkg, path string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if ok, err := verified(path, p); err != nil {
		return err
	} else if ok {
		return nil
	}
	part := path + ".part"
	f, err := os.OpenFile(part, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("stage %s: %w", p.Filename, err)
	}
	err = up.Download(ctx, p, f)
	if cerr := f.Close(); err == nil && cerr != nil {
		err = fmt.Errorf("stage %s: %w", p.Filename, cerr)
	}
	if err == nil {
		if err = os.Rename(part, path); err != nil {
			err = fmt.Errorf("stage %s: %w", p.Filename, err)
		}
	}
	if err != nil {
		os.Remove(part)
	}
	return err
}

// verified reports whether path holds exactly p's bytes; a mismatching file is removed.
func verified(path string, p UpstreamPkg) (bool, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("stage %s: %w", p.Filename, err)
	}
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, p.Size+1))
	f.Close()
	if err == nil && n == p.Size && hex.EncodeToString(h.Sum(nil)) == p.SHA256 {
		return true, nil
	}
	if err := os.Remove(path); err != nil {
		return false, fmt.Errorf("stage %s: %w", p.Filename, err)
	}
	return false, nil
}

// checkHeaders fails unless every staged rpm's header NEVRA matches its upstream entry.
func checkHeaders(files []pipeline.StagedFile, want []UpstreamPkg) error {
	for i, f := range files {
		m, err := rpmmeta.Parse(f.Path)
		if err != nil {
			return fmt.Errorf("%s: %w", f.Filename, err)
		}
		p := want[i]
		if m.Name != p.Name || m.Arch != p.Arch || m.Version != p.Version || m.Release != p.Release {
			return fmt.Errorf("%s: rpm header says %s-%s-%s.%s, upstream metadata says %s-%s-%s.%s", f.Filename,
				m.Name, m.Version, m.Release, m.Arch, p.Name, p.Version, p.Release, p.Arch)
		}
	}
	return nil
}
