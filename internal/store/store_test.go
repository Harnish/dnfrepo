package store_test

import (
	"context"
	"testing"

	"dnfrepo/internal/store"
	"dnfrepo/internal/store/storetest"
)

func TestOpenAppliesMigrationsIdempotently(t *testing.T) {
	s, dsn := storetest.NewWithDSN(t)
	s2, err := store.Open(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	var n int
	if err := s.Pool().QueryRow(context.Background(), `SELECT count(*) FROM repos`).Scan(&n); err != nil {
		t.Fatal(err)
	}
}
