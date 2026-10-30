package pipeline

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"dnfrepo/internal/store"
)

// chain makes dev -> test -> prod and uploads the test rpm (under several names) into dev.
func chain(t *testing.T, names ...string) (*Service, *fakePub, map[string]store.Repo) {
	s, fp := newSvc(t)
	dev := mkRepo(t, s, fp, "dev", nil)
	tst := mkRepo(t, s, fp, "test", &dev.ID)
	prod := mkRepo(t, s, fp, "prod", &tst.ID)
	for _, n := range names {
		if _, err := s.Upload(context.Background(), "dev", n, bytes.NewReader(rpmBytes(t)), "a"); err != nil {
			t.Fatal(err)
		}
	}
	fp.rebuilt = nil
	return s, fp, map[string]store.Repo{"dev": dev, "test": tst, "prod": prod}
}

func TestPromoteSetCopiesAndRebuildsOnce(t *testing.T) {
	s, fp, repos := chain(t, "foo-1.rpm", "bar-2.rpm")
	res, err := s.Promote(context.Background(), "dev", []string{"foo-1.rpm", "bar-2.rpm"}, "admin")
	if err != nil {
		t.Fatal(err)
	}
	if res.To != "test" || len(res.Promoted) != 2 {
		t.Fatalf("res=%+v", res)
	}
	if len(fp.rebuilt) != 1 || fp.rebuilt[0] != "test" {
		t.Fatalf("rebuilt=%v", fp.rebuilt)
	}
	for _, slug := range []string{"dev", "test"} {
		if got := filesIn(t, fp.RepoDir(slug)); len(got) != 2 {
			t.Fatalf("%s files=%v", slug, got)
		}
	}
	ps, _ := s.Store.ListPackages(context.Background(), repos["test"].ID)
	if len(ps) != 2 {
		t.Fatalf("db rows=%d", len(ps))
	}
	ev, _ := s.Store.ListAudit(context.Background(), repos["test"].ID, 10)
	if len(ev) != 2 || ev[0].Action != "promoted-in" {
		t.Fatalf("audit=%+v", ev)
	}
}

func TestPromoteCannotSkipStage(t *testing.T) {
	s, fp, _ := chain(t, "foo-1.rpm")
	if _, err := s.Promote(context.Background(), "dev", []string{"foo-1.rpm"}, "a"); err != nil {
		t.Fatal(err)
	}
	if got := filesIn(t, fp.RepoDir("prod")); len(got) != 0 {
		t.Fatalf("prod got files without passing through test: %v", got)
	}
}

func TestPromoteSkipsIdentical(t *testing.T) {
	s, fp, _ := chain(t, "foo-1.rpm", "bar-2.rpm")
	ctx := context.Background()
	s.Promote(ctx, "dev", []string{"foo-1.rpm"}, "a")
	fp.rebuilt = nil
	res, err := s.Promote(ctx, "dev", []string{"foo-1.rpm", "bar-2.rpm"}, "a")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Skipped) != 1 || res.Skipped[0] != "foo-1.rpm" || len(res.Promoted) != 1 || res.Promoted[0] != "bar-2.rpm" {
		t.Fatalf("res=%+v", res)
	}
}

func TestPromoteConflictChangesNothing(t *testing.T) {
	s, fp, repos := chain(t, "foo-1.rpm", "bar-2.rpm")
	ctx := context.Background()
	// target already has a different foo-1.rpm
	os.WriteFile(filepath.Join(fp.RepoDir("test"), "foo-1.rpm"), []byte("different"), 0o644)
	s.Store.AddPackage(ctx, store.Package{RepoID: repos["test"].ID, Filename: "foo-1.rpm", Name: "foo", Version: "1", Release: "1", Arch: "noarch", SHA256: "deadbeef", Size: 9})
	_, err := s.Promote(ctx, "dev", []string{"bar-2.rpm", "foo-1.rpm"}, "a")
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("got %v", err)
	}
	got := filesIn(t, fp.RepoDir("test"))
	if len(got) != 1 || got[0] != "foo-1.rpm" {
		t.Fatalf("test dir changed: %v", got)
	}
	ps, _ := s.Store.ListPackages(ctx, repos["test"].ID)
	if len(ps) != 1 {
		t.Fatalf("db rows=%d", len(ps))
	}
	if len(fp.rebuilt) != 0 {
		t.Fatal("rebuilt after failed promotion")
	}
}

func TestPromoteMissingFileNothingApplied(t *testing.T) {
	s, fp, _ := chain(t, "foo-1.rpm")
	_, err := s.Promote(context.Background(), "dev", []string{"foo-1.rpm", "ghost.rpm"}, "a")
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("got %v", err)
	}
	if got := filesIn(t, fp.RepoDir("test")); len(got) != 0 {
		t.Fatalf("applied despite missing: %v", got)
	}
}

func TestPromoteNoNextStage(t *testing.T) {
	s, _, _ := chain(t, "foo-1.rpm")
	s.Promote(context.Background(), "dev", []string{"foo-1.rpm"}, "a")
	s.Promote(context.Background(), "test", []string{"foo-1.rpm"}, "a")
	if _, err := s.Promote(context.Background(), "prod", []string{"foo-1.rpm"}, "a"); !errors.Is(err, ErrNoNextStage) {
		t.Fatalf("got %v", err)
	}
}

func TestPromoteEmptyAndBadNames(t *testing.T) {
	s, _, _ := chain(t, "foo-1.rpm")
	if _, err := s.Promote(context.Background(), "dev", nil, "a"); !errors.Is(err, ErrEmptyPromotion) {
		t.Fatalf("empty: %v", err)
	}
	if _, err := s.Promote(context.Background(), "dev", []string{"../foo-1.rpm"}, "a"); !errors.Is(err, ErrBadFilename) {
		t.Fatalf("traversal: %v", err)
	}
}

func TestPromoteSurvivesSourceDelete(t *testing.T) {
	s, fp, _ := chain(t, "foo-1.rpm")
	ctx := context.Background()
	s.Promote(ctx, "dev", []string{"foo-1.rpm"}, "a")
	if err := s.Delete(ctx, "dev", "foo-1.rpm", "a"); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(fp.RepoDir("test"), "foo-1.rpm"))
	if err != nil || !bytes.Equal(b, rpmBytes(t)) {
		t.Fatalf("promoted copy damaged by source delete: %v", err)
	}
}
