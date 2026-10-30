package pipeline

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"dnfrepo/internal/publish"
	"dnfrepo/internal/signer"
	"dnfrepo/internal/store"
	"dnfrepo/internal/store/storetest"
)

// Upload then promote through the real createrepo_c-backed publisher.
func TestUploadAndPromoteWithRealPublisher(t *testing.T) {
	if _, err := exec.LookPath("createrepo_c"); err != nil {
		t.Skip("createrepo_c not installed")
	}
	dir := t.TempDir()
	sg, err := signer.LoadOrCreate(filepath.Join(dir, "k.asc"))
	if err != nil {
		t.Fatal(err)
	}
	pub := publish.New(dir, sg)
	st := storetest.New(t)
	s := &Service{Store: st, Publisher: pub, MaxBytes: 1 << 20}
	ctx := context.Background()
	dev, _ := st.CreateRepo(ctx, "dev", "Dev", store.ModePublic, nil)
	st.CreateRepo(ctx, "test", "Test", store.ModePublic, &dev.ID)
	os.MkdirAll(pub.RepoDir("dev"), 0o755)
	os.MkdirAll(pub.RepoDir("test"), 0o755)

	if _, err := s.Upload(ctx, "dev", "hello-1.2.3-4.noarch.rpm", bytes.NewReader(rpmBytes(t)), "a"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Promote(ctx, "dev", []string{"hello-1.2.3-4.noarch.rpm"}, "a"); err != nil {
		t.Fatal(err)
	}
	for _, slug := range []string{"dev", "test"} {
		for _, f := range []string{"repodata/repomd.xml", "repodata/repomd.xml.asc", "hello-1.2.3-4.noarch.rpm"} {
			if _, err := os.Stat(filepath.Join(pub.RepoDir(slug), f)); err != nil {
				t.Errorf("%s/%s: %v", slug, f, err)
			}
		}
	}
}
