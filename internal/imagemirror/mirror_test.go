package imagemirror

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"dnfrepo/internal/access"
	"dnfrepo/internal/registry"
	"dnfrepo/internal/registry/registrytest"
	"dnfrepo/internal/secretbox"
	"dnfrepo/internal/store"
	"dnfrepo/internal/store/storetest"
)

type env struct {
	m   *Mirrors
	f   *fakeUp
	st  *store.Store
	reg *registry.Registry
	now time.Time
}

func setup(t *testing.T) *env {
	t.Helper()
	st := storetest.New(t)
	reg, err := registry.New(context.Background(), registry.Config{Root: t.TempDir(), Policy: &registry.Policy{Store: st, Throttle: access.NewThrottle()}})
	if err != nil {
		t.Fatal(err)
	}
	f := newFake(t)
	f.bearer = true
	box, _ := secretbox.New("test-secret-key-test-secret-key!")
	e := &env{f: f, st: st, reg: reg, now: time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)}
	e.m = &Mirrors{Store: st, Reg: reg, Box: box, StagingDir: t.TempDir(), Scheme: "http", Now: func() time.Time { return e.now }}
	return e
}

func (e *env) add(t *testing.T, repo string, platforms []string, keep int) store.ImageMirror {
	t.Helper()
	r, err := ParseRef(e.f.host() + "/" + repo + ":latest")
	if err != nil {
		t.Fatal(err)
	}
	m, err := e.st.AddImageMirror(context.Background(), store.ImageMirror{Upstream: r.Name(), Tag: r.Tag, LocalName: "mirror/" + repo, Platforms: platforms, Keep: keep})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func (e *env) run(t *testing.T, id int64) store.ImageMirror {
	t.Helper()
	e.m.RunOne(context.Background(), id)
	m, err := e.st.ImageMirrorByID(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func (e *env) tag(t *testing.T, name, tag string) string {
	t.Helper()
	d, err := e.reg.TagDigest(context.Background(), name, tag)
	if err != nil {
		t.Fatalf("%s:%s: %v", name, tag, err)
	}
	return d
}

func TestRunCopiesAllPlatformsDigestMatches(t *testing.T) {
	e := setup(t)
	up := e.f.fakeIndex(t, "plex/pms", "latest", "linux/amd64", "linux/arm64")
	m := e.run(t, e.add(t, "plex/pms", nil, 3).ID)
	if m.LastStatus != StatusDownloaded || m.LastError != "" || m.UpstreamDigest != up {
		t.Fatalf("%+v", m)
	}
	if got := e.tag(t, "mirror/plex/pms", "latest"); got != up {
		t.Fatalf("latest %s, upstream %s", got, up)
	}
	if got := e.tag(t, "mirror/plex/pms", "latest-20261123"); got != up {
		t.Fatalf("dated %s", got)
	}
	if r, _ := e.st.RegistryRepoByName(context.Background(), "mirror/plex/pms"); r.Mode != store.ModePublic || r.Listed {
		t.Fatalf("settings %+v", r)
	}
}

func TestRunSecondTimeUpToDate(t *testing.T) {
	e := setup(t)
	e.f.fakeIndex(t, "a/b", "latest", "linux/amd64")
	id := e.add(t, "a/b", nil, 3).ID
	e.run(t, id)
	before := e.f.gets()
	if m := e.run(t, id); m.LastStatus != StatusUpToDate {
		t.Fatalf("%+v", m)
	}
	if e.f.gets() != before {
		t.Fatalf("blobs downloaded on an up-to-date run")
	}
}

func TestRunSubsetDropsAttestation(t *testing.T) {
	e := setup(t)
	up := e.f.fakeIndex(t, "a/b", "latest", "linux/amd64", "linux/arm64")
	m := e.run(t, e.add(t, "a/b", []string{"linux/amd64"}, 3).ID)
	if m.LastStatus != StatusDownloaded {
		t.Fatalf("%+v", m)
	}
	got := e.tag(t, "mirror/a/b", "latest")
	if got == up {
		t.Fatal("subset kept upstream digest")
	}
	ts, err := e.reg.Tags(context.Background(), "mirror/a/b")
	if err != nil || len(ts) == 0 || strings.Join(ts[0].Arches, ",") != "linux/amd64" {
		t.Fatalf("arches %+v %v", ts, err)
	}
}

func TestRunMissingPlatformFailsKeepsOld(t *testing.T) {
	e := setup(t)
	e.f.fakeIndex(t, "a/b", "latest", "linux/amd64", "linux/arm/v7")
	mr := e.add(t, "a/b", []string{"linux/amd64", "linux/arm/v7"}, 3)
	e.run(t, mr.ID)
	e.f.fakeIndex(t, "a/b", "latest", "linux/amd64") // upstream dropped arm/v7
	m := e.run(t, mr.ID)
	if m.LastStatus != "error" || !strings.Contains(m.LastError, "linux/arm/v7") {
		t.Fatalf("%+v", m)
	}
	vs, _ := e.st.ImageVersions(context.Background(), mr.ID)
	if len(vs) != 1 || e.tag(t, "mirror/a/b", "latest") != vs[0].Digest {
		t.Fatalf("old copy moved: %+v", vs)
	}
}

func TestRunRetentionAndSameDay(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	_, tok, err := e.st.CreatePushToken(ctx, "ci", "test")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(e.reg.FrontHandler())
	defer srv.Close()
	if _, st := registrytest.Push(t, srv.URL, "mirror/a/b", "latest-20260101", "ci", tok); st != 0 {
		t.Fatalf("push %d", st)
	}
	id := e.add(t, "a/b", nil, 2).ID
	for i := 0; i < 3; i++ {
		e.f.fakeIndex(t, "a/b", "latest", "linux/amd64")
		if m := e.run(t, id); m.LastStatus != StatusDownloaded {
			t.Fatalf("run %d: %+v", i, m)
		}
	}
	if _, err := e.reg.TagDigest(ctx, "mirror/a/b", "latest-20261123"); !errors.Is(err, registry.ErrNoSuchTag) {
		t.Fatalf("oldest not pruned: %v", err)
	}
	for _, tg := range []string{"latest-20261123-2", "latest-20261123-3", "latest-20260101"} {
		e.tag(t, "mirror/a/b", tg)
	}
	if vs, _ := e.st.ImageVersions(ctx, id); len(vs) != 2 || vs[0].Tag != "latest-20261123-3" {
		t.Fatalf("ledger %+v", vs)
	}
}

func TestRunRecopiesAfterPushOverTag(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	up := e.f.fakeIndex(t, "a/b", "latest", "linux/amd64")
	id := e.add(t, "a/b", nil, 3).ID
	e.run(t, id)
	_, tok, _ := e.st.CreatePushToken(ctx, "ci", "test")
	srv := httptest.NewServer(e.reg.FrontHandler())
	defer srv.Close()
	if _, st := registrytest.Push(t, srv.URL, "mirror/a/b", "latest", "ci", tok); st != 0 {
		t.Fatalf("push %d", st)
	}
	if m := e.run(t, id); m.LastStatus != StatusDownloaded {
		t.Fatalf("%+v", m)
	}
	if got := e.tag(t, "mirror/a/b", "latest"); got != up {
		t.Fatalf("latest %s", got)
	}
}

func TestRunGCBusy(t *testing.T) {
	e := setup(t)
	e.f.fakeIndex(t, "a/b", "latest", "linux/amd64")
	id := e.add(t, "a/b", nil, 3).ID
	e.reg.Gate().Close(time.Second)
	defer e.reg.Gate().Open()
	if m := e.run(t, id); m.LastStatus != "error" || !strings.Contains(m.LastError, "registry busy (garbage collection)") {
		t.Fatalf("%+v", m)
	}
}

func TestCredentialedMirrorDefaultsToUsers(t *testing.T) {
	e := setup(t)
	e.f.user, e.f.pass = "u", "pw"
	e.f.fakeIndex(t, "a/b", "latest", "linux/amd64")
	mr := e.add(t, "a/b", nil, 3)
	sealed, _ := e.m.Box.Seal("pw")
	u := "u"
	if err := e.st.UpdateImageMirror(context.Background(), mr.ID, nil, 3, &u, sealed, false); err != nil {
		t.Fatal(err)
	}
	if m := e.run(t, mr.ID); m.LastStatus != StatusDownloaded {
		t.Fatalf("%+v", m)
	}
	if r, _ := e.st.RegistryRepoByName(context.Background(), "mirror/a/b"); r.Mode != store.ModeUsers {
		t.Fatalf("settings %+v", r)
	}
}

func TestStagingRemovedAfterRun(t *testing.T) {
	e := setup(t)
	e.f.fakeIndex(t, "a/b", "latest", "linux/amd64")
	ok := e.add(t, "a/b", nil, 3)
	bad := e.add(t, "a/c", []string{"linux/s390x"}, 3) // a/c does not exist upstream
	e.run(t, ok.ID)
	e.run(t, bad.ID)
	if ents, _ := os.ReadDir(e.m.StagingDir); len(ents) != 0 {
		t.Fatalf("staging left: %v", ents)
	}
}

func TestInspectListsPlatforms(t *testing.T) {
	e := setup(t)
	up := e.f.fakeIndex(t, "a/b", "latest", "linux/amd64", "linux/arm/v7")
	dg, ps, err := e.m.Inspect(context.Background(), e.f.host()+"/a/b:latest", "", "")
	if err != nil || dg != up || strings.Join(ps, ",") != "linux/amd64,linux/arm/v7" {
		t.Fatalf("%s %v %v", dg, ps, err)
	}
	e.f.fakeSingle(t, "a/s", "latest")
	if _, ps, err := e.m.Inspect(context.Background(), e.f.host()+"/a/s", "", ""); err != nil || len(ps) != 0 {
		b, _ := json.Marshal(ps)
		t.Fatalf("single: %s %v", b, err)
	}
}
