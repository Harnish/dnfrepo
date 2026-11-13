package sources

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dnfrepo/internal/debmeta"
	"dnfrepo/internal/debmeta/debtest"
	"dnfrepo/internal/pipeline"
	"dnfrepo/internal/store"
	"dnfrepo/internal/store/storetest"
)

func names(rs []Release) []string {
	var out []string
	for _, r := range rs {
		out = append(out, r.Filename)
	}
	return out
}

func TestLatestGitHubDebPicksOneAssetPerWantedArch(t *testing.T) {
	srv := ghServer(t, "v1", "tool_1_arm64.deb", "tool_1_amd64.deb", "tool-1.x86_64.rpm", "tool_1_s390x.deb", "tool_1_all.deb")
	rels, err := LatestGitHubDeb(context.Background(), srv.Client(), srv.URL, "", "owner/tool", []string{"amd64", "arm64"})
	if err != nil || strings.Join(names(rels), ",") != "tool_1_amd64.deb,tool_1_arm64.deb,tool_1_all.deb" || !strings.HasSuffix(rels[0].URL, "/dl/tool_1_amd64.deb") {
		t.Fatalf("%v %v", names(rels), err)
	}
}

func TestLatestGitHubDebAcceptsCommonAssetNames(t *testing.T) {
	srv := ghServer(t, "v1", "tool_1.2.3_linux_amd64.deb", "tool-1.2.3-arm64.deb")
	rels, err := LatestGitHubDeb(context.Background(), srv.Client(), srv.URL, "", "owner/tool", []string{"amd64", "arm64"})
	if err != nil || len(rels) != 2 {
		t.Fatalf("%v %v", names(rels), err)
	}
}

func TestLatestGitHubDebNoDebErrors(t *testing.T) {
	srv := ghServer(t, "v1", "tool-1.x86_64.rpm", "tool_1_s390x.deb")
	if _, err := LatestGitHubDeb(context.Background(), srv.Client(), srv.URL, "", "owner/tool", []string{"amd64"}); err == nil {
		t.Fatal("no matching deb should fail")
	}
}

type debEnv struct {
	sch  *Scheduler
	st   *store.Store
	fp   *fakePub
	repo store.Repo
}

// newDebEnv: a deb repo "apt" (amd64) and a fake GitHub serving owner/tool with one deb asset.
func newDebEnv(t *testing.T, assetName string, spec debtest.Spec) *debEnv {
	t.Helper()
	st := storetest.New(t)
	fp := &fakePub{dir: t.TempDir()}
	deb := debtest.Build(t, spec)
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/owner/tool/releases/latest":
			w.Write([]byte(`{"tag_name":"v1","assets":[{"name":"` + assetName + `","browser_download_url":"` + srv.URL + `/dl/` + assetName + `"}]}`))
		case "/dl/" + assetName:
			w.Write(deb)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	repo, err := st.CreateDebRepo(context.Background(), "apt", "apt", store.ModePublic, nil, store.DebRepo{Components: []string{"main"}, Arches: []string{"amd64"}})
	if err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(fp.RepoDir("apt"), 0o755)
	pl := &pipeline.Service{Store: st, Publisher: fp, MaxBytes: 1 << 20}
	return &debEnv{sch: &Scheduler{Store: st, Pipeline: pl, Client: srv.Client(), GitHubAPI: srv.URL}, st: st, fp: fp, repo: repo}
}

func TestSchedulerFetchesDebIntoDebRepo(t *testing.T) {
	sp := debtest.Spec{Package: "tool", Version: "1.0-1", Arch: "amd64"}
	name := debmeta.Filename(debmeta.Control{Package: "tool", Version: "1.0-1", Arch: "amd64"})
	e := newDebEnv(t, name, sp)
	ctx := context.Background()
	src, _ := e.st.AddSource(ctx, e.repo.ID, "github", "owner/tool")
	if err := e.sch.RunOne(ctx, src.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(e.fp.RepoDir("apt"), name)); err != nil {
		t.Fatalf("deb not stored under its own name: %v", err)
	}
	rows, _ := e.st.ListPackages(ctx, e.repo.ID)
	if len(rows) != 1 || rows[0].Component != "main" || rows[0].Name != "tool" {
		t.Fatalf("%+v", rows)
	}
	if err := e.sch.RunOne(ctx, src.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := e.st.SourceByID(ctx, src.ID); got.LastStatus != "up to date" {
		t.Fatalf("second run: %+v", got)
	}
}

// The control data is authoritative: an asset named differently is stored under its canonical name,
// and the next run recognizes it instead of failing or re-adding it.
func TestSchedulerDebStoresUnderCanonicalName(t *testing.T) {
	sp := debtest.Spec{Package: "tool", Version: "1.0-1", Arch: "amd64"}
	canon := debmeta.Filename(debmeta.Control{Package: "tool", Version: "1.0-1", Arch: "amd64"})
	e := newDebEnv(t, "tool_1.0_linux_amd64.deb", sp)
	ctx := context.Background()
	src, _ := e.st.AddSource(ctx, e.repo.ID, "github", "owner/tool")
	for i := 0; i < 2; i++ {
		if err := e.sch.RunOne(ctx, src.ID); err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
	}
	if rows, _ := e.st.ListPackages(ctx, e.repo.ID); len(rows) != 1 || rows[0].Filename != canon {
		t.Fatalf("%+v", rows)
	}
	if got, _ := e.st.SourceByID(ctx, src.ID); got.LastStatus != "up to date" {
		t.Fatalf("second run: %+v", got)
	}
}

func TestSchedulerPlexOnDebRepoFails(t *testing.T) {
	e := newDebEnv(t, "x_1_amd64.deb", debtest.Spec{Package: "x", Version: "1", Arch: "amd64"})
	ctx := context.Background()
	src, _ := e.st.AddSource(ctx, e.repo.ID, "plex", "")
	if err := e.sch.RunOne(ctx, src.ID); err == nil {
		t.Fatal("plex source ran against a deb repo")
	}
}
