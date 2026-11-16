package registry

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/distribution/reference"
	digest "github.com/opencontainers/go-digest"
)

func layerExists(t *testing.T, r *testReg, repo, content string) bool {
	t.Helper()
	ctx := context.Background()
	named, _ := reference.WithName(repo)
	rp, _ := r.Reg.Namespace().Repository(ctx, named)
	_, err := rp.Blobs(ctx).Stat(ctx, digest.Digest(sha([]byte(content))))
	return err == nil
}

func TestGCRemovesUntaggedBlobs(t *testing.T) {
	r := newTestReg(t)
	pushImage(t, r.Srv.URL, "app", "v1", "ci", "ptok")
	pushImage(t, r.Srv.URL, "other", "v1", "ci", "ptok")
	ctx := context.Background()
	if err := r.Reg.DeleteTag(ctx, "app", "v1"); err != nil {
		t.Fatal(err)
	}
	res, err := r.Reg.GC(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res.Removed == 0 || res.BytesFreed == 0 {
		t.Fatalf("%+v", res)
	}
	if layerExists(t, r, "app", "layer-appv1amd64") {
		t.Fatal("untagged image's layer survived GC")
	}
	if resp, _ := do(t, "GET", r.Srv.URL+"/v2/other/manifests/v1", "", "", nil, ociAccept); resp.StatusCode != 200 {
		t.Fatalf("tagged image damaged: %d", resp.StatusCode)
	}
	if resp, _ := do(t, "GET", r.Srv.URL+"/v2/other/blobs/"+sha([]byte("layer-otherv1amd64")), "", "", nil, nil); resp.StatusCode != 200 {
		t.Fatalf("tagged blob gone: %d", resp.StatusCode)
	}
	if st := r.Reg.GCStatus(); st.State != GCDone || st.Result.Removed != res.Removed {
		t.Fatalf("%+v", st)
	}
}

func TestGCPausesWritesDuringRun(t *testing.T) {
	r := newTestReg(t)
	pushImage(t, r.Srv.URL, "app", "v1", "ci", "ptok")
	var during int
	gcAfterClose = func() {
		_, during = pushImage(t, r.Srv.URL, "app", "v2", "ci", "ptok")
	}
	t.Cleanup(func() { gcAfterClose = nil })
	if _, err := r.Reg.GC(context.Background()); err != nil {
		t.Fatal(err)
	}
	if during != 503 {
		t.Fatalf("write during GC: %d", during)
	}
	if _, st := pushImage(t, r.Srv.URL, "app", "v2", "ci", "ptok"); st != 0 {
		t.Fatalf("write after GC: %d", st)
	}
}

func TestGCAbortsWhenWriteInFlight(t *testing.T) {
	r := newTestReg(t)
	pushImage(t, r.Srv.URL, "app", "v1", "ci", "ptok")
	r.Reg.DeleteTag(context.Background(), "app", "v1")
	old := gcDrainTimeout
	gcDrainTimeout = 100 * time.Millisecond
	t.Cleanup(func() { gcDrainTimeout = old })
	g := r.Reg.Gate()
	g.Begin() // an upload is running
	_, err := r.Reg.GC(context.Background())
	if !errors.Is(err, ErrDrainTimeout) {
		t.Fatalf("got %v", err)
	}
	g.End()
	if !layerExists(t, r, "app", "layer-appv1amd64") {
		t.Fatal("aborted GC deleted data")
	}
	if !g.Begin() {
		t.Fatal("gate left closed after abort")
	}
	g.End()
	if st := r.Reg.GCStatus(); st.State != GCAborted || st.Err == "" {
		t.Fatalf("%+v", st)
	}
}

func TestGCSingleFlight(t *testing.T) {
	r := newTestReg(t)
	started, release := make(chan struct{}), make(chan struct{})
	gcAfterClose = func() { close(started); <-release }
	t.Cleanup(func() { gcAfterClose = nil })
	done := make(chan error)
	go func() { _, err := r.Reg.GC(context.Background()); done <- err }()
	<-started
	if _, err := r.Reg.GC(context.Background()); !errors.Is(err, ErrGCRunning) {
		t.Fatalf("second GC: %v", err)
	}
	if st := r.Reg.GCStatus(); st.State != GCRunning {
		t.Fatalf("%+v", st)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
