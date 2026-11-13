package publish

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/clearsign"

	"dnfrepo/internal/signer"
	"dnfrepo/internal/store"
	"dnfrepo/internal/store/storetest"
)

func aptSetup(t *testing.T) (*Publisher, *signer.Signer, *store.Store, store.Repo) {
	t.Helper()
	st := storetest.New(t)
	dir := t.TempDir()
	sg, err := signer.LoadOrCreate(filepath.Join(dir, "key.asc"))
	if err != nil {
		t.Fatal(err)
	}
	p := New(dir, sg)
	p.Store = st
	r, err := st.CreateDebRepo(context.Background(), "apt", "apt", store.ModePublic, nil,
		store.DebRepo{Suite: "stable", Components: []string{"main"}, Arches: []string{"amd64", "arm64"}})
	if err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(p.RepoDir("apt"), 0o755)
	return p, sg, st, r
}

func addDeb(t *testing.T, st *store.Store, repoID int64, name, arch string) {
	t.Helper()
	_, err := st.AddPackage(context.Background(), store.Package{RepoID: repoID, Filename: name + "_1_" + arch + ".deb", Name: name, Version: "1", Arch: arch,
		SHA256: "s256", Size: 10, Component: "main", MD5: "m5", SHA1: "s1", Control: "Package: " + name + "\nVersion: 1\nArchitecture: " + arch})
	if err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// verifyApt checks Release.gpg and InRelease against the key and that both cover the same Release.
func verifyApt(t *testing.T, sg *signer.Signer, suiteDir string) string {
	t.Helper()
	kr, err := openpgp.ReadArmoredKeyRing(strings.NewReader(sg.PublicKeyArmored()))
	if err != nil {
		t.Fatal(err)
	}
	rel := readFile(t, filepath.Join(suiteDir, "Release"))
	if _, err := openpgp.CheckArmoredDetachedSignature(kr, bytes.NewReader(rel), bytes.NewReader(readFile(t, filepath.Join(suiteDir, "Release.gpg"))), nil); err != nil {
		t.Fatalf("Release.gpg: %v", err)
	}
	blk, _ := clearsign.Decode(readFile(t, filepath.Join(suiteDir, "InRelease")))
	if blk == nil {
		t.Fatal("InRelease is not clearsigned")
	}
	if _, err := blk.VerifySignature(kr, nil); err != nil {
		t.Fatalf("InRelease: %v", err)
	}
	if strings.TrimRight(strings.ReplaceAll(string(blk.Bytes), "\r\n", "\n"), "\n") != strings.TrimRight(string(rel), "\n") {
		t.Fatal("InRelease and Release differ")
	}
	return string(rel)
}

func TestAptRebuildWritesSignedArchive(t *testing.T) {
	p, sg, st, r := aptSetup(t)
	addDeb(t, st, r.ID, "tool", "amd64")
	addDeb(t, st, r.ID, "data", "all")
	if err := p.Rebuild(context.Background(), "apt"); err != nil {
		t.Fatal(err)
	}
	suite := filepath.Join(p.RepoDir("apt"), "dists", "stable")
	rel := verifyApt(t, sg, suite)
	if !strings.Contains(rel, "main/binary-amd64/Packages.gz") {
		t.Fatalf("Release:\n%s", rel)
	}
	amd := string(readFile(t, filepath.Join(suite, "main", "binary-amd64", "Packages")))
	arm := string(readFile(t, filepath.Join(suite, "main", "binary-arm64", "Packages")))
	if !strings.Contains(amd, "Package: tool") || !strings.Contains(amd, "Package: data") || strings.Contains(arm, "Package: tool") || !strings.Contains(arm, "Package: data") {
		t.Fatalf("amd64:\n%s\narm64:\n%s", amd, arm)
	}
}

func TestAptRebuildEmptyRepo(t *testing.T) {
	p, sg, _, _ := aptSetup(t)
	if err := p.Rebuild(context.Background(), "apt"); err != nil {
		t.Fatal(err)
	}
	suite := filepath.Join(p.RepoDir("apt"), "dists", "stable")
	verifyApt(t, sg, suite)
	if _, err := os.Stat(filepath.Join(suite, "main", "binary-amd64", "Packages.gz")); err != nil {
		t.Fatal(err)
	}
}

func TestAptRebuildSwapsAtomically(t *testing.T) {
	p, _, _, _ := aptSetup(t)
	ctx := context.Background()
	dir := p.RepoDir("apt")
	var targets []string
	for i := 0; i < 3; i++ {
		if err := p.Rebuild(ctx, "apt"); err != nil {
			t.Fatal(err)
		}
		tg, err := os.Readlink(filepath.Join(dir, "dists"))
		if err != nil || !strings.HasPrefix(tg, "dists.") {
			t.Fatalf("dists is not a symlink to a generation: %q %v", tg, err)
		}
		targets = append(targets, tg)
	}
	if targets[0] == targets[1] || targets[1] == targets[2] {
		t.Fatalf("generation did not change: %v", targets)
	}
	var gens []string
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), "dists.") {
			gens = append(gens, e.Name())
		}
	}
	if len(gens) != 2 || gens[0] == targets[0] || gens[1] == targets[0] {
		t.Fatalf("want the two newest generations kept, got %v (first was %s)", gens, targets[0])
	}
}

// A failed repo lookup must fail the rebuild, not fall through to createrepo_c (which would
// leave a deb repo with rpm metadata and a stale dists tree).
func TestRebuildLookupErrorDoesNotBuildRpmMetadata(t *testing.T) {
	p, _, _, _ := aptSetup(t)
	os.MkdirAll(p.RepoDir("ghost"), 0o755)
	if err := p.Rebuild(context.Background(), "ghost"); err == nil {
		t.Fatal("Rebuild succeeded despite failed kind lookup")
	}
	if _, err := os.Lstat(filepath.Join(p.RepoDir("ghost"), "repodata")); err == nil {
		t.Fatal("rpm repodata created after failed lookup")
	}
}
