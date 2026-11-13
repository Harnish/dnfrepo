package mirror

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dnfrepo/internal/debmeta/debtest"
	"dnfrepo/internal/pipeline"
	"dnfrepo/internal/store"
	"dnfrepo/internal/store/storetest"
)

func TestPlanDebKeepLatestUsesDpkgOrder(t *testing.T) {
	in := []UpstreamPkg{
		{Name: "hello", Arch: "amd64", Version: "1:1.0-1", Filename: "hello_1.0-1_amd64.deb", SHA256: "a"},
		{Name: "hello", Arch: "amd64", Version: "2.0-1", Filename: "hello_2.0-1_amd64.deb", SHA256: "b"},
	}
	got, err := Plan(in, nil, Filter{KeepLatest: 1, Compare: debCompare}, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	eq(t, got, "hello_1.0-1_amd64.deb") // the epoch makes 1:1.0-1 newer than 2.0-1
	got, _ = Plan(in, nil, Filter{KeepLatest: 1}, 0, nil)
	eq(t, got, "hello_2.0-1_amd64.deb") // default (rpm) ordering is unchanged
}

func TestPlanAllArchAlwaysAllowed(t *testing.T) {
	in := []UpstreamPkg{
		{Name: "data", Arch: "all", Version: "1", Filename: "data_1_all.deb", SHA256: "a"},
		{Name: "tool", Arch: "arm64", Version: "1", Filename: "tool_1_arm64.deb", SHA256: "b"},
		{Name: "tool", Arch: "amd64", Version: "1", Filename: "tool_1_amd64.deb", SHA256: "c"},
	}
	got, err := Plan(in, nil, Filter{Arches: []string{"amd64"}, Compare: debCompare}, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	eq(t, got, "data_1_all.deb", "tool_1_amd64.deb")
}

type debSyncEnv struct {
	t    *testing.T
	st   *store.Store
	pub  *syncPub
	up   *fakeApt
	s    *Syncer
	repo store.Repo
}

func newDebSyncEnv(t *testing.T, cfg func(*store.Mirror, *fakeApt)) *debSyncEnv {
	t.Helper()
	ctx := context.Background()
	st := storetest.New(t)
	pub := &syncPub{dir: t.TempDir()}
	repo, err := st.CreateDebRepo(ctx, "m", "m", store.ModePublic, nil, store.DebRepo{Suite: "stable", Components: []string{"main"}, Arches: []string{"amd64"}})
	if err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(pub.RepoDir("m"), 0o755)
	up := newFakeApt(t)
	m := store.Mirror{RepoID: repo.ID, UpstreamURL: up.URL(), Schedule: store.Schedule{Frequency: "manual", TimeOfDay: "00:00", Timezone: "UTC"}}
	if cfg != nil {
		cfg(&m, up)
	}
	if err := st.SetMirror(ctx, m); err != nil {
		t.Fatal(err)
	}
	s := &Syncer{Store: st, Pipeline: &pipeline.Service{Store: st, Publisher: pub}, StagingDir: filepath.Join(t.TempDir(), "staging"), Client: up.srv.Client()}
	return &debSyncEnv{t: t, st: st, pub: pub, up: up, s: s, repo: repo}
}

func (e *debSyncEnv) run() (Result, error) {
	e.t.Helper()
	if err := e.st.ClaimMirror(context.Background(), e.repo.ID); err != nil {
		e.t.Fatal(err)
	}
	return e.s.Run(context.Background(), e.repo.ID, "mirror:m")
}

func (e *debSyncEnv) rows() []store.Package {
	e.t.Helper()
	p, err := e.st.ListPackages(context.Background(), e.repo.ID)
	if err != nil {
		e.t.Fatal(err)
	}
	return p
}

func (e *debSyncEnv) addDeb(name, version, arch string) *fakeDeb {
	return e.up.AddDeb(name, version, arch, "main", debtest.Build(e.t, debtest.Spec{Package: name, Version: version, Arch: arch}))
}

func TestSyncDebMirrorEndToEnd(t *testing.T) {
	e := newDebSyncEnv(t, nil)
	e.addDeb("tool", "1:2.0-1", "amd64")
	e.addDeb("data", "3.0", "all")
	e.addDeb("armtool", "1.0", "arm64") // not a wanted arch
	res, err := e.run()
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "synced" || res.Added != 2 || res.BatchID == 0 {
		t.Fatalf("res=%+v", res)
	}
	byName := map[string]store.Package{}
	for _, p := range e.rows() {
		byName[p.Name] = p
		if p.OriginBatchID == nil || *p.OriginBatchID != res.BatchID || p.Component != "main" || p.MD5 == "" || p.SHA1 == "" {
			t.Errorf("%+v", p)
		}
	}
	tool := byName["tool"]
	if len(byName) != 2 || tool.Version != "1:2.0-1" || tool.Release != "" || !strings.Contains(tool.Control, "Package: tool") || !strings.Contains(tool.Control, "Filename: pool/main/tool_2.0-1_amd64.deb") {
		t.Fatalf("%+v", byName)
	}
	if files := filesIn(e.pub.RepoDir("m")); len(files) != 2 {
		t.Fatalf("files=%v", files)
	}
	if e.pub.rebuilds != 1 {
		t.Fatalf("rebuilds=%d", e.pub.rebuilds)
	}
	res, err = e.run()
	if err != nil || res.Status != "up to date" {
		t.Fatalf("second run: %+v %v", res, err)
	}
}

func TestSyncDebBadUpstreamSignatureLeavesRepoUnchanged(t *testing.T) {
	other, _ := newEntity(t)
	e := newDebSyncEnv(t, func(m *store.Mirror, up *fakeApt) {
		m.GPGKey = up.PubKey
		m.RequireSig = true
		up.SignWith = other // signed by a key the mirror does not trust
	})
	e.addDeb("tool", "1.0", "amd64")
	if _, err := e.run(); err == nil || !strings.Contains(err.Error(), "signature") {
		t.Fatalf("got %v", err)
	}
	if r := e.rows(); len(r) != 0 {
		t.Errorf("rows=%v", r)
	}
	if f := filesIn(e.pub.RepoDir("m")); len(f) != 0 {
		t.Errorf("files=%v", f)
	}
	if e.pub.rebuilds != 0 {
		t.Errorf("rebuilds=%d", e.pub.rebuilds)
	}
	if m, _ := e.st.MirrorByRepo(context.Background(), e.repo.ID); m.State != "idle" || m.LastStatus != "error" {
		t.Errorf("mirror not finished as an error: %+v", m)
	}
}

func TestSyncDebHeaderMismatchFails(t *testing.T) {
	e := newDebSyncEnv(t, nil)
	// upstream index says 1.0-1; the deb it serves says 2.0-1
	e.up.AddDeb("tool", "1.0-1", "amd64", "main", debtest.Build(t, debtest.Spec{Package: "tool", Version: "2.0-1", Arch: "amd64"}))
	if _, err := e.run(); err == nil || !strings.Contains(err.Error(), "header") && !strings.Contains(err.Error(), "control") {
		t.Fatalf("got %v", err)
	}
	if r := e.rows(); len(r) != 0 {
		t.Errorf("rows=%v", r)
	}
}
