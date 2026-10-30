package pipeline

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestOpsSerializedPerRepo(t *testing.T) {
	s, fp := newSvc(t)
	mkRepo(t, s, fp, "dev", nil)
	ctx := context.Background()
	fp.block, fp.entered = make(chan struct{}), make(chan struct{}, 4)
	upDone := make(chan error, 1)
	go func() { _, err := s.Upload(ctx, "dev", "a.rpm", bytes.NewReader(rpmBytes(t)), "x"); upDone <- err }()
	<-fp.entered // upload is inside Rebuild, still holding the repo's lock
	delDone := make(chan error, 1)
	go func() { delDone <- s.Delete(ctx, "dev", "a.rpm", "x") }()
	time.Sleep(200 * time.Millisecond)
	repo, _ := s.Store.RepoBySlug(ctx, "dev")
	if ps, _ := s.Store.ListPackages(ctx, repo.ID); len(ps) != 1 {
		t.Fatalf("delete ran while upload held the repo lock: %d rows", len(ps))
	}
	close(fp.block)
	if err := <-upDone; err != nil {
		t.Fatal(err)
	}
	if err := <-delDone; err != nil {
		t.Fatal(err)
	}
	if ps, _ := s.Store.ListPackages(ctx, repo.ID); len(ps) != 0 {
		t.Fatalf("rows left: %d", len(ps))
	}
}

func TestPromoteHoldsTargetLock(t *testing.T) {
	s, fp, repos := chain(t, "foo-1.rpm")
	ctx := context.Background()
	fp.block, fp.entered = make(chan struct{}), make(chan struct{}, 4)
	prDone := make(chan error, 1)
	go func() { _, err := s.Promote(ctx, "dev", []string{"foo-1.rpm"}, "x"); prDone <- err }()
	<-fp.entered
	delDone := make(chan error, 1)
	go func() { delDone <- s.Delete(ctx, "test", "foo-1.rpm", "x") }()
	time.Sleep(200 * time.Millisecond)
	if ps, _ := s.Store.ListPackages(ctx, repos["test"].ID); len(ps) != 1 {
		t.Fatalf("delete in target ran during promotion: %d rows", len(ps))
	}
	close(fp.block)
	if err := <-prDone; err != nil {
		t.Fatal(err)
	}
	if err := <-delDone; err != nil {
		t.Fatal(err)
	}
}

func TestRebuildSurvivesCancelledRequest(t *testing.T) {
	s, fp := newSvc(t)
	mkRepo(t, s, fp, "dev", nil)
	ctx, cancel := context.WithCancel(context.Background())
	fp.onRebuild = cancel // client disconnects right before the rebuild
	if _, err := s.Upload(ctx, "dev", "a.rpm", bytes.NewReader(rpmBytes(t)), "x"); err != nil {
		t.Fatal(err)
	}
	if fp.ctxErr != nil {
		t.Fatalf("rebuild ran on the request context: %v", fp.ctxErr)
	}
}

func TestQuarantineOrphans(t *testing.T) {
	s, fp := newSvc(t)
	mkRepo(t, s, fp, "dev", nil)
	ctx := context.Background()
	if _, err := s.Upload(ctx, "dev", "tracked-1.rpm", bytes.NewReader(rpmBytes(t)), "x"); err != nil {
		t.Fatal(err)
	}
	dir := fp.RepoDir("dev")
	os.WriteFile(filepath.Join(dir, "stray-1.rpm"), []byte("left by a crash"), 0o644)
	os.WriteFile(filepath.Join(dir, ".upload-123"), []byte("partial"), 0o600)
	os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("keep"), 0o644)
	moved, err := s.QuarantineOrphans(ctx, "dev")
	if err != nil {
		t.Fatal(err)
	}
	if len(moved) != 1 || moved[0] != "stray-1.rpm" {
		t.Fatalf("moved=%v", moved)
	}
	got := filesIn(t, dir)
	if len(got) != 2 { // tracked-1.rpm + notes.txt
		t.Fatalf("dir=%v", got)
	}
	if _, err := os.Stat(filepath.Join(fp.dir, "orphans", "dev", "stray-1.rpm")); err != nil {
		t.Fatalf("not quarantined: %v", err)
	}
}
