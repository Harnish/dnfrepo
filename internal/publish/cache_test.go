package publish

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dnfrepo/internal/aptindex"
	"dnfrepo/internal/signer"
	"dnfrepo/internal/store"
	"dnfrepo/internal/store/storetest"
)

func cacheSetup(t *testing.T) (*Publisher, *signer.Signer, string) {
	t.Helper()
	st := storetest.New(t)
	dir := t.TempDir()
	sg, err := signer.LoadOrCreate(filepath.Join(dir, "key.asc"))
	if err != nil {
		t.Fatal(err)
	}
	p := New(dir, sg)
	p.Store = st
	r, err := st.CreateRepo(context.Background(), "c", "c", store.ModePublic, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetMirror(context.Background(), store.Mirror{RepoID: r.ID, UpstreamURL: "https://u/", Mode: store.ModeCache}); err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(p.RepoDir("c"), 0o755)
	return p, sg, dir
}

func upstreamRepodata(t *testing.T, dir, tag string) string {
	t.Helper()
	src := filepath.Join(dir, "src-"+tag)
	os.MkdirAll(src, 0o755)
	os.WriteFile(filepath.Join(src, "repomd.xml"), []byte("<repomd>"+tag+"</repomd>"), 0o644)
	os.WriteFile(filepath.Join(src, "x-primary.xml.gz"), []byte("primary"+tag), 0o644)
	return src
}

func TestRebuildSkipsCacheRepo(t *testing.T) {
	p, _, dir := cacheSetup(t)
	if err := p.SwapRepodata("c", upstreamRepodata(t, dir, "1")); err != nil {
		t.Fatal(err)
	}
	before, _ := os.Readlink(filepath.Join(p.RepoDir("c"), "repodata"))
	if err := p.Rebuild(context.Background(), "c"); err != nil {
		t.Fatal(err)
	}
	after, _ := os.Readlink(filepath.Join(p.RepoDir("c"), "repodata"))
	if before == "" || before != after {
		t.Fatalf("repodata changed: %q -> %q", before, after)
	}
	if got := string(readFile(t, filepath.Join(p.RepoDir("c"), "repodata", "repomd.xml"))); got != "<repomd>1</repomd>" {
		t.Fatalf("repomd overwritten: %q", got)
	}
}

func TestSwapRepodataSignsAndSwaps(t *testing.T) {
	p, sg, dir := cacheSetup(t)
	for _, tag := range []string{"1", "2", "3"} {
		if err := p.SwapRepodata("c", upstreamRepodata(t, dir, tag)); err != nil {
			t.Fatal(err)
		}
	}
	rd := filepath.Join(p.RepoDir("c"), "repodata")
	if got := string(readFile(t, filepath.Join(rd, "repomd.xml"))); got != "<repomd>3</repomd>" {
		t.Fatalf("repomd %q", got)
	}
	if got := string(readFile(t, filepath.Join(rd, "x-primary.xml.gz"))); got != "primary3" {
		t.Fatalf("primary %q", got)
	}
	if err := verifyDir(t, sg, rd); err != nil {
		t.Fatalf("signature: %v", err)
	}
	gens, _ := filepath.Glob(filepath.Join(p.RepoDir("c"), "repodata.*"))
	if len(gens) != 2 {
		t.Fatalf("generations %v", gens)
	}
}

func TestPublishAptFromPackages(t *testing.T) {
	p, sg, _, r := aptSetup(t)
	pkgs := []aptindex.Package{{Component: "main", Arch: "amd64", Filename: "foo_1_amd64.deb", Size: 3, MD5: "m", SHA1: "s", SHA256: "x",
		Control: "Package: foo\nVersion: 1\nArchitecture: amd64"}}
	if err := p.PublishApt(context.Background(), r, pkgs); err != nil {
		t.Fatal(err)
	}
	suite := filepath.Join(p.RepoDir("apt"), "dists", "stable")
	verifyApt(t, sg, suite)
	idx := string(readFile(t, filepath.Join(suite, "main", "binary-amd64", "Packages")))
	if !strings.Contains(idx, "Filename: pool/main/foo_1_amd64.deb") {
		t.Fatalf("Packages:\n%s", idx)
	}
}
