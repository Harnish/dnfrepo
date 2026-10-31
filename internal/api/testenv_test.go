package api

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"dnfrepo/internal/adminauth"
	"dnfrepo/internal/config"
	"dnfrepo/internal/store"
	"dnfrepo/internal/store/storetest"
)

// fakeIDP approves everyone as a member of dnfrepo-admins.
var fakeIDP = adminauth.IDP{
	AuthURL: func(state, nonce, challenge string) (string, error) {
		return "https://idp.test/authorize?state=" + url.QueryEscape(state), nil
	},
	Exchange: func(context.Context, string, string, string) (adminauth.Claims, error) {
		return adminauth.Claims{Email: "admin@x.org", Groups: []string{"dnfrepo-admins"}}, nil
	},
}

// env is a logged-in admin session plus a mux that tests register routes on.
type env struct {
	St     *store.Store
	Admin  *adminauth.Service
	Mux    *http.ServeMux
	Cookie *http.Cookie
}

func newEnv(t *testing.T) *env {
	t.Helper()
	st := storetest.New(t)
	svc := adminauth.New(st, config.Config{BaseURL: "http://x", OIDCAdminGroup: "dnfrepo-admins"}, fakeIDP)
	pub := http.NewServeMux()
	svc.PublicRoutes(pub)

	// sign in through the real login + callback flow
	w := httptest.NewRecorder()
	pub.ServeHTTP(w, httptest.NewRequest("GET", "/api/auth/oidc/login", nil))
	loc, err := url.Parse(w.Header().Get("Location"))
	if err != nil || w.Code != http.StatusFound {
		t.Fatalf("login: %d %v", w.Code, err)
	}
	r := httptest.NewRequest("GET", "/api/auth/oidc/callback?code=c&state="+url.QueryEscape(loc.Query().Get("state")), nil)
	for _, c := range w.Result().Cookies() {
		r.AddCookie(c)
	}
	w = httptest.NewRecorder()
	pub.ServeHTTP(w, r)
	var sess *http.Cookie
	for _, c := range w.Result().Cookies() {
		if c.Name == adminauth.SessionCookie && c.Value != "" {
			sess = c
		}
	}
	if sess == nil {
		t.Fatalf("callback gave no session: %d %s", w.Code, w.Body)
	}
	return &env{St: st, Admin: svc, Mux: http.NewServeMux(), Cookie: sess}
}

func (e *env) handler() http.Handler { return e.Admin.RequireAdmin(adminauth.CSRF(e.Mux)) }

// Do sends an authenticated request with the CSRF header.
func (e *env) Do(method, path, body string) *httptest.ResponseRecorder {
	return e.do(method, path, body, true)
}

// DoAnon sends a request with no session.
func (e *env) DoAnon(method, path, body string) *httptest.ResponseRecorder {
	return e.do(method, path, body, false)
}

func (e *env) do(method, path, body string, auth bool) *httptest.ResponseRecorder {
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	r := httptest.NewRequest(method, path, rd)
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	if auth {
		r.AddCookie(e.Cookie)
		r.Header.Set("X-Requested-With", "dnfrepo")
	}
	w := httptest.NewRecorder()
	e.handler().ServeHTTP(w, r)
	return w
}
