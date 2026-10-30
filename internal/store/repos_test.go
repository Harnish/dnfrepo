package store_test

import (
	"context"
	"errors"
	"net/netip"
	"testing"

	"dnfrepo/internal/store"
	"dnfrepo/internal/store/storetest"
)

func mustRepo(t *testing.T, s *store.Store, slug string, prev *int64) store.Repo {
	t.Helper()
	r, err := s.CreateRepo(context.Background(), slug, slug, store.ModePublic, prev)
	if err != nil {
		t.Fatalf("create %s: %v", slug, err)
	}
	return r
}

func TestCreateRepoRejectsBadSlug(t *testing.T) {
	s := storetest.New(t)
	for _, slug := range []string{"", "Dev", "a/b", "../x", "-x", "a b", "x.y"} {
		if _, err := s.CreateRepo(context.Background(), slug, "n", store.ModePublic, nil); !errors.Is(err, store.ErrBadSlug) {
			t.Errorf("slug %q: want ErrBadSlug, got %v", slug, err)
		}
	}
}

func TestPreviousMustBeUnlinked(t *testing.T) {
	s := storetest.New(t)
	dev := mustRepo(t, s, "dev", nil)
	mustRepo(t, s, "test", &dev.ID)
	if _, err := s.CreateRepo(context.Background(), "other", "o", store.ModePublic, &dev.ID); !errors.Is(err, store.ErrChain) {
		t.Fatalf("want ErrChain, got %v", err)
	}
}

func TestNoCycles(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	a := mustRepo(t, s, "a", nil)
	b := mustRepo(t, s, "b", &a.ID)
	c := mustRepo(t, s, "c", &b.ID)
	if err := s.SetPrevious(ctx, a.ID, &c.ID); !errors.Is(err, store.ErrChain) {
		t.Fatalf("a<-c should be a cycle, got %v", err)
	}
	if err := s.SetPrevious(ctx, a.ID, &a.ID); !errors.Is(err, store.ErrChain) {
		t.Fatalf("self link should fail, got %v", err)
	}
}

func TestDeleteMiddleOfChainRejected(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	a := mustRepo(t, s, "a", nil)
	b := mustRepo(t, s, "b", &a.ID)
	if err := s.DeleteRepo(ctx, a.ID); !errors.Is(err, store.ErrInChain) {
		t.Fatalf("delete head: %v", err)
	}
	if err := s.DeleteRepo(ctx, b.ID); !errors.Is(err, store.ErrInChain) {
		t.Fatalf("delete tail: %v", err)
	}
	if err := s.SetPrevious(ctx, b.ID, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteRepo(ctx, b.ID); err != nil {
		t.Fatalf("delete after unlink: %v", err)
	}
}

func TestNextRepo(t *testing.T) {
	s := storetest.New(t)
	a := mustRepo(t, s, "a", nil)
	b := mustRepo(t, s, "b", &a.ID)
	n, err := s.NextRepo(context.Background(), a.ID)
	if err != nil || n == nil || n.ID != b.ID {
		t.Fatalf("next=%v err=%v", n, err)
	}
	n, err = s.NextRepo(context.Background(), b.ID)
	if err != nil || n != nil {
		t.Fatalf("tail next=%v err=%v", n, err)
	}
}

func TestIPsRoundTripIncludingIPv6(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	r := mustRepo(t, s, "a", nil)
	in := []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("2001:db8::/32"), netip.MustParsePrefix("192.168.1.5/32")}
	if err := s.SetRepoIPs(ctx, r.ID, in); err != nil {
		t.Fatal(err)
	}
	got, err := s.RepoIPs(ctx, r.ID)
	if err != nil || len(got) != 3 {
		t.Fatalf("got=%v err=%v", got, err)
	}
	have := map[netip.Prefix]bool{}
	for _, p := range got {
		have[p] = true
	}
	for _, p := range in {
		if !have[p] {
			t.Errorf("missing %v", p)
		}
	}
}

func TestRepoBySlugNotFound(t *testing.T) {
	s := storetest.New(t)
	if _, err := s.RepoBySlug(context.Background(), "nope"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("got %v", err)
	}
}

func TestSetPreviousRejectsRepoWithContent(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	head := mustRepo(t, s, "head", nil)
	withPkg := mustRepo(t, s, "withpkg", nil)
	if _, err := s.AddPackage(ctx, store.Package{RepoID: withPkg.ID, Filename: "a.rpm", Name: "a", Version: "1", Release: "1", Arch: "noarch", SHA256: "x", Size: 1}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetPrevious(ctx, withPkg.ID, &head.ID); !errors.Is(err, store.ErrHasContent) {
		t.Fatalf("repo with packages linked behind another: %v", err)
	}
	withSrc := mustRepo(t, s, "withsrc", nil)
	if _, err := s.AddSource(ctx, withSrc.ID, "plex", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.SetPrevious(ctx, withSrc.ID, &head.ID); !errors.Is(err, store.ErrHasContent) {
		t.Fatalf("repo with sources linked behind another: %v", err)
	}
	empty := mustRepo(t, s, "empty", nil)
	if err := s.SetPrevious(ctx, empty.ID, &head.ID); err != nil {
		t.Fatalf("empty repo should link: %v", err)
	}
	// unlinking is always allowed
	if err := s.SetPrevious(ctx, empty.ID, nil); err != nil {
		t.Fatal(err)
	}
}

func TestAddPackageFirstStageRejectsLaterStage(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	a := mustRepo(t, s, "a", nil)
	b := mustRepo(t, s, "b", &a.ID)
	p := store.Package{Filename: "a.rpm", Name: "a", Version: "1", Release: "1", Arch: "noarch", SHA256: "x", Size: 1}
	p.RepoID = a.ID
	if _, err := s.AddPackageFirstStage(ctx, p); err != nil {
		t.Fatal(err)
	}
	p.RepoID = b.ID
	if _, err := s.AddPackageFirstStage(ctx, p); !errors.Is(err, store.ErrLaterStage) {
		t.Fatalf("got %v", err)
	}
}
