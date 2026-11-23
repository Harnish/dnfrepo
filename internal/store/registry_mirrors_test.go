package store_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"dnfrepo/internal/store"
	"dnfrepo/internal/store/storetest"
)

func addMirror(t *testing.T, s *store.Store, local, tag string) store.ImageMirror {
	t.Helper()
	m, err := s.AddImageMirror(context.Background(), store.ImageMirror{Upstream: "docker.io/" + local, Tag: tag, LocalName: local,
		Platforms: []string{"linux/amd64"}, Keep: 3, Username: "u", Secret: []byte("sealed")})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestImageMirrorCRUD(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	m := addMirror(t, s, "plexinc/pms-docker", "latest")
	got, err := s.ImageMirrorByID(ctx, m.ID)
	if err != nil || !reflect.DeepEqual(got.Platforms, []string{"linux/amd64"}) || got.Keep != 3 || string(got.Secret) != "sealed" || got.Username != "u" {
		t.Fatalf("%+v %v", got, err)
	}
	if _, err := s.AddImageMirror(ctx, store.ImageMirror{Upstream: "x", Tag: "latest", LocalName: "plexinc/pms-docker", Keep: 3}); !errors.Is(err, store.ErrMirrorExists) {
		t.Fatalf("dup: %v", err)
	}
	if err := s.AddImageVersion(ctx, store.ImageVersion{MirrorID: m.ID, Tag: "latest-20261123", Digest: "sha256:l"}, "sha256:up"); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateImageMirror(ctx, m.ID, nil, 5, nil, nil, false); err != nil {
		t.Fatal(err)
	}
	got, _ = s.ImageMirrorByID(ctx, m.ID)
	if got.Keep != 5 || len(got.Platforms) != 0 || string(got.Secret) != "sealed" || got.UpstreamDigest != "sha256:up" {
		t.Fatalf("update kept creds/digest: %+v", got)
	}
	empty := ""
	if err := s.UpdateImageMirror(ctx, m.ID, []string{"linux/arm64"}, 5, &empty, nil, true); err != nil {
		t.Fatal(err)
	}
	got, _ = s.ImageMirrorByID(ctx, m.ID)
	if got.Secret != nil || got.Username != "" || got.UpstreamDigest != "" {
		t.Fatalf("clear: %+v", got)
	}
	if err := s.RecordImageMirrorRun(ctx, m.ID, "error", "boom"); err != nil {
		t.Fatal(err)
	}
	if got, _ = s.ImageMirrorByID(ctx, m.ID); got.LastStatus != "error" || got.LastError != "boom" || got.LastRun == nil {
		t.Fatalf("run: %+v", got)
	}
	if ms, err := s.ListImageMirrors(ctx); err != nil || len(ms) != 1 {
		t.Fatalf("list %v %v", ms, err)
	}
	if err := s.DeleteImageMirror(ctx, m.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ImageMirrorByID(ctx, m.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("after delete: %v", err)
	}
	if vs, _ := s.ImageVersions(ctx, m.ID); len(vs) != 0 {
		t.Fatalf("versions not cascaded: %v", vs)
	}
}

func TestImageVersionsLedger(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	m := addMirror(t, s, "a/b", "latest")
	for _, tg := range []string{"latest-20261121", "latest-20261122", "latest-20261123"} {
		if err := s.AddImageVersion(ctx, store.ImageVersion{MirrorID: m.ID, Tag: tg, Digest: "sha256:" + tg}, "sha256:up-"+tg); err != nil {
			t.Fatal(err)
		}
	}
	got, _ := s.ImageMirrorByID(ctx, m.ID)
	if got.UpstreamDigest != "sha256:up-latest-20261123" {
		t.Fatalf("digest %q", got.UpstreamDigest)
	}
	vs, err := s.ImageVersions(ctx, m.ID)
	if err != nil || len(vs) != 3 || vs[0].Tag != "latest-20261123" || vs[2].Tag != "latest-20261121" {
		t.Fatalf("%+v %v", vs, err)
	}
	if err := s.DeleteImageVersion(ctx, m.ID, "latest-20261121"); err != nil {
		t.Fatal(err)
	}
	if vs, _ = s.ImageVersions(ctx, m.ID); len(vs) != 2 {
		t.Fatalf("after delete %+v", vs)
	}
}

func TestEnsureRegistryRepoModeKeepsExisting(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	if err := s.EnsureRegistryRepo(ctx, "old/img"); err != nil {
		t.Fatal(err)
	}
	if err := s.EnsureRegistryRepoMode(ctx, "old/img", store.ModeUsers); err != nil {
		t.Fatal(err)
	}
	if r, _ := s.RegistryRepoByName(ctx, "old/img"); r.Mode != store.ModePublic {
		t.Fatalf("existing changed: %+v", r)
	}
	if err := s.EnsureRegistryRepoMode(ctx, "new/img", store.ModeUsers); err != nil {
		t.Fatal(err)
	}
	if r, _ := s.RegistryRepoByName(ctx, "new/img"); r.Mode != store.ModeUsers || r.Listed {
		t.Fatalf("new: %+v", r)
	}
}
