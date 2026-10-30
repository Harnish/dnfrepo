// Package adminauth handles admin accounts: email verification, passwords, sessions.
package adminauth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/argon2"

	"dnfrepo/internal/access"
	"dnfrepo/internal/config"
	"dnfrepo/internal/store"
)

const (
	SessionCookie = "dnfrepo_session"
	sessionTTL    = 14 * 24 * time.Hour
	minPassword   = 8
)

var (
	ErrForbidden   = errors.New("forbidden")
	ErrBadCreds    = errors.New("invalid email or password")
	ErrBadToken    = errors.New("invalid or used token")
	ErrShortPasswd = fmt.Errorf("password must be at least %d characters", minPassword)
)

type Service struct {
	st        *store.Store
	mail      Mailer
	baseURL   string
	bootstrap string
	trusted   []netip.Prefix
	th        *access.Throttle // failed-login throttle, per client IP
	hashSem   chan struct{}    // bounds concurrent argon2 runs (64 MiB each)
}

func New(st *store.Store, m Mailer, cfg config.Config) *Service {
	return &Service{st: st, mail: m, baseURL: cfg.BaseURL, bootstrap: strings.ToLower(cfg.BootstrapAdminEmail),
		trusted: cfg.TrustedProxies, th: access.NewThrottle(), hashSem: make(chan struct{}, 4)}
}

// dummyHash is checked when the email is unknown so unknown and known emails take equally long.
var (
	dummyOnce sync.Once
	dummyHash string
)

// acquire takes an argon2 slot, giving up if ctx ends first.
func (s *Service) acquire(ctx context.Context) (release func(), err error) {
	select {
	case s.hashSem <- struct{}{}:
		return func() { <-s.hashSem }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (s *Service) hash(ctx context.Context, pw string) (string, error) {
	release, err := s.acquire(ctx)
	if err != nil {
		return "", err
	}
	defer release()
	return hashPassword(pw), nil
}

func (s *Service) check(ctx context.Context, pw, enc string) (bool, error) {
	release, err := s.acquire(ctx)
	if err != nil {
		return false, err
	}
	defer release()
	return checkPassword(pw, enc), nil
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

func hashPassword(pw string) string {
	salt := make([]byte, 16)
	rand.Read(salt)
	key := argon2.IDKey([]byte(pw), salt, 1, 64*1024, 2, 32)
	return "argon2id$" + base64.RawStdEncoding.EncodeToString(salt) + "$" + base64.RawStdEncoding.EncodeToString(key)
}

func checkPassword(pw, enc string) bool {
	p := strings.Split(enc, "$")
	if len(p) != 3 || p[0] != "argon2id" {
		return false
	}
	salt, err1 := base64.RawStdEncoding.DecodeString(p[1])
	want, err2 := base64.RawStdEncoding.DecodeString(p[2])
	if err1 != nil || err2 != nil {
		return false
	}
	got := argon2.IDKey([]byte(pw), salt, 1, 64*1024, 2, 32)
	return subtle.ConstantTimeCompare(got, want) == 1
}

// invite creates or refreshes an unverified admin and mails a set-password link.
func (s *Service) invite(ctx context.Context, email string) error {
	email = strings.ToLower(strings.TrimSpace(email))
	if !strings.Contains(email, "@") {
		return fmt.Errorf("invalid email")
	}
	tok := randToken()
	tag, err := s.st.Pool().Exec(ctx, `
		INSERT INTO admins(email, verify_token_hash) VALUES($1,$2)
		ON CONFLICT (email) DO UPDATE SET verify_token_hash=$2 WHERE admins.verified=false`, email, hashToken(tok))
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%s is already an admin", email)
	}
	return s.mail.Send(email, "Verify your dnfrepo admin account",
		fmt.Sprintf("Set your password and verify your email:\n\n%s/verify?token=%s\n", s.baseURL, tok))
}

// Bootstrap creates the first admin from BOOTSTRAP_ADMIN_EMAIL when none exist.
func (s *Service) Bootstrap(ctx context.Context) error {
	if s.bootstrap == "" {
		return nil
	}
	var n int
	if err := s.st.Pool().QueryRow(ctx, `SELECT count(*) FROM admins`).Scan(&n); err != nil || n > 0 {
		return err
	}
	return s.invite(ctx, s.bootstrap)
}

// Invite lets a verified admin invite another.
func (s *Service) Invite(ctx context.Context, actor, email string) error {
	var ok bool
	if err := s.st.Pool().QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM admins WHERE email=$1 AND verified)`, strings.ToLower(actor)).Scan(&ok); err != nil {
		return err
	}
	if !ok {
		return ErrForbidden
	}
	return s.invite(ctx, email)
}

func (s *Service) VerifyAndSetPassword(ctx context.Context, token, password string) error {
	if len(password) < minPassword {
		return ErrShortPasswd
	}
	// check the token first so garbage tokens never spend an argon2 slot
	var known bool
	if err := s.st.Pool().QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM admins WHERE verify_token_hash=$1)`, hashToken(token)).Scan(&known); err != nil {
		return err
	}
	if !known {
		return ErrBadToken
	}
	pwHash, err := s.hash(ctx, password)
	if err != nil {
		return err
	}
	tag, err := s.st.Pool().Exec(ctx, `
		UPDATE admins SET verified=true, password_hash=$2, verify_token_hash=NULL
		WHERE verify_token_hash=$1`, hashToken(token), pwHash)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrBadToken
	}
	return nil
}

func (s *Service) Login(ctx context.Context, email, password string) (string, error) {
	var id int64
	var hash *string
	var verified bool
	err := s.st.Pool().QueryRow(ctx, `SELECT id, password_hash, verified FROM admins WHERE email=$1`, strings.ToLower(email)).Scan(&id, &hash, &verified)
	if err != nil || hash == nil {
		dummyOnce.Do(func() { dummyHash = hashPassword("dummy-password") })
		if _, err := s.check(ctx, password, dummyHash); err != nil {
			return "", err
		}
		return "", ErrBadCreds
	}
	ok, err := s.check(ctx, password, *hash)
	if err != nil {
		return "", err
	}
	if !ok || !verified {
		return "", ErrBadCreds
	}
	tok := randToken()
	_, err = s.st.Pool().Exec(ctx, `INSERT INTO admin_sessions(token_hash, admin_id, expires_at) VALUES($1,$2,$3)`,
		hashToken(tok), id, time.Now().Add(sessionTTL))
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
	err = s.st.Pool().QueryRow(r.Context(), `
		SELECT a.email FROM admin_sessions s JOIN admins a ON a.id=s.admin_id
		WHERE s.token_hash=$1 AND s.expires_at > now()`, hashToken(c.Value)).Scan(&email)
	return email, err == nil
}
