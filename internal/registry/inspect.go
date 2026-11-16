package registry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/distribution/distribution/v3"
	"github.com/distribution/distribution/v3/registry/storage/driver"
	"github.com/distribution/reference"
	digest "github.com/opencontainers/go-digest"
	v1 "github.com/opencontainers/image-spec/specs-go/v1"
)

var (
	ErrNoSuchRepo = errors.New("no such repository")
	ErrNoSuchTag  = errors.New("no such tag")
)

// Layer is one layer of an image.
type Layer struct {
	Digest string
	Size   int64
}

// Tag describes one tagged manifest. For an image index, Size, Layers and Created come from
// the first platform's image and Arches lists every platform.
type Tag struct {
	Name    string
	Digest  string
	Size    int64
	Created *time.Time
	Arches  []string
	Layers  []Layer
}

// Repos lists every image repository in storage, sorted. A registry nothing was ever pushed
// to has none.
func (r *Registry) Repos(ctx context.Context) ([]string, error) {
	en, ok := r.ns.(distribution.RepositoryEnumerator)
	if !ok {
		return nil, fmt.Errorf("registry namespace cannot enumerate")
	}
	var names []string
	err := en.Enumerate(ctx, func(n string) error { names = append(names, n); return nil })
	var nf driver.PathNotFoundError
	if err != nil && !errors.As(err, &nf) {
		return nil, err
	}
	sort.Strings(names)
	return names, nil
}

func (r *Registry) repository(ctx context.Context, name string) (distribution.Repository, error) {
	named, err := reference.WithName(name)
	if err != nil {
		return nil, ErrNoSuchRepo
	}
	return r.ns.Repository(ctx, named)
}

func unknownRepo(err error) bool {
	var e distribution.ErrRepositoryUnknown
	return errors.As(err, &e)
}

// Tags returns every tag of repo with its metadata, sorted by name.
func (r *Registry) Tags(ctx context.Context, repo string) ([]Tag, error) {
	rp, err := r.repository(ctx, repo)
	if err != nil {
		return nil, err
	}
	names, err := rp.Tags(ctx).All(ctx)
	if err != nil {
		if unknownRepo(err) {
			return nil, ErrNoSuchRepo
		}
		return nil, err
	}
	ms, err := rp.Manifests(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Tag, 0, len(names))
	for _, n := range names {
		desc, err := rp.Tags(ctx).Get(ctx, n)
		if err != nil {
			return nil, err
		}
		t := Tag{Name: n, Digest: desc.Digest.String()}
		if err := r.fill(ctx, rp, ms, desc.Digest, &t); err != nil {
			return nil, fmt.Errorf("%s:%s: %w", repo, n, err)
		}
		out = append(out, t)
	}
	return out, nil
}

func platform(p *v1.Platform) string {
	if p == nil {
		return ""
	}
	s := p.OS + "/" + p.Architecture
	if p.Variant != "" {
		s += "/" + p.Variant
	}
	return s
}

// fill reads manifest dg and completes t.
func (r *Registry) fill(ctx context.Context, rp distribution.Repository, ms distribution.ManifestService, dg digest.Digest, t *Tag) error {
	man, err := ms.Get(ctx, dg)
	if err != nil {
		return err
	}
	_, payload, err := man.Payload()
	if err != nil {
		return err
	}
	var probe struct {
		Manifests []v1.Descriptor `json:"manifests"`
	}
	if err := json.Unmarshal(payload, &probe); err != nil {
		return err
	}
	if len(probe.Manifests) == 0 { // a plain image manifest
		return r.fillImage(ctx, rp, payload, t, true)
	}
	t.Size = int64(len(payload))
	first := true
	for _, m := range probe.Manifests {
		p := platform(m.Platform)
		if p == "" || strings.HasPrefix(p, "unknown/") { // attestations and the like
			continue
		}
		t.Arches = append(t.Arches, p)
		if !first {
			continue
		}
		first = false
		child, err := ms.Get(ctx, m.Digest)
		if err != nil {
			return err
		}
		_, cp, err := child.Payload()
		if err != nil {
			return err
		}
		if err := r.fillImage(ctx, rp, cp, t, false); err != nil {
			return err
		}
	}
	return nil
}

// fillImage adds an image manifest's size, layers, created time and (when asArches) platform.
func (r *Registry) fillImage(ctx context.Context, rp distribution.Repository, payload []byte, t *Tag, asArches bool) error {
	var m v1.Manifest
	if err := json.Unmarshal(payload, &m); err != nil {
		return err
	}
	t.Size += int64(len(payload)) + m.Config.Size
	for _, l := range m.Layers {
		t.Size += l.Size
		t.Layers = append(t.Layers, Layer{Digest: l.Digest.String(), Size: l.Size})
	}
	b, err := rp.Blobs(ctx).Get(ctx, m.Config.Digest)
	if err != nil {
		return nil // config blob missing: size and layers stand, created/arch unknown
	}
	var cfg struct {
		Created      *time.Time `json:"created"`
		Architecture string     `json:"architecture"`
		OS           string     `json:"os"`
		Variant      string     `json:"variant"`
	}
	if json.Unmarshal(b, &cfg) == nil {
		t.Created = cfg.Created
		if asArches && cfg.Architecture != "" {
			t.Arches = []string{platform(&v1.Platform{OS: cfg.OS, Architecture: cfg.Architecture, Variant: cfg.Variant})}
		}
	}
	return nil
}

// DeleteTag removes a tag only; the manifest and blobs stay until garbage collection.
func (r *Registry) DeleteTag(ctx context.Context, repo, tag string) error {
	rp, err := r.repository(ctx, repo)
	if err != nil {
		return err
	}
	if _, err := rp.Tags(ctx).All(ctx); err != nil {
		if unknownRepo(err) {
			return ErrNoSuchRepo
		}
		return err
	}
	if _, err := rp.Tags(ctx).Get(ctx, tag); err != nil {
		var e distribution.ErrTagUnknown
		if errors.As(err, &e) {
			return ErrNoSuchTag
		}
		return err
	}
	return rp.Tags(ctx).Untag(ctx, tag)
}

// TagNames lists a repo's tag names without reading any manifest.
func (r *Registry) TagNames(ctx context.Context, repo string) ([]string, error) {
	rp, err := r.repository(ctx, repo)
	if err != nil {
		return nil, err
	}
	names, err := rp.Tags(ctx).All(ctx)
	if unknownRepo(err) {
		return nil, ErrNoSuchRepo
	}
	return names, err
}
