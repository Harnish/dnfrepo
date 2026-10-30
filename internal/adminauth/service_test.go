package adminauth

import (
	"context"
	"regexp"
	"testing"
	"time"

	"dnfrepo/internal/config"
	"dnfrepo/internal/store/storetest"
)

type fakeMailer struct {
	sent []struct{ to, subject, body string }
}

func (f *fakeMailer) Send(to, subject, body string) error {
	f.sent = append(f.sent, struct{ to, subject, body string }{to, subject, body})
	return nil
}

var tokRe = regexp.MustCompile(`token=([A-Za-z0-9_-]+)`)

func (f *fakeMailer) lastToken(t *testing.T) string {
	t.Helper()
	m := tokRe.FindStringSubmatch(f.sent[len(f.sent)-1].body)
	if m == nil {
		t.Fatal("no token in mail")
	}
	return m[1]
}

func newSvc(t *testing.T) (*Service, *fakeMailer) {
	st := storetest.New(t)
	fm := &fakeMailer{}
	return New(st, fm, config.Config{BaseURL: "http://x", BootstrapAdminEmail: "a@x.org"}), fm
}

func TestBootstrapSendsVerificationOnce(t *testing.T) {
	s, fm := newSvc(t)
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if err := s.Bootstrap(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if len(fm.sent) != 1 || fm.sent[0].to != "a@x.org" {
		t.Fatalf("sent=%v", fm.sent)
	}
}

func TestLoginRejectedUntilVerified(t *testing.T) {
	s, fm := newSvc(t)
	ctx := context.Background()
	s.Bootstrap(ctx)
	if _, err := s.Login(ctx, "a@x.org", "whatever12"); err == nil {
		t.Fatal("login before verify should fail")
	}
	if err := s.VerifyAndSetPassword(ctx, fm.lastToken(t), "correct-horse"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Login(ctx, "a@x.org", "wrong-password"); err == nil {
		t.Fatal("wrong password should fail")
	}
	tok, err := s.Login(ctx, "a@x.org", "correct-horse")
	if err != nil || tok == "" {
		t.Fatalf("login: %v", err)
	}
}

func TestVerifyTokenSingleUse(t *testing.T) {
	s, fm := newSvc(t)
	ctx := context.Background()
	s.Bootstrap(ctx)
	tok := fm.lastToken(t)
	if err := s.VerifyAndSetPassword(ctx, tok, "correct-horse"); err != nil {
		t.Fatal(err)
	}
	if err := s.VerifyAndSetPassword(ctx, tok, "another-pass1"); err == nil {
		t.Fatal("token reuse should fail")
	}
}

func TestInviteRequiresExistingAdmin(t *testing.T) {
	s, fm := newSvc(t)
	ctx := context.Background()
	if err := s.Invite(ctx, "nobody@x.org", "b@x.org"); err == nil {
		t.Fatal("invite by non-admin should fail")
	}
	s.Bootstrap(ctx)
	s.VerifyAndSetPassword(ctx, fm.lastToken(t), "correct-horse")
	if err := s.Invite(ctx, "a@x.org", "b@x.org"); err != nil {
		t.Fatal(err)
	}
	if fm.sent[len(fm.sent)-1].to != "b@x.org" {
		t.Fatal("invite mail not sent to invitee")
	}
}

func TestPasswordHashedNotStored(t *testing.T) {
	s, fm := newSvc(t)
	ctx := context.Background()
	s.Bootstrap(ctx)
	s.VerifyAndSetPassword(ctx, fm.lastToken(t), "correct-horse")
	var h string
	if err := s.st.Pool().QueryRow(ctx, `SELECT password_hash FROM admins WHERE email='a@x.org'`).Scan(&h); err != nil {
		t.Fatal(err)
	}
	if h == "" || h == "correct-horse" {
		t.Fatalf("hash=%q", h)
	}
}

func TestShortPasswordRejected(t *testing.T) {
	s, fm := newSvc(t)
	ctx := context.Background()
	s.Bootstrap(ctx)
	if err := s.VerifyAndSetPassword(ctx, fm.lastToken(t), "short"); err == nil {
		t.Fatal("short password should fail")
	}
}

func TestBadVerifyTokenDoesNotTakeHashSlot(t *testing.T) {
	s, _ := newSvc(t)
	for i := 0; i < cap(s.hashSem); i++ {
		s.hashSem <- struct{}{} // every argon2 slot busy
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- s.VerifyAndSetPassword(ctx, "garbage-token", "long-enough-pw") }()
	select {
	case err := <-done:
		if err != ErrBadToken {
			t.Fatalf("got %v", err)
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("bad token waited for a hash slot")
	}
}

func TestLoginHonoursContextWhileWaitingForHashSlot(t *testing.T) {
	s, fm := newSvc(t)
	s.Bootstrap(context.Background())
	s.VerifyAndSetPassword(context.Background(), fm.lastToken(t), "correct-horse")
	for i := 0; i < cap(s.hashSem); i++ {
		s.hashSem <- struct{}{}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := s.Login(ctx, "a@x.org", "correct-horse")
	if err == nil || time.Since(start) > time.Second {
		t.Fatalf("err=%v after %v", err, time.Since(start))
	}
}
