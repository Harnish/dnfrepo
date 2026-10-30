package serve

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dnfrepo/internal/store"
	"dnfrepo/internal/store/storetest"
)

type fixture struct {
	st     *store.Store
	dir    string
	h      http.Handler
	token  string
	secret string
}

func repoDir(dir string) func(string) string {
	return func(slug string) string { return filepath.Join(dir, "repos", slug) }
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	st := storetest.New(t)
	dir := t.TempDir()
	ctx := context.Background()
	f := &fixture{st: st, dir: dir}
	mk := func(slug string, mode store.AccessMode) store.Repo {
		r, err := st.CreateRepo(ctx, slug, "Repo "+slug, mode, nil)
		if err != nil {
			t.Fatal(err)
		}
		rd := repoDir(dir)(slug)
		os.MkdirAll(filepath.Join(rd, "repodata.1"), 0o755)
		os.WriteFile(filepath.Join(rd, "repodata.1", "repomd.xml"), []byte("<repomd/>"), 0o644)
		os.WriteFile(filepath.Join(rd, "repodata.1", "repomd.xml.asc"), []byte("SIG"), 0o644)
		os.Symlink("repodata.1", filepath.Join(rd, "repodata"))
		os.WriteFile(filepath.Join(rd, "a.rpm"), []byte("0123456789"), 0o644)
		return r
	}
	mk("pub", store.ModePublic)
	ipr := mk("ipr", store.ModeIPs)
	st.SetRepoIPs(ctx, ipr.ID, []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")})
	usr := mk("usr", store.ModeUsers)
	id, tok, _ := st.CreateRepoUser(ctx, "alice")
	st.GrantRepoUser(ctx, id, usr.ID)
	f.token = tok
	// a secret outside any repo, and a symlink trying to reach it
	f.secret = filepath.Join(dir, "SECRET.txt")
	os.WriteFile(f.secret, []byte("SECRET"), 0o644)
	os.Symlink(f.secret, filepath.Join(repoDir(dir)("pub"), "evil.rpm"))
	pubRepo, _ := st.RepoBySlug(ctx, "pub")
	st.AddPackage(ctx, store.Package{RepoID: pubRepo.ID, Filename: "a.rpm", Name: "alpha", Version: "1.0", Release: "1", Arch: "x86_64", SHA256: "x", Size: 10})
	f.h = Handler(Deps{Store: st, RepoDir: repoDir(dir), PublicKey: "-----BEGIN PGP PUBLIC KEY BLOCK-----\nKEY\n", BaseURL: "https://repo.example.org"})
	return f
}

func (f *fixture) get(path, remote string, mod func(*http.Request)) *httptest.ResponseRecorder {
	r := httptest.NewRequest("GET", path, nil)
	r.RemoteAddr = remote
	if mod != nil {
		mod(r)
	}
	w := httptest.NewRecorder()
	f.h.ServeHTTP(w, r)
	return w
}

func TestRepodataAndRPMServed(t *testing.T) {
	f := newFixture(t)
	if w := f.get("/r/pub/repodata/repomd.xml", "1.1.1.1:1", nil); w.Code != 200 || w.Body.String() != "<repomd/>" {
		t.Fatalf("repomd: %d %q", w.Code, w.Body)
	}
	if w := f.get("/r/pub/repodata/repomd.xml.asc", "1.1.1.1:1", nil); w.Code != 200 || w.Body.String() != "SIG" {
		t.Fatalf("asc: %d", w.Code)
	}
	if w := f.get("/r/pub/a.rpm", "1.1.1.1:1", nil); w.Code != 200 || w.Body.String() != "0123456789" {
		t.Fatalf("rpm: %d", w.Code)
	}
}

func TestRangeRequest(t *testing.T) {
	f := newFixture(t)
	w := f.get("/r/pub/a.rpm", "1.1.1.1:1", func(r *http.Request) { r.Header.Set("Range", "bytes=2-4") })
	if w.Code != 206 || w.Body.String() != "234" {
		t.Fatalf("range: %d %q", w.Code, w.Body)
	}
}

func TestTraversalRejected(t *testing.T) {
	f := newFixture(t)
	paths := []string{
		"/r/pub/%2e%2e/ipr/a.rpm",
		"/r/pub/..%2fipr%2fa.rpm",
		"/r/pub/repodata/..%2f..%2fSECRET.txt",
		"/r/pub/repodata/%2e%2e%2fa.rpm",
		"/r/pub/evil.rpm", // symlink out of the repo
		"/r/pub/.hidden",
		"/r/pub/repodata.1/repomd.xml", // generation dirs are not public paths
		"/r/pub/sub/dir/a.rpm",
		"/r/pub/%00.rpm",
	}
	for _, p := range paths {
		w := f.get(p, "1.1.1.1:1", nil)
		if w.Code == 200 || strings.Contains(w.Body.String(), "SECRET") {
			t.Errorf("%s -> %d %q", p, w.Code, w.Body.String())
		}
	}
}

func TestPrivateRepoBrowseFeedRepoFileRequireAuth(t *testing.T) {
	f := newFixture(t)
	for _, c := range []struct {
		slug   string
		remote string
		want   int
	}{{"usr", "1.1.1.1:1", 401}, {"ipr", "11.1.1.1:1", 403}} {
		for _, p := range []string{"/", "/feed.xml", "/" + c.slug + ".repo", "/a.rpm", "/repodata/repomd.xml", "/repodata/repomd.xml.asc"} {
			if w := f.get("/r/"+c.slug+p, c.remote, nil); w.Code != c.want {
				t.Errorf("%s%s: got %d want %d", c.slug, p, w.Code, c.want)
			}
		}
	}
	// and they open for an allowed client
	if w := f.get("/r/ipr/a.rpm", "10.1.1.1:1", nil); w.Code != 200 {
		t.Fatalf("allowed ip: %d", w.Code)
	}
}

func TestRepoFileContents(t *testing.T) {
	f := newFixture(t)
	w := f.get("/r/pub/pub.repo", "1.1.1.1:1", nil)
	want := "[pub]\nname=Repo pub\nbaseurl=https://repo.example.org/r/pub/\nenabled=1\ngpgcheck=0\nrepo_gpgcheck=1\ngpgkey=https://repo.example.org/gpg.key\n"
	if w.Code != 200 || w.Body.String() != want {
		t.Fatalf("%d\n%q\nwant\n%q", w.Code, w.Body.String(), want)
	}
	if !strings.Contains(w.Header().Get("Content-Disposition"), "pub.repo") {
		t.Fatalf("disposition=%q", w.Header().Get("Content-Disposition"))
	}
}

func TestRepoFilePlaceholdersForUsersMode(t *testing.T) {
	f := newFixture(t)
	w := f.get("/r/usr/usr.repo", "1.1.1.1:1", func(r *http.Request) { r.SetBasicAuth("alice", f.token) })
	b := w.Body.String()
	if w.Code != 200 || !strings.Contains(b, "username=YOUR_USERNAME\npassword=YOUR_TOKEN\n") {
		t.Fatalf("%d %q", w.Code, b)
	}
	if strings.Contains(b, f.token) {
		t.Fatal("real token in .repo file")
	}
}

func TestGPGKeyPublic(t *testing.T) {
	f := newFixture(t)
	w := f.get("/gpg.key", "1.1.1.1:1", nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "BEGIN PGP PUBLIC KEY BLOCK") {
		t.Fatalf("%d %q", w.Code, w.Body)
	}
}

func TestBrowsePageListsPackages(t *testing.T) {
	f := newFixture(t)
	w := f.get("/r/pub/", "1.1.1.1:1", nil)
	b := w.Body.String()
	for _, want := range []string{"Repo pub", "alpha", "1.0", `href="a.rpm"`, "pub.repo", "feed.xml"} {
		if !strings.Contains(b, want) {
			t.Errorf("browse page missing %q", want)
		}
	}
}
