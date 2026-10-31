package pipeline

import (
	"bytes"
	"context"
	"os"
	"sort"
	"testing"

	"dnfrepo/internal/store"
)

func rpmFile(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("../rpmmeta/testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func up(t *testing.T, s *Service, slug, file string) {
	t.Helper()
	if _, err := s.Upload(context.Background(), slug, file, bytes.NewReader(rpmFile(t, file)), "a"); err != nil {
		t.Fatalf("upload %s: %v", file, err)
	}
}

func setKeep(t *testing.T, s *Service, slug string, keep int, ov map[string]int) {
	t.Helper()
	r, _ := s.Store.RepoBySlug(context.Background(), slug)
	if err := s.Store.SetRepoKeep(context.Background(), r.ID, keep, ov); err != nil {
		t.Fatal(err)
	}
}

func sortedFiles(t *testing.T, dir string) []string {
	got := filesIn(t, dir)
	sort.Strings(got)
	return got
}

func same(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

const (
	h123  = "hello-1.2.3-4.noarch.rpm"
	h19   = "hello-1.9.0-1.noarch.rpm"
	h110  = "hello-1.10.0-1.noarch.rpm"
	hx64  = "hello-1.0.0-1.x86_64.rpm"
	other = "other-2.0-1.noarch.rpm"
)

func TestKeepPurgesOldestOnUpload(t *testing.T) {
	s, fp := newSvc(t)
	mkRepo(t, s, fp, "dev", nil)
	setKeep(t, s, "dev", 2, nil)
	up(t, s, "dev", h123)
	up(t, s, "dev", h19)
	up(t, s, "dev", h110) // 1.10.0 is newer than 1.9.0 (numeric order); 1.2.3 is purged
	if got := sortedFiles(t, fp.RepoDir("dev")); !same(got, []string{h110, h19}) {
		t.Fatalf("files=%v", got)
	}
	ctx := context.Background()
	repo, _ := s.Store.RepoBySlug(ctx, "dev")
	if ps, _ := s.Store.ListPackages(ctx, repo.ID); len(ps) != 2 {
		t.Fatalf("rows=%d", len(ps))
	}
	ev, _ := s.Store.ListAudit(ctx, repo.ID, 20)
	purged := 0
	for _, e := range ev {
		if e.Action == "purged" && e.Detail == h123 {
			purged++
		}
	}
	if purged != 1 {
		t.Fatalf("audit=%+v", ev)
	}
}

func TestKeepIsPerNameAndArch(t *testing.T) {
	s, fp := newSvc(t)
	mkRepo(t, s, fp, "dev", nil)
	setKeep(t, s, "dev", 1, nil)
	up(t, s, "dev", h19)
	up(t, s, "dev", hx64) // same name, different arch: its own group
	up(t, s, "dev", other)
	if got := sortedFiles(t, fp.RepoDir("dev")); !same(got, []string{hx64, h19, other}) {
		t.Fatalf("files=%v", got)
	}
}

func TestKeepOverrideBeatsDefault(t *testing.T) {
	s, fp := newSvc(t)
	mkRepo(t, s, fp, "dev", nil)
	setKeep(t, s, "dev", 1, map[string]int{"hello": 2})
	up(t, s, "dev", h123)
	up(t, s, "dev", h19)
	up(t, s, "dev", h110)
	if got := sortedFiles(t, fp.RepoDir("dev")); !same(got, []string{h110, h19}) {
		t.Fatalf("override 2: files=%v", got)
	}
	// override 0 means keep everything of that package even though the default is 1
	setKeep(t, s, "dev", 1, map[string]int{"hello": 0})
	up(t, s, "dev", h123)
	if got := sortedFiles(t, fp.RepoDir("dev")); len(got) != 3 {
		t.Fatalf("override 0: files=%v", got)
	}
}

func TestKeepZeroKeepsAll(t *testing.T) {
	s, fp := newSvc(t)
	mkRepo(t, s, fp, "dev", nil)
	up(t, s, "dev", h123)
	up(t, s, "dev", h19)
	up(t, s, "dev", h110)
	if got := filesIn(t, fp.RepoDir("dev")); len(got) != 3 {
		t.Fatalf("files=%v", got)
	}
}

func TestKeepAppliesPerStageOnPromotion(t *testing.T) {
	s, fp := newSvc(t)
	dev := mkRepo(t, s, fp, "dev", nil)
	mkRepo(t, s, fp, "test", &dev.ID)
	setKeep(t, s, "test", 1, nil) // dev keeps everything, test keeps the newest
	up(t, s, "dev", h123)
	up(t, s, "dev", h19)
	up(t, s, "dev", h110)
	fp.rebuilt = nil
	res, err := s.Promote(context.Background(), "dev", []string{h123, h19, h110}, "a")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Promoted) != 3 {
		t.Fatalf("res=%+v", res)
	}
	if got := sortedFiles(t, fp.RepoDir("test")); !same(got, []string{h110}) {
		t.Fatalf("test files=%v", got)
	}
	if got := filesIn(t, fp.RepoDir("dev")); len(got) != 3 {
		t.Fatalf("dev files=%v", got)
	}
	if len(fp.rebuilt) != 1 {
		t.Fatalf("rebuilt=%v, want exactly one rebuild of test", fp.rebuilt)
	}
}

func TestApplyKeepOnDemand(t *testing.T) {
	s, fp := newSvc(t)
	mkRepo(t, s, fp, "dev", nil)
	up(t, s, "dev", h123)
	up(t, s, "dev", h19)
	up(t, s, "dev", h110)
	setKeep(t, s, "dev", 1, nil)
	fp.rebuilt = nil
	removed, err := s.ApplyKeep(context.Background(), "dev", "admin")
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(removed)
	if !same(removed, []string{h123, h19}) {
		t.Fatalf("removed=%v", removed)
	}
	if got := sortedFiles(t, fp.RepoDir("dev")); !same(got, []string{h110}) {
		t.Fatalf("files=%v", got)
	}
	if len(fp.rebuilt) != 1 {
		t.Fatalf("rebuilt=%v", fp.rebuilt)
	}
	// nothing left to purge: no rebuild
	fp.rebuilt = nil
	removed, _ = s.ApplyKeep(context.Background(), "dev", "admin")
	if len(removed) != 0 || len(fp.rebuilt) != 0 {
		t.Fatalf("removed=%v rebuilt=%v", removed, fp.rebuilt)
	}
}

var _ = store.ErrNotFound
