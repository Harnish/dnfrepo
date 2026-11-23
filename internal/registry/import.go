package registry

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/distribution/distribution/v3"
	_ "github.com/distribution/distribution/v3/manifest/manifestlist" // registers media types for UnmarshalManifest
	_ "github.com/distribution/distribution/v3/manifest/ocischema"
	_ "github.com/distribution/distribution/v3/manifest/schema2"
	"github.com/opencontainers/go-digest"
)

// ImportBlob is a blob staged on local disk.
type ImportBlob struct {
	Digest    string
	Size      int64
	MediaType string
	Path      string
}

// ImportManifest is a manifest to store; children must come before the index that names them.
type ImportManifest struct {
	MediaType string
	Payload   []byte
}

// Import stores blobs (skipping ones already present), then manifests in order, then points
// every tag at the last manifest. It runs under the write gate (ErrReadOnly while garbage
// collection holds it), and tags move only after every blob and manifest is stored.
func (r *Registry) Import(ctx context.Context, name string, blobs []ImportBlob, manifests []ImportManifest, tags []string) (string, error) {
	if len(manifests) == 0 {
		return "", fmt.Errorf("import %s: no manifest", name)
	}
	for _, t := range tags {
		if !ValidTag(t) {
			return "", fmt.Errorf("import %s: invalid tag %q", name, t)
		}
	}
	if !r.gate.Begin() {
		return "", ErrReadOnly
	}
	defer r.gate.End()
	rp, err := r.repository(ctx, name)
	if err != nil {
		return "", err
	}
	bs := rp.Blobs(ctx)
	for _, b := range blobs {
		if err := importBlob(ctx, bs, b); err != nil {
			return "", fmt.Errorf("import %s: blob %s: %w", name, b.Digest, err)
		}
	}
	ms, err := rp.Manifests(ctx)
	if err != nil {
		return "", err
	}
	var last distribution.Descriptor
	for _, m := range manifests {
		man, desc, err := distribution.UnmarshalManifest(m.MediaType, m.Payload)
		if err != nil {
			return "", fmt.Errorf("import %s: manifest: %w", name, err)
		}
		if _, err := ms.Put(ctx, man); err != nil {
			return "", fmt.Errorf("import %s: manifest %s: %w", name, desc.Digest, err)
		}
		last = desc
	}
	for _, t := range tags {
		if err := rp.Tags(ctx).Tag(ctx, t, last); err != nil {
			return "", fmt.Errorf("import %s: tag %s: %w", name, t, err)
		}
	}
	return last.Digest.String(), nil
}

func importBlob(ctx context.Context, bs distribution.BlobStore, b ImportBlob) error {
	dg, err := digest.Parse(b.Digest)
	if err != nil {
		return err
	}
	if _, err := bs.Stat(ctx, dg); err == nil {
		return nil
	} else if !errors.Is(err, distribution.ErrBlobUnknown) {
		return err
	}
	f, err := os.Open(b.Path)
	if err != nil {
		return err
	}
	defer f.Close()
	w, err := bs.Create(ctx)
	if err != nil {
		return err
	}
	if _, err := io.Copy(w, f); err != nil {
		w.Cancel(ctx)
		return err
	}
	_, err = w.Commit(ctx, distribution.Descriptor{Digest: dg, Size: b.Size, MediaType: b.MediaType})
	return err // Commit verifies the digest and size
}

// TagDigest is the digest a tag points at.
func (r *Registry) TagDigest(ctx context.Context, name, tag string) (string, error) {
	rp, err := r.repository(ctx, name)
	if err != nil {
		return "", err
	}
	d, err := rp.Tags(ctx).Get(ctx, tag)
	if err != nil {
		var tu distribution.ErrTagUnknown
		if errors.As(err, &tu) {
			if _, aerr := rp.Tags(ctx).All(ctx); unknownRepo(aerr) {
				return "", ErrNoSuchRepo
			}
			return "", ErrNoSuchTag
		}
		if unknownRepo(err) {
			return "", ErrNoSuchRepo
		}
		return "", err
	}
	return d.Digest.String(), nil
}

// HasBlob reports whether blob dg is already linked into image name (so an import can skip it).
func (r *Registry) HasBlob(ctx context.Context, name, dg string) bool {
	d, err := digest.Parse(dg)
	if err != nil {
		return false
	}
	rp, err := r.repository(ctx, name)
	if err != nil {
		return false
	}
	_, err = rp.Blobs(ctx).Stat(ctx, d)
	return err == nil
}
