package store_test

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"dnfrepo/internal/store"
	"dnfrepo/internal/store/storetest"
)

func cacheMirror(t *testing.T, s *store.Store, slug string) store.Repo {
	t.Helper()
	r := mustRepo(t, s, slug, nil)
	if err := s.SetMirror(context.Background(), store.Mirror{RepoID: r.ID, UpstreamURL: "https://u/", Mode: store.ModeCache}); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestMirrorModeRoundTrip(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	c := cacheMirror(t, s, "c")
	m, err := s.MirrorByRepo(ctx, c.ID)
	if err != nil || m.Mode != store.ModeCache {
		t.Fatalf("cache mode: %q %v", m.Mode, err)
	}
	r := mustRepo(t, s, "d", nil)
	if err := s.SetMirror(ctx, store.Mirror{RepoID: r.ID, UpstreamURL: "https://u/"}); err != nil {
		t.Fatal(err)
	}
	if m, _ := s.MirrorByRepo(ctx, r.ID); m.Mode != store.ModeSync {
		t.Fatalf("default mode %q", m.Mode)
	}
}

func TestMirrorModeChangeRefusedWithPackages(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	c := cacheMirror(t, s, "c")
	sync := store.Mirror{RepoID: c.ID, UpstreamURL: "https://u/", Mode: store.ModeSync}
	if err := s.SetMirror(ctx, sync); err != nil {
		t.Fatalf("empty repo mode change: %v", err)
	}
	if err := s.SetMirror(ctx, store.Mirror{RepoID: c.ID, UpstreamURL: "https://u/", Mode: store.ModeCache}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddMirrorPackages(ctx, c.ID, 0, []store.Package{bpkg(c.ID, "a-1-1.x86_64.rpm", nil)}, "t"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetMirror(ctx, sync); !errors.Is(err, store.ErrModeChange) {
		t.Fatalf("want ErrModeChange, got %v", err)
	}
	// same mode with packages is still a normal update
	if err := s.SetMirror(ctx, store.Mirror{RepoID: c.ID, UpstreamURL: "https://v/", Mode: store.ModeCache}); err != nil {
		t.Fatal(err)
	}
}

func TestReplaceCacheEntries(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	c := cacheMirror(t, s, "c")
	e := func(p string) store.CacheEntry {
		return store.CacheEntry{Path: p, Href: "h/" + p, Filename: p, Name: "n", Arch: "x86_64", Version: "1", Release: "2",
			SHA256: "s", Size: 7, Component: "main", Control: "Package: n", MD5: "m", SHA1: "h"}
	}
	if err := s.ReplaceCacheEntries(ctx, c.ID, []store.CacheEntry{e("a"), e("b")}); err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceCacheEntries(ctx, c.ID, []store.CacheEntry{e("b"), e("c")}); err != nil {
		t.Fatal(err)
	}
	all, err := s.ListCacheEntries(ctx, c.ID)
	if err != nil || len(all) != 2 || all[0].Path != "b" || all[1].Path != "c" {
		t.Fatalf("list: %+v %v", all, err)
	}
	if _, err := s.CacheEntryByPath(ctx, c.ID, "a"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a: %v", err)
	}
	got, err := s.CacheEntryByPath(ctx, c.ID, "c")
	if err != nil || !reflect.DeepEqual(got, e("c")) {
		t.Fatalf("c: %+v %v", got, err)
	}
}

func TestUncachedPackages(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	c := cacheMirror(t, s, "c")
	if err := s.AddMirrorPackages(ctx, c.ID, 0, []store.Package{bpkg(c.ID, "x.rpm", nil), bpkg(c.ID, "y.rpm", nil)}, "t"); err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceCacheEntries(ctx, c.ID, []store.CacheEntry{{Path: "P/y.rpm", Href: "P/y.rpm", Filename: "y.rpm"}}); err != nil {
		t.Fatal(err)
	}
	got, err := s.UncachedPackages(ctx, c.ID)
	if err != nil || !reflect.DeepEqual(got, []string{"x.rpm"}) {
		t.Fatalf("%v %v", got, err)
	}
}

func TestTouchPackage(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	c := cacheMirror(t, s, "c")
	if err := s.AddMirrorPackages(ctx, c.ID, 0, []store.Package{bpkg(c.ID, "x.rpm", nil)}, "t"); err != nil {
		t.Fatal(err)
	}
	p0, _ := s.PackageByFilename(ctx, c.ID, "x.rpm")
	if p0.LastPulled.IsZero() {
		t.Fatal("last_pulled not set on insert")
	}
	time.Sleep(5 * time.Millisecond)
	if err := s.TouchPackage(ctx, c.ID, "x.rpm"); err != nil {
		t.Fatal(err)
	}
	p1, _ := s.PackageByFilename(ctx, c.ID, "x.rpm")
	if !p1.LastPulled.After(p0.LastPulled) {
		t.Fatalf("not advanced: %v -> %v", p0.LastPulled, p1.LastPulled)
	}
}

func TestAddMirrorPackagesBatchZero(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	c := cacheMirror(t, s, "c")
	if err := s.AddMirrorPackages(ctx, c.ID, 0, []store.Package{bpkg(c.ID, "x.rpm", nil)}, "t"); err != nil {
		t.Fatal(err)
	}
	p, _ := s.PackageByFilename(ctx, c.ID, "x.rpm")
	if p.OriginBatchID != nil {
		t.Fatalf("batch %v", *p.OriginBatchID)
	}
	ev, _ := s.ListAudit(ctx, c.ID, 10)
	if len(ev) != 1 || ev[0].Action != "cached" {
		t.Fatalf("audit %+v", ev)
	}
}

func TestIsCacheRepo(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	plain := mustRepo(t, s, "p", nil)
	syncR := mustRepo(t, s, "s", nil)
	if err := s.SetMirror(ctx, store.Mirror{RepoID: syncR.ID, UpstreamURL: "https://u/"}); err != nil {
		t.Fatal(err)
	}
	c := cacheMirror(t, s, "c")
	for id, want := range map[int64]bool{plain.ID: false, syncR.ID: false, c.ID: true} {
		if got, err := s.IsCacheRepo(ctx, id); err != nil || got != want {
			t.Errorf("repo %d: %v %v", id, got, err)
		}
	}
}
