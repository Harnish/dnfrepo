package api

import (
	"bytes"
	"context"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"dnfrepo/internal/pipeline"
)

type nopPub struct{ dir string }

func (n nopPub) RepoDir(slug string) string                     { return filepath.Join(n.dir, "repos", slug) }
func (n nopPub) Rebuild(ctx context.Context, slug string) error { return nil }

func upload(e *env, slug, filename string, body []byte) *httptest.ResponseRecorder {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", filename)
	fw.Write(body)
	mw.Close()
	r := httptest.NewRequest("POST", "/api/repos/"+slug+"/packages", &buf)
	r.Header.Set("Content-Type", mw.FormDataContentType())
	r.AddCookie(e.Cookie)
	r.Header.Set("X-Requested-With", "dnfrepo")
	w := httptest.NewRecorder()
	e.handler().ServeHTTP(w, r)
	return w
}

func pkgEnv(t *testing.T) (*env, string) {
	e := newEnv(t)
	dir := t.TempDir()
	pl := &pipeline.Service{Store: e.St, Publisher: nopPub{dir}, MaxBytes: 1 << 20}
	(&Repos{Store: e.St, DataDir: dir}).Routes(e.Mux)
	pk := &Packages{Pipeline: pl, Actor: func(*http.Request) string { return "admin@x.org" }}
	pk.Routes(e.Mux)
	pk.RoutesPromote(e.Mux)
	pk.RoutesAudit(e.Mux)
	pk.RoutesKeep(e.Mux)
	return e, dir
}

func TestUploadListDeleteViaAPI(t *testing.T) {
	e, _ := pkgEnv(t)
	e.Do("POST", "/api/repos", `{"slug":"dev","name":"Dev","mode":"public"}`)
	rpm, _ := os.ReadFile("../rpmmeta/testdata/hello-1.2.3-4.noarch.rpm")
	if w := upload(e, "dev", "hello-1.2.3-4.noarch.rpm", rpm); w.Code != 201 {
		t.Fatalf("upload: %d %s", w.Code, w.Body)
	}
	if w := upload(e, "dev", "hello-1.2.3-4.noarch.rpm", rpm); w.Code != 409 {
		t.Fatalf("dup: %d", w.Code)
	}
	if w := e.Do("GET", "/api/repos/dev/packages", ""); w.Code != 200 || !bytes.Contains(w.Body.Bytes(), []byte(`"name":"hello"`)) {
		t.Fatalf("list: %d %s", w.Code, w.Body)
	}
	if w := e.Do("DELETE", "/api/repos/dev/packages/hello-1.2.3-4.noarch.rpm", ""); w.Code != 200 {
		t.Fatalf("delete: %d", w.Code)
	}
}

func TestUploadToLaterStageIs409AndNonRPMIs400(t *testing.T) {
	e, dir := pkgEnv(t)
	e.Do("POST", "/api/repos", `{"slug":"dev","name":"Dev","mode":"public"}`)
	e.Do("POST", "/api/repos", `{"slug":"test","name":"Test","mode":"public","previous":"dev"}`)
	rpm, _ := os.ReadFile("../rpmmeta/testdata/hello-1.2.3-4.noarch.rpm")
	if w := upload(e, "test", "hello-1.2.3-4.noarch.rpm", rpm); w.Code != 409 {
		t.Fatalf("later stage: %d", w.Code)
	}
	if w := upload(e, "dev", "x.rpm", []byte("not an rpm")); w.Code != 400 {
		t.Fatalf("non-rpm: %d", w.Code)
	}
	// net/http strips directories from multipart filenames, so "../x.rpm" lands safely as "x.rpm" inside the repo
	if w := upload(e, "dev", "../x.rpm", rpm); w.Code != 201 || !bytes.Contains(w.Body.Bytes(), []byte(`"filename":"x.rpm"`)) {
		t.Fatalf("traversal: %d %s", w.Code, w.Body)
	}
	for _, p := range []string{filepath.Join(dir, "x.rpm"), filepath.Join(dir, "repos", "x.rpm")} {
		if _, err := os.Stat(p); err == nil {
			t.Fatalf("file escaped repo dir: %s", p)
		}
	}
}

func TestPromoteViaAPI(t *testing.T) {
	e, _ := pkgEnv(t)
	e.Do("POST", "/api/repos", `{"slug":"dev","name":"Dev","mode":"public"}`)
	e.Do("POST", "/api/repos", `{"slug":"test","name":"Test","mode":"public","previous":"dev"}`)
	if w := e.Do("POST", "/api/repos/dev/promote", `{"filenames":[]}`); w.Code != 400 {
		t.Fatalf("empty: %d %s", w.Code, w.Body)
	}
	if w := e.Do("POST", "/api/repos/test/promote", `{"filenames":["x.rpm"]}`); w.Code != 409 {
		t.Fatalf("no next stage: %d %s", w.Code, w.Body)
	}
	if w := e.Do("POST", "/api/repos/dev/promote", `{"filenames":["ghost.rpm"]}`); w.Code != 404 {
		t.Fatalf("missing: %d %s", w.Code, w.Body)
	}
}

func TestAuditEndpoint(t *testing.T) {
	e, _ := pkgEnv(t)
	e.Do("POST", "/api/repos", `{"slug":"dev","name":"Dev","mode":"public"}`)
	rpm, _ := os.ReadFile("../rpmmeta/testdata/hello-1.2.3-4.noarch.rpm")
	upload(e, "dev", "hello-1.2.3-4.noarch.rpm", rpm)
	w := e.Do("GET", "/api/repos/dev/audit", "")
	if w.Code != 200 || !bytes.Contains(w.Body.Bytes(), []byte(`"action":"uploaded"`)) || !bytes.Contains(w.Body.Bytes(), []byte(`hello-1.2.3-4.noarch.rpm`)) {
		t.Fatalf("audit: %d %s", w.Code, w.Body)
	}
	if w := e.Do("GET", "/api/repos/ghost/audit", ""); w.Code != 404 {
		t.Fatalf("unknown repo: %d", w.Code)
	}
}

func TestKeepEndpointValidatesAndPurges(t *testing.T) {
	e, _ := pkgEnv(t)
	e.Do("POST", "/api/repos", `{"slug":"dev","name":"Dev","mode":"public"}`)
	for _, f := range []string{"hello-1.2.3-4.noarch.rpm", "hello-1.9.0-1.noarch.rpm", "hello-1.10.0-1.noarch.rpm"} {
		b, _ := os.ReadFile("../rpmmeta/testdata/" + f)
		if w := upload(e, "dev", f, b); w.Code != 201 {
			t.Fatalf("upload %s: %d %s", f, w.Code, w.Body)
		}
	}
	for _, bad := range []string{`{"keep":-1}`, `{"keep":1,"overrides":{"hello":-2}}`, `{"keep":1,"overrides":{"bad name!":1}}`} {
		if w := e.Do("PUT", "/api/repos/dev/keep", bad); w.Code != 400 {
			t.Errorf("%s: %d", bad, w.Code)
		}
	}
	w := e.Do("PUT", "/api/repos/dev/keep", `{"keep":1,"overrides":{"other":5}}`)
	if w.Code != 200 || !bytes.Contains(w.Body.Bytes(), []byte("hello-1.2.3-4.noarch.rpm")) || !bytes.Contains(w.Body.Bytes(), []byte("hello-1.9.0-1.noarch.rpm")) {
		t.Fatalf("put keep: %d %s", w.Code, w.Body)
	}
	g := e.Do("GET", "/api/repos/dev", "")
	if !bytes.Contains(g.Body.Bytes(), []byte(`"keep":1`)) || !bytes.Contains(g.Body.Bytes(), []byte(`"keep_overrides":{"other":5}`)) {
		t.Fatalf("repo json: %s", g.Body)
	}
	if w := e.Do("PUT", "/api/repos/ghost/keep", `{"keep":1}`); w.Code != 404 {
		t.Fatalf("unknown repo: %d", w.Code)
	}
}
