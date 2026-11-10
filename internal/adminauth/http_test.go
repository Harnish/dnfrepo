package adminauth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"dnfrepo/internal/config"
	"dnfrepo/internal/store/storetest"
)

// fakeIDP stands in for Authentik: it records what the service sent and returns canned claims.
type fakeIDP struct {
	claims                                  Claims
	err                                     error
	authErr                                 error  // AuthURL fails (provider down)
	nonce, challenge                        string // seen by AuthURL
	gotCode, gotVerifier, gotNonce, authURL string // seen by Exchange / AuthURL
}

func (f *fakeIDP) idp() IDP {
	return IDP{
		AuthURL: func(state, nonce, challenge string) (string, error) {
			f.nonce, f.challenge = nonce, challenge
			f.authURL = "https://idp.test/authorize?state=" + url.QueryEscape(state)
			return f.authURL, f.authErr
		},
		Exchange: func(_ context.Context, code, verifier, nonce string) (Claims, error) {
			f.gotCode, f.gotVerifier, f.gotNonce = code, verifier, nonce
			return f.claims, f.err
		},
	}
}

func newHandler(t *testing.T, f *fakeIDP) (*Service, http.Handler) {
	t.Helper()
	s := New(storetest.New(t), config.Config{BaseURL: "http://x", OIDCAdminGroup: "dnfrepo-admins"}, f.idp())
	mux := http.NewServeMux()
	s.PublicRoutes(mux)
	s.AdminRoutes(mux)
	return s, mux
}

func admin(email string) Claims {
	return Claims{Email: email, Groups: []string{"other", "dnfrepo-admins"}}
}

// startLogin hits the login endpoint and returns the state sent to the IdP and the flow cookie.
func startLogin(t *testing.T, h http.Handler) (state string, flow *http.Cookie) {
	t.Helper()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/api/auth/oidc/login", nil))
	if w.Code != http.StatusFound {
		t.Fatalf("login: %d", w.Code)
	}
	loc, err := url.Parse(w.Header().Get("Location"))
	if err != nil || loc.Host != "idp.test" {
		t.Fatalf("redirect %q", w.Header().Get("Location"))
	}
	for _, c := range w.Result().Cookies() {
		if c.Name == oidcCookie {
			flow = c
		}
	}
	if flow == nil || !flow.HttpOnly {
		t.Fatalf("flow cookie missing or not HttpOnly: %+v", flow)
	}
	return loc.Query().Get("state"), flow
}

func callback(h http.Handler, remote, state, code string, flow *http.Cookie) *httptest.ResponseRecorder {
	r := httptest.NewRequest("GET", "/api/auth/oidc/callback?state="+url.QueryEscape(state)+"&code="+code, nil)
	r.RemoteAddr = remote
	if flow != nil {
		r.AddCookie(flow)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func sessionCookie(w *httptest.ResponseRecorder) *http.Cookie {
	for _, c := range w.Result().Cookies() {
		if c.Name == SessionCookie && c.Value != "" {
			return c
		}
	}
	return nil
}

func TestLoginIs503WhenProviderUnavailable(t *testing.T) {
	f := &fakeIDP{authErr: errors.New("dial tcp: connection refused to authentik.internal")}
	_, h := newHandler(t, f)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/api/auth/oidc/login", nil))
	if w.Code != http.StatusServiceUnavailable || w.Header().Get("Location") != "" || strings.Contains(w.Body.String(), "authentik.internal") {
		t.Fatalf("provider down: %d %q", w.Code, w.Body.String())
	}
}

func TestAdminGroupMemberGetsSession(t *testing.T) {
	f := &fakeIDP{claims: admin("Joe@Example.org")}
	s, h := newHandler(t, f)
	state, flow := startLogin(t, h)
	w := callback(h, "1.1.1.1:1", state, "abc", flow)
	if w.Code != http.StatusFound || w.Header().Get("Location") != "/" {
		t.Fatalf("callback: %d %q", w.Code, w.Header().Get("Location"))
	}
	c := sessionCookie(w)
	if c == nil || !c.HttpOnly || c.SameSite != http.SameSiteLaxMode {
		t.Fatalf("session cookie: %+v", c)
	}
	r := httptest.NewRequest("GET", "/api/auth/me", nil)
	r.AddCookie(c)
	if email, ok := s.Authenticate(r); !ok || email != "joe@example.org" {
		t.Fatalf("authenticate: %q %v", email, ok)
	}
}

func TestNonMemberRejectedNoSession(t *testing.T) {
	f := &fakeIDP{claims: Claims{Email: "eve@example.org", Groups: []string{"dnfrepo-admins-not", "users"}}}
	_, h := newHandler(t, f)
	state, flow := startLogin(t, h)
	w := callback(h, "1.1.1.1:1", state, "abc", flow)
	if w.Code != http.StatusForbidden || sessionCookie(w) != nil {
		t.Fatalf("non-member: %d cookie=%v", w.Code, sessionCookie(w))
	}
}

func TestEmptyEmailRejected(t *testing.T) {
	f := &fakeIDP{claims: Claims{Groups: []string{"dnfrepo-admins"}}}
	_, h := newHandler(t, f)
	state, flow := startLogin(t, h)
	if w := callback(h, "1.1.1.1:1", state, "abc", flow); w.Code != http.StatusForbidden || sessionCookie(w) != nil {
		t.Fatalf("empty email: %d", w.Code)
	}
}

func TestStateMismatchAndMissingCookieRejected(t *testing.T) {
	f := &fakeIDP{claims: admin("a@x.org")}
	_, h := newHandler(t, f)
	state, flow := startLogin(t, h)
	if w := callback(h, "1.1.1.1:1", "forged-state", "abc", flow); w.Code != http.StatusBadRequest || sessionCookie(w) != nil {
		t.Fatalf("forged state: %d", w.Code)
	}
	if w := callback(h, "1.1.1.1:1", state, "abc", nil); w.Code != http.StatusBadRequest || sessionCookie(w) != nil {
		t.Fatalf("no flow cookie: %d", w.Code)
	}
	if f.gotCode != "" {
		t.Fatal("exchange ran despite bad state")
	}
}

func TestExchangeGetsCodeVerifierAndNonceAndPKCEMatches(t *testing.T) {
	f := &fakeIDP{claims: admin("a@x.org")}
	_, h := newHandler(t, f)
	state, flow := startLogin(t, h)
	callback(h, "1.1.1.1:1", state, "the-code", flow)
	if f.gotCode != "the-code" || f.gotNonce != f.nonce || f.gotNonce == "" {
		t.Fatalf("code=%q nonce=%q want %q", f.gotCode, f.gotNonce, f.nonce)
	}
	sum := sha256.Sum256([]byte(f.gotVerifier))
	if f.gotVerifier == "" || base64.RawURLEncoding.EncodeToString(sum[:]) != f.challenge {
		t.Fatalf("challenge %q does not match verifier %q", f.challenge, f.gotVerifier)
	}
}

func TestFlowCookieClearedAfterCallback(t *testing.T) {
	f := &fakeIDP{claims: admin("a@x.org")}
	_, h := newHandler(t, f)
	state, flow := startLogin(t, h)
	w := callback(h, "1.1.1.1:1", state, "abc", flow)
	cleared := false
	for _, c := range w.Result().Cookies() {
		if c.Name == oidcCookie && c.MaxAge < 0 {
			cleared = true
		}
	}
	if !cleared {
		t.Fatal("flow cookie not cleared after callback")
	}
}

func TestExchangeFailureIs401NoSession(t *testing.T) {
	f := &fakeIDP{err: errors.New("bad id token")}
	_, h := newHandler(t, f)
	state, flow := startLogin(t, h)
	w := callback(h, "1.1.1.1:1", state, "abc", flow)
	if w.Code != http.StatusUnauthorized || sessionCookie(w) != nil || strings.Contains(w.Body.String(), "bad id token") {
		t.Fatalf("exchange failure: %d %q", w.Code, w.Body.String())
	}
}

func TestLogoutRevokesSession(t *testing.T) {
	f := &fakeIDP{claims: admin("a@x.org")}
	s, h := newHandler(t, f)
	state, flow := startLogin(t, h)
	c := sessionCookie(callback(h, "1.1.1.1:1", state, "abc", flow))
	r := httptest.NewRequest("POST", "/api/auth/logout", nil)
	r.AddCookie(c)
	h.ServeHTTP(httptest.NewRecorder(), r)
	r = httptest.NewRequest("GET", "/api/auth/me", nil)
	r.AddCookie(c)
	if _, ok := s.Authenticate(r); ok {
		t.Fatal("session still valid after logout")
	}
}

func TestCallbackThrottledPerIP(t *testing.T) {
	f := &fakeIDP{claims: admin("a@x.org")}
	_, h := newHandler(t, f)
	for i := 0; i < 10; i++ {
		if c := callback(h, "9.9.9.9:1", "bad", "x", nil).Code; c != 400 {
			t.Fatalf("attempt %d: %d", i, c)
		}
	}
	state, flow := startLogin(t, h)
	if c := callback(h, "9.9.9.9:1", state, "abc", flow).Code; c != 429 {
		t.Fatalf("blocked IP with valid flow: %d", c)
	}
	if c := callback(h, "8.8.8.8:1", state, "abc", flow).Code; c != 302 {
		t.Fatalf("other IP: %d", c)
	}
}

func localHandler(t *testing.T) (*Service, http.Handler) {
	t.Helper()
	s := New(storetest.New(t), config.Config{BaseURL: "http://x", AdminEmail: "Joe@Home.lan", AdminPassword: "s3cret"}, (&fakeIDP{}).idp())
	mux := http.NewServeMux()
	s.PublicRoutes(mux)
	s.AdminRoutes(mux)
	return s, mux
}

func localLogin(h http.Handler, remote, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", "/api/auth/local", strings.NewReader(body))
	r.RemoteAddr = remote
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestLocalLoginGivesSession(t *testing.T) {
	s, h := localHandler(t)
	w := localLogin(h, "1.1.1.1:1", `{"email":" joe@home.lan ","password":"s3cret"}`)
	c := sessionCookie(w)
	if w.Code != http.StatusOK || c == nil || !c.HttpOnly {
		t.Fatalf("local login: %d %+v", w.Code, c)
	}
	r := httptest.NewRequest("GET", "/api/auth/me", nil)
	r.AddCookie(c)
	if email, ok := s.Authenticate(r); !ok || email != "joe@home.lan" {
		t.Fatalf("authenticate: %q %v", email, ok)
	}
}

func TestLocalLoginRejectsBadCredentials(t *testing.T) {
	_, h := localHandler(t)
	for _, b := range []string{`{"email":"joe@home.lan","password":"nope"}`, `{"email":"eve@home.lan","password":"s3cret"}`, `{}`, `junk`} {
		if w := localLogin(h, "1.1.1.1:1", b); w.Code != http.StatusUnauthorized || sessionCookie(w) != nil {
			t.Fatalf("%s: %d", b, w.Code)
		}
	}
}

func TestLocalLoginThrottled(t *testing.T) {
	_, h := localHandler(t)
	var last int
	for i := 0; i < 50; i++ {
		last = localLogin(h, "2.2.2.2:1", `{"email":"joe@home.lan","password":"bad"}`).Code
	}
	if last != http.StatusTooManyRequests {
		t.Fatalf("not throttled: %d", last)
	}
	if w := localLogin(h, "2.2.2.2:1", `{"email":"joe@home.lan","password":"s3cret"}`); w.Code != http.StatusTooManyRequests {
		t.Fatalf("blocked IP got in: %d", w.Code)
	}
}

func TestLocalLoginAbsentUnlessConfigured(t *testing.T) {
	_, h := newHandler(t, &fakeIDP{})
	if w := localLogin(h, "1.1.1.1:1", `{"email":"","password":""}`); w.Code != http.StatusNotFound && w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("local route exists unconfigured: %d", w.Code)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/api/auth/methods", nil))
	if !strings.Contains(w.Body.String(), `"local":false`) {
		t.Fatalf("methods: %s", w.Body.String())
	}
}
