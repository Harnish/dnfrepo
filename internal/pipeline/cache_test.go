package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
	"time"

	"dnfrepo/internal/store"
)

func cacheRepo(t *testing.T, s *Service, fp *fakePub, slug string, keepLatest int) store.Repo {
	t.Helper()
	r := mkRepo(t, s, fp, slug, nil)
	if err := s.Store.SetMirror(context.Background(), store.Mirror{RepoID: r.ID, UpstreamURL: "u", Mode: store.ModeCache, KeepLatest: keepLatest}); err != nil {
		t.Fatal(err)
	}
	return r
}

// fill ingests one testdata rpm the way a cache miss does (batch 0).
func fill(t *testing.T, s *Service, slug, name string) {
	t.Helper()
	b := rpmFile(t, name)
	p := filepath.Join(t.TempDir(), name)
	os.WriteFile(p, b, 0o600)
	sum := sha256.Sum256(b)
	if _, err := s.IngestBatch(context.Background(), slug, 0, []StagedFile{{Path: p, Filename: name, SHA256: hex.EncodeToString(sum[:]), Size: int64(len(b))}}, "cache"); err != nil {
		t.Fatalf("fill %s: %v", name, err)
	}
	time.Sleep(2 * time.Millisecond) // distinct last_pulled
}

func TestCachePurgeByLastPulled(t *testing.T) {
	s, fp := newSvc(t)
	ctx := context.Background()
	r := cacheRepo(t, s, fp, "c", 0)
	fill(t, s, "c", h123)
	fill(t, s, "c", h19)
	fill(t, s, "c", h110)
	if err := s.Store.TouchPackage(ctx, r.ID, h123); err != nil {
		t.Fatal(err)
	}
	setKeep(t, s, "c", 2, nil)
	removed, err := s.ApplyKeep(ctx, "c", "admin")
	if err != nil {
		t.Fatal(err)
	}
	if !same(removed, []string{h19}) {
		t.Fatalf("removed %v", removed)
	}
	if got := sortedFiles(t, fp.RepoDir("c")); !same(got, []string{h110, h123}) {
		t.Fatalf("files %v", got)
	}
}

func TestCachePurgeUsesMirrorKeepLatest(t *testing.T) {
	s, fp := newSvc(t)
	ctx := context.Background()
	r := cacheRepo(t, s, fp, "c", 1)
	fill(t, s, "c", h110)
	fill(t, s, "c", h19) // older version, pulled later: it stays
	if got := sortedFiles(t, fp.RepoDir("c")); !same(got, []string{h19}) {
		t.Fatalf("files %v", got)
	}
	ev, _ := s.Store.ListAudit(ctx, r.ID, 10)
	if len(ev) == 0 || ev[0].Action != "purged" || ev[0].Detail != h110 {
		t.Fatalf("audit %+v", ev)
	}
}

func TestRefreshCachePurgesDroppedFromUpstream(t *testing.T) {
	s, fp := newSvc(t)
	ctx := context.Background()
	r := cacheRepo(t, s, fp, "c", 0)
	fill(t, s, "c", h123)
	fill(t, s, "c", h19)
	src := t.TempDir()
	kept, _ := s.Store.PackageByFilename(ctx, r.ID, h19)
	removed, err := s.RefreshCache(ctx, "c", []store.CacheEntry{{Path: "P/" + h19, Href: "P/" + h19, Filename: h19, SHA256: kept.SHA256}}, src, "mirror")
	if err != nil {
		t.Fatal(err)
	}
	if !same(removed, []string{h123}) {
		t.Fatalf("removed %v", removed)
	}
	if got := sortedFiles(t, fp.RepoDir("c")); !same(got, []string{h19}) {
		t.Fatalf("files %v", got)
	}
	if len(fp.swapped) != 1 || fp.swapped[0] != src {
		t.Fatalf("swaps %v", fp.swapped)
	}
	if es, _ := s.Store.ListCacheEntries(ctx, r.ID); len(es) != 1 {
		t.Fatalf("entries %v", es)
	}
}
