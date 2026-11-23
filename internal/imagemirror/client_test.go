package imagemirror

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseRef(t *testing.T) {
	cases := []struct{ in, host, repo, tag, str, local string }{
		{"plexinc/pms-docker", "registry-1.docker.io", "plexinc/pms-docker", "latest", "docker.io/plexinc/pms-docker:latest", "plexinc/pms-docker"},
		{"docker.io/plexinc/pms-docker:latest", "registry-1.docker.io", "plexinc/pms-docker", "latest", "docker.io/plexinc/pms-docker:latest", "plexinc/pms-docker"},
		{"alpine", "registry-1.docker.io", "library/alpine", "latest", "docker.io/library/alpine:latest", "library/alpine"},
		{"ghcr.io/a/b:1.2", "ghcr.io", "a/b", "1.2", "ghcr.io/a/b:1.2", "a/b"},
	}
	for _, c := range cases {
		r, err := ParseRef(c.in)
		if err != nil || r.Host != c.host || r.Repo != c.repo || r.Tag != c.tag || r.String() != c.str || r.DefaultLocalName() != c.local {
			t.Errorf("%s: %+v %q %q %v", c.in, r, r.String(), r.DefaultLocalName(), err)
		}
	}
	if _, err := ParseRef("alpine@sha256:" + strings.Repeat("a", 64)); err == nil {
		t.Error("digest ref accepted")
	}
	if _, err := ParseRef("Bad Name"); err == nil {
		t.Error("bad ref accepted")
	}
}

func fakeRef(t *testing.T, f *fakeUp, repo, tag string) Ref {
	t.Helper()
	r, err := ParseRef(f.host() + "/" + repo + ":" + tag)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestClientBearerAnonymous(t *testing.T) {
	f := newFake(t)
	f.bearer = true
	want := f.fakeIndex(t, "a/b", "latest", "linux/amd64")
	c := &Client{Scheme: "http"}
	got, err := c.Head(context.Background(), fakeRef(t, f, "a/b", "latest"))
	if err != nil || got != want {
		t.Fatalf("%q %v", got, err)
	}
}

func TestClientBearerWithCreds(t *testing.T) {
	f := newFake(t)
	f.bearer, f.user, f.pass = true, "u", "secret-token"
	want := f.fakeIndex(t, "a/b", "latest", "linux/amd64")
	c := &Client{Scheme: "http", Username: "u", Token: "secret-token"}
	ctx := context.Background()
	got, err := c.Head(ctx, fakeRef(t, f, "a/b", "latest"))
	if err != nil || got != want {
		t.Fatalf("%q %v", got, err)
	}
	mt, b, err := c.Manifest(ctx, fakeRef(t, f, "a/b", "latest"), want)
	if err != nil || mt != "application/vnd.oci.image.index.v1+json" || digestOf(b) != want {
		t.Fatalf("manifest %q %v", mt, err)
	}
}

func TestClientBasic(t *testing.T) {
	f := newFake(t)
	f.user, f.pass = "u", "p"
	want := f.fakeSingle(t, "a/b", "latest")
	got, err := (&Client{Scheme: "http", Username: "u", Token: "p"}).Head(context.Background(), fakeRef(t, f, "a/b", "latest"))
	if err != nil || got != want {
		t.Fatalf("%q %v", got, err)
	}
}

func TestClientBadCredsIsErrAuth(t *testing.T) {
	f := newFake(t)
	f.bearer, f.user, f.pass = true, "u", "right"
	f.fakeSingle(t, "a/b", "latest")
	_, err := (&Client{Scheme: "http", Username: "u", Token: "wrong-secret"}).Head(context.Background(), fakeRef(t, f, "a/b", "latest"))
	if !errors.Is(err, ErrAuth) || strings.Contains(err.Error(), "wrong-secret") {
		t.Fatalf("%v", err)
	}
}

func TestBlobDigestMismatchRemovesFile(t *testing.T) {
	f := newFake(t)
	f.fakeSingle(t, "a/b", "latest")
	var dg string
	for d := range f.blobs {
		dg = d
		break
	}
	f.blobs[dg] = []byte("tampered")
	dst := filepath.Join(t.TempDir(), "blob")
	_, err := (&Client{Scheme: "http"}).Blob(context.Background(), fakeRef(t, f, "a/b", "latest"), dg, dst, 0)
	if err == nil {
		t.Fatal("mismatch accepted")
	}
	if _, serr := os.Stat(dst); !os.IsNotExist(serr) {
		t.Fatalf("file left behind: %v", serr)
	}
}

func TestBlobOK(t *testing.T) {
	f := newFake(t)
	f.fakeSingle(t, "a/b", "latest")
	for dg, b := range f.blobs {
		dst := filepath.Join(t.TempDir(), "blob")
		n, err := (&Client{Scheme: "http"}).Blob(context.Background(), fakeRef(t, f, "a/b", "latest"), dg, dst, 0)
		got, _ := os.ReadFile(dst)
		if err != nil || n != int64(len(b)) || string(got) != string(b) {
			t.Fatalf("%d %v", n, err)
		}
	}
}

func TestManifestSchema1Rejected(t *testing.T) {
	f := newFake(t)
	f.putManifest("a/b", "old", "application/vnd.docker.distribution.manifest.v1+prettyjws", []byte(`{"schemaVersion":1}`))
	_, err := (&Client{Scheme: "http"}).Head(context.Background(), fakeRef(t, f, "a/b", "old"))
	if err == nil || !strings.Contains(err.Error(), "schema1") {
		t.Fatalf("%v", err)
	}
}

func TestMaliciousDigestsRejected(t *testing.T) {
	f := newFake(t)
	f.fakeSingle(t, "a/b", "latest")
	c := &Client{Scheme: "http"}
	dir := t.TempDir()
	for _, dg := range []string{"sha256:../../evil", "sha256:" + strings.Repeat("a", 63), "md5:abc", "../x"} {
		f.blobs[dg] = []byte("x") // a hostile upstream serves whatever it named
		f.putManifest("a/b", dg, "application/vnd.oci.image.manifest.v1+json", []byte("{}"))
		dst := filepath.Join(dir, "sub", "..", "..", "escaped")
		if _, err := c.Blob(context.Background(), fakeRef(t, f, "a/b", "latest"), dg, dst, 0); err == nil {
			t.Errorf("blob %q accepted", dg)
		}
		if _, err := os.Stat(dst); !os.IsNotExist(err) {
			t.Errorf("blob %q wrote a file", dg)
		}
		if _, _, err := c.Manifest(context.Background(), fakeRef(t, f, "a/b", "latest"), dg); err == nil {
			t.Errorf("manifest %q accepted", dg)
		}
	}
	if f.gets() != 0 {
		t.Errorf("%d blob requests made for invalid digests", f.gets())
	}
}

func TestBlobLargerThanManifestSizeRefused(t *testing.T) {
	f := newFake(t)
	f.fakeSingle(t, "a/b", "latest")
	for dg, b := range f.blobs {
		dst := filepath.Join(t.TempDir(), "blob")
		if _, err := (&Client{Scheme: "http"}).Blob(context.Background(), fakeRef(t, f, "a/b", "latest"), dg, dst, int64(len(b)-1)); err == nil {
			t.Fatal("oversized blob accepted")
		}
		if _, err := os.Stat(dst); !os.IsNotExist(err) {
			t.Fatal("oversized blob left on disk")
		}
	}
}
