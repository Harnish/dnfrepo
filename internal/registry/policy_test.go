package registry

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"

	"dnfrepo/internal/access"
	"dnfrepo/internal/store"
)

type fakeStore struct {
	rows   map[string]store.RegistryRepo
	ips    map[string][]netip.Prefix
	users  map[string]string // "repo|user" -> token
	pushes map[string]string // name -> token
	ensure []string
}

func newFake() *fakeStore {
	return &fakeStore{rows: map[string]store.RegistryRepo{}, ips: map[string][]netip.Prefix{}, users: map[string]string{}, pushes: map[string]string{}}
}

func (f *fakeStore) RegistryRepoByName(_ context.Context, name string) (store.RegistryRepo, error) {
	if r, ok := f.rows[name]; ok {
		return r, nil
	}
	return store.RegistryRepo{Name: name, Mode: store.ModePublic}, nil
}
func (f *fakeStore) EnsureRegistryRepo(_ context.Context, name string) error {
	f.ensure = append(f.ensure, name)
	if _, ok := f.rows[name]; !ok {
		f.rows[name] = store.RegistryRepo{Name: name, Mode: store.ModePublic, Exists: true}
	}
	return nil
}
func (f *fakeStore) RegistryIPs(_ context.Context, name string) ([]netip.Prefix, error) {
	return f.ips[name], nil
}
func (f *fakeStore) CheckRegistryUser(_ context.Context, name, user, tok string) (bool, error) {
	t, ok := f.users[name+"|"+user]
	return ok && t == tok, nil
}
func (f *fakeStore) CheckPushToken(_ context.Context, user, tok string) (bool, error) {
	t, ok := f.pushes[user]
	return ok && t == tok, nil
}

func newPolicy(f *fakeStore, existing ...string) *Policy {
	set := map[string]bool{}
	for _, e := range existing {
		set[e] = true
	}
	return &Policy{Store: f, Throttle: access.NewThrottle(),
		Exists: func(_ context.Context, n string) bool { return set[n] || f.rows[n].Exists }}
}

func req(user, pass string) *http.Request {
	r := httptest.NewRequest("GET", "/v2/x/manifests/latest", nil)
	if user != "" {
		r.SetBasicAuth(user, pass)
	}
	return r
}

var (
	pull = func(n string) []Access { return []Access{{n, "pull"}} }
	push = func(n string) []Access { return []Access{{n, "push"}} }
)

func TestAnonPullPublicAllowed(t *testing.T) {
	f := newFake()
	f.rows["pub"] = store.RegistryRepo{Name: "pub", Mode: store.ModePublic, Exists: true}
	if v := newPolicy(f).Authorize(req("", ""), pull("pub")); v != Allow {
		t.Fatalf("got %v", v)
	}
}

func TestAnonPullExistingImageWithoutRow(t *testing.T) {
	// every image at cutover: in storage, no settings row
	if v := newPolicy(newFake(), "old/img").Authorize(req("", ""), pull("old/img")); v != Allow {
		t.Fatalf("got %v", v)
	}
}

func TestAnonPullIdenticalDenials(t *testing.T) {
	f := newFake()
	f.rows["priv"] = store.RegistryRepo{Name: "priv", Mode: store.ModeUsers, Exists: true}
	f.rows["lan"] = store.RegistryRepo{Name: "lan", Mode: store.ModeIPs, Exists: true}
	f.ips["lan"] = []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}
	p := newPolicy(f)
	for _, n := range []string{"priv", "lan", "ghost"} {
		if v := p.Authorize(req("", ""), pull(n)); v != Challenge {
			t.Errorf("%s: got %v want Challenge", n, v)
		}
	}
}

func TestUsersModeNeedsGrant(t *testing.T) {
	f := newFake()
	f.rows["priv"] = store.RegistryRepo{Name: "priv", Mode: store.ModeUsers, Exists: true}
	f.users["priv|alice"] = "tok"
	p := newPolicy(f)
	if v := p.Authorize(req("alice", "tok"), pull("priv")); v != Allow {
		t.Fatalf("granted: %v", v)
	}
	if v := p.Authorize(req("alice", "bad"), pull("priv")); v != Challenge {
		t.Fatalf("bad token: %v", v)
	}
	if v := p.Authorize(req("bob", "tok"), pull("priv")); v != Challenge {
		t.Fatalf("not granted: %v", v)
	}
	if v := p.Authorize(req("alice", "tok"), pull("other")); v != Challenge {
		t.Fatalf("other repo: %v", v)
	}
}

func TestIPsModeUsesClientIP(t *testing.T) {
	f := newFake()
	f.rows["lan"] = store.RegistryRepo{Name: "lan", Mode: store.ModeIPs, Exists: true}
	f.ips["lan"] = []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}
	p := newPolicy(f)
	r := req("", "")
	r.RemoteAddr = "10.1.2.3:5555"
	if v := p.Authorize(r, pull("lan")); v != Allow {
		t.Fatalf("in range: %v", v)
	}
	r = req("", "")
	r.RemoteAddr = "192.0.2.9:5555"
	r.Header.Set("X-Forwarded-For", "10.1.2.3") // spoofed, peer is not a trusted proxy
	if v := p.Authorize(r, pull("lan")); v != Challenge {
		t.Fatalf("untrusted XFF honoured: %v", v)
	}
	p.Trusted = []netip.Prefix{netip.MustParsePrefix("192.0.2.0/24")}
	if v := p.Authorize(r, pull("lan")); v != Allow {
		t.Fatalf("trusted XFF ignored: %v", v)
	}
}

func TestPushNeedsPushToken(t *testing.T) {
	f := newFake()
	f.pushes["ci"] = "ptok"
	f.users["x|alice"] = "utok"
	p := newPolicy(f)
	for name, r := range map[string]*http.Request{
		"anonymous":  req("", ""),
		"user token": req("alice", "utok"),
		"bad token":  req("ci", "nope"),
	} {
		if v := p.Authorize(r, push("x")); v != Challenge {
			t.Errorf("%s: got %v", name, v)
		}
	}
	if v := p.Authorize(req("ci", "ptok"), push("new/img")); v != Allow {
		t.Fatalf("push token: %v", v)
	}
}

func TestPushTokenCreatesRow(t *testing.T) {
	f := newFake()
	f.pushes["ci"] = "ptok"
	newPolicy(f).Authorize(req("ci", "ptok"), push("new/img"))
	if len(f.ensure) != 1 || f.ensure[0] != "new/img" {
		t.Fatalf("%v", f.ensure)
	}
	if r := f.rows["new/img"]; r.Mode != store.ModePublic || r.Listed {
		t.Fatalf("%+v", r)
	}
}

func TestPushTokenPullsAnything(t *testing.T) {
	f := newFake()
	f.pushes["ci"] = "ptok"
	f.rows["priv"] = store.RegistryRepo{Name: "priv", Mode: store.ModeUsers, Exists: true}
	if v := newPolicy(f).Authorize(req("ci", "ptok"), pull("priv")); v != Allow {
		t.Fatalf("%v", v)
	}
}

func TestDeleteAlwaysRefused(t *testing.T) {
	f := newFake()
	f.pushes["ci"] = "ptok"
	if v := newPolicy(f, "x").Authorize(req("ci", "ptok"), []Access{{"x", "delete"}}); v != Challenge {
		t.Fatalf("%v", v)
	}
}

func TestThrottleCountsBeforeCheck(t *testing.T) {
	f := newFake()
	f.pushes["ci"] = "ptok"
	f.rows["pub"] = store.RegistryRepo{Name: "pub", Mode: store.ModePublic, Exists: true}
	p := newPolicy(f)
	for i := 0; i < 10; i++ {
		if v := p.Authorize(req("ci", "bad"), push("x")); v != Challenge {
			t.Fatalf("attempt %d: %v", i, v)
		}
	}
	if v := p.Authorize(req("ci", "bad"), push("x")); v != Throttled {
		t.Fatalf("11th attempt: %v", v)
	}
	if v := p.Authorize(req("ci", "ptok"), push("x")); v != Throttled {
		t.Fatalf("correct token while blocked: %v", v)
	}
	if v := p.Authorize(req("", ""), pull("pub")); v != Allow {
		t.Fatalf("anonymous pull from a blocked IP must still work: %v", v)
	}
}

func TestSuccessResetsThrottle(t *testing.T) {
	f := newFake()
	f.pushes["ci"] = "ptok"
	p := newPolicy(f)
	for i := 0; i < 20; i++ {
		if v := p.Authorize(req("ci", "ptok"), push("x")); v != Allow {
			t.Fatalf("legit request %d: %v", i, v)
		}
	}
}

func TestCatalogAllowedForEveryone(t *testing.T) {
	if v := newPolicy(newFake()).Authorize(req("", ""), []Access{{"", "catalog"}}); v != Allow {
		t.Fatalf("%v", v)
	}
}
