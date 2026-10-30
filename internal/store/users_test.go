package store_test

import (
	"context"
	"errors"
	"testing"

	"dnfrepo/internal/store"
	"dnfrepo/internal/store/storetest"
)

func TestRepoUserTokenLifecycle(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	r := mustRepo(t, s, "dev", nil)
	other := mustRepo(t, s, "other", nil)
	id, tok, err := s.CreateRepoUser(ctx, "alice")
	if err != nil || tok == "" {
		t.Fatalf("create: %v", err)
	}
	if ok, _ := s.CheckRepoToken(ctx, r.ID, "alice", tok); ok {
		t.Fatal("ungranted user accepted")
	}
	if err := s.GrantRepoUser(ctx, id, r.ID); err != nil {
		t.Fatal(err)
	}
	if ok, _ := s.CheckRepoToken(ctx, r.ID, "alice", tok); !ok {
		t.Fatal("granted user rejected")
	}
	if ok, _ := s.CheckRepoToken(ctx, r.ID, "alice", tok+"x"); ok {
		t.Fatal("wrong token accepted")
	}
	if ok, _ := s.CheckRepoToken(ctx, other.ID, "alice", tok); ok {
		t.Fatal("token valid on repo it was not granted")
	}
	if err := s.RevokeRepoUser(ctx, id); err != nil {
		t.Fatal(err)
	}
	if ok, _ := s.CheckRepoToken(ctx, r.ID, "alice", tok); ok {
		t.Fatal("revoked token still accepted")
	}
}

func TestRepoUserNameRules(t *testing.T) {
	s := storetest.New(t)
	for _, n := range []string{"", "a:b", ":", "  "} {
		if _, _, err := s.CreateRepoUser(context.Background(), n); err == nil {
			t.Errorf("name %q accepted", n)
		}
	}
	if _, _, err := s.CreateRepoUser(context.Background(), "bob"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.CreateRepoUser(context.Background(), "bob"); !errors.Is(err, store.ErrExists) {
		t.Fatalf("dup: %v", err)
	}
}

func TestTokenNotStoredPlain(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	_, tok, _ := s.CreateRepoUser(ctx, "carol")
	var h string
	s.Pool().QueryRow(ctx, `SELECT token_hash FROM repo_users WHERE name='carol'`).Scan(&h)
	if h == "" || h == tok {
		t.Fatalf("hash=%q", h)
	}
}
