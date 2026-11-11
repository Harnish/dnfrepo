package pipeline

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
)

func upTo(t *testing.T, s *Service, slug, name string) string {
	t.Helper()
	if _, err := s.Upload(context.Background(), slug, name, bytes.NewReader(rpmBytes(t)), "a"); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(s.Publisher.RepoDir(slug), name)
}

func sameInode(t *testing.T, a, b string) bool {
	t.Helper()
	sa, err := os.Stat(a)
	if err != nil {
		t.Fatal(err)
	}
	sb, err := os.Stat(b)
	if err != nil {
		t.Fatal(err)
	}
	return os.SameFile(sa, sb)
}

func TestUploadDedupHardlinksAcrossRepos(t *testing.T) {
	s, fp := newSvc(t)
	s.Dedup = true
	mkRepo(t, s, fp, "a", nil)
	mkRepo(t, s, fp, "b", nil)
	pa := upTo(t, s, "a", "hello-1.2.3-4.noarch.rpm")
	pb := upTo(t, s, "b", "other-name.rpm")
	if !sameInode(t, pa, pb) {
		t.Fatal("identical rpm not hardlinked")
	}
}

func TestUploadWithoutDedupKeepsCopies(t *testing.T) {
	s, fp := newSvc(t)
	mkRepo(t, s, fp, "a", nil)
	mkRepo(t, s, fp, "b", nil)
	if sameInode(t, upTo(t, s, "a", "x.rpm"), upTo(t, s, "b", "x.rpm")) {
		t.Fatal("linked although dedup is off")
	}
}

func TestIngestBatchDedupHardlinks(t *testing.T) {
	s, fp := newSvc(t)
	s.Dedup = true
	ctx := context.Background()
	mkRepo(t, s, fp, "a", nil)
	pa := upTo(t, s, "a", "x.rpm")
	r := mirrorRepo(t, s, fp, "m")
	bid, _ := s.Store.CreateBatch(ctx, r.ID)
	if _, err := s.IngestBatch(ctx, "m", bid, []StagedFile{stage(t, t.TempDir(), "y.rpm")}, "mirror"); err != nil {
		t.Fatal(err)
	}
	if !sameInode(t, pa, filepath.Join(fp.RepoDir("m"), "y.rpm")) {
		t.Fatal("mirror ingest not hardlinked")
	}
}

func TestDedupFallsBackWhenSourceMissing(t *testing.T) {
	s, fp := newSvc(t)
	s.Dedup = true
	mkRepo(t, s, fp, "a", nil)
	mkRepo(t, s, fp, "b", nil)
	os.Remove(upTo(t, s, "a", "x.rpm")) // row stays, file gone
	pb := upTo(t, s, "b", "x.rpm")
	if b, err := os.ReadFile(pb); err != nil || !bytes.Equal(b, rpmBytes(t)) {
		t.Fatalf("fallback copy bad: %v", err)
	}
}

func TestSweepMergesExistingDuplicates(t *testing.T) {
	s, fp := newSvc(t)
	ctx := context.Background()
	mkRepo(t, s, fp, "a", nil)
	mkRepo(t, s, fp, "b", nil)
	pa, pb := upTo(t, s, "a", "x.rpm"), upTo(t, s, "b", "y.rpm")
	if sameInode(t, pa, pb) {
		t.Fatal("precondition: should be separate copies")
	}
	res, err := s.DedupSweep(ctx, "admin")
	if err != nil {
		t.Fatal(err)
	}
	if res.Linked != 1 || res.BytesSaved != int64(len(rpmBytes(t))) || !sameInode(t, pa, pb) {
		t.Fatalf("res=%+v same=%v", res, sameInode(t, pa, pb))
	}
	res, _ = s.DedupSweep(ctx, "admin") // idempotent
	if res.Linked != 0 || res.BytesSaved != 0 {
		t.Fatalf("second run: %+v", res)
	}
	if len(fp.rebuilt) != 2 { // only the two uploads; sweep never rebuilds
		t.Fatalf("rebuilt=%v", fp.rebuilt)
	}
}

func TestSweepSkipsContentMismatch(t *testing.T) {
	s, fp := newSvc(t)
	mkRepo(t, s, fp, "a", nil)
	mkRepo(t, s, fp, "b", nil)
	pa, pb := upTo(t, s, "a", "x.rpm"), upTo(t, s, "b", "y.rpm")
	os.WriteFile(pb, []byte("tampered"), 0o644) // DB still says same sha
	res, err := s.DedupSweep(context.Background(), "admin")
	if err != nil {
		t.Fatal(err)
	}
	if res.Linked != 0 || res.Skipped != 1 || sameInode(t, pa, pb) {
		t.Fatalf("res=%+v", res)
	}
	if b, _ := os.ReadFile(pb); string(b) != "tampered" {
		t.Fatal("mismatched file was overwritten")
	}
}

func TestDeleteOneLinkKeepsOther(t *testing.T) {
	s, fp := newSvc(t)
	s.Dedup = true
	mkRepo(t, s, fp, "a", nil)
	mkRepo(t, s, fp, "b", nil)
	pa, pb := upTo(t, s, "a", "x.rpm"), upTo(t, s, "b", "x.rpm")
	if err := s.Delete(context.Background(), "a", "x.rpm", "admin"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(pa); !os.IsNotExist(err) {
		t.Fatal("a not deleted")
	}
	if b, err := os.ReadFile(pb); err != nil || !bytes.Equal(b, rpmBytes(t)) {
		t.Fatalf("b damaged: %v", err)
	}
}
