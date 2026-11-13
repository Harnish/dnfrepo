package api

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dnfrepo/internal/debmeta"
	"dnfrepo/internal/debmeta/debtest"
	"dnfrepo/internal/pipeline"
	"dnfrepo/internal/sources"
)

func debAPIEnv(t *testing.T) (*env, string, *[]string) {
	t.Helper()
	e := newEnv(t)
	dir := t.TempDir()
	hooked := &[]string{}
	pl := &pipeline.Service{Store: e.St, Publisher: nopPub{dir}, MaxBytes: 1 << 20}
	(&Repos{Store: e.St, DataDir: dir, OnChange: func(s string) { *hooked = append(*hooked, s) }}).Routes(e.Mux)
	pk := &Packages{Pipeline: pl, Actor: func(*http.Request) string { return "admin@x.org" }}
	pk.Routes(e.Mux)
	pk.RoutesPromote(e.Mux)
	(&Sources{Store: e.St, Scheduler: &sources.Scheduler{}}).Routes(e.Mux)
	return e, dir, hooked
}

func debBytes(t *testing.T, name, version, arch string) (string, []byte) {
	t.Helper()
	sp := debtest.Spec{Package: name, Version: version, Arch: arch}
	return debmeta.Filename(debmeta.Control{Package: name, Version: version, Arch: arch}), debtest.Build(t, sp)
}

func jsonOf(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("%v: %s", err, body)
	}
	return m
}

func TestCreateDebRepoViaAPI(t *testing.T) {
	e, dir, hooked := debAPIEnv(t)
	w := e.Do("POST", "/api/repos", `{"slug":"d","kind":"deb","mode":"public","suite":"stable","components":["main","contrib"],"arches":["amd64"]}`)
	if w.Code != 201 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	m := jsonOf(t, w.Body.Bytes())
	if m["kind"] != "deb" || m["suite"] != "stable" || len(m["components"].([]any)) != 2 || len(m["arches"].([]any)) != 1 {
		t.Fatalf("%v", m)
	}
	if st, err := os.Stat(filepath.Join(dir, "repos", "d")); err != nil || !st.IsDir() {
		t.Fatal("repo dir missing")
	}
	if len(*hooked) != 1 || (*hooked)[0] != "d" {
		t.Fatalf("hook=%v", *hooked)
	}
}

func TestCreateDebRepoDefaults(t *testing.T) {
	e, _, _ := debAPIEnv(t)
	w := e.Do("POST", "/api/repos", `{"slug":"bk","kind":"deb","mode":"public"}`)
	m := jsonOf(t, w.Body.Bytes())
	if w.Code != 201 || m["suite"] != "bk" || strings.Join(toStrings(m["components"]), ",") != "main" || strings.Join(toStrings(m["arches"]), ",") != "amd64,arm64" {
		t.Fatalf("%d %v", w.Code, m)
	}
}

func toStrings(v any) []string {
	var out []string
	for _, x := range v.([]any) {
		out = append(out, x.(string))
	}
	return out
}

func TestCreateRepoValidation(t *testing.T) {
	e, _, _ := debAPIEnv(t)
	for name, body := range map[string]string{
		"bad suite":        `{"slug":"a","kind":"deb","mode":"public","suite":"Bad Suite"}`,
		"all-only arch":    `{"slug":"b","kind":"deb","mode":"public","arches":["all"]}`,
		"unknown kind":     `{"slug":"c","kind":"zip","mode":"public"}`,
		"deb field on rpm": `{"slug":"d","mode":"public","suite":"stable"}`,
	} {
		if w := e.Do("POST", "/api/repos", body); w.Code != 400 {
			t.Errorf("%s: %d %s", name, w.Code, w.Body)
		}
	}
}

func TestListShowsKind(t *testing.T) {
	e, _, _ := debAPIEnv(t)
	e.Do("POST", "/api/repos", `{"slug":"r","mode":"public"}`)
	e.Do("POST", "/api/repos", `{"slug":"d","kind":"deb","mode":"public"}`)
	w := e.Do("GET", "/api/repos", "")
	var list []map[string]any
	json.Unmarshal(w.Body.Bytes(), &list)
	kinds := map[string]any{}
	for _, r := range list {
		kinds[r["slug"].(string)] = r["kind"]
	}
	if kinds["r"] != "rpm" || kinds["d"] != "deb" {
		t.Fatalf("%v", kinds)
	}
	if _, has := list[0]["suite"]; list[0]["slug"] == "r" && has {
		t.Fatal("rpm repo JSON carries deb fields")
	}
}

func TestPatchDebArches(t *testing.T) {
	e, _, hooked := debAPIEnv(t)
	e.Do("POST", "/api/repos", `{"slug":"d","kind":"deb","mode":"public"}`)
	e.Do("POST", "/api/repos", `{"slug":"r","mode":"public"}`)
	*hooked = nil
	if w := e.Do("PATCH", "/api/repos/d", `{"arches":["amd64","riscv64"]}`); w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if len(*hooked) != 1 || (*hooked)[0] != "d" {
		t.Fatalf("no rebuild after arch change: %v", *hooked)
	}
	m := jsonOf(t, e.Do("GET", "/api/repos/d", "").Body.Bytes())
	if strings.Join(toStrings(m["arches"]), ",") != "amd64,riscv64" {
		t.Fatalf("%v", m)
	}
	if w := e.Do("PATCH", "/api/repos/d", `{"arches":["all"]}`); w.Code != 400 {
		t.Errorf("all-only: %d", w.Code)
	}
	if w := e.Do("PATCH", "/api/repos/r", `{"arches":["amd64"]}`); w.Code != 409 {
		t.Errorf("arches on rpm repo: %d", w.Code)
	}
}

func TestUploadDebViaAPI(t *testing.T) {
	e, _, _ := debAPIEnv(t)
	e.Do("POST", "/api/repos", `{"slug":"d","kind":"deb","mode":"public"}`)
	name, b := debBytes(t, "hello", "1:2.0-1", "amd64")
	w := upload(e, "d", name, b)
	if w.Code != 201 || !strings.Contains(w.Body.String(), `"component":"main"`) || !strings.Contains(w.Body.String(), `"version":"1:2.0-1"`) {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if w := e.Do("GET", "/api/repos/d/packages", ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"name":"hello"`) {
		t.Fatalf("list: %d %s", w.Code, w.Body)
	}
	if w := upload(e, "d", name, b); w.Code != 409 {
		t.Errorf("duplicate: %d", w.Code)
	}
	if w := e.Do("DELETE", "/api/repos/d/packages/"+name, ""); w.Code != 200 {
		t.Errorf("delete: %d", w.Code)
	}
}

func TestUploadDebErrors(t *testing.T) {
	e, _, _ := debAPIEnv(t)
	e.Do("POST", "/api/repos", `{"slug":"d","kind":"deb","mode":"public","arches":["amd64"]}`)
	e.Do("POST", "/api/repos", `{"slug":"r","mode":"public"}`)
	rpm, _ := os.ReadFile("../rpmmeta/testdata/hello-1.2.3-4.noarch.rpm")
	name, b := debBytes(t, "hello", "1", "amd64")
	_, arm := debBytes(t, "hello", "1", "arm64")
	for what, w := range map[string]int{
		"rpm to deb repo":   upload(e, "d", "hello-1.2.3-4.noarch.rpm", rpm).Code,
		"deb to rpm repo":   upload(e, "r", name, b).Code,
		"filename mismatch": upload(e, "d", "other_9_amd64.deb", b).Code,
		"arch not in repo":  upload(e, "d", "hello_1_arm64.deb", arm).Code,
		"not a deb":         upload(e, "d", "junk_1_amd64.deb", []byte("not a deb")).Code,
	} {
		if w != 400 {
			t.Errorf("%s: %d", what, w)
		}
	}
}

func TestPromoteDebIncompatible409(t *testing.T) {
	e, _, _ := debAPIEnv(t)
	e.Do("POST", "/api/repos", `{"slug":"dev","kind":"deb","mode":"public","arches":["amd64","arm64"]}`)
	if w := e.Do("POST", "/api/repos", `{"slug":"prod","kind":"deb","mode":"public","arches":["amd64"],"previous":"dev"}`); w.Code != 201 {
		t.Fatalf("create prod: %d %s", w.Code, w.Body)
	}
	name, b := debBytes(t, "hello", "1", "arm64")
	if w := upload(e, "dev", name, b); w.Code != 201 {
		t.Fatalf("upload: %d %s", w.Code, w.Body)
	}
	if w := e.Do("POST", "/api/repos/dev/promote", `{"filenames":["`+name+`"]}`); w.Code != 409 {
		t.Fatalf("promote: %d %s", w.Code, w.Body)
	}
}

func TestKindsCannotShareAPipeline(t *testing.T) {
	e, _, _ := debAPIEnv(t)
	e.Do("POST", "/api/repos", `{"slug":"d","kind":"deb","mode":"public"}`)
	if w := e.Do("POST", "/api/repos", `{"slug":"r","mode":"public","previous":"d"}`); w.Code != 409 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
}

func TestSourcesOnDebRepo(t *testing.T) {
	e, _, _ := debAPIEnv(t)
	e.Do("POST", "/api/repos", `{"slug":"d","kind":"deb","mode":"public"}`)
	if w := e.Do("POST", "/api/repos/d/sources", `{"kind":"plex"}`); w.Code != 409 {
		t.Errorf("plex on deb: %d %s", w.Code, w.Body)
	}
	if w := e.Do("POST", "/api/repos/d/sources", `{"kind":"github","spec":"owner/tool"}`); w.Code != 201 {
		t.Errorf("github on deb: %d %s", w.Code, w.Body)
	}
}
