package store_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"dnfrepo/internal/store"
	"dnfrepo/internal/store/storetest"
)

func bpkg(repo int64, fn string, batch *int64) store.Package {
	return store.Package{RepoID: repo, Filename: fn, Name: "a", Version: "1", Release: "1", Arch: "x86_64", SHA256: "x", Size: 1, OriginBatchID: batch}
}

func TestBatchLifecycle(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	r := mustRepo(t, s, "m", nil)
	id, err := s.CreateBatch(ctx, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	bs, _ := s.ListBatches(ctx, r.ID)
	if len(bs) != 1 || bs[0].Status != "running" || bs[0].FinishedAt != nil {
		t.Fatalf("running batch: %+v", bs)
	}
	if err := s.FinishBatch(ctx, id, "ok", 3, "boom"); err != nil {
		t.Fatal(err)
	}
	id2, _ := s.CreateBatch(ctx, r.ID)
	bs, err = s.ListBatches(ctx, r.ID)
	if err != nil || len(bs) != 2 || bs[0].ID != id2 || bs[1].ID != id {
		t.Fatalf("want newest first: %+v %v", bs, err)
	}
	b := bs[1]
	if b.Status != "ok" || b.AddedCount != 3 || b.Error != "boom" || b.FinishedAt == nil || b.RepoID != r.ID {
		t.Errorf("finished batch: %+v", b)
	}
}

func TestAddMirrorPackagesSetsOriginAndAudits(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	r := mustRepo(t, s, "m", nil)
	if err := s.SetMirror(ctx, store.Mirror{RepoID: r.ID, UpstreamURL: "https://u/"}); err != nil {
		t.Fatal(err)
	}
	id, _ := s.CreateBatch(ctx, r.ID)
	if err := s.AddMirrorPackages(ctx, r.ID, id, []store.Package{bpkg(r.ID, "a-1-1.x86_64.rpm", nil), bpkg(r.ID, "b-1-1.x86_64.rpm", nil)}, "mirror"); err != nil {
		t.Fatal(err)
	}
	p, err := s.PackageByFilename(ctx, r.ID, "a-1-1.x86_64.rpm")
	if err != nil || p.OriginBatchID == nil || *p.OriginBatchID != id {
		t.Fatalf("origin: %+v %v", p, err)
	}
	ev, _ := s.ListAudit(ctx, r.ID, 10)
	if len(ev) != 2 || ev[0].Action != "synced" || ev[0].Actor != "mirror" {
		t.Errorf("audit: %+v", ev)
	}
	if err := s.AddMirrorPackages(ctx, r.ID, id, []store.Package{bpkg(r.ID, "a-1-1.x86_64.rpm", nil)}, "mirror"); !errors.Is(err, store.ErrExists) {
		t.Errorf("dup: want ErrExists, got %v", err)
	}
	later := mustRepo(t, s, "later", &r.ID)
	if err := s.AddMirrorPackages(ctx, later.ID, id, []store.Package{bpkg(later.ID, "c-1-1.x86_64.rpm", nil)}, "mirror"); !errors.Is(err, store.ErrLaterStage) {
		t.Errorf("later stage: want ErrLaterStage, got %v", err)
	}
}

func TestOriginBatchSurvivesPromotionInsert(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	r := mustRepo(t, s, "m", nil)
	next := mustRepo(t, s, "next", &r.ID)
	id, _ := s.CreateBatch(ctx, r.ID)
	if err := s.AddPackagesAudited(ctx, next.ID, []store.Package{bpkg(next.ID, "a-1-1.x86_64.rpm", &id)}, "u", "promoted", []string{"d"}); err != nil {
		t.Fatal(err)
	}
	p, err := s.PackageByFilename(ctx, next.ID, "a-1-1.x86_64.rpm")
	if err != nil || p.OriginBatchID == nil || *p.OriginBatchID != id {
		t.Fatalf("origin lost: %+v %v", p, err)
	}
}

func TestBatchFilenamesScopedToRepo(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	r := mustRepo(t, s, "m", nil)
	next := mustRepo(t, s, "next", &r.ID)
	id, _ := s.CreateBatch(ctx, r.ID)
	if err := s.AddMirrorPackages(ctx, r.ID, id, []store.Package{bpkg(r.ID, "b-1-1.x86_64.rpm", nil), bpkg(r.ID, "a-1-1.x86_64.rpm", nil)}, "mirror"); err != nil {
		t.Fatal(err)
	}
	if err := s.AddPackagesAudited(ctx, next.ID, []store.Package{bpkg(next.ID, "a-1-1.x86_64.rpm", &id)}, "u", "promoted", []string{"d"}); err != nil {
		t.Fatal(err)
	}
	got, err := s.BatchFilenames(ctx, r.ID, id)
	if err != nil || !reflect.DeepEqual(got, []string{"a-1-1.x86_64.rpm", "b-1-1.x86_64.rpm"}) {
		t.Errorf("repo: %v %v", got, err)
	}
	got, _ = s.BatchFilenames(ctx, next.ID, id)
	if !reflect.DeepEqual(got, []string{"a-1-1.x86_64.rpm"}) {
		t.Errorf("next: %v", got)
	}
}
