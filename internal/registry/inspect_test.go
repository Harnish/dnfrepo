package registry

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/distribution/reference"
	digest "github.com/opencontainers/go-digest"
)

func layerLen(repo, tag, arch string) int { return len("layer-" + repo + tag + arch) }

func TestTagsSingleImage(t *testing.T) {
	r := newTestReg(t)
	dg, size, _ := pushArch(t, r.Srv.URL, "app", "v1", "amd64", "ci", "ptok")
	tags, err := r.Reg.Tags(context.Background(), "app")
	if err != nil || len(tags) != 1 {
		t.Fatalf("%v %v", tags, err)
	}
	g := tags[0]
	wantSize := int64(size + len(testConfig("amd64")) + layerLen("app", "v1", "amd64"))
	if g.Name != "v1" || g.Digest != dg || g.Size != wantSize {
		t.Fatalf("%+v want size %d", g, wantSize)
	}
	if g.Created == nil || !g.Created.Equal(time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)) {
		t.Fatalf("created %v", g.Created)
	}
	if len(g.Arches) != 1 || g.Arches[0] != "linux/amd64" {
		t.Fatalf("arches %v", g.Arches)
	}
	if len(g.Layers) != 1 || g.Layers[0].Size != int64(layerLen("app", "v1", "amd64")) {
		t.Fatalf("layers %+v", g.Layers)
	}
}

func TestTagsImageIndexListsArches(t *testing.T) {
	r := newTestReg(t)
	a, asz, _ := pushArch(t, r.Srv.URL, "multi", "amd", "amd64", "ci", "ptok")
	b, bsz, _ := pushArch(t, r.Srv.URL, "multi", "arm", "arm64", "ci", "ptok")
	idx := []byte(fmt.Sprintf(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.index.v1+json","manifests":[`+
		`{"mediaType":"application/vnd.oci.image.manifest.v1+json","digest":%q,"size":%d,"platform":{"architecture":"amd64","os":"linux"}},`+
		`{"mediaType":"application/vnd.oci.image.manifest.v1+json","digest":%q,"size":%d,"platform":{"architecture":"arm64","os":"linux"}}]}`, a, asz, b, bsz))
	resp, _ := do(t, "PUT", r.Srv.URL+"/v2/multi/manifests/latest", "ci", "ptok", idx,
		map[string]string{"Content-Type": "application/vnd.oci.image.index.v1+json"})
	if resp.StatusCode != 201 {
		t.Fatalf("index push %d", resp.StatusCode)
	}
	tags, err := r.Reg.Tags(context.Background(), "multi")
	if err != nil || len(tags) != 3 {
		t.Fatalf("%v %v", tags, err)
	}
	var got Tag
	for _, g := range tags {
		if g.Name == "latest" {
			got = g
		}
	}
	if len(got.Arches) != 2 || got.Arches[0] != "linux/amd64" || got.Arches[1] != "linux/arm64" {
		t.Fatalf("arches %v", got.Arches)
	}
	want := int64(len(idx) + asz + len(testConfig("amd64")) + layerLen("multi", "amd", "amd64"))
	if got.Size != want || len(got.Layers) != 1 {
		t.Fatalf("size %d want %d layers %v", got.Size, want, got.Layers)
	}
}

func TestDeleteTagLeavesBlobsAndOtherTags(t *testing.T) {
	r := newTestReg(t)
	pushImage(t, r.Srv.URL, "app", "v1", "ci", "ptok")
	pushImage(t, r.Srv.URL, "app", "v2", "ci", "ptok")
	ctx := context.Background()
	if err := r.Reg.DeleteTag(ctx, "app", "v1"); err != nil {
		t.Fatal(err)
	}
	tags, _ := r.Reg.Tags(ctx, "app")
	if len(tags) != 1 || tags[0].Name != "v2" {
		t.Fatalf("%+v", tags)
	}
	named, _ := reference.WithName("app")
	repo, _ := r.Reg.Namespace().Repository(ctx, named)
	if _, err := repo.Blobs(ctx).Stat(ctx, digest.Digest(sha([]byte("layer-appv1amd64")))); err != nil {
		t.Fatalf("blob removed with tag: %v", err)
	}
}

func TestDeleteMissingTagAndRepo(t *testing.T) {
	r := newTestReg(t)
	pushImage(t, r.Srv.URL, "app", "v1", "ci", "ptok")
	ctx := context.Background()
	if err := r.Reg.DeleteTag(ctx, "app", "nope"); !errors.Is(err, ErrNoSuchTag) {
		t.Fatalf("%v", err)
	}
	if _, err := r.Reg.Tags(ctx, "ghost"); !errors.Is(err, ErrNoSuchRepo) {
		t.Fatalf("%v", err)
	}
	if err := r.Reg.DeleteTag(ctx, "ghost", "v1"); !errors.Is(err, ErrNoSuchRepo) {
		t.Fatalf("%v", err)
	}
}

func TestReposSorted(t *testing.T) {
	r := newTestReg(t)
	for _, n := range []string{"b", "a/x", "a"} {
		pushImage(t, r.Srv.URL, n, "v1", "ci", "ptok")
	}
	got, err := r.Reg.Repos(context.Background())
	if err != nil || !eq(got, []string{"a", "a/x", "b"}) {
		t.Fatalf("%v %v", got, err)
	}
}
