package store_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"dnfrepo/internal/store"
	"dnfrepo/internal/store/storetest"
)

func TestCreateDebRepoDefaults(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	r, err := s.CreateDebRepo(ctx, "bookworm", "Bookworm", store.ModePublic, nil, store.DebRepo{Components: []string{"main"}, Arches: []string{"amd64", "all"}})
	if err != nil {
		t.Fatal(err)
	}
	if r.Kind != store.KindDeb {
		t.Fatalf("kind=%q", r.Kind)
	}
	d, err := s.DebRepoByID(ctx, r.ID)
	if err != nil || d.Suite != "bookworm" || !reflect.DeepEqual(d.Components, []string{"main"}) || !reflect.DeepEqual(d.Arches, []string{"amd64", "all"}) {
		t.Fatalf("%+v %v", d, err)
	}
	got, _ := s.RepoBySlug(ctx, "bookworm")
	if got.Kind != store.KindDeb {
		t.Fatalf("RepoBySlug kind=%q", got.Kind)
	}
}

func TestCreateDebRepoValidation(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	for name, d := range map[string]store.DebRepo{
		"all-only arch": {Components: []string{"main"}, Arches: []string{"all"}},
		"no arch":       {Components: []string{"main"}},
		"no component":  {Arches: []string{"amd64"}},
		"bad suite":     {Suite: "Bad Suite!", Components: []string{"main"}, Arches: []string{"amd64"}},
		"bad component": {Components: []string{"ma in"}, Arches: []string{"amd64"}},
		"bad arch":      {Components: []string{"main"}, Arches: []string{"am d64"}},
	} {
		if _, err := s.CreateDebRepo(ctx, "x"+name[:2], "x", store.ModePublic, nil, d); !errors.Is(err, store.ErrBadDeb) {
			t.Errorf("%s: got %v", name, err)
		}
	}
	if repos, _ := s.ListRepos(ctx); len(repos) != 0 {
		t.Fatalf("rejected deb repos left rows: %+v", repos)
	}
}

func TestDebRepoByIDNotFoundForRPM(t *testing.T) {
	s := storetest.New(t)
	r := mustRepo(t, s, "fed", nil)
	if r.Kind != store.KindRPM {
		t.Fatalf("existing repos are rpm, got %q", r.Kind)
	}
	if _, err := s.DebRepoByID(context.Background(), r.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("got %v", err)
	}
}

func TestSetDebRepo(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	r, _ := s.CreateDebRepo(ctx, "d", "d", store.ModePublic, nil, store.DebRepo{Components: []string{"main"}, Arches: []string{"amd64"}})
	if err := s.SetDebRepo(ctx, r.ID, store.DebRepo{Suite: "noble", Components: []string{"main", "universe"}, Arches: []string{"amd64", "arm64"}}); err != nil {
		t.Fatal(err)
	}
	d, _ := s.DebRepoByID(ctx, r.ID)
	if d.Suite != "noble" || len(d.Components) != 2 || len(d.Arches) != 2 {
		t.Fatalf("%+v", d)
	}
	if err := s.SetDebRepo(ctx, r.ID, store.DebRepo{Suite: "noble", Components: []string{"main"}, Arches: []string{"all"}}); !errors.Is(err, store.ErrBadDeb) {
		t.Fatalf("all-only arch accepted: %v", err)
	}
}

func TestPackageDebFieldsRoundTrip(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	r, _ := s.CreateDebRepo(ctx, "d", "d", store.ModePublic, nil, store.DebRepo{Components: []string{"main"}, Arches: []string{"amd64"}})
	p := store.Package{RepoID: r.ID, Filename: "a_1_amd64.deb", Name: "a", Version: "1:1", Arch: "amd64", SHA256: "s", Size: 3,
		Component: "main", Control: "Package: a", MD5: "m", SHA1: "h"}
	got, err := s.AddPackage(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	if got.Component != "main" || got.Control != "Package: a" || got.MD5 != "m" || got.SHA1 != "h" {
		t.Fatalf("AddPackage: %+v", got)
	}
	q := p
	q.Filename = "b_1_amd64.deb"
	if err := s.AddPackagesAudited(ctx, r.ID, []store.Package{q}, "a", "promoted", []string{"x"}); err != nil {
		t.Fatal(err)
	}
	b, err := s.PackageByFilename(ctx, r.ID, "b_1_amd64.deb")
	if err != nil || b.Component != "main" || b.Control != "Package: a" || b.MD5 != "m" || b.SHA1 != "h" {
		t.Fatalf("AddPackagesAudited: %+v %v", b, err)
	}
	m, _ := s.CreateDebRepo(ctx, "m", "m", store.ModePublic, nil, store.DebRepo{Components: []string{"main"}, Arches: []string{"amd64"}})
	bid, _ := s.CreateBatch(ctx, m.ID)
	c := p
	c.RepoID, c.Filename = m.ID, "c_1_amd64.deb"
	if err := s.AddMirrorPackages(ctx, m.ID, bid, []store.Package{c}, "mirror"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.PackageByFilename(ctx, m.ID, "c_1_amd64.deb"); got.Component != "main" || got.Control != "Package: a" || got.MD5 != "m" {
		t.Fatalf("AddMirrorPackages: %+v", got)
	}
}

func TestLinkRequiresSameKind(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	d := store.DebRepo{Components: []string{"main"}, Arches: []string{"amd64"}}
	rpm := mustRepo(t, s, "rpm1", nil)
	deb, _ := s.CreateDebRepo(ctx, "deb1", "deb1", store.ModePublic, nil, d)
	if _, err := s.CreateDebRepo(ctx, "deb2", "deb2", store.ModePublic, &rpm.ID, d); !errors.Is(err, store.ErrChain) {
		t.Errorf("deb behind rpm: %v", err)
	}
	if _, err := s.CreateRepo(ctx, "rpm2", "rpm2", store.ModePublic, &deb.ID); !errors.Is(err, store.ErrChain) {
		t.Errorf("rpm behind deb: %v", err)
	}
	loose := mustRepo(t, s, "rpm3", nil)
	if err := s.SetPrevious(ctx, loose.ID, &deb.ID); !errors.Is(err, store.ErrChain) {
		t.Errorf("SetPrevious across kinds: %v", err)
	}
	if _, err := s.CreateDebRepo(ctx, "deb3", "deb3", store.ModePublic, &deb.ID, d); err != nil {
		t.Errorf("deb behind deb: %v", err)
	}
	if r, err := s.CreateRepo(ctx, "rpm4", "rpm4", store.ModePublic, &rpm.ID); err != nil || r.Kind != store.KindRPM {
		t.Errorf("rpm behind rpm: %v", err)
	}
}
