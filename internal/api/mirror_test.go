package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dnfrepo/internal/mirror"
	"dnfrepo/internal/pipeline"
	"dnfrepo/internal/secretbox"
	"dnfrepo/internal/sources"
	"dnfrepo/internal/store"
)

func mirrorEnv(t *testing.T) (*env, *Mirrors, string) {
	t.Helper()
	e := newEnv(t)
	dir := t.TempDir()
	pl := &pipeline.Service{Store: e.St, Publisher: nopPub{dir}, MaxBytes: 1 << 20}
	actor := func(*http.Request) string { return "admin@x.org" }
	(&Repos{Store: e.St, DataDir: dir}).Routes(e.Mux)
	pk := &Packages{Pipeline: pl, Actor: actor}
	pk.Routes(e.Mux)
	pk.RoutesPromote(e.Mux)
	(&Sources{Store: e.St, Scheduler: &sources.Scheduler{Store: e.St, Pipeline: pl}}).Routes(e.Mux)
	box, err := secretbox.New("test-key")
	if err != nil {
		t.Fatal(err)
	}
	m := &Mirrors{Store: e.St, Syncer: &mirror.Syncer{Store: e.St, Pipeline: pl, StagingDir: filepath.Join(dir, "staging")},
		Box: box, Actor: actor}
	m.Routes(e.Mux)
	return e, m, dir
}

func mkRepo(t *testing.T, e *env, slug, prev string) store.Repo {
	t.Helper()
	body := fmt.Sprintf(`{"slug":%q,"name":%q,"mode":"public"`, slug, slug)
	if prev != "" {
		body += fmt.Sprintf(`,"previous":%q`, prev)
	}
	if w := e.Do("POST", "/api/repos", body+"}"); w.Code != 201 {
		t.Fatalf("create %s: %d %s", slug, w.Code, w.Body)
	}
	r, err := e.St.RepoBySlug(context.Background(), slug)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func putMirror(t *testing.T, e *env, slug, body string, want int) *httptest.ResponseRecorder {
	t.Helper()
	w := e.Do("PUT", "/api/repos/"+slug+"/mirror", body)
	if w.Code != want {
		t.Fatalf("PUT mirror %s: want %d got %d %s", body, want, w.Code, w.Body)
	}
	return w
}

func TestParseRepoReportsSectionsAndVariables(t *testing.T) {
	e, _, _ := mirrorEnv(t)
	file := "[fedora]\nname=Fedora $releasever\nmetalink=https://mirrors.example/metalink?repo=fedora-$releasever&arch=$basearch\n" +
		"[private]\nbaseurl=https://h/repo/\nusername=u\npassword=p\n[nourl]\nname=x\n"
	b, _ := json.Marshal(map[string]string{"repo_file": file})
	w := e.Do("POST", "/api/mirror/parse-repo", string(b))
	if w.Code != 200 {
		t.Fatalf("parse: %d %s", w.Code, w.Body)
	}
	var out struct {
		Sections []struct {
			ID, Name       string
			URLKind        string `json:"url_kind"`
			Variables      []string
			HasCredentials bool `json:"has_credentials"`
		}
	}
	json.Unmarshal(w.Body.Bytes(), &out)
	if len(out.Sections) != 2 || out.Sections[0].ID != "fedora" || out.Sections[0].URLKind != "metalink" ||
		strings.Join(out.Sections[0].Variables, ",") != "basearch,releasever" || out.Sections[0].HasCredentials ||
		out.Sections[1].URLKind != "baseurl" || !out.Sections[1].HasCredentials || len(out.Sections[1].Variables) != 0 {
		t.Fatalf("sections: %s", w.Body)
	}
	if strings.Contains(w.Body.String(), `"p"`) {
		t.Fatal("password echoed")
	}
	if w := e.Do("POST", "/api/mirror/parse-repo", `{"repo_file":"[x]\nname=y\n"}`); w.Code != 400 {
		t.Fatalf("unparseable: %d", w.Code)
	}
}

func TestPutMirrorRoundTripAndEligibility(t *testing.T) {
	e, _, _ := mirrorEnv(t)
	mkRepo(t, e, "dev", "")
	if w := e.Do("GET", "/api/repos/dev/mirror", ""); w.Code != 404 {
		t.Fatalf("GET non-mirror: %d", w.Code)
	}
	putMirror(t, e, "dev", `{"upstream_url":"ftp://h/x/"}`, 400)
	putMirror(t, e, "dev", `{"upstream_url":"https://u:p@h/x/"}`, 400)
	putMirror(t, e, "dev", `{"upstream_url":"https://h/x/","schedule":{"frequency":"hourly"}}`, 400)
	putMirror(t, e, "dev", `{"upstream_url":"https://h/x/","require_signature":true}`, 400)
	putMirror(t, e, "dev", `{"upstream_url":"https://h/x/","include":["["]}`, 400)
	before := time.Now()
	putMirror(t, e, "dev", `{"upstream_url":"https://h/x/","arches":["x86_64"],"include":["k*"],"exclude":["kd"],"keep_latest":2,
		"schedule":{"frequency":"daily","time_of_day":"03:00","timezone":"UTC"}}`, 200)
	w := e.Do("GET", "/api/repos/dev/mirror", "")
	var got struct {
		UpstreamURL string   `json:"upstream_url"`
		Arches      []string `json:"arches"`
		KeepLatest  int      `json:"keep_latest"`
		State       string   `json:"state"`
		NextRun     *string  `json:"next_run"`
		Schedule    struct {
			Frequency string `json:"frequency"`
		} `json:"schedule"`
	}
	json.Unmarshal(w.Body.Bytes(), &got)
	if w.Code != 200 || got.UpstreamURL != "https://h/x/" || got.Arches[0] != "x86_64" || got.KeepLatest != 2 ||
		got.State != "idle" || got.Schedule.Frequency != "daily" || got.NextRun == nil {
		t.Fatalf("GET: %d %s", w.Code, w.Body)
	}
	if nr, _ := time.Parse(time.RFC3339, *got.NextRun); !nr.After(before) || nr.After(before.Add(25*time.Hour)) {
		t.Fatalf("next_run %s", *got.NextRun)
	}
	// manual: next_run cleared
	putMirror(t, e, "dev", `{"upstream_url":"https://h/x/"}`, 200)
	if m, _ := e.St.MirrorByRepo(context.Background(), mkRepoID(t, e, "dev")); m.NextRun != nil || m.Schedule.Frequency != "manual" {
		t.Fatalf("manual: %+v", m)
	}

	// a repo with packages is not eligible
	rpm, _ := os.ReadFile("../rpmmeta/testdata/hello-1.2.3-4.noarch.rpm")
	mkRepo(t, e, "full", "")
	if w := upload(e, "full", "hello-1.2.3-4.noarch.rpm", rpm); w.Code != 201 {
		t.Fatalf("upload: %d", w.Code)
	}
	putMirror(t, e, "full", `{"upstream_url":"https://h/x/"}`, 409)
	putMirror(t, e, "ghost", `{"upstream_url":"https://h/x/"}`, 404)

	if w := e.Do("DELETE", "/api/repos/dev/mirror", ""); w.Code != 200 {
		t.Fatalf("delete: %d %s", w.Code, w.Body)
	}
	if w := e.Do("DELETE", "/api/repos/dev/mirror", ""); w.Code != 404 {
		t.Fatalf("delete again: %d", w.Code)
	}
}

func mkRepoID(t *testing.T, e *env, slug string) int64 {
	r, err := e.St.RepoBySlug(context.Background(), slug)
	if err != nil {
		t.Fatal(err)
	}
	return r.ID
}

func TestPutMirrorMissingVarsListsThem(t *testing.T) {
	e, _, _ := mirrorEnv(t)
	mkRepo(t, e, "dev", "")
	b, _ := json.Marshal(map[string]any{"repo_file": "[f]\nbaseurl=https://h/$releasever/$basearch/\n", "vars": map[string]string{"basearch": "x86_64"}})
	w := putMirror(t, e, "dev", string(b), 400)
	var out struct {
		MissingVars []string `json:"missing_vars"`
	}
	json.Unmarshal(w.Body.Bytes(), &out)
	if len(out.MissingVars) != 1 || out.MissingVars[0] != "releasever" {
		t.Fatalf("missing vars: %s", w.Body)
	}
	putMirror(t, e, "dev", `{"upstream_url":"https://h/$arch/"}`, 400)
	// several sections and no choice
	b, _ = json.Marshal(map[string]any{"repo_file": "[a]\nbaseurl=https://h/a/\n[b]\nbaseurl=https://h/b/\n"})
	w = putMirror(t, e, "dev", string(b), 400)
	var secs struct{ Sections []string }
	json.Unmarshal(w.Body.Bytes(), &secs)
	if strings.Join(secs.Sections, ",") != "a,b" {
		t.Fatalf("sections: %s", w.Body)
	}
}

func TestPutMirrorResolvesBaseurlVars(t *testing.T) {
	e, _, _ := mirrorEnv(t)
	mkRepo(t, e, "dev", "")
	b, _ := json.Marshal(map[string]any{"repo_file": "[a]\nbaseurl=https://h/a/\n[b]\nbaseurl=https://h/$releasever/$basearch/\n",
		"section": "b", "vars": map[string]string{"releasever": "42", "basearch": "x86_64"}})
	putMirror(t, e, "dev", string(b), 200)
	if m, _ := e.St.MirrorByRepo(context.Background(), mkRepoID(t, e, "dev")); m.UpstreamURL != "https://h/42/x86_64/" {
		t.Fatalf("stored %q", m.UpstreamURL)
	}
	// mirrorlist resolves to its first URL
	ml := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "# comment\nhttps://m1/fedora/42/\nhttps://m2/fedora/42/")
	}))
	defer ml.Close()
	b, _ = json.Marshal(map[string]any{"repo_file": "[f]\nmirrorlist=" + ml.URL + "/list?r=$releasever\n", "vars": map[string]string{"releasever": "42"}})
	putMirror(t, e, "dev", string(b), 200)
	if m, _ := e.St.MirrorByRepo(context.Background(), mkRepoID(t, e, "dev")); m.UpstreamURL != "https://m1/fedora/42/" {
		t.Fatalf("mirrorlist stored %q", m.UpstreamURL)
	}
	// an http(s) gpgkey is fetched and stored; repo_gpgcheck=1 requires the signature
	const key = "-----BEGIN PGP PUBLIC KEY BLOCK-----\nx\n-----END PGP PUBLIC KEY BLOCK-----\n"
	ks := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, key) }))
	defer ks.Close()
	b, _ = json.Marshal(map[string]any{"repo_file": "[f]\nbaseurl=https://h/x/\nrepo_gpgcheck=1\ngpgkey=file:///etc/k " + ks.URL + "/k\n"})
	putMirror(t, e, "dev", string(b), 200)
	if m, _ := e.St.MirrorByRepo(context.Background(), mkRepoID(t, e, "dev")); m.GPGKey != key || !m.RequireSig {
		t.Fatalf("gpg key %q require %v", m.GPGKey, m.RequireSig)
	}
	// a gpgkey that can't be fetched (file://) leaves repo_gpgcheck=1 without a key
	b, _ = json.Marshal(map[string]any{"repo_file": "[f]\nbaseurl=https://h/x/\nrepo_gpgcheck=1\ngpgkey=file:///etc/k\n"})
	putMirror(t, e, "dev", string(b), 400)
	putMirror(t, e, "dev", `{"upstream_url":"https://h/x/","gpg_key":"not a key"}`, 400)
}

const secretTok = "s3cr3t-T0KEN-value"

func TestMirrorResponsesNeverContainToken(t *testing.T) {
	e, _, _ := mirrorEnv(t)
	mkRepo(t, e, "dev", "")
	check := func(what string, w *httptest.ResponseRecorder) {
		t.Helper()
		if strings.Contains(w.Body.String(), secretTok) {
			t.Fatalf("%s leaks token: %s", what, w.Body)
		}
	}
	check("PUT", putMirror(t, e, "dev", `{"upstream_url":"https://h/x/","username":"bob","token":"`+secretTok+`"}`, 200))
	check("GET", e.Do("GET", "/api/repos/dev/mirror", ""))
	check("bad schedule", putMirror(t, e, "dev", `{"upstream_url":"https://h/x/","token":"`+secretTok+`","schedule":{"frequency":"x"}}`, 400))
	check("bad url", putMirror(t, e, "dev", `{"upstream_url":"nope`+secretTok+`","token":"`+secretTok+`"}`, 400))
	check("bad json", putMirror(t, e, "dev", `{"token":"`+secretTok+`",`, 400))
	b, _ := json.Marshal(map[string]any{"repo_file": "[f]\nbaseurl=https://h/$x/\npassword=" + secretTok + "\n"})
	check("repo file vars", putMirror(t, e, "dev", string(b), 400))
	check("audit", e.Do("GET", "/api/repos/dev/audit", ""))

	m, err := e.St.MirrorByRepo(context.Background(), mkRepoID(t, e, "dev"))
	if err != nil {
		t.Fatal(err)
	}
	if len(m.TokenEnc) == 0 || bytes.Contains(m.TokenEnc, []byte(secretTok)) || m.Username != "bob" {
		t.Fatalf("stored: user %q tokenEnc %q", m.Username, m.TokenEnc)
	}
	var got struct {
		Username       string `json:"username"`
		HasCredentials bool   `json:"has_credentials"`
	}
	json.Unmarshal(e.Do("GET", "/api/repos/dev/mirror", "").Body.Bytes(), &got)
	if got.Username != "bob" || !got.HasCredentials {
		t.Fatalf("GET creds: %+v", got)
	}
}

func TestPutMirrorCredentialsNeedSecretKey(t *testing.T) {
	e, m, _ := mirrorEnv(t)
	m.Box = nil
	mkRepo(t, e, "dev", "")
	w := putMirror(t, e, "dev", `{"upstream_url":"https://h/x/","token":"t"}`, 400)
	if !strings.Contains(w.Body.String(), "SECRET_KEY") {
		t.Fatalf("message: %s", w.Body)
	}
	b, _ := json.Marshal(map[string]any{"repo_file": "[f]\nbaseurl=https://h/x/\nusername=u\npassword=p\n"})
	putMirror(t, e, "dev", string(b), 400)
	putMirror(t, e, "dev", `{"upstream_url":"https://h/x/","username":"u"}`, 200) // a username alone needs no key
}

func TestPutMirrorKeepsTokenWhenOmitted(t *testing.T) {
	e, _, _ := mirrorEnv(t)
	id := mkRepo(t, e, "dev", "").ID
	putMirror(t, e, "dev", `{"upstream_url":"https://h/x/","username":"bob","token":"t1"}`, 200)
	m1, _ := e.St.MirrorByRepo(context.Background(), id)
	putMirror(t, e, "dev", `{"upstream_url":"https://h/y/"}`, 200)
	m2, _ := e.St.MirrorByRepo(context.Background(), id)
	if !bytes.Equal(m1.TokenEnc, m2.TokenEnc) || m2.Username != "bob" || m2.UpstreamURL != "https://h/y/" {
		t.Fatalf("token/username not kept: %+v", m2)
	}
	putMirror(t, e, "dev", `{"upstream_url":"https://h/y/","clear_credentials":true}`, 200)
	m3, _ := e.St.MirrorByRepo(context.Background(), id)
	if len(m3.TokenEnc) != 0 || m3.Username != "" {
		t.Fatalf("not cleared: %+v", m3)
	}
}

func TestSyncWhileRunningIs409(t *testing.T) {
	e, _, _ := mirrorEnv(t)
	id := mkRepo(t, e, "dev", "").ID
	putMirror(t, e, "dev", `{"upstream_url":"https://h/x/"}`, 200)
	if err := e.St.ClaimMirror(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if w := e.Do("POST", "/api/repos/dev/mirror/sync", ""); w.Code != 409 {
		t.Fatalf("sync while running: %d %s", w.Code, w.Body)
	}
	if w := e.Do("DELETE", "/api/repos/dev/mirror", ""); w.Code != 409 {
		t.Fatalf("delete while running: %d %s", w.Code, w.Body)
	}
}

func TestSyncStartsInBackground(t *testing.T) {
	e, _, _ := mirrorEnv(t)
	id := mkRepo(t, e, "dev", "").ID
	up := httptest.NewServer(http.NotFoundHandler())
	defer up.Close()
	putMirror(t, e, "dev", `{"upstream_url":"`+up.URL+`/"}`, 200)
	if w := e.Do("POST", "/api/repos/dev/mirror/sync", ""); w.Code != 202 || !strings.Contains(w.Body.String(), `"started"`) {
		t.Fatalf("sync: %d %s", w.Code, w.Body)
	}
	// sync-failed is the background run's last write; sync-requested names the admin
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		evs, _ := e.St.ListAudit(context.Background(), id, 10)
		got := map[string]string{}
		for _, ev := range evs {
			got[ev.Action] = ev.Actor
		}
		if a, ok := got["sync-failed"]; ok && got["sync-requested"] != "" {
			if a != "mirror:dev" || got["sync-requested"] != "admin@x.org" {
				t.Fatalf("actors: %v", got)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("sync never finished: %+v", evs)
		}
	}
}

func TestSyncNonMirrorIs404(t *testing.T) {
	e, _, _ := mirrorEnv(t)
	mkRepo(t, e, "dev", "")
	if w := e.Do("POST", "/api/repos/dev/mirror/sync", ""); w.Code != 404 {
		t.Fatalf("non-mirror: %d", w.Code)
	}
	if w := e.Do("POST", "/api/repos/ghost/mirror/sync", ""); w.Code != 404 {
		t.Fatalf("unknown repo: %d", w.Code)
	}
}

func TestUploadAndAddSourceOnMirrorAre409(t *testing.T) {
	e, _, _ := mirrorEnv(t)
	mkRepo(t, e, "dev", "")
	mkRepo(t, e, "base", "")
	putMirror(t, e, "dev", `{"upstream_url":"https://h/x/"}`, 200)
	rpm, _ := os.ReadFile("../rpmmeta/testdata/hello-1.2.3-4.noarch.rpm")
	if w := upload(e, "dev", "hello-1.2.3-4.noarch.rpm", rpm); w.Code != 409 {
		t.Fatalf("upload: %d %s", w.Code, w.Body)
	}
	if w := e.Do("POST", "/api/repos/dev/sources", `{"kind":"plex"}`); w.Code != 409 {
		t.Fatalf("source: %d %s", w.Code, w.Body)
	}
	if w := e.Do("PUT", "/api/repos/dev/previous", `{"previous":"base"}`); w.Code != 409 {
		t.Fatalf("link: %d %s", w.Code, w.Body)
	}
}

func TestPromoteBatchAndExclusiveBodyRule(t *testing.T) {
	e, _, dir := mirrorEnv(t)
	ctx := context.Background()
	dev := mkRepo(t, e, "dev", "")
	mkRepo(t, e, "test", "dev")
	putMirror(t, e, "dev", `{"upstream_url":"https://h/x/"}`, 200)
	bid, err := e.St.CreateBatch(ctx, dev.ID)
	if err != nil {
		t.Fatal(err)
	}
	fn := "a-1-1.noarch.rpm"
	os.WriteFile(filepath.Join(dir, "repos", "dev", fn), []byte("x"), 0o644)
	if err := e.St.AddMirrorPackages(ctx, dev.ID, bid, []store.Package{{Filename: fn, Name: "a", Version: "1", Release: "1", Arch: "noarch", SHA256: "s", Size: 1}}, "mirror:dev"); err != nil {
		t.Fatal(err)
	}
	e.St.FinishBatch(ctx, bid, "synced", 1, "")

	w := e.Do("GET", "/api/repos/dev/batches", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), fmt.Sprintf(`"id":%d`, bid)) || !strings.Contains(w.Body.String(), `"added":1`) {
		t.Fatalf("batches: %d %s", w.Code, w.Body)
	}
	if w := e.Do("GET", "/api/repos/ghost/batches", ""); w.Code != 404 {
		t.Fatalf("batches unknown repo: %d", w.Code)
	}
	if w := e.Do("POST", "/api/repos/dev/promote", fmt.Sprintf(`{"batch":%d,"filenames":["%s"]}`, bid, fn)); w.Code != 400 {
		t.Fatalf("both: %d %s", w.Code, w.Body)
	}
	if w := e.Do("POST", "/api/repos/dev/promote", `{}`); w.Code != 400 {
		t.Fatalf("neither: %d %s", w.Code, w.Body)
	}
	if w := e.Do("POST", "/api/repos/dev/promote", `{"batch":999999}`); w.Code != 404 {
		t.Fatalf("unknown batch: %d %s", w.Code, w.Body)
	}
	w = e.Do("POST", "/api/repos/dev/promote", fmt.Sprintf(`{"batch":%d}`, bid))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"promoted":["`+fn+`"]`) || !strings.Contains(w.Body.String(), `"to":"test"`) {
		t.Fatalf("promote batch: %d %s", w.Code, w.Body)
	}
	// the batch is now also resolvable in the next stage
	if names, _ := e.St.BatchFilenames(ctx, mkRepoID(t, e, "test"), bid); len(names) != 1 {
		t.Fatalf("origin batch not carried: %v", names)
	}
}

func TestPutMirrorFixups(t *testing.T) {
	e, m, _ := mirrorEnv(t)
	id := mkRepo(t, e, "dev", "").ID
	// a mirrorlist entry with userinfo is refused
	ml := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprintln(w, "https://user:pw@host/") }))
	defer ml.Close()
	b, _ := json.Marshal(map[string]any{"repo_file": "[f]\nmirrorlist=" + ml.URL + "/list\n"})
	if w := putMirror(t, e, "dev", string(b), 400); strings.Contains(w.Body.String(), "pw") {
		t.Fatalf("echoes credentials: %s", w.Body)
	}
	// audit detail is redacted: no query
	putMirror(t, e, "dev", `{"upstream_url":"https://h/x/?k=secret","username":"u","token":"t"}`, 200)
	evs, _ := e.St.ListAudit(context.Background(), id, 1)
	if len(evs) != 1 || evs[0].Action != "mirror-configured" || evs[0].Detail != "https://h/x/" {
		t.Fatalf("audit: %+v", evs)
	}
	// without SECRET_KEY, stored credentials can't be kept
	m.Box = nil
	if w := putMirror(t, e, "dev", `{"upstream_url":"https://h/x/"}`, 400); !strings.Contains(w.Body.String(), "SECRET_KEY") {
		t.Fatalf("message: %s", w.Body)
	}
	putMirror(t, e, "dev", `{"upstream_url":"https://h/x/","clear_credentials":true}`, 200)
}
