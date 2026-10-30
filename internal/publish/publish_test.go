package publish

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/ProtonMail/go-crypto/openpgp"

	"dnfrepo/internal/signer"
)

func setup(t *testing.T) (*Publisher, *signer.Signer, string) {
	t.Helper()
	if _, err := exec.LookPath("createrepo_c"); err != nil {
		t.Skip("createrepo_c not installed")
	}
	dir := t.TempDir()
	s, err := signer.LoadOrCreate(filepath.Join(dir, "key.asc"))
	if err != nil {
		t.Fatal(err)
	}
	p := New(dir, s)
	os.MkdirAll(p.RepoDir("dev"), 0o755)
	return p, s, dir
}

func verifyDir(t *testing.T, s *signer.Signer, dir string) error {
	t.Helper()
	repomd, err := os.ReadFile(filepath.Join(dir, "repomd.xml"))
	if err != nil {
		return err
	}
	sig, err := os.ReadFile(filepath.Join(dir, "repomd.xml.asc"))
	if err != nil {
		return err
	}
	kr, err := openpgp.ReadArmoredKeyRing(strings.NewReader(s.PublicKeyArmored()))
	if err != nil {
		t.Fatal(err)
	}
	_, err = openpgp.CheckArmoredDetachedSignature(kr, bytes.NewReader(repomd), bytes.NewReader(sig), nil)
	return err
}

func copyTestRPM(t *testing.T, dst string) {
	t.Helper()
	in, err := os.Open("../rpmmeta/testdata/hello-1.2.3-4.noarch.rpm")
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	out, _ := os.Create(dst)
	defer out.Close()
	io.Copy(out, in)
}

func TestRebuildEmptyRepo(t *testing.T) {
	p, s, _ := setup(t)
	if err := p.Rebuild(context.Background(), "dev"); err != nil {
		t.Fatal(err)
	}
	if err := verifyDir(t, s, filepath.Join(p.RepoDir("dev"), "repodata")); err != nil {
		t.Fatal(err)
	}
}

func TestRebuildSignatureVerifiesAndListsPackage(t *testing.T) {
	p, s, _ := setup(t)
	copyTestRPM(t, filepath.Join(p.RepoDir("dev"), "hello-1.2.3-4.noarch.rpm"))
	if err := p.Rebuild(context.Background(), "dev"); err != nil {
		t.Fatal(err)
	}
	rd := filepath.Join(p.RepoDir("dev"), "repodata")
	if err := verifyDir(t, s, rd); err != nil {
		t.Fatal(err)
	}
	ents, _ := os.ReadDir(rd)
	found := false
	for _, e := range ents {
		if strings.HasSuffix(e.Name(), "primary.xml.gz") || strings.HasSuffix(e.Name(), "primary.xml.zst") {
			found = true
		}
	}
	if !found {
		t.Fatalf("no primary metadata in %v", ents)
	}
}

func TestRebuildRejectsBadSlug(t *testing.T) {
	p, _, _ := setup(t)
	if err := p.Rebuild(context.Background(), "../etc"); err == nil {
		t.Fatal("bad slug accepted")
	}
}

func TestConcurrentReadersNeverSeeMismatch(t *testing.T) {
	p, s, _ := setup(t)
	copyTestRPM(t, filepath.Join(p.RepoDir("dev"), "hello-1.2.3-4.noarch.rpm"))
	if err := p.Rebuild(context.Background(), "dev"); err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	var wg sync.WaitGroup
	var bad []string
	var mu sync.Mutex
	wg.Add(1)
	go func() {
		defer wg.Done()
		link := filepath.Join(p.RepoDir("dev"), "repodata")
		for {
			select {
			case <-stop:
				return
			default:
			}
			target, err := os.Readlink(link) // resolve once, then read one generation
			if err != nil {
				mu.Lock()
				bad = append(bad, "readlink: "+err.Error())
				mu.Unlock()
				return
			}
			if err := verifyDir(t, s, filepath.Join(p.RepoDir("dev"), target)); err != nil {
				mu.Lock()
				bad = append(bad, err.Error())
				mu.Unlock()
			}
		}
	}()
	for i := 0; i < 8; i++ {
		if err := p.Rebuild(context.Background(), "dev"); err != nil {
			t.Fatal(err)
		}
	}
	close(stop)
	wg.Wait()
	if len(bad) > 0 {
		t.Fatalf("reader saw inconsistent metadata: %v", bad[:1])
	}
	ents, _ := os.ReadDir(p.RepoDir("dev"))
	gens := 0
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), "repodata.") {
			gens++
		}
	}
	if gens > 2 {
		t.Fatalf("%d generations kept, want <=2", gens)
	}
}

func TestRebuildSerializedPerRepo(t *testing.T) {
	p, s, _ := setup(t)
	copyTestRPM(t, filepath.Join(p.RepoDir("dev"), "hello-1.2.3-4.noarch.rpm"))
	var wg sync.WaitGroup
	errs := make(chan error, 6)
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); errs <- p.Rebuild(context.Background(), "dev") }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := verifyDir(t, s, filepath.Join(p.RepoDir("dev"), "repodata")); err != nil {
		t.Fatal(err)
	}
}
