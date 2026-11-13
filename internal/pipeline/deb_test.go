package pipeline

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"dnfrepo/internal/debmeta"
	"dnfrepo/internal/debmeta/debtest"
	"dnfrepo/internal/store"
)

func mkDebRepo(t *testing.T, s *Service, fp *fakePub, slug string, prev *int64, d store.DebRepo) store.Repo {
	t.Helper()
	if d.Components == nil {
		d.Components = []string{"main"}
	}
	if d.Arches == nil {
		d.Arches = []string{"amd64"}
	}
	r, err := s.Store.CreateDebRepo(context.Background(), slug, slug, store.ModePublic, prev, d)
	if err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(fp.RepoDir(slug), 0o755)
	return r
}

func debSpec(name, version, arch string) debtest.Spec {
	return debtest.Spec{Package: name, Version: version, Arch: arch, Fields: map[string]string{"Section": "utils"}}
}

func debFile(t *testing.T, sp debtest.Spec) (string, []byte) {
	t.Helper()
	b := debtest.Build(t, sp)
	return debmeta.Filename(debmeta.Control{Package: sp.Package, Version: sp.Version, Arch: sp.Arch}), b
}

func uploadDeb(t *testing.T, s *Service, slug string, sp debtest.Spec) (store.Package, error) {
	t.Helper()
	name, b := debFile(t, sp)
	return s.Upload(context.Background(), slug, name, bytes.NewReader(b), "a")
}

func stagedDeb(t *testing.T, dir string, sp debtest.Spec, component, control string) StagedFile {
	t.Helper()
	name, b := debFile(t, sp)
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(b)
	return StagedFile{Path: p, Filename: name, SHA256: hex.EncodeToString(sum[:]), Size: int64(len(b)), Component: component, Control: control}
}

func TestUploadDebFillsRow(t *testing.T) {
	s, fp := newSvc(t)
	r := mkDebRepo(t, s, fp, "d", nil, store.DebRepo{})
	p, err := uploadDeb(t, s, "d", debSpec("hello", "1.2-3", "amd64"))
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "hello" || p.Version != "1.2-3" || p.Release != "" || p.Arch != "amd64" || p.Component != "main" {
		t.Fatalf("%+v", p)
	}
	if p.SHA256 == "" || p.MD5 == "" || p.SHA1 == "" || p.Control == "" || p.Size == 0 {
		t.Fatalf("missing index data: %+v", p)
	}
	if p.Control != debtest.Control(debSpec("hello", "1.2-3", "amd64")) {
		t.Errorf("control not verbatim: %q", p.Control)
	}
	if got := filesIn(t, fp.RepoDir("d")); len(got) != 1 || got[0] != "hello_1.2-3_amd64.deb" {
		t.Fatalf("files=%v", got)
	}
	if len(fp.rebuilt) != 1 || fp.rebuilt[0] != "d" {
		t.Fatalf("rebuilt=%v", fp.rebuilt)
	}
	rows, _ := s.Store.ListPackages(context.Background(), r.ID)
	if len(rows) != 1 {
		t.Fatalf("rows=%d", len(rows))
	}
}

func TestUploadDebWrongExtensionAndKind(t *testing.T) {
	s, fp := newSvc(t)
	mkDebRepo(t, s, fp, "d", nil, store.DebRepo{})
	mkRepo(t, s, fp, "r", nil)
	ctx := context.Background()
	if _, err := s.Upload(ctx, "d", "x.rpm", bytes.NewReader(rpmBytes(t)), "a"); !errors.Is(err, ErrWrongKind) {
		t.Errorf("rpm to deb repo: %v", err)
	}
	name, b := debFile(t, debSpec("hello", "1", "amd64"))
	if _, err := s.Upload(ctx, "r", name, bytes.NewReader(b), "a"); !errors.Is(err, ErrWrongKind) {
		t.Errorf("deb to rpm repo: %v", err)
	}
	if got := filesIn(t, fp.RepoDir("d")); len(got) != 0 {
		t.Errorf("files left: %v", got)
	}
}

func TestUploadDebFilenameMismatch(t *testing.T) {
	s, fp := newSvc(t)
	mkDebRepo(t, s, fp, "d", nil, store.DebRepo{})
	_, b := debFile(t, debSpec("hello", "1.0-1", "amd64"))
	_, err := s.Upload(context.Background(), "d", "other_9.9_amd64.deb", bytes.NewReader(b), "a")
	if !errors.Is(err, ErrFilenameMismatch) {
		t.Fatalf("got %v", err)
	}
	if got := filesIn(t, fp.RepoDir("d")); len(got) != 0 {
		t.Fatalf("files left: %v", got)
	}
}

func TestUploadDebArchRules(t *testing.T) {
	s, fp := newSvc(t)
	mkDebRepo(t, s, fp, "d", nil, store.DebRepo{Arches: []string{"amd64"}})
	if _, err := uploadDeb(t, s, "d", debSpec("hello", "1", "s390x")); !errors.Is(err, ErrBadArch) {
		t.Errorf("s390x: %v", err)
	}
	if _, err := uploadDeb(t, s, "d", debSpec("hello", "1", "all")); err != nil {
		t.Errorf("all rejected: %v", err)
	}
	if _, err := uploadDeb(t, s, "d", debSpec("hello", "1", "amd64")); err != nil {
		t.Errorf("amd64 rejected: %v", err)
	}
}

func TestUploadDebEpochFilename(t *testing.T) {
	s, fp := newSvc(t)
	mkDebRepo(t, s, fp, "d", nil, store.DebRepo{})
	p, err := uploadDeb(t, s, "d", debSpec("hello", "1:2.0-1", "amd64"))
	if err != nil {
		t.Fatal(err)
	}
	if p.Version != "1:2.0-1" || p.Filename != "hello_2.0-1_amd64.deb" {
		t.Fatalf("%+v", p)
	}
}

func TestDeleteDeb(t *testing.T) {
	s, fp := newSvc(t)
	mkDebRepo(t, s, fp, "d", nil, store.DebRepo{})
	p, _ := uploadDeb(t, s, "d", debSpec("hello", "1", "amd64"))
	if err := s.Delete(context.Background(), "d", p.Filename, "a"); err != nil {
		t.Fatal(err)
	}
	if got := filesIn(t, fp.RepoDir("d")); len(got) != 0 {
		t.Fatalf("files=%v", got)
	}
}

func TestQuarantineOrphansDeb(t *testing.T) {
	s, fp := newSvc(t)
	mkDebRepo(t, s, fp, "d", nil, store.DebRepo{})
	orphan := filepath.Join(fp.RepoDir("d"), "ghost_1_amd64.deb")
	os.WriteFile(orphan, []byte("x"), 0o644)
	moved, err := s.QuarantineOrphans(context.Background(), "d")
	if err != nil || len(moved) != 1 || moved[0] != "ghost_1_amd64.deb" {
		t.Fatalf("%v %v", moved, err)
	}
	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Fatal("orphan not moved")
	}
}

// debMirror makes a deb mirror repo with the given settings, ready for IngestBatch.
func debMirror(t *testing.T, s *Service, fp *fakePub, slug string, d store.DebRepo) store.Repo {
	t.Helper()
	r := mkDebRepo(t, s, fp, slug, nil, d)
	if err := s.Store.SetMirror(context.Background(), store.Mirror{RepoID: r.ID, UpstreamURL: "u"}); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestIngestBatchDeb(t *testing.T) {
	s, fp := newSvc(t)
	ctx := context.Background()
	r := debMirror(t, s, fp, "m", store.DebRepo{Components: []string{"main", "universe"}})
	bid, _ := s.Store.CreateBatch(ctx, r.ID)
	ctl := "Package: hello\nVersion: 1:2.0-1\nArchitecture: amd64\nProvides: greeter\nMulti-Arch: foreign"
	f := stagedDeb(t, t.TempDir(), debSpec("hello", "1:2.0-1", "amd64"), "universe", ctl)
	pkgs, err := s.IngestBatch(ctx, "m", bid, []StagedFile{f}, "mirror")
	if err != nil {
		t.Fatal(err)
	}
	p := pkgs[0]
	if p.Name != "hello" || p.Version != "1:2.0-1" || p.Arch != "amd64" || p.Component != "universe" || p.Control != ctl {
		t.Fatalf("%+v", p)
	}
	if p.MD5 == "" || p.SHA1 == "" {
		t.Fatalf("md5/sha1 not computed: %+v", p)
	}
	rows, _ := s.Store.ListPackages(ctx, r.ID)
	if len(rows) != 1 || rows[0].Control != ctl || rows[0].Component != "universe" {
		t.Fatalf("%+v", rows)
	}
}

func TestPromoteDebRejectsIncompatibleStage(t *testing.T) {
	s, fp := newSvc(t)
	ctx := context.Background()
	m := debMirror(t, s, fp, "m", store.DebRepo{Components: []string{"main", "universe"}, Arches: []string{"amd64", "arm64"}})
	mkDebRepo(t, s, fp, "next", &m.ID, store.DebRepo{Components: []string{"main"}, Arches: []string{"amd64"}})
	bid, _ := s.Store.CreateBatch(ctx, m.ID)
	dir := t.TempDir()
	uni := stagedDeb(t, dir, debSpec("uni", "1", "amd64"), "universe", "")
	arm := stagedDeb(t, dir, debSpec("arm", "1", "arm64"), "main", "")
	okf := stagedDeb(t, dir, debSpec("fine", "1", "amd64"), "main", "")
	all := stagedDeb(t, dir, debSpec("data", "1", "all"), "main", "")
	if _, err := s.IngestBatch(ctx, "m", bid, []StagedFile{uni, arm, okf, all}, "mirror"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{uni.Filename, arm.Filename} {
		_, err := s.Promote(ctx, "m", []string{okf.Filename, name}, "a")
		if !errors.Is(err, ErrDebIncompatible) {
			t.Fatalf("%s: got %v", name, err)
		}
	}
	next, _ := s.Store.RepoBySlug(ctx, "next")
	if rows, _ := s.Store.ListPackages(ctx, next.ID); len(rows) != 0 {
		t.Fatalf("rejected promotion left rows: %+v", rows)
	}
	if got := filesIn(t, fp.RepoDir("next")); len(got) != 0 {
		t.Fatalf("rejected promotion left files: %v", got)
	}
	// compatible packages, including arch all, go through and keep their index data
	if _, err := s.Promote(ctx, "m", []string{okf.Filename, all.Filename}, "a"); err != nil {
		t.Fatal(err)
	}
	src, _ := s.Store.PackageByFilename(ctx, m.ID, okf.Filename)
	got, err := s.Store.PackageByFilename(ctx, next.ID, okf.Filename)
	if err != nil || got.Component != "main" || got.MD5 != src.MD5 || got.SHA1 != src.SHA1 || got.Control != src.Control || got.MD5 == "" {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestRetentionDebOrdersByEpoch(t *testing.T) {
	s, fp := newSvc(t)
	ctx := context.Background()
	r := mkDebRepo(t, s, fp, "d", nil, store.DebRepo{})
	if err := s.Store.SetRepoKeep(ctx, r.ID, 1, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := uploadDeb(t, s, "d", debSpec("hello", "2.0-1", "amd64")); err != nil {
		t.Fatal(err)
	}
	if _, err := uploadDeb(t, s, "d", debSpec("hello", "1:1.0-1", "amd64")); err != nil {
		t.Fatal(err)
	}
	rows, _ := s.Store.ListPackages(ctx, r.ID)
	if len(rows) != 1 || rows[0].Version != "1:1.0-1" {
		t.Fatalf("epoch version should win retention: %+v", rows)
	}
}
