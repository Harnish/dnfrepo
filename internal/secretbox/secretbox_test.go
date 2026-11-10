package secretbox

import (
	"bytes"
	"testing"
)

func mustNew(t *testing.T, k string) *Box {
	t.Helper()
	b, err := New(k)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestSealOpenRoundTrip(t *testing.T) {
	b := mustNew(t, "k")
	for _, p := range []string{"", "token-123", "päss wörd !@#"} {
		s, err := b.Seal(p)
		if err != nil {
			t.Fatal(err)
		}
		got, err := b.Open(s)
		if err != nil || got != p {
			t.Fatalf("got %q %v want %q", got, err, p)
		}
	}
}

func TestOpenRejectsTamperAndWrongKey(t *testing.T) {
	b := mustNew(t, "k")
	s, _ := b.Seal("secret")
	bad := bytes.Clone(s)
	bad[len(bad)-1] ^= 1
	if _, err := b.Open(bad); err == nil {
		t.Fatal("tampered accepted")
	}
	if _, err := mustNew(t, "other").Open(s); err == nil {
		t.Fatal("wrong key accepted")
	}
	if _, err := b.Open([]byte("x")); err == nil {
		t.Fatal("short input accepted")
	}
}

func TestSealIsNonDeterministic(t *testing.T) {
	b := mustNew(t, "k")
	a, _ := b.Seal("same")
	c, _ := b.Seal("same")
	if bytes.Equal(a, c) {
		t.Fatal("seals equal")
	}
}

func TestNewRejectsEmptyKey(t *testing.T) {
	if _, err := New(""); err == nil {
		t.Fatal("empty key accepted")
	}
}
