package api

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"dnfrepo/internal/adminauth"
	"dnfrepo/internal/config"
	"dnfrepo/internal/store"
	"dnfrepo/internal/store/storetest"
)

type capMailer struct{ last string }

func (c *capMailer) Send(to, subject, body string) error { c.last = body; return nil }

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
	cm := &capMailer{}
	svc := adminauth.New(st, cm, config.Config{BaseURL: "http://x", BootstrapAdminEmail: "admin@x.org"})
	ctx := context.Background()
	if err := svc.Bootstrap(ctx); err != nil {
		t.Fatal(err)
	}
	tok := regexp.MustCompile(`token=([A-Za-z0-9_-]+)`).FindStringSubmatch(cm.last)[1]
	if err := svc.VerifyAndSetPassword(ctx, tok, "correct-horse"); err != nil {
		t.Fatal(err)
	}
	sess, err := svc.Login(ctx, "admin@x.org", "correct-horse")
	if err != nil {
		t.Fatal(err)
	}
	return &env{St: st, Admin: svc, Mux: http.NewServeMux(), Cookie: &http.Cookie{Name: adminauth.SessionCookie, Value: sess}}
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
