// Package adminauth signs admins in through an OpenID Connect provider (Authentik) and keeps sessions.
package adminauth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"dnfrepo/internal/access"
	"dnfrepo/internal/config"
	"dnfrepo/internal/store"
)

const (
	SessionCookie = "dnfrepo_session"
	oidcCookie    = "dnfrepo_oidc" // state.nonce.verifier for the login in flight
	sessionTTL    = 12 * time.Hour
	flowTTL       = 10 * time.Minute
)

// Claims are the ID-token claims dnfrepo uses.
type Claims struct {
	Email  string
	Groups []string
}

// IDP is the identity provider. DiscoverIDP builds the real one; tests substitute a fake.
type IDP struct {
	AuthURL  func(state, nonce, challenge string) (string, error)
	Exchange func(ctx context.Context, code, verifier, nonce string) (Claims, error)
}

type Service struct {
	st      *store.Store
	idp     IDP
	baseURL string
	group   string
	trusted []netip.Prefix
	th      *access.Throttle // failed-callback throttle, per client IP
}

func New(st *store.Store, cfg config.Config, idp IDP) *Service {
	return &Service{st: st, idp: idp, baseURL: cfg.BaseURL, group: cfg.OIDCAdminGroup,
		trusted: cfg.TrustedProxies, th: access.NewThrottle()}
}

func randToken() string {
	b := make([]byte, 32)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func hashToken(t string) string {
	h := sha256.Sum256([]byte(t))
	return hex.EncodeToString(h[:])
}

func (s *Service) cookie(name, val string, maxAge int) *http.Cookie {
	return &http.Cookie{Name: name, Value: val, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode,
		Secure: strings.HasPrefix(s.baseURL, "https://"), MaxAge: maxAge}
}

func (s *Service) newSession(ctx context.Context, email string) (string, error) {
	tok := randToken()
	_, err := s.st.Pool().Exec(ctx, `INSERT INTO admin_sessions(token_hash, email, expires_at) VALUES($1,$2,$3)`,
		hashToken(tok), email, time.Now().Add(sessionTTL))
	return tok, err
}

func (s *Service) Logout(ctx context.Context, token string) {
	s.st.Pool().Exec(ctx, `DELETE FROM admin_sessions WHERE token_hash=$1`, hashToken(token))
}

// Authenticate returns the signed-in admin's email from the session cookie.
func (s *Service) Authenticate(r *http.Request) (string, bool) {
	c, err := r.Cookie(SessionCookie)
	if err != nil || c.Value == "" {
		return "", false
	}
	var email string
	err = s.st.Pool().QueryRow(r.Context(), `SELECT email FROM admin_sessions WHERE token_hash=$1 AND expires_at > now()`,
		hashToken(c.Value)).Scan(&email)
	return email, err == nil
}
