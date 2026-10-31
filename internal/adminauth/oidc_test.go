package adminauth

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"

	"dnfrepo/internal/config"
)

// fakeProvider is a minimal OIDC provider: discovery, JWKS and a token endpoint that signs ID tokens.
type fakeProvider struct {
	srv      *httptest.Server
	key      *rsa.PrivateKey
	aud      string // audience put in the ID token
	nonce    string // nonce put in the ID token
	groups   []string
	gotForm  url.Values
	notAfter time.Time
	down     atomic.Bool // discovery answers 503 while set
}

func newFakeProvider(t *testing.T) *fakeProvider {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	p := &fakeProvider{key: key, aud: "client-id", nonce: "n1", groups: []string{"dnfrepo-admins"}, notAfter: time.Now().Add(time.Hour)}
	mux := http.NewServeMux()
	p.srv = httptest.NewServer(mux)
	t.Cleanup(p.srv.Close)
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		if p.down.Load() {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"issuer": p.srv.URL, "authorization_endpoint": p.srv.URL + "/authorize",
			"token_endpoint": p.srv.URL + "/token", "jwks_uri": p.srv.URL + "/jwks", "id_token_signing_alg_values_supported": []string{"RS256"}})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &key.PublicKey, KeyID: "k1", Algorithm: "RS256", Use: "sig"}}})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		p.gotForm = r.PostForm
		signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: key}, (&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", "k1"))
		if err != nil {
			t.Error(err)
		}
		payload, _ := json.Marshal(map[string]any{"iss": p.srv.URL, "aud": p.aud, "sub": "u1", "exp": p.notAfter.Unix(),
			"iat": time.Now().Unix(), "nonce": p.nonce, "email": "joe@example.org", "groups": p.groups})
		jws, _ := signer.Sign(payload)
		idt, _ := jws.CompactSerialize()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"access_token": "at", "token_type": "Bearer", "id_token": idt})
	})
	return p
}

func (p *fakeProvider) idp(t *testing.T) IDP {
	t.Helper()
	idp, err := DiscoverIDP(t.Context(), config.Config{BaseURL: "https://dnf.example.org", OIDCIssuer: p.srv.URL,
		OIDCClientID: "client-id", OIDCClientSecret: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	return idp
}

func TestAuthURLCarriesPKCEStateNonceAndRedirect(t *testing.T) {
	p := newFakeProvider(t)
	raw, err := p.idp(t).AuthURL("st", "n1", "chal")
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	want := map[string]string{"client_id": "client-id", "response_type": "code", "state": "st", "nonce": "n1",
		"code_challenge": "chal", "code_challenge_method": "S256", "redirect_uri": "https://dnf.example.org/api/auth/oidc/callback"}
	for k, v := range want {
		if q.Get(k) != v {
			t.Errorf("%s=%q want %q", k, q.Get(k), v)
		}
	}
	for _, sc := range []string{"openid", "email", "profile"} {
		if !slices.Contains(strings.Fields(q.Get("scope")), sc) {
			t.Errorf("scope %q lacks %s", q.Get("scope"), sc)
		}
	}
}

func TestLazyIDPRecoversWhenProviderComesUp(t *testing.T) {
	p := newFakeProvider(t)
	p.down.Store(true)
	idp := LazyIDP(config.Config{BaseURL: "https://dnf.example.org", OIDCIssuer: p.srv.URL, OIDCClientID: "client-id", OIDCClientSecret: "secret"})
	if _, err := idp.AuthURL("s", "n", "c"); err == nil {
		t.Fatal("want an error while the provider is down")
	}
	if _, err := idp.Exchange(t.Context(), "c", "v", "n"); err == nil {
		t.Fatal("exchange should fail while the provider is down")
	}
	p.down.Store(false)
	if u, err := idp.AuthURL("s", "n", "c"); err != nil || !strings.HasPrefix(u, p.srv.URL+"/authorize") {
		t.Fatalf("after recovery: %q %v", u, err)
	}
}

func TestExchangeReturnsVerifiedClaimsAndSendsVerifier(t *testing.T) {
	p := newFakeProvider(t)
	c, err := p.idp(t).Exchange(t.Context(), "the-code", "the-verifier", "n1")
	if err != nil {
		t.Fatal(err)
	}
	if c.Email != "joe@example.org" || len(c.Groups) != 1 || c.Groups[0] != "dnfrepo-admins" {
		t.Fatalf("claims %+v", c)
	}
	if p.gotForm.Get("code") != "the-code" || p.gotForm.Get("code_verifier") != "the-verifier" {
		t.Fatalf("token request form %v", p.gotForm)
	}
}

func TestExchangeRejectsWrongNonce(t *testing.T) {
	p := newFakeProvider(t)
	p.nonce = "attacker-nonce"
	if _, err := p.idp(t).Exchange(t.Context(), "c", "v", "n1"); err == nil {
		t.Fatal("nonce mismatch accepted")
	}
}

func TestExchangeRejectsWrongAudience(t *testing.T) {
	p := newFakeProvider(t)
	p.aud = "some-other-client"
	if _, err := p.idp(t).Exchange(t.Context(), "c", "v", "n1"); err == nil {
		t.Fatal("token for another client accepted")
	}
}

func TestExchangeRejectsExpiredToken(t *testing.T) {
	p := newFakeProvider(t)
	p.notAfter = time.Now().Add(-time.Hour)
	if _, err := p.idp(t).Exchange(t.Context(), "c", "v", "n1"); err == nil {
		t.Fatal("expired token accepted")
	}
}
