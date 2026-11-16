package registry

import (
	"context"
	"encoding/json"
	"net/netip"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"dnfrepo/internal/store"
)

func seed(t *testing.T, r *testReg, name string, listed bool, mode store.AccessMode) {
	t.Helper()
	r.F.rows[name] = store.RegistryRepo{Name: name, Listed: listed, Mode: mode, Exists: true}
	if _, st := pushImage(t, r.Srv.URL, name, "v1", "ci", "ptok"); st != 0 {
		t.Fatalf("seed %s: %d", name, st)
	}
}

func catalog(t *testing.T, r *testReg, user, pass, query string) ([]string, string) {
	t.Helper()
	resp, b := do(t, "GET", r.Srv.URL+"/v2/_catalog"+query, user, pass, nil, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("catalog %d %s", resp.StatusCode, b)
	}
	var out struct{ Repositories []string }
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out.Repositories, resp.Header.Get("Link")
}

func eq(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func seedMix(t *testing.T) *testReg {
	r := newTestReg(t)
	seed(t, r, "publisted", true, store.ModePublic)
	seed(t, r, "pubhidden", false, store.ModePublic)
	seed(t, r, "priv", true, store.ModeUsers)
	seed(t, r, "lan", true, store.ModeIPs)
	r.F.ips["lan"] = []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}
	r.F.users["priv|alice"] = "utok"
	return r
}

func TestCatalogAnonymousOnlyListedPublic(t *testing.T) {
	r := seedMix(t)
	if got, _ := catalog(t, r, "", "", ""); !eq(got, []string{"publisted"}) {
		t.Fatalf("%v", got)
	}
}

func TestCatalogAuthenticatedSeesAllPullable(t *testing.T) {
	r := seedMix(t)
	if got, _ := catalog(t, r, "ci", "ptok", ""); !eq(got, []string{"lan", "priv", "pubhidden", "publisted"}) {
		t.Fatalf("push token: %v", got)
	}
	if got, _ := catalog(t, r, "alice", "utok", ""); !eq(got, []string{"priv", "publisted"}) {
		t.Fatalf("granted user: %v", got)
	}
	// garbage credentials must not reveal unlisted names
	if got, _ := catalog(t, r, "nobody", "junk", ""); !eq(got, []string{"publisted"}) {
		t.Fatalf("bad credentials: %v", got)
	}
}

func TestCatalogPagination(t *testing.T) {
	r := newTestReg(t)
	var want []string
	for i := 0; i < 12; i++ {
		name := "r" + strconv.Itoa(100+i)
		listed := i%3 == 0 // most are hidden between the visible ones
		seed(t, r, name, listed, store.ModePublic)
		if listed {
			want = append(want, name)
		}
	}
	var got []string
	q := "?n=3"
	for pages := 0; pages < 10; pages++ {
		page, link := catalog(t, r, "", "", q)
		if link != "" && len(page) != 3 {
			t.Fatalf("short page %v while more remain", page)
		}
		got = append(got, page...)
		m := regexp.MustCompile(`<([^>]+)>; rel="next"`).FindStringSubmatch(link)
		if m == nil {
			break
		}
		u, _ := url.Parse(m[1])
		if u.Path != "/v2/_catalog" {
			t.Fatalf("link %q", link)
		}
		q = "?" + u.RawQuery
	}
	if !eq(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestTagListFollowsAccess(t *testing.T) {
	r := seedMix(t)
	if resp, _ := do(t, "GET", r.Srv.URL+"/v2/priv/tags/list", "", "", nil, nil); resp.StatusCode != 401 {
		t.Fatalf("anon %d", resp.StatusCode)
	}
	if resp, _ := do(t, "GET", r.Srv.URL+"/v2/priv/tags/list", "alice", "utok", nil, nil); resp.StatusCode != 200 {
		t.Fatalf("granted %d", resp.StatusCode)
	}
}

func TestGateRejectsWrites503(t *testing.T) {
	r := newTestReg(t)
	seed(t, r, "app", true, store.ModePublic)
	if err := r.Reg.Gate().Close(time.Second); err != nil {
		t.Fatal(err)
	}
	if _, st := pushImage(t, r.Srv.URL, "app", "v2", "ci", "ptok"); st != 503 {
		t.Fatalf("write while closed: %d", st)
	}
	if resp, _ := do(t, "GET", r.Srv.URL+"/v2/app/manifests/v1", "", "", nil, ociAccept); resp.StatusCode != 200 {
		t.Fatalf("pull while closed: %d", resp.StatusCode)
	}
	r.Reg.Gate().Open()
	if _, st := pushImage(t, r.Srv.URL, "app", "v2", "ci", "ptok"); st != 0 {
		t.Fatalf("write after open: %d", st)
	}
}

func TestGateWaitsForInflight(t *testing.T) {
	g := &Gate{}
	if !g.Begin() {
		t.Fatal("open gate refused a write")
	}
	if err := g.Close(50 * time.Millisecond); err != ErrDrainTimeout {
		t.Fatalf("got %v", err)
	}
	if g.Begin() {
		t.Fatal("gate must stay closed after a drain timeout")
	}
	g.End()
	if err := g.Close(50 * time.Millisecond); err != nil {
		t.Fatalf("after drain: %v", err)
	}
	g.Open()
	if !g.Begin() {
		t.Fatal("reopened gate refused a write")
	}
	g.End()
}

func TestEmptyRegistryListsNothing(t *testing.T) {
	r := newTestReg(t)
	for _, c := range [][2]string{{"", ""}, {"ci", "ptok"}} {
		if got, link := catalog(t, r, c[0], c[1], ""); len(got) != 0 || link != "" {
			t.Fatalf("%v %q", got, link)
		}
	}
	if names, err := r.Reg.Repos(context.Background()); err != nil || len(names) != 0 {
		t.Fatalf("%v %v", names, err)
	}
}

// Docker and podman probe /v2/ first: a 401 with a Basic challenge makes them send saved
// credentials on later requests. Pulls stay anonymous-friendly because each request is
// authorized on its own.
func TestBaseChallengesAnonymousCallers(t *testing.T) {
	r := newTestReg(t)
	resp, _ := do(t, "GET", r.Srv.URL+"/v2/", "", "", nil, nil)
	if resp.StatusCode != 401 || !strings.HasPrefix(resp.Header.Get("WWW-Authenticate"), "Basic ") {
		t.Fatalf("anonymous: %d %q", resp.StatusCode, resp.Header.Get("WWW-Authenticate"))
	}
	if resp, _ := do(t, "GET", r.Srv.URL+"/v2/", "ci", "ptok", nil, nil); resp.StatusCode != 200 {
		t.Fatalf("with credentials: %d", resp.StatusCode)
	}
	seed(t, r, "pub", true, store.ModePublic)
	if resp, _ := do(t, "GET", r.Srv.URL+"/v2/pub/manifests/v1", "", "", nil, ociAccept); resp.StatusCode != 200 {
		t.Fatalf("anonymous pull after a 401 ping: %d", resp.StatusCode)
	}
}

func TestCatalogWithUserCredsIsNotCounted(t *testing.T) {
	r := seedMix(t)
	for i := 0; i < 30; i++ {
		catalog(t, r, "alice", "utok", "")
	}
}

// The catalog's output depends on the credentials (a right push token or grant shows more), so
// wrong guesses must stay counted: refunding them would make the catalog a guessing oracle.
func TestCatalogGuessesAreThrottled(t *testing.T) {
	r := seedMix(t)
	for i := 0; i < 10; i++ {
		catalog(t, r, "nobody", "junk", "")
	}
	resp, _ := do(t, "GET", r.Srv.URL+"/v2/_catalog", "nobody", "junk", nil, nil)
	if resp.StatusCode != 429 {
		t.Fatalf("11th guess: %d", resp.StatusCode)
	}
	resp, _ = do(t, "GET", r.Srv.URL+"/v2/_catalog", "ci", "ptok", nil, nil)
	if resp.StatusCode != 429 {
		t.Fatalf("a blocked client must stay blocked even with a right token: %d", resp.StatusCode)
	}
}
