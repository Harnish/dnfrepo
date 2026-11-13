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

type aptFixture struct {
	h   http.Handler
	dir string
}

func newAptFixture(t *testing.T) *aptFixture {
	t.Helper()
	st := storetest.New(t)
	dir := t.TempDir()
	ctx := context.Background()
	d := store.DebRepo{Suite: "stable", Components: []string{"main"}, Arches: []string{"amd64"}}
	var repos []store.Repo
	for _, c := range []struct {
		slug string
		mode store.AccessMode
	}{{"apt", store.ModePublic}, {"priv", store.ModeIPs}} {
		r, err := st.CreateDebRepo(ctx, c.slug, "Apt "+c.slug, c.mode, nil, d)
		if err != nil {
			t.Fatal(err)
		}
		rd := repoDir(dir)(c.slug)
		os.MkdirAll(filepath.Join(rd, "dists.1", "stable", "main", "binary-amd64"), 0o755)
		os.WriteFile(filepath.Join(rd, "dists.1", "stable", "Release"), []byte("RELEASE"), 0o644)
		os.WriteFile(filepath.Join(rd, "dists.1", "stable", "InRelease"), []byte("INRELEASE"), 0o644)
		os.WriteFile(filepath.Join(rd, "dists.1", "stable", "main", "binary-amd64", "Packages.gz"), []byte("PKGS"), 0o644)
		os.Symlink("dists.1", filepath.Join(rd, "dists"))
		os.WriteFile(filepath.Join(rd, "x_1_amd64.deb"), []byte("DEBBYTES"), 0o644)
		st.AddPackage(ctx, store.Package{RepoID: r.ID, Filename: "x_1_amd64.deb", Name: "x", Version: "1", Arch: "amd64", SHA256: "s", Size: 8, Component: "main"})
		repos = append(repos, r)
	}
	st.SetRepoIPs(ctx, repos[1].ID, []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")})
	os.WriteFile(filepath.Join(dir, "SECRET.txt"), []byte("SECRET"), 0o644)
	os.Symlink(filepath.Join(dir, "SECRET.txt"), filepath.Join(repoDir(dir)("apt"), "evil_1_amd64.deb"))
	h := Handler(Deps{Store: st, RepoDir: repoDir(dir), PublicKey: "KEY", BaseURL: "https://repo.example.org"})
	return &aptFixture{h: h, dir: dir}
}

func (f *aptFixture) get(path, remote string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("GET", path, nil)
	r.RemoteAddr = remote
	w := httptest.NewRecorder()
	f.h.ServeHTTP(w, r)
	return w
}

func TestServesDists(t *testing.T) {
	f := newAptFixture(t)
	for p, want := range map[string]string{
		"/r/apt/dists/stable/Release":                       "RELEASE",
		"/r/apt/dists/stable/InRelease":                     "INRELEASE",
		"/r/apt/dists/stable/main/binary-amd64/Packages.gz": "PKGS",
	} {
		if w := f.get(p, "1.1.1.1:1"); w.Code != 200 || w.Body.String() != want {
			t.Errorf("%s: %d %q", p, w.Code, w.Body)
		}
	}
	if w := f.get("/r/apt/dists/other/Release", "1.1.1.1:1"); w.Code != 404 {
		t.Errorf("wrong suite: %d", w.Code)
	}
}

func TestServesPoolMapsToFlatFile(t *testing.T) {
	f := newAptFixture(t)
	if w := f.get("/r/apt/pool/main/x_1_amd64.deb", "1.1.1.1:1"); w.Code != 200 || w.Body.String() != "DEBBYTES" {
		t.Fatalf("%d %q", w.Code, w.Body)
	}
	for _, p := range []string{"/r/apt/pool/universe/x_1_amd64.deb", "/r/apt/pool/main/missing_1_amd64.deb", "/r/apt/x_1_amd64.deb", "/r/apt/pool/main/x.rpm"} {
		if w := f.get(p, "1.1.1.1:1"); w.Code != 404 {
			t.Errorf("%s: %d", p, w.Code)
		}
	}
}

func TestAptTraversalRejected(t *testing.T) {
	f := newAptFixture(t)
	for _, p := range []string{
		"/r/apt/dists/stable/../../x_1_amd64.deb",
		"/r/apt/dists/stable/..%2f..%2fSECRET.txt",
		"/r/apt/dists/%2e%2e/stable/Release",
		"/r/apt/pool/main/..%2fSECRET.txt",
		"/r/apt/pool/main/%2e%2e/x_1_amd64.deb",
		"/r/apt/pool/main/evil_1_amd64.deb", // symlink out of the repo
		"/r/apt/dists.1/stable/Release",     // generation dirs are not public paths
		"/r/apt/dists/stable/main/binary-amd64",
		"/r/apt/dists/stable/%00",
	} {
		if w := f.get(p, "1.1.1.1:1"); w.Code == 200 || strings.Contains(w.Body.String(), "SECRET") {
			t.Errorf("%s -> %d %q", p, w.Code, w.Body.String())
		}
	}
}

func TestRepoFile404ForDeb(t *testing.T) {
	if w := newAptFixture(t).get("/r/apt/apt.repo", "1.1.1.1:1"); w.Code != 404 {
		t.Fatalf("%d", w.Code)
	}
}

func TestBrowseDebShowsSourcesLine(t *testing.T) {
	w := newAptFixture(t).get("/r/apt/", "1.1.1.1:1")
	body := w.Body.String()
	for _, want := range []string{
		"deb [signed-by=/etc/apt/keyrings/dnfrepo.asc] https://repo.example.org/r/apt/ stable main",
		"https://repo.example.org/gpg.key",
		"x_1_amd64.deb", "pool/main/x_1_amd64.deb",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("browse page lacks %q", want)
		}
	}
	if strings.Contains(body, "apt.repo") {
		t.Error("deb repo page links a .repo file")
	}
}

func TestAccessGuardCoversDeb(t *testing.T) {
	f := newAptFixture(t)
	for _, p := range []string{"/", "/dists/stable/Release", "/dists/stable/InRelease", "/pool/main/x_1_amd64.deb", "/feed.xml"} {
		if w := f.get("/r/priv"+p, "11.1.1.1:1"); w.Code != 403 {
			t.Errorf("priv%s from foreign ip: %d", p, w.Code)
		}
	}
	if w := f.get("/r/priv/dists/stable/Release", "10.1.1.1:1"); w.Code != 200 {
		t.Fatalf("allowed ip: %d", w.Code)
	}
}
