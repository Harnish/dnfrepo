package store_test

import (
	"context"
	"errors"
	"net/netip"
	"reflect"
	"testing"

	"dnfrepo/internal/store"
	"dnfrepo/internal/store/storetest"
)

func TestRegistryRepoDefaults(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	r, err := s.RegistryRepoByName(ctx, "a/b")
	if err != nil || r.Name != "a/b" || r.Mode != store.ModePublic || r.Listed || r.Exists {
		t.Fatalf("%+v %v", r, err)
	}
	for i := 0; i < 2; i++ {
		if err := s.EnsureRegistryRepo(ctx, "a/b"); err != nil {
			t.Fatal(err)
		}
	}
	r, _ = s.RegistryRepoByName(ctx, "a/b")
	if !r.Exists || r.Mode != store.ModePublic || r.Listed {
		t.Fatalf("%+v", r)
	}
	if all, _ := s.ListRegistryRepos(ctx); len(all) != 1 {
		t.Fatalf("%+v", all)
	}
}

func TestSetRegistryRepoRejectsBadMode(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	if err := s.SetRegistryRepo(ctx, "x", true, "nope"); !errors.Is(err, store.ErrBadMode) {
		t.Fatalf("got %v", err)
	}
	if err := s.SetRegistryRepo(ctx, "x", true, store.ModeUsers); err != nil {
		t.Fatal(err)
	}
	if r, _ := s.RegistryRepoByName(ctx, "x"); !r.Listed || r.Mode != store.ModeUsers || !r.Exists {
		t.Fatalf("%+v", r)
	}
}

func TestRegistryGrantsAndCheck(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	uid, tok, _ := s.CreateRepoUser(ctx, "alice")
	oid, otok, _ := s.CreateRepoUser(ctx, "bob")
	if err := s.SetRegistryGrants(ctx, "a/b/c", []int64{uid}); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.RegistryGrants(ctx, "a/b/c"); !reflect.DeepEqual(got, []int64{uid}) {
		t.Fatalf("%v", got)
	}
	for name, c := range map[string]struct {
		repo, user, tok string
		want            bool
	}{
		"right":        {"a/b/c", "alice", tok, true},
		"wrong repo":   {"a/b", "alice", tok, false},
		"wrong token":  {"a/b/c", "alice", "x", false},
		"not granted":  {"a/b/c", "bob", otok, false},
		"unknown user": {"a/b/c", "carol", tok, false},
	} {
		if got, err := s.CheckRegistryUser(ctx, c.repo, c.user, c.tok); err != nil || got != c.want {
			t.Errorf("%s: got %v %v", name, got, err)
		}
	}
	s.RevokeRepoUser(ctx, uid)
	if ok, _ := s.CheckRegistryUser(ctx, "a/b/c", "alice", tok); ok {
		t.Fatal("revoked user accepted")
	}
	_ = oid
}

func TestRegistryIPsRoundTrip(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	ps := []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("192.168.1.5/32")}
	if err := s.SetRegistryIPs(ctx, "img", ps); err != nil {
		t.Fatal(err)
	}
	got, err := s.RegistryIPs(ctx, "img")
	if err != nil || !reflect.DeepEqual(got, ps) {
		t.Fatalf("%v %v", got, err)
	}
}

func TestPushTokenLifecycle(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	id, tok, err := s.CreatePushToken(ctx, "ci", "admin@x.org")
	if err != nil || tok == "" {
		t.Fatalf("%v", err)
	}
	if ok, _ := s.CheckPushToken(ctx, "ci", tok); !ok {
		t.Fatal("good token rejected")
	}
	if ok, _ := s.CheckPushToken(ctx, "ci", tok+"x"); ok {
		t.Fatal("bad token accepted")
	}
	if _, _, err := s.CreatePushToken(ctx, "ci", "a"); !errors.Is(err, store.ErrExists) {
		t.Fatalf("duplicate: %v", err)
	}
	if _, _, err := s.CreatePushToken(ctx, "a:b", "a"); err == nil {
		t.Fatal("colon in name accepted")
	}
	list, _ := s.ListPushTokens(ctx)
	if len(list) != 1 || list[0].Name != "ci" || list[0].CreatedBy != "admin@x.org" {
		t.Fatalf("%+v", list)
	}
	if err := s.RevokePushToken(ctx, id); err != nil {
		t.Fatal(err)
	}
	if ok, _ := s.CheckPushToken(ctx, "ci", tok); ok {
		t.Fatal("revoked token accepted")
	}
	if err := s.RevokePushToken(ctx, 9999); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("%v", err)
	}
}

func TestDeleteRegistryRepoCascades(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	uid, _, _ := s.CreateRepoUser(ctx, "alice")
	s.SetRegistryGrants(ctx, "img", []int64{uid})
	s.SetRegistryIPs(ctx, "img", []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")})
	if err := s.DeleteRegistryRepo(ctx, "img"); err != nil {
		t.Fatal(err)
	}
	if g, _ := s.RegistryGrants(ctx, "img"); len(g) != 0 {
		t.Fatalf("%v", g)
	}
	if ips, _ := s.RegistryIPs(ctx, "img"); len(ips) != 0 {
		t.Fatalf("%v", ips)
	}
	if r, _ := s.RegistryRepoByName(ctx, "img"); r.Exists {
		t.Fatal("row survived")
	}
}
