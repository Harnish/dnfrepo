package rpmmeta

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestParseReadsHeader(t *testing.T) {
	m, err := Parse("testdata/hello-1.2.3-4.noarch.rpm")
	if err != nil {
		t.Fatal(err)
	}
	want := Meta{Name: "hello", Version: "1.2.3", Release: "4", Arch: "noarch"}
	if m != want {
		t.Fatalf("got %+v want %+v", m, want)
	}
}

func TestParseRejectsGarbage(t *testing.T) {
	p := filepath.Join(t.TempDir(), "x.rpm")
	os.WriteFile(p, []byte("this is not an rpm at all, just text"), 0o644)
	if _, err := Parse(p); !errors.Is(err, ErrNotRPM) {
		t.Fatalf("want ErrNotRPM, got %v", err)
	}
}

func TestParseRejectsEmpty(t *testing.T) {
	p := filepath.Join(t.TempDir(), "x.rpm")
	os.WriteFile(p, nil, 0o644)
	if _, err := Parse(p); !errors.Is(err, ErrNotRPM) {
		t.Fatalf("want ErrNotRPM, got %v", err)
	}
}
