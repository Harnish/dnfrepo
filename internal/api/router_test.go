package api

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"dnfrepo/internal/pipeline"
	"dnfrepo/internal/serve"
	"dnfrepo/internal/sources"
)

func routerEnv(t *testing.T) (*env, http.Handler, string) {
	t.Helper()
	e := newEnv(t)
	dir := t.TempDir()
	actor := func(r *http.Request) string { em, _ := e.Admin.Authenticate(r); return em }
	pl := &pipeline.Service{Store: e.St, Publisher: nopPub{dir}, MaxBytes: 1 << 20}
	h := Router(Deps{
		Admin:     e.Admin,
		Repos:     &Repos{Store: e.St, DataDir: dir},
		RepoUsers: &RepoUsers{Store: e.St},
		Packages:  &Packages{Pipeline: pl, Actor: actor},
		Sources:   &Sources{Store: e.St, Scheduler: &sources.Scheduler{Store: e.St, Pipeline: pl}},
		Serve: serve.Handler(serve.Deps{Store: e.St, RepoDir: func(s string) string { return filepath.Join(dir, "repos", s) },
			PublicKey: "KEY", BaseURL: "http://x", Trusted: []netip.Prefix{}}),
		Static: fstest.MapFS{
			"index.html":    {Data: []byte("<html>SPA</html>")},
			"assets/app.js": {Data: []byte("console.log(1)")},
		},
	})
	return e, h, dir
}

func call(h http.Handler, method, path string, e *env, csrf bool) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader("{}"))
	r.Header.Set("Content-Type", "application/json")
	if e != nil {
		r.AddCookie(e.Cookie)
	}
	if csrf {
		r.Header.Set("X-Requested-With", "dnfrepo")
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

var adminRoutes = []struct{ method, path string }{
	{"GET", "/api/auth/me"}, {"POST", "/api/auth/logout"}, {"POST", "/api/admins/invite"},
	{"GET", "/api/repos"}, {"POST", "/api/repos"}, {"GET", "/api/repos/dev"}, {"PATCH", "/api/repos/dev"},
	{"DELETE", "/api/repos/dev"}, {"PUT", "/api/repos/dev/ips"}, {"PUT", "/api/repos/dev/previous"},
	{"GET", "/api/repos/dev/users"}, {"PUT", "/api/repos/dev/users"},
	{"GET", "/api/repo-users"}, {"POST", "/api/repo-users"}, {"DELETE", "/api/repo-users/1"},
	{"GET", "/api/repos/dev/packages"}, {"POST", "/api/repos/dev/packages"},
	{"DELETE", "/api/repos/dev/packages/x.rpm"}, {"POST", "/api/repos/dev/promote"}, {"GET", "/api/repos/dev/audit"}, {"PUT", "/api/repos/dev/keep"},
	{"GET", "/api/repos/dev/sources"}, {"POST", "/api/repos/dev/sources"},
	{"DELETE", "/api/sources/1"}, {"POST", "/api/sources/1/run"},
}

func TestAdminRoutesRequireSessionAndCSRFHeader(t *testing.T) {
	e, h, _ := routerEnv(t)
	for _, rt := range adminRoutes {
		if w := call(h, rt.method, rt.path, nil, true); w.Code != 401 {
			t.Errorf("anon %s %s: %d", rt.method, rt.path, w.Code)
		}
		if rt.method != "GET" {
			if w := call(h, rt.method, rt.path, e, false); w.Code != 403 {
				t.Errorf("no-CSRF %s %s: %d", rt.method, rt.path, w.Code)
			}
		}
	}
}

func TestLoginAndVerifyArePublic(t *testing.T) {
	_, h, _ := routerEnv(t)
	// reachable without a session (bad body -> 400/401, never the admin 401 "login required" wall for verify)
	if w := call(h, "POST", "/api/auth/verify", nil, true); w.Code != 400 {
		t.Errorf("verify: %d", w.Code)
	}
	if w := call(h, "POST", "/api/auth/login", nil, true); w.Code != 401 {
		t.Errorf("login: %d", w.Code)
	}
}

func TestRepoRoutesNotBehindAdminAuth(t *testing.T) {
	e, h, dir := routerEnv(t)
	if w := call(h, "POST", "/api/repos", e, true); w.Code == 401 {
		t.Fatal("admin create should be allowed with session")
	}
	// make a public repo through the admin API, then fetch its .repo anonymously
	r := httptest.NewRequest("POST", "/api/repos", strings.NewReader(`{"slug":"dev","name":"Dev","mode":"public"}`))
	r.AddCookie(e.Cookie)
	r.Header.Set("X-Requested-With", "dnfrepo")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 201 {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	os.WriteFile(filepath.Join(dir, "repos", "dev", "a.rpm"), []byte("x"), 0o644)
	if w := call(h, "GET", "/r/dev/dev.repo", nil, false); w.Code != 200 {
		t.Fatalf(".repo anon: %d", w.Code)
	}
	if w := call(h, "GET", "/gpg.key", nil, false); w.Code != 200 || w.Body.String() != "KEY" {
		t.Fatalf("gpg.key: %d", w.Code)
	}
	if w := call(h, "GET", "/healthz", nil, false); w.Code != 200 {
		t.Fatalf("healthz: %d", w.Code)
	}
}

func TestSPAFallback(t *testing.T) {
	e, h, _ := routerEnv(t)
	if w := call(h, "GET", "/repos/dev", nil, false); w.Code != 200 || !strings.Contains(w.Body.String(), "SPA") {
		t.Fatalf("fallback: %d %q", w.Code, w.Body)
	}
	if w := call(h, "GET", "/assets/app.js", nil, false); w.Code != 200 || !strings.Contains(w.Body.String(), "console.log") {
		t.Fatalf("asset: %d %q", w.Code, w.Body)
	}
	if w := call(h, "GET", "/api/nope", nil, false); w.Code != 401 {
		t.Fatalf("unknown api path anon should be 401, got %d", w.Code)
	}
	if w := call(h, "GET", "/api/nope", e, false); w.Code != 404 || strings.Contains(w.Body.String(), "SPA") {
		t.Fatalf("unknown api path authed: %d", w.Code)
	}
}
