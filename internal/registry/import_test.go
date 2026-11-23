package registry

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/opencontainers/go-digest"
	v1 "github.com/opencontainers/image-spec/specs-go/v1"
)

// stagedImage writes a config and a layer to dir and returns them plus an OCI manifest naming them.
func stagedImage(t *testing.T, dir, salt string) ([]ImportBlob, ImportManifest) {
	t.Helper()
	var blobs []ImportBlob
	desc := func(mt string, b []byte) v1.Descriptor {
		p := filepath.Join(dir, salt+mt[len(mt)-4:])
		if err := os.WriteFile(p, b, 0o644); err != nil {
			t.Fatal(err)
		}
		blobs = append(blobs, ImportBlob{Digest: sha(b), Size: int64(len(b)), MediaType: mt, Path: p})
		return v1.Descriptor{MediaType: mt, Digest: digestOf(sha(b)), Size: int64(len(b))}
	}
	cfg := desc(v1.MediaTypeImageConfig, testConfig("amd64"))
	layer := desc(v1.MediaTypeImageLayerGzip, []byte("layer-"+salt))
	m := v1.Manifest{MediaType: v1.MediaTypeImageManifest, Config: cfg, Layers: []v1.Descriptor{layer}}
	m.SchemaVersion = 2
	b, _ := json.Marshal(m)
	return blobs, ImportManifest{MediaType: v1.MediaTypeImageManifest, Payload: b}
}

func TestImportThenPull(t *testing.T) {
	r := newTestReg(t)
	ctx := context.Background()
	blobs, man := stagedImage(t, t.TempDir(), "a")
	dg, err := r.Reg.Import(ctx, "mirror/app", blobs, []ImportManifest{man}, []string{"latest", "latest-20261123"})
	if err != nil {
		t.Fatal(err)
	}
	if dg != sha(man.Payload) {
		t.Fatalf("digest %s, want %s", dg, sha(man.Payload))
	}
	for _, tag := range []string{"latest", "latest-20261123"} {
		if got, err := r.Reg.TagDigest(ctx, "mirror/app", tag); err != nil || got != dg {
			t.Fatalf("%s: %s %v", tag, got, err)
		}
	}
	if _, err := r.Reg.TagDigest(ctx, "mirror/app", "nope"); !errors.Is(err, ErrNoSuchTag) {
		t.Fatalf("missing tag: %v", err)
	}
	if _, err := r.Reg.TagDigest(ctx, "no/such", "latest"); !errors.Is(err, ErrNoSuchRepo) {
		t.Fatalf("missing repo: %v", err)
	}
	ts, err := r.Reg.Tags(ctx, "mirror/app")
	if err != nil || len(ts) != 2 {
		t.Fatalf("tags %+v %v", ts, err)
	}
	// importing again (blobs already present) is fine
	if _, err := r.Reg.Import(ctx, "mirror/app", blobs, []ImportManifest{man}, []string{"latest"}); err != nil {
		t.Fatal(err)
	}
}

func TestImportRefusedDuringGC(t *testing.T) {
	r := newTestReg(t)
	blobs, man := stagedImage(t, t.TempDir(), "b")
	r.Reg.Gate().Close(time.Second)
	defer r.Reg.Gate().Open()
	if _, err := r.Reg.Import(context.Background(), "mirror/app", blobs, []ImportManifest{man}, []string{"latest"}); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("%v", err)
	}
}

func TestImportMissingBlobLeavesTag(t *testing.T) {
	r := newTestReg(t)
	ctx := context.Background()
	dir := t.TempDir()
	blobs, man := stagedImage(t, dir, "old")
	old, err := r.Reg.Import(ctx, "mirror/app", blobs, []ImportManifest{man}, []string{"latest"})
	if err != nil {
		t.Fatal(err)
	}
	blobs2, man2 := stagedImage(t, dir, "new")
	if _, err := r.Reg.Import(ctx, "mirror/app", blobs2[:1], []ImportManifest{man2}, []string{"latest"}); err == nil {
		t.Fatal("import with a missing layer succeeded")
	}
	if got, _ := r.Reg.TagDigest(ctx, "mirror/app", "latest"); got != old {
		t.Fatalf("tag moved to %s", got)
	}
}

func digestOf(s string) digest.Digest { return digest.Digest(s) }
