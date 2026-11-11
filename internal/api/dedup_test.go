package api

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"dnfrepo/internal/pipeline"
)

func TestDedupSweepViaAPI(t *testing.T) {
	e := newEnv(t)
	dir := t.TempDir()
	pl := &pipeline.Service{Store: e.St, Publisher: nopPub{dir}, MaxBytes: 1 << 20}
	(&Repos{Store: e.St, DataDir: dir}).Routes(e.Mux)
	pk := &Packages{Pipeline: pl, Actor: func(*http.Request) string { return "admin@x.org" }}
	pk.Routes(e.Mux)
	pk.RoutesDedup(e.Mux)

	if w := e.DoAnon("POST", "/api/dedup", ""); w.Code != 401 {
		t.Fatalf("anon: %d", w.Code)
	}
	rpm, _ := os.ReadFile("../rpmmeta/testdata/hello-1.2.3-4.noarch.rpm")
	for _, slug := range []string{"a", "b"} {
		e.Do("POST", "/api/repos", `{"slug":"`+slug+`","name":"`+slug+`","mode":"public"}`)
		if w := upload(e, slug, "hello-1.2.3-4.noarch.rpm", rpm); w.Code != 201 {
			t.Fatalf("upload %s: %d %s", slug, w.Code, w.Body)
		}
	}
	w := e.Do("POST", "/api/dedup", "")
	var out struct {
		Linked     int64
		BytesSaved int64 `json:"bytes_saved"`
		Skipped    int64
	}
	json.Unmarshal(w.Body.Bytes(), &out)
	if w.Code != 200 || out.Linked != 1 || out.BytesSaved != int64(len(rpm)) {
		t.Fatalf("sweep: %d %s", w.Code, w.Body)
	}
	sa, _ := os.Stat(filepath.Join(dir, "repos", "a", "hello-1.2.3-4.noarch.rpm"))
	sb, _ := os.Stat(filepath.Join(dir, "repos", "b", "hello-1.2.3-4.noarch.rpm"))
	if !os.SameFile(sa, sb) {
		t.Fatal("not linked")
	}
}
