package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"dnfrepo/internal/access"
	"dnfrepo/internal/registry"
	"dnfrepo/internal/registry/registrytest"
	"dnfrepo/internal/store"
)

type regEnv struct {
	*env
	pub *http.ServeMux
	Srv *httptest.Server
	Tok string // push token secret, user "ci"
}

func newRegEnv(t *testing.T) *regEnv {
	t.Helper()
	e := newEnv(t)
	pol := &registry.Policy{Store: e.St, Throttle: access.NewThrottle()}
	reg, err := registry.New(context.Background(), registry.Config{Root: t.TempDir(), Policy: pol})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(reg.FrontHandler())
	t.Cleanup(srv.Close)
	_, tok, err := e.St.CreatePushToken(context.Background(), "ci", "test")
	if err != nil {
		t.Fatal(err)
	}
	a := &RegistryAPI{Store: e.St, Reg: reg, Policy: pol, Actor: func(*http.Request) string { return "admin@x.org" }, PublicHost: "reg.example.org"}
	a.Routes(e.Mux)
	pub := http.NewServeMux()
	a.PublicRoutes(pub)
	return &regEnv{env: e, pub: pub, Srv: srv, Tok: tok}
}

func (r *regEnv) push(t *testing.T, repo, tag string) {
	t.Helper()
	if _, st := registrytest.Push(t, r.Srv.URL, repo, tag, "ci", r.Tok); st != 0 {
		t.Fatalf("push %s:%s -> %d", repo, tag, st)
	}
}

func (r *regEnv) public(path string, basic ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("GET", path, nil)
	if len(basic) == 2 {
		req.SetBasicAuth(basic[0], basic[1])
	}
	w := httptest.NewRecorder()
	r.pub.ServeHTTP(w, req)
	return w
}

func TestRegistryAdminRequiresSession(t *testing.T) {
	r := newRegEnv(t)
	for _, p := range []string{"/api/registry/repos", "/api/registry/gc", "/api/registry/push-tokens"} {
		if w := r.DoAnon("GET", p, ""); w.Code != 401 {
			t.Errorf("%s: %d", p, w.Code)
		}
	}
}

func TestRegistrySettingsRoundTrip(t *testing.T) {
	r := newRegEnv(t)
	r.push(t, "a/b/c", "v1")
	uid, _, _ := r.St.CreateRepoUser(context.Background(), "alice")
	body := `{"listed":true,"mode":"users","ips":["10.0.0.0/8"],"user_ids":[` + strconv.FormatInt(uid, 10) + `]}`
	if w := r.Do("PUT", "/api/registry/repo?name=a/b/c", body); w.Code != 200 {
		t.Fatalf("put %d %s", w.Code, w.Body)
	}
	w := r.Do("GET", "/api/registry/repo?name=a/b/c", "")
	var got struct {
		Name    string
		Listed  bool
		Mode    string
		IPs     []string `json:"ips"`
		UserIDs []int64  `json:"user_ids"`
		Tags    []struct{ Name, Pull string }
	}
	json.Unmarshal(w.Body.Bytes(), &got)
	if w.Code != 200 || got.Name != "a/b/c" || !got.Listed || got.Mode != "users" || len(got.IPs) != 1 || len(got.UserIDs) != 1 ||
		len(got.Tags) != 1 || got.Tags[0].Pull != "reg.example.org/a/b/c:v1" {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	l := r.Do("GET", "/api/registry/repos", "")
	if !strings.Contains(l.Body.String(), `"a/b/c"`) || !strings.Contains(l.Body.String(), `"listed":true`) {
		t.Fatalf("%s", l.Body)
	}
}

func TestRegistryRejectsBadInput(t *testing.T) {
	r := newRegEnv(t)
	for name, c := range map[string]struct{ path, body string }{
		"bad mode": {"/api/registry/repo?name=x", `{"mode":"nope"}`},
		"bad name": {"/api/registry/repo?name=Bad%20Name", `{"mode":"public"}`},
		"bad ip":   {"/api/registry/repo?name=x", `{"mode":"ips","ips":["not-an-ip"]}`},
	} {
		if w := r.Do("PUT", c.path, c.body); w.Code != 400 {
			t.Errorf("%s: %d %s", name, w.Code, w.Body)
		}
	}
}

func TestRegistryPushTokenShownOnce(t *testing.T) {
	r := newRegEnv(t)
	w := r.Do("POST", "/api/registry/push-tokens", `{"name":"build"}`)
	var c struct {
		ID    int64
		Token string
	}
	json.Unmarshal(w.Body.Bytes(), &c)
	if w.Code != 201 || c.Token == "" {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if l := r.Do("GET", "/api/registry/push-tokens", ""); strings.Contains(l.Body.String(), c.Token) || !strings.Contains(l.Body.String(), "build") {
		t.Fatalf("list: %s", l.Body)
	}
	if w := r.Do("POST", "/api/registry/push-tokens", `{"name":"build"}`); w.Code != 409 {
		t.Fatalf("dup %d", w.Code)
	}
	if w := r.Do("DELETE", "/api/registry/push-tokens/"+strconv.FormatInt(c.ID, 10), ""); w.Code != 200 {
		t.Fatalf("revoke %d", w.Code)
	}
	if ok, _ := r.St.CheckPushToken(context.Background(), "build", c.Token); ok {
		t.Fatal("revoked token still works")
	}
}

func TestRegistryDeleteTag(t *testing.T) {
	r := newRegEnv(t)
	r.push(t, "app", "v1")
	r.push(t, "app", "v2")
	if w := r.Do("DELETE", "/api/registry/tag?name=app&tag=v1", ""); w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	var got struct{ Tags []struct{ Name string } }
	json.Unmarshal(r.Do("GET", "/api/registry/repo?name=app", "").Body.Bytes(), &got)
	if len(got.Tags) != 1 || got.Tags[0].Name != "v2" {
		t.Fatalf("%+v", got)
	}
	if w := r.Do("DELETE", "/api/registry/tag?name=app&tag=nope", ""); w.Code != 404 {
		t.Fatalf("missing tag %d", w.Code)
	}
	if w := r.Do("DELETE", "/api/registry/tag?name=ghost&tag=v1", ""); w.Code != 404 {
		t.Fatalf("missing repo %d", w.Code)
	}
}

func TestRegistryGCStartsDetached(t *testing.T) {
	r := newRegEnv(t)
	r.push(t, "app", "v1")
	r.Do("DELETE", "/api/registry/tag?name=app&tag=v1", "")
	if w := r.Do("POST", "/api/registry/gc", ""); w.Code != 202 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	var st struct {
		State   string
		Removed int
	}
	for i := 0; i < 100; i++ {
		json.Unmarshal(r.Do("GET", "/api/registry/gc", "").Body.Bytes(), &st)
		if st.State == "done" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if st.State != "done" || st.Removed == 0 {
		t.Fatalf("%+v", st)
	}
}

func TestPublicRegistryHidesUnlistedAndNonPublic(t *testing.T) {
	r := newRegEnv(t)
	ctx := context.Background()
	for _, n := range []string{"pub", "hid", "usr", "lan"} {
		r.push(t, n, "v1")
	}
	r.St.SetRegistryRepo(ctx, "pub", true, store.ModePublic)
	r.St.SetRegistryRepo(ctx, "hid", false, store.ModePublic)
	r.St.SetRegistryRepo(ctx, "usr", true, store.ModeUsers)
	r.St.SetRegistryRepo(ctx, "lan", true, store.ModeIPs)
	w := r.public("/api/public/registry/repos")
	var list []struct{ Name string }
	json.Unmarshal(w.Body.Bytes(), &list)
	if w.Code != 200 || len(list) != 1 || list[0].Name != "pub" {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if w := r.public("/api/public/registry/repo?name=pub"); w.Code != 200 || !strings.Contains(w.Body.String(), "reg.example.org/pub:v1") {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	unknown := r.public("/api/public/registry/repo?name=ghost")
	for _, n := range []string{"hid", "usr", "lan"} {
		w := r.public("/api/public/registry/repo?name=" + n)
		if w.Code != 404 || w.Body.String() != unknown.Body.String() {
			t.Errorf("%s: %d %q (unknown: %d %q)", n, w.Code, w.Body, unknown.Code, unknown.Body)
		}
	}
}

func TestPublicRegistryIgnoresCredentials(t *testing.T) {
	r := newRegEnv(t)
	r.push(t, "hid", "v1")
	r.St.SetRegistryRepo(context.Background(), "hid", false, store.ModePublic)
	w := r.public("/api/public/registry/repos", "ci", r.Tok)
	if strings.Contains(w.Body.String(), "hid") || strings.Contains(w.Body.String(), r.Tok) {
		t.Fatalf("%s", w.Body)
	}
}

func TestRegistryDeleteTagRejectsBadTagAndPublicIgnoresUserCreds(t *testing.T) {
	r := newRegEnv(t)
	r.push(t, "usr", "v1")
	ctx := context.Background()
	uid, tok, _ := r.St.CreateRepoUser(ctx, "alice")
	r.St.SetRegistryRepo(ctx, "usr", true, store.ModeUsers)
	r.St.SetRegistryGrants(ctx, "usr", []int64{uid})
	if w := r.Do("DELETE", "/api/registry/tag?name=usr&tag=../../x", ""); w.Code != 400 {
		t.Fatalf("path tag: %d", w.Code)
	}
	// a granted user's credentials do not make a users-mode image appear on the public page
	if w := r.public("/api/public/registry/repos", "alice", tok); strings.Contains(w.Body.String(), "usr") {
		t.Fatalf("%s", w.Body)
	}
	if w := r.public("/api/public/registry/repo?name=usr", "alice", tok); w.Code != 404 {
		t.Fatalf("%d", w.Code)
	}
}
