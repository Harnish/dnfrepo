package sources

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"dnfrepo/internal/pipeline"
	"dnfrepo/internal/store"
	"dnfrepo/internal/store/storetest"
)

type fakePub struct {
	dir string
	mu  sync.Mutex
	n   int
}

func (f *fakePub) RepoDir(slug string) string { return filepath.Join(f.dir, "repos", slug) }
func (f *fakePub) Rebuild(context.Context, string) error {
	f.mu.Lock()
	f.n++
	f.mu.Unlock()
	return nil
}

func rpmBytes(t *testing.T) []byte {
	b, err := os.ReadFile("../rpmmeta/testdata/hello-1.2.3-4.noarch.rpm")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

type env struct {
	sch  *Scheduler
	st   *store.Store
	fp   *fakePub
	repo store.Repo
	dl   *httptest.Server
	hits *int
}

// newEnv builds a scheduler whose GitHub API and downloads are one fake server:
// owner/good has a release, owner/bad 404s.
func newEnv(t *testing.T) *env {
	t.Helper()
	st := storetest.New(t)
	fp := &fakePub{dir: t.TempDir()}
	hits := 0
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/owner/good/releases/latest":
			w.Write([]byte(`{"tag_name":"v1.2.3","assets":[{"name":"hello_amd64.rpm","browser_download_url":"` + srv.URL + `/dl/hello_amd64.rpm"}]}`))
		case "/dl/hello_amd64.rpm":
			hits++
			w.Write(rpmBytes(t))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	repo, err := st.CreateRepo(context.Background(), "dev", "Dev", store.ModePublic, nil)
	if err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(fp.RepoDir("dev"), 0o755)
	pl := &pipeline.Service{Store: st, Publisher: fp, MaxBytes: 1 << 20}
	return &env{sch: &Scheduler{Store: st, Pipeline: pl, Client: srv.Client(), GitHubAPI: srv.URL}, st: st, fp: fp, repo: repo, dl: srv, hits: &hits}
}

func TestSchedulerFetchesThenSkipsExisting(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	src, _ := e.st.AddSource(ctx, e.repo.ID, "github", "owner/good")
	if err := e.sch.RunOne(ctx, src.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(e.fp.RepoDir("dev"), "hello_amd64.rpm")); err != nil {
		// asset name has no N.N so the tag is appended
		if _, err2 := os.Stat(filepath.Join(e.fp.RepoDir("dev"), "hello_amd64-1.2.3.rpm")); err2 != nil {
			t.Fatalf("package not stored: %v", err2)
		}
	}
	if *e.hits != 1 {
		t.Fatalf("download hits=%d", *e.hits)
	}
	if err := e.sch.RunOne(ctx, src.ID); err != nil {
		t.Fatal(err)
	}
	if *e.hits != 1 {
		t.Fatalf("existing release re-downloaded: hits=%d", *e.hits)
	}
	got, _ := e.st.SourceByID(ctx, src.ID)
	if got.LastRun == nil || got.LastStatus != "up to date" || got.LastError != "" {
		t.Fatalf("source=%+v", got)
	}
	ev, _ := e.st.ListAudit(ctx, e.repo.ID, 5)
	if len(ev) != 1 || ev[0].Actor != "source:github:owner/good" {
		t.Fatalf("audit=%+v", ev)
	}
}

func TestSchedulerRecordsFailureAndContinues(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	bad, _ := e.st.AddSource(ctx, e.repo.ID, "github", "owner/bad")
	good, _ := e.st.AddSource(ctx, e.repo.ID, "github", "owner/good")
	e.sch.RunAll(ctx)
	b, _ := e.st.SourceByID(ctx, bad.ID)
	g, _ := e.st.SourceByID(ctx, good.ID)
	if b.LastStatus != "error" || b.LastError == "" {
		t.Fatalf("bad=%+v", b)
	}
	if g.LastStatus != "downloaded" {
		t.Fatalf("good=%+v", g)
	}
}

func TestSchedulerLaterStageFails(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	next, _ := e.st.CreateRepo(ctx, "test", "Test", store.ModePublic, &e.repo.ID)
	os.MkdirAll(e.fp.RepoDir("test"), 0o755)
	src, _ := e.st.AddSource(ctx, next.ID, "github", "owner/good")
	err := e.sch.RunOne(ctx, src.ID)
	if !errors.Is(err, pipeline.ErrNotFirstStage) {
		t.Fatalf("got %v", err)
	}
	if files, _ := os.ReadDir(e.fp.RepoDir("test")); len(files) != 0 {
		t.Fatalf("file landed in later stage: %v", files)
	}
}

func TestSchedulerRejectsUnknownKind(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	if _, err := e.st.AddSource(ctx, e.repo.ID, "ftp", "x"); err == nil {
		t.Fatal("db accepted unknown kind")
	}
}
