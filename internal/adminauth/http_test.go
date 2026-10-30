package adminauth

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func loginReq(h http.Handler, remote, body string) int {
	r := httptest.NewRequest("POST", "/api/auth/login", strings.NewReader(body))
	r.RemoteAddr = remote
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w.Code
}

func verifiedSvc(t *testing.T) http.Handler {
	s, fm := newSvc(t)
	s.Bootstrap(t.Context())
	if err := s.VerifyAndSetPassword(t.Context(), fm.lastToken(t), "correct-horse"); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	s.PublicRoutes(mux)
	return mux
}

func TestLoginThrottled(t *testing.T) {
	h := verifiedSvc(t)
	bad := `{"email":"a@x.org","password":"wrong-password"}`
	for i := 0; i < 10; i++ {
		if c := loginReq(h, "9.9.9.9:1", bad); c != 401 {
			t.Fatalf("attempt %d: %d", i, c)
		}
	}
	good := `{"email":"a@x.org","password":"correct-horse"}`
	if c := loginReq(h, "9.9.9.9:1", good); c != 429 {
		t.Fatalf("blocked IP with valid creds: %d", c)
	}
	if c := loginReq(h, "8.8.8.8:1", good); c != 200 {
		t.Fatalf("other IP: %d", c)
	}
}

func TestAuthBodyLimited(t *testing.T) {
	h := verifiedSvc(t)
	huge := `{"email":"` + strings.Repeat("a", 2<<20) + `","password":"x"}`
	if c := loginReq(h, "7.7.7.7:1", huge); c != 400 {
		t.Fatalf("oversized body: %d", c)
	}
}
