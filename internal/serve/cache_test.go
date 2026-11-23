package serve

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"testing"
	"time"

	"dnfrepo/internal/store"
	"dnfrepo/internal/store/storetest"
)

type cacheFix struct {
	st      *store.Store
	dir     string
	h       http.Handler
	repo    store.Repo
	fetched []store.CacheEntry
	fail    bool
}

func newCacheFix(t *testing.T, mode store.AccessMode, deb bool) *cacheFix {
	t.Helper()
	ctx := context.Background()
	st := storetest.New(t)
	f := &cacheFix{st: st, dir: t.TempDir()}
	var err error
	if deb {
		f.repo, err = st.CreateDebRepo(ctx, "c", "c", mode, nil, store.DebRepo{Suite: "stable", Components: []string{"main"}, Arches: []string{"amd64"}})
	} else {
		f.repo, err = st.CreateRepo(ctx, "c", "c", mode, nil)
	}
	if err != nil {
		t.Fatal(err)
	}
	if mode == store.ModeIPs {
		st.SetRepoIPs(ctx, f.repo.ID, []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")})
	}
	if err := st.SetMirror(ctx, store.Mirror{RepoID: f.repo.ID, UpstreamURL: "https://u/", Mode: store.ModeCache}); err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(repoDir(f.dir)("c"), 0o755)
	fetch := func(ctx context.Context, repo store.Repo, e store.CacheEntry, w io.Writer) error {
		f.fetched = append(f.fetched, e)
		if f.fail {
			return errors.New("upstream down")
		}
		_, err := w.Write([]byte("FROM-UPSTREAM"))
		return err
	}
	f.h = Handler(Deps{Store: st, RepoDir: repoDir(f.dir), BaseURL: "https://x", Fetch: fetch})
	return f
}

func (f *cacheFix) entry(t *testing.T, path, filename string) {
	t.Helper()
	err := f.st.ReplaceCacheEntries(context.Background(), f.repo.ID, []store.CacheEntry{{Path: path, Href: path, Filename: filename,
		Name: "a", Arch: "noarch", Version: "1", Release: "1", SHA256: "s", Size: 13}})
	if err != nil {
		t.Fatal(err)
	}
}

func (f *cacheFix) get(path, remote string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("GET", path, nil)
	r.RemoteAddr = remote
	w := httptest.NewRecorder()
	f.h.ServeHTTP(w, r)
	return w
}

func TestCacheServeLocalTouches(t *testing.T) {
	f := newCacheFix(t, store.ModePublic, false)
	ctx := context.Background()
	f.entry(t, "Packages/a/a-1-1.noarch.rpm", "a-1-1.noarch.rpm")
	os.WriteFile(filepath.Join(repoDir(f.dir)("c"), "a-1-1.noarch.rpm"), []byte("LOCAL"), 0o644)
	if err := f.st.AddMirrorPackages(ctx, f.repo.ID, 0, []store.Package{{RepoID: f.repo.ID, Filename: "a-1-1.noarch.rpm", Name: "a",
		Version: "1", Release: "1", Arch: "noarch", SHA256: "s", Size: 5}}, "t"); err != nil {
		t.Fatal(err)
	}
	p0, _ := f.st.PackageByFilename(ctx, f.repo.ID, "a-1-1.noarch.rpm")
	time.Sleep(5 * time.Millisecond)
	w := f.get("/r/c/Packages/a/a-1-1.noarch.rpm", "192.0.2.1:1")
	if w.Code != 200 || w.Body.String() != "LOCAL" || len(f.fetched) != 0 {
		t.Fatalf("%d %q fetched=%d", w.Code, w.Body.String(), len(f.fetched))
	}
	p1, _ := f.st.PackageByFilename(ctx, f.repo.ID, "a-1-1.noarch.rpm")
	if !p1.LastPulled.After(p0.LastPulled) {
		t.Fatal("last_pulled not advanced")
	}
}

func TestCacheServeMissFetches(t *testing.T) {
	f := newCacheFix(t, store.ModePublic, false)
	f.entry(t, "Packages/a/a-1-1.noarch.rpm", "a-1-1.noarch.rpm")
	w := f.get("/r/c/Packages/a/a-1-1.noarch.rpm", "192.0.2.1:1")
	if w.Code != 200 || w.Body.String() != "FROM-UPSTREAM" || w.Header().Get("Content-Length") != "13" {
		t.Fatalf("%d %q %v", w.Code, w.Body.String(), w.Header())
	}
	if len(f.fetched) != 1 || f.fetched[0].Filename != "a-1-1.noarch.rpm" {
		t.Fatalf("fetched %+v", f.fetched)
	}
}

func TestCacheServeUnknownPath404(t *testing.T) {
	f := newCacheFix(t, store.ModePublic, false)
	f.entry(t, "Packages/a/a-1-1.noarch.rpm", "a-1-1.noarch.rpm")
	for _, p := range []string{"/r/c/Packages/b/b-1-1.noarch.rpm", "/r/c/a-1-1.noarch.rpm"} {
		if w := f.get(p, "192.0.2.1:1"); w.Code != 404 {
			t.Errorf("%s: %d", p, w.Code)
		}
	}
	if len(f.fetched) != 0 {
		t.Fatal("fetched unknown path")
	}
}

func TestCacheServeUpstreamDown502(t *testing.T) {
	f := newCacheFix(t, store.ModePublic, false)
	f.fail = true
	f.entry(t, "Packages/a/a-1-1.noarch.rpm", "a-1-1.noarch.rpm")
	w := f.get("/r/c/Packages/a/a-1-1.noarch.rpm", "192.0.2.1:1")
	if w.Code != http.StatusBadGateway {
		t.Fatalf("%d %q", w.Code, w.Body.String())
	}
}

func TestCacheServeTraversal404(t *testing.T) {
	f := newCacheFix(t, store.ModePublic, false)
	f.entry(t, "../x.rpm", "x.rpm")
	for _, p := range []string{"/r/c/../x.rpm", "/r/c/Packages/../../x.rpm", "/r/c/Packages/%2e%2e/x.rpm"} {
		if w := f.get(p, "192.0.2.1:1"); w.Code == 200 {
			t.Errorf("%s: %d", p, w.Code)
		}
	}
	if len(f.fetched) != 0 {
		t.Fatal("fetched traversal path")
	}
}

func TestCacheServeDebPool(t *testing.T) {
	f := newCacheFix(t, store.ModePublic, true)
	f.entry(t, "pool/main/x_1_amd64.deb", "x_1_amd64.deb")
	w := f.get("/r/c/pool/main/x_1_amd64.deb", "192.0.2.1:1")
	if w.Code != 200 || len(f.fetched) != 1 {
		t.Fatalf("%d fetched=%d", w.Code, len(f.fetched))
	}
}

func TestCacheServeRespectsAccessMode(t *testing.T) {
	f := newCacheFix(t, store.ModeIPs, false)
	f.entry(t, "Packages/a/a-1-1.noarch.rpm", "a-1-1.noarch.rpm")
	if w := f.get("/r/c/Packages/a/a-1-1.noarch.rpm", "192.0.2.1:1"); w.Code == 200 || len(f.fetched) != 0 {
		t.Fatalf("denied ip: %d fetched=%d", w.Code, len(f.fetched))
	}
	if w := f.get("/r/c/Packages/a/a-1-1.noarch.rpm", "10.1.2.3:1"); w.Code != 200 {
		t.Fatalf("allowed ip: %d", w.Code)
	}
}

func TestNonCacheRepoUnchanged(t *testing.T) {
	f := newFixture(t)
	if w := f.get("/r/pub/Packages/a.rpm", "192.0.2.1:1", nil); w.Code != 404 {
		t.Fatalf("nested rpm path on plain repo: %d", w.Code)
	}
}
