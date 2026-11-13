package debmeta

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dnfrepo/internal/debmeta/debtest"
)

func write(t *testing.T, b []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "x.deb")
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestParse(t *testing.T) {
	for _, c := range []string{"gz", "zst"} {
		s := debtest.Spec{Package: "hello", Version: "1:2.0-1", Arch: "amd64", Fields: map[string]string{"Depends": "libc6"}, Compress: c}
		got, err := Parse(write(t, debtest.Build(t, s)))
		if err != nil {
			t.Fatalf("%s: %v", c, err)
		}
		if got.Package != "hello" || got.Version != "1:2.0-1" || got.Arch != "amd64" {
			t.Errorf("%s: %+v", c, got)
		}
		if got.Stanza != debtest.Control(s) || !strings.Contains(got.Stanza, "Depends: libc6") {
			t.Errorf("%s: stanza %q", c, got.Stanza)
		}
	}
}

func TestFilenameStripsEpoch(t *testing.T) {
	got := Filename(Control{Package: "hello", Version: "1:2.0-1", Arch: "amd64"})
	if got != "hello_2.0-1_amd64.deb" {
		t.Fatal(got)
	}
	if Filename(Control{Package: "a", Version: "1.0", Arch: "all"}) != "a_1.0_all.deb" {
		t.Fatal("no epoch case")
	}
}

func TestParseRejectsGarbage(t *testing.T) {
	good := debtest.Build(t, debtest.Spec{Package: "a", Version: "1", Arch: "all"})
	for name, b := range map[string][]byte{
		"text":      []byte("#!/bin/sh\necho hi\n"),
		"truncated": good[:len(good)/2],
		"empty":     nil,
	} {
		if _, err := Parse(write(t, b)); err != ErrNotDeb {
			t.Errorf("%s: got %v", name, err)
		}
	}
}

func TestParseStanzas(t *testing.T) {
	in := "Package: a\nDescription: one\n two\nVersion: 1\n\nPackage: b\nVersion: 2\n\n"
	st, err := ParseStanzas(strings.NewReader(in))
	if err != nil || len(st) != 2 {
		t.Fatalf("%v %v", st, err)
	}
	if st[0].Fields["Package"] != "a" || st[0].Fields["Description"] != "one\n two" || st[1].Fields["Version"] != "2" {
		t.Errorf("%+v", st)
	}
	if st[0].Raw != "Package: a\nDescription: one\n two\nVersion: 1" {
		t.Errorf("raw %q", st[0].Raw)
	}
}

func TestParseStanzasRejectsHugeLine(t *testing.T) {
	if _, err := ParseStanzas(strings.NewReader("Package: " + strings.Repeat("x", 2<<20) + "\n")); err == nil {
		t.Fatal("huge line accepted")
	}
}
