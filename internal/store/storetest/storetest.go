// Package storetest gives tests an isolated Postgres schema per test.
package storetest

import (
	"context"
	"fmt"
	"math/rand"
	"net/url"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"

	"dnfrepo/internal/store"
)

// New returns a Store bound to a fresh schema, dropped at test cleanup.
// Skips the test when DNFREPO_TEST_DSN is unset.
func New(t *testing.T) *store.Store {
	s, _ := NewWithDSN(t)
	return s
}

// NewWithDSN is New plus the schema-scoped DSN, for tests that reopen the store.
func NewWithDSN(t *testing.T) (*store.Store, string) {
	t.Helper()
	base := os.Getenv("DNFREPO_TEST_DSN")
	if base == "" {
		t.Skip("DNFREPO_TEST_DSN not set")
	}
	ctx := context.Background()
	schema := fmt.Sprintf("t_%d", rand.Int63())
	conn, err := pgx.Connect(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	s, err := store.Open(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		s.Close()
		conn.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE")
		conn.Close(ctx)
	})
	return s, u.String()
}
