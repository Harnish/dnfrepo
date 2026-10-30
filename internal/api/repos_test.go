package api

import (
	"context"
	"dnfrepo/internal/store"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestCreateRepoRequiresAdmin(t *testing.T) {
	e := newEnv(t)
	(&Repos{Store: e.St, DataDir: t.TempDir()}).Routes(e.Mux)
	if w := e.DoAnon("POST", "/api/repos", `{"slug":"dev","name":"Dev","mode":"public"}`); w.Code != 401 {
		t.Fatalf("anon create: %d", w.Code)
	}
}

func TestCreateRepoMakesDirAndFiresHook(t *testing.T) {
	e := newEnv(t)
	dir := t.TempDir()
	var hooked []string
	(&Repos{Store: e.St, DataDir: dir, OnChange: func(s string) { hooked = append(hooked, s) }}).Routes(e.Mux)
	w := e.Do("POST", "/api/repos", `{"slug":"dev","name":"Dev","mode":"public"}`)
	if w.Code != 201 {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	if st, err := os.Stat(filepath.Join(dir, "repos", "dev")); err != nil || !st.IsDir() {
		t.Fatal("repo dir missing")
	}
	if len(hooked) != 1 || hooked[0] != "dev" {
		t.Fatalf("hook=%v", hooked)
	}
}

func TestChainViaAPIAndDeleteGuard(t *testing.T) {
	e := newEnv(t)
	(&Repos{Store: e.St, DataDir: t.TempDir()}).Routes(e.Mux)
	e.Do("POST", "/api/repos", `{"slug":"dev","name":"Dev","mode":"public"}`)
	if w := e.Do("POST", "/api/repos", `{"slug":"test","name":"Test","mode":"ips","previous":"dev"}`); w.Code != 201 {
		t.Fatalf("create test: %d %s", w.Code, w.Body)
	}
	if w := e.Do("POST", "/api/repos", `{"slug":"x","name":"X","mode":"public","previous":"dev"}`); w.Code != 409 {
		t.Fatalf("second child of dev: %d", w.Code)
	}
	if w := e.Do("DELETE", "/api/repos/dev", ""); w.Code != 409 {
		t.Fatalf("delete mid-chain: %d", w.Code)
	}
	w := e.Do("GET", "/api/repos", "")
	var list []map[string]any
	json.Unmarshal(w.Body.Bytes(), &list)
	if len(list) != 2 {
		t.Fatalf("list=%s", w.Body)
	}
}

func TestIPsEndpointValidates(t *testing.T) {
	e := newEnv(t)
	(&Repos{Store: e.St, DataDir: t.TempDir()}).Routes(e.Mux)
	e.Do("POST", "/api/repos", `{"slug":"dev","name":"Dev","mode":"ips"}`)
	if w := e.Do("PUT", "/api/repos/dev/ips", `{"ips":["10.0.0.0/8","1.2.3.4"]}`); w.Code != 200 {
		t.Fatalf("good: %d %s", w.Code, w.Body)
	}
	if w := e.Do("PUT", "/api/repos/dev/ips", `{"ips":["not-an-ip"]}`); w.Code != 400 {
		t.Fatalf("bad: %d", w.Code)
	}
}

func TestRelinkRepoWithPackagesIs409(t *testing.T) {
	e := newEnv(t)
	(&Repos{Store: e.St, DataDir: t.TempDir()}).Routes(e.Mux)
	e.Do("POST", "/api/repos", `{"slug":"dev","name":"Dev","mode":"public"}`)
	e.Do("POST", "/api/repos", `{"slug":"scratch","name":"Scratch","mode":"public"}`)
	sc, _ := e.St.RepoBySlug(context.Background(), "scratch")
	e.St.AddPackage(context.Background(), store.Package{RepoID: sc.ID, Filename: "evil-1.rpm", Name: "evil", Version: "1", Release: "1", Arch: "noarch", SHA256: "x", Size: 1})
	if w := e.Do("PUT", "/api/repos/scratch/previous", `{"previous":"dev"}`); w.Code != 409 {
		t.Fatalf("got %d %s", w.Code, w.Body)
	}
}
