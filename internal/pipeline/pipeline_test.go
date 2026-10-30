package pipeline

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"dnfrepo/internal/rpmmeta"
	"dnfrepo/internal/store"
	"dnfrepo/internal/store/storetest"
)

const testRPM = "../rpmmeta/testdata/hello-1.2.3-4.noarch.rpm"

type fakePub struct {
	dir       string
	mu        sync.Mutex
	rebuilt   []string
	onRebuild func()        // runs at the start of Rebuild (e.g. cancel the caller's ctx)
	ctxErr    error         // ctx.Err() as seen by the last Rebuild
	block     chan struct{} // when set, Rebuild waits until it is closed
	entered   chan struct{} // when set, Rebuild signals here on entry
}

func (f *fakePub) RepoDir(slug string) string { return filepath.Join(f.dir, "repos", slug) }
func (f *fakePub) Rebuild(ctx context.Context, slug string) error {
	if f.onRebuild != nil {
		f.onRebuild()
	}
	if f.entered != nil {
		f.entered <- struct{}{}
	}
	if f.block != nil {
		<-f.block
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ctxErr = ctx.Err()
	f.rebuilt = append(f.rebuilt, slug)
	return nil
}

func newSvc(t *testing.T) (*Service, *fakePub) {
	st := storetest.New(t)
	fp := &fakePub{dir: t.TempDir()}
	return &Service{Store: st, Publisher: fp, MaxBytes: 1 << 20}, fp
}

func mkRepo(t *testing.T, s *Service, fp *fakePub, slug string, prev *int64) store.Repo {
	t.Helper()
	r, err := s.Store.CreateRepo(context.Background(), slug, slug, store.ModePublic, prev)
	if err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(fp.RepoDir(slug), 0o755)
	return r
}

func rpmBytes(t *testing.T) []byte {
	b, err := os.ReadFile(testRPM)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func filesIn(t *testing.T, dir string) []string {
	ents, _ := os.ReadDir(dir)
	var out []string
	for _, e := range ents {
		out = append(out, e.Name())
	}
	return out
}

func TestUploadFirstStageSucceedsAndRebuilds(t *testing.T) {
	s, fp := newSvc(t)
	mkRepo(t, s, fp, "dev", nil)
	p, err := s.Upload(context.Background(), "dev", "hello-1.2.3-4.noarch.rpm", bytes.NewReader(rpmBytes(t)), "admin@x")
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "hello" || p.Version != "1.2.3" || p.Release != "4" || p.Arch != "noarch" || p.SHA256 == "" || p.Size == 0 {
		t.Fatalf("pkg=%+v", p)
	}
	if got := filesIn(t, fp.RepoDir("dev")); len(got) != 1 || got[0] != "hello-1.2.3-4.noarch.rpm" {
		t.Fatalf("files=%v", got)
	}
	if len(fp.rebuilt) != 1 || fp.rebuilt[0] != "dev" {
		t.Fatalf("rebuilt=%v", fp.rebuilt)
	}
}

func TestUploadToLaterStageRejected(t *testing.T) {
	s, fp := newSvc(t)
	dev := mkRepo(t, s, fp, "dev", nil)
	mkRepo(t, s, fp, "test", &dev.ID)
	_, err := s.Upload(context.Background(), "test", "hello-1.2.3-4.noarch.rpm", bytes.NewReader(rpmBytes(t)), "a")
	if !errors.Is(err, ErrNotFirstStage) {
		t.Fatalf("got %v", err)
	}
	if got := filesIn(t, fp.RepoDir("test")); len(got) != 0 {
		t.Fatalf("files left: %v", got)
	}
}

func TestUploadNonRPMRejectedNoFileLeft(t *testing.T) {
	s, fp := newSvc(t)
	mkRepo(t, s, fp, "dev", nil)
	_, err := s.Upload(context.Background(), "dev", "evil.rpm", strings.NewReader("#!/bin/sh\necho hi\n"), "a")
	if !errors.Is(err, rpmmeta.ErrNotRPM) {
		t.Fatalf("got %v", err)
	}
	if got := filesIn(t, fp.RepoDir("dev")); len(got) != 0 {
		t.Fatalf("files left: %v", got)
	}
	if len(fp.rebuilt) != 0 {
		t.Fatal("rebuilt after failed upload")
	}
}

func TestUploadTraversalFilenameRejected(t *testing.T) {
	s, fp := newSvc(t)
	mkRepo(t, s, fp, "dev", nil)
	for _, name := range []string{"../x.rpm", "/etc/x.rpm", "a/b.rpm", ".hidden.rpm", "x.txt", "", "a b.rpm", `..\x.rpm`} {
		_, err := s.Upload(context.Background(), "dev", name, bytes.NewReader(rpmBytes(t)), "a")
		if !errors.Is(err, ErrBadFilename) {
			t.Errorf("%q: got %v", name, err)
		}
	}
	if got := filesIn(t, fp.RepoDir("dev")); len(got) != 0 {
		t.Fatalf("files left: %v", got)
	}
	if _, err := os.Stat(filepath.Join(fp.dir, "repos", "x.rpm")); err == nil {
		t.Fatal("escaped repo dir")
	}
}

func TestUploadDuplicateFilename(t *testing.T) {
	s, fp := newSvc(t)
	mkRepo(t, s, fp, "dev", nil)
	ctx := context.Background()
	if _, err := s.Upload(ctx, "dev", "hello-1.2.3-4.noarch.rpm", bytes.NewReader(rpmBytes(t)), "a"); err != nil {
		t.Fatal(err)
	}
	_, err := s.Upload(ctx, "dev", "hello-1.2.3-4.noarch.rpm", bytes.NewReader(rpmBytes(t)), "a")
	if !errors.Is(err, ErrExists) {
		t.Fatalf("got %v", err)
	}
	if got := filesIn(t, fp.RepoDir("dev")); len(got) != 1 {
		t.Fatalf("files=%v", got)
	}
}

func TestUploadTooLarge(t *testing.T) {
	s, fp := newSvc(t)
	s.MaxBytes = 100
	mkRepo(t, s, fp, "dev", nil)
	_, err := s.Upload(context.Background(), "dev", "hello-1.2.3-4.noarch.rpm", bytes.NewReader(rpmBytes(t)), "a")
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("got %v", err)
	}
	if got := filesIn(t, fp.RepoDir("dev")); len(got) != 0 {
		t.Fatalf("files left: %v", got)
	}
}

func TestDeleteRemovesFileAndRebuilds(t *testing.T) {
	s, fp := newSvc(t)
	mkRepo(t, s, fp, "dev", nil)
	ctx := context.Background()
	s.Upload(ctx, "dev", "hello-1.2.3-4.noarch.rpm", bytes.NewReader(rpmBytes(t)), "a")
	if err := s.Delete(ctx, "dev", "hello-1.2.3-4.noarch.rpm", "a"); err != nil {
		t.Fatal(err)
	}
	if got := filesIn(t, fp.RepoDir("dev")); len(got) != 0 {
		t.Fatalf("files=%v", got)
	}
	r, _ := s.Store.RepoBySlug(ctx, "dev")
	if ps, _ := s.Store.ListPackages(ctx, r.ID); len(ps) != 0 {
		t.Fatalf("db rows=%v", ps)
	}
	if len(fp.rebuilt) != 2 {
		t.Fatalf("rebuilt=%v", fp.rebuilt)
	}
	if err := s.Delete(ctx, "dev", "hello-1.2.3-4.noarch.rpm", "a"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("second delete: %v", err)
	}
}

func TestDeleteTraversalRejected(t *testing.T) {
	s, fp := newSvc(t)
	mkRepo(t, s, fp, "dev", nil)
	if err := s.Delete(context.Background(), "dev", "../../x.rpm", "a"); !errors.Is(err, ErrBadFilename) {
		t.Fatalf("got %v", err)
	}
}
