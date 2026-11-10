package pipeline

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"dnfrepo/internal/store"
)

// stage writes the test rpm under staging/<name> and returns the StagedFile.
func stage(t *testing.T, dir, name string) StagedFile {
	t.Helper()
	b := rpmBytes(t)
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(b)
	return StagedFile{Path: p, Filename: name, SHA256: hex.EncodeToString(sum[:]), Size: int64(len(b))}
}

func mirrorRepo(t *testing.T, s *Service, fp *fakePub, slug string) store.Repo {
	t.Helper()
	r := mkRepo(t, s, fp, slug, nil)
	if err := s.Store.SetMirror(context.Background(), store.Mirror{RepoID: r.ID, UpstreamURL: "u"}); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestIngestBatchRebuildsOnce(t *testing.T) {
	s, fp := newSvc(t)
	ctx := context.Background()
	r := mirrorRepo(t, s, fp, "m")
	bid, _ := s.Store.CreateBatch(ctx, r.ID)
	st := t.TempDir()
	files := []StagedFile{stage(t, st, "a-1.rpm"), stage(t, st, "b-1.rpm"), stage(t, st, "c-1.rpm")}
	pkgs, err := s.IngestBatch(ctx, "m", bid, files, "mirror")
	if err != nil {
		t.Fatal(err)
	}
	if len(pkgs) != 3 || len(fp.rebuilt) != 1 {
		t.Fatalf("pkgs=%d rebuilds=%v", len(pkgs), fp.rebuilt)
	}
	if pkgs[0].Name != "hello" {
		t.Errorf("name not from header: %+v", pkgs[0])
	}
	rows, _ := s.Store.ListPackages(ctx, r.ID)
	if len(rows) != 3 {
		t.Fatalf("rows=%d", len(rows))
	}
	for _, p := range rows {
		if p.OriginBatchID == nil || *p.OriginBatchID != bid {
			t.Errorf("origin: %+v", p)
		}
	}
	if got := filesIn(t, fp.RepoDir("m")); len(got) != 3 {
		t.Errorf("files=%v", got)
	}
}

func TestIngestBatchRollsBackOnDuplicate(t *testing.T) {
	s, fp := newSvc(t)
	ctx := context.Background()
	r := mirrorRepo(t, s, fp, "m")
	bid, _ := s.Store.CreateBatch(ctx, r.ID)
	st := t.TempDir()
	// b-1.rpm already sits in the repo dir (no row): second file collides.
	os.WriteFile(filepath.Join(fp.RepoDir("m"), "b-1.rpm"), []byte("x"), 0o644)
	files := []StagedFile{stage(t, st, "a-1.rpm"), stage(t, st, "b-1.rpm"), stage(t, st, "c-1.rpm")}
	if _, err := s.IngestBatch(ctx, "m", bid, files, "mirror"); !errors.Is(err, ErrExists) {
		t.Fatalf("want ErrExists, got %v", err)
	}
	if got := filesIn(t, fp.RepoDir("m")); len(got) != 1 || got[0] != "b-1.rpm" {
		t.Errorf("files=%v", got)
	}
	if rows, _ := s.Store.ListPackages(ctx, r.ID); len(rows) != 0 {
		t.Errorf("rows=%v", rows)
	}
	if len(fp.rebuilt) != 0 {
		t.Errorf("rebuilt=%v", fp.rebuilt)
	}
}

func TestIngestBatchRollsBackOnDBDuplicate(t *testing.T) {
	s, fp := newSvc(t)
	ctx := context.Background()
	r := mirrorRepo(t, s, fp, "m")
	bid, _ := s.Store.CreateBatch(ctx, r.ID)
	// row exists, file does not: the DB insert fails after files were placed.
	s.Store.AddMirrorPackages(ctx, r.ID, bid, []store.Package{{RepoID: r.ID, Filename: "b-1.rpm", Name: "x", Version: "1", Release: "1", Arch: "noarch", SHA256: "x", Size: 1}}, "m")
	st := t.TempDir()
	files := []StagedFile{stage(t, st, "a-1.rpm"), stage(t, st, "b-1.rpm")}
	if _, err := s.IngestBatch(ctx, "m", bid, files, "mirror"); !errors.Is(err, store.ErrExists) {
		t.Fatalf("want store.ErrExists, got %v", err)
	}
	if got := filesIn(t, fp.RepoDir("m")); len(got) != 0 {
		t.Errorf("files=%v", got)
	}
}

func TestIngestBatchChecksumMismatchPlacesNothing(t *testing.T) {
	s, fp := newSvc(t)
	ctx := context.Background()
	r := mirrorRepo(t, s, fp, "m")
	bid, _ := s.Store.CreateBatch(ctx, r.ID)
	st := t.TempDir()
	bad := stage(t, st, "b-1.rpm")
	bad.SHA256 = "00"
	if _, err := s.IngestBatch(ctx, "m", bid, []StagedFile{stage(t, st, "a-1.rpm"), bad}, "m"); !errors.Is(err, ErrBadChecksum) {
		t.Fatalf("want ErrBadChecksum, got %v", err)
	}
	if got := filesIn(t, fp.RepoDir("m")); len(got) != 0 {
		t.Errorf("files=%v", got)
	}
}

func TestIngestBatchToLaterStageRejected(t *testing.T) {
	s, fp := newSvc(t)
	ctx := context.Background()
	a := mkRepo(t, s, fp, "a", nil)
	mkRepo(t, s, fp, "b", &a.ID)
	st := t.TempDir()
	if _, err := s.IngestBatch(ctx, "b", 1, []StagedFile{stage(t, st, "a-1.rpm")}, "m"); !errors.Is(err, ErrNotFirstStage) {
		t.Fatalf("want ErrNotFirstStage, got %v", err)
	}
	if got := filesIn(t, fp.RepoDir("b")); len(got) != 0 {
		t.Errorf("files=%v", got)
	}
}

func TestUploadRejectedOnMirrorRepo(t *testing.T) {
	s, fp := newSvc(t)
	r := mirrorRepo(t, s, fp, "m")
	_, err := s.Upload(context.Background(), "m", "x-1.rpm", bytes.NewReader(rpmBytes(t)), "a")
	if !errors.Is(err, store.ErrMirrorRepo) {
		t.Fatalf("want ErrMirrorRepo, got %v", err)
	}
	if got := filesIn(t, fp.RepoDir("m")); len(got) != 0 {
		t.Errorf("files left: %v", got)
	}
	if rows, _ := s.Store.ListPackages(context.Background(), r.ID); len(rows) != 0 {
		t.Errorf("rows=%v", rows)
	}
}

func TestPromoteBatchMovesOnlyThatBatch(t *testing.T) {
	s, fp := newSvc(t)
	ctx := context.Background()
	a := mirrorRepo(t, s, fp, "a")
	b := mkRepo(t, s, fp, "b", &a.ID)
	c := mkRepo(t, s, fp, "c", &b.ID)
	st := t.TempDir()
	b1, _ := s.Store.CreateBatch(ctx, a.ID)
	b2, _ := s.Store.CreateBatch(ctx, a.ID)
	if _, err := s.IngestBatch(ctx, "a", b1, []StagedFile{stage(t, st, "a1-1.rpm"), stage(t, st, "a2-1.rpm")}, "m"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.IngestBatch(ctx, "a", b2, []StagedFile{stage(t, st, "b1-1.rpm")}, "m"); err != nil {
		t.Fatal(err)
	}
	res, err := s.PromoteBatch(ctx, "a", b1, "admin")
	if err != nil || len(res.Promoted) != 2 {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if got := filesIn(t, fp.RepoDir("b")); len(got) != 2 {
		t.Fatalf("b files=%v", got)
	}
	rows, _ := s.Store.ListPackages(ctx, b.ID)
	for _, p := range rows {
		if p.OriginBatchID == nil || *p.OriginBatchID != b1 {
			t.Errorf("origin lost in b: %+v", p)
		}
	}
	res, err = s.PromoteBatch(ctx, "b", b1, "admin")
	if err != nil || len(res.Promoted) != 2 || res.To != "c" {
		t.Fatalf("B->C res=%+v err=%v", res, err)
	}
	rows, _ = s.Store.ListPackages(ctx, c.ID)
	if len(rows) != 2 || rows[0].OriginBatchID == nil || *rows[0].OriginBatchID != b1 {
		t.Errorf("c rows=%+v", rows)
	}
}

func TestPromoteBatchEmptyIs400Error(t *testing.T) {
	s, fp := newSvc(t)
	a := mkRepo(t, s, fp, "a", nil)
	mkRepo(t, s, fp, "b", &a.ID)
	if _, err := s.PromoteBatch(context.Background(), "a", 999, "x"); !errors.Is(err, ErrEmptyPromotion) {
		t.Fatalf("got %v", err)
	}
}

func TestRebuildTimeoutDefaultsWhenNonPositive(t *testing.T) {
	for _, tc := range []struct {
		to      time.Duration
		min, mx time.Duration
	}{{0, 9 * time.Minute, 10 * time.Minute}, {-5, 9 * time.Minute, 10 * time.Minute}, {time.Minute, 0, time.Minute}} {
		s, fp := newSvc(t)
		s.RebuildTimeout = tc.to
		cp := &deadlinePub{fakePub: fp}
		s.Publisher = cp
		if err := s.rebuild(context.Background(), "x"); err != nil {
			t.Fatal(err)
		}
		d := time.Until(cp.deadline)
		if !cp.ok || d <= tc.min || d > tc.mx {
			t.Errorf("timeout %v: remaining %v ok=%v", tc.to, d, cp.ok)
		}
	}
}

type deadlinePub struct {
	*fakePub
	deadline time.Time
	ok       bool
}

func (d *deadlinePub) Rebuild(ctx context.Context, slug string) error {
	d.deadline, d.ok = ctx.Deadline()
	return nil
}
