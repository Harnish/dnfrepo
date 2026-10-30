package access

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"

	"dnfrepo/internal/store"
)

type fakeStore struct {
	repos  map[string]store.Repo
	ips    map[int64][]netip.Prefix
	tokens map[string]string // "repoID:user" -> token
}

func (f *fakeStore) RepoBySlug(_ context.Context, slug string) (store.Repo, error) {
	r, ok := f.repos[slug]
	if !ok {
		return r, store.ErrNotFound
	}
	return r, nil
}
func (f *fakeStore) RepoIPs(_ context.Context, id int64) ([]netip.Prefix, error) {
	return f.ips[id], nil
}
func (f *fakeStore) CheckRepoToken(_ context.Context, id int64, user, tok string) (bool, error) {
	want, ok := f.tokens[string(rune('0'+id))+":"+user]
	return ok && want == tok, nil
}

func newFake() *fakeStore {
	return &fakeStore{
		repos: map[string]store.Repo{
			"pub": {ID: 1, Slug: "pub", Mode: store.ModePublic},
			"ipr": {ID: 2, Slug: "ipr", Mode: store.ModeIPs},
			"usr": {ID: 3, Slug: "usr", Mode: store.ModeUsers},
		},
		ips:    map[int64][]netip.Prefix{2: {netip.MustParsePrefix("10.0.0.0/8")}},
		tokens: map[string]string{"3:alice": "secret"},
	}
}

func handler(f *fakeStore, trusted ...netip.Prefix) http.Handler {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })
	return Guard(f, trusted)(func(r *http.Request) string { return r.PathValue("slug") }, next)
}

func get(h http.Handler, slug, remote string, mod func(*http.Request)) *httptest.ResponseRecorder {
	mux := http.NewServeMux()
	mux.Handle("GET /r/{slug}/", h)
	r := httptest.NewRequest("GET", "/r/"+slug+"/x", nil)
	r.RemoteAddr = remote
	if mod != nil {
		mod(r)
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	return w
}

func TestPublicAllows(t *testing.T) {
	if w := get(handler(newFake()), "pub", "1.2.3.4:5", nil); w.Code != 200 {
		t.Fatal(w.Code)
	}
}

func TestUnknownRepo404(t *testing.T) {
	if w := get(handler(newFake()), "nope", "1.2.3.4:5", nil); w.Code != 404 {
		t.Fatal(w.Code)
	}
}

func TestIPsModeAllowsCIDRDeniesOthers(t *testing.T) {
	h := handler(newFake())
	if w := get(h, "ipr", "10.1.2.3:5", nil); w.Code != 200 {
		t.Fatalf("in-range: %d", w.Code)
	}
	if w := get(h, "ipr", "11.1.2.3:5", nil); w.Code != 403 {
		t.Fatalf("out-of-range: %d", w.Code)
	}
}

func TestXFFIgnoredFromUntrustedPeer(t *testing.T) {
	h := handler(newFake())
	w := get(h, "ipr", "11.1.2.3:5", func(r *http.Request) { r.Header.Set("X-Forwarded-For", "10.9.9.9") })
	if w.Code != 403 {
		t.Fatalf("spoofed XFF accepted: %d", w.Code)
	}
}

func TestXFFUsedFromTrustedProxy(t *testing.T) {
	h := handler(newFake(), netip.MustParsePrefix("192.168.0.1/32"))
	w := get(h, "ipr", "192.168.0.1:5", func(r *http.Request) { r.Header.Set("X-Forwarded-For", "10.9.9.9") })
	if w.Code != 200 {
		t.Fatalf("trusted proxy XFF: %d", w.Code)
	}
	// client-supplied extra hop to the left must not win over what the proxy saw
	w = get(h, "ipr", "192.168.0.1:5", func(r *http.Request) { r.Header.Set("X-Forwarded-For", "10.9.9.9, 11.1.1.1") })
	if w.Code != 403 {
		t.Fatalf("left-spoofed XFF accepted: %d", w.Code)
	}
}

func TestIPv4MappedMatchesV4CIDR(t *testing.T) {
	if w := get(handler(newFake()), "ipr", "[::ffff:10.0.0.5]:5", nil); w.Code != 200 {
		t.Fatalf("mapped: %d", w.Code)
	}
}

func basic(user, pass string) func(*http.Request) {
	return func(r *http.Request) { r.SetBasicAuth(user, pass) }
}

func TestUsersModeBasicAuth(t *testing.T) {
	h := handler(newFake())
	w := get(h, "usr", "1.1.1.1:5", nil)
	if w.Code != 401 || w.Header().Get("WWW-Authenticate") == "" {
		t.Fatalf("no creds: %d %v", w.Code, w.Header())
	}
	if w := get(h, "usr", "1.1.1.1:5", basic("alice", "wrong")); w.Code != 401 {
		t.Fatalf("wrong token: %d", w.Code)
	}
	if w := get(h, "usr", "1.1.1.1:5", basic("bob", "secret")); w.Code != 401 {
		t.Fatalf("wrong user: %d", w.Code)
	}
	if w := get(h, "usr", "1.1.1.1:5", basic("alice", "secret")); w.Code != 200 {
		t.Fatalf("valid: %d", w.Code)
	}
}

func TestThrottleAfterTenFailures(t *testing.T) {
	h := handler(newFake())
	for i := 0; i < 10; i++ {
		if w := get(h, "usr", "9.9.9.9:5", basic("alice", "bad")); w.Code != 401 {
			t.Fatalf("attempt %d: %d", i, w.Code)
		}
	}
	if w := get(h, "usr", "9.9.9.9:5", basic("alice", "secret")); w.Code != 429 {
		t.Fatalf("after 10 failures even valid creds should 429, got %d", w.Code)
	}
	if w := get(h, "usr", "8.8.8.8:5", basic("alice", "secret")); w.Code != 200 {
		t.Fatalf("other IP unaffected: %d", w.Code)
	}
}

func TestMissingCredsDoNotCountAsFailures(t *testing.T) {
	h := handler(newFake())
	for i := 0; i < 15; i++ {
		get(h, "usr", "7.7.7.7:5", nil)
	}
	if w := get(h, "usr", "7.7.7.7:5", basic("alice", "secret")); w.Code != 200 {
		t.Fatalf("got %d", w.Code)
	}
}
