package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"dnfrepo/internal/sources"
)

// A new source is checked right away, not at the next daily tick.
func TestAddSourceRunsImmediately(t *testing.T) {
	e := newEnv(t)
	gh := httptest.NewServer(http.NotFoundHandler()) // every release lookup 404s: the run records an error
	defer gh.Close()
	(&Repos{Store: e.St, DataDir: t.TempDir()}).Routes(e.Mux)
	(&Sources{Store: e.St, Scheduler: &sources.Scheduler{Store: e.St, GitHubAPI: gh.URL}}).Routes(e.Mux)
	e.Do("POST", "/api/repos", `{"slug":"dev","name":"Dev","mode":"public"}`)
	if w := e.Do("POST", "/api/repos/dev/sources", `{"kind":"github","spec":"o/n"}`); w.Code != 201 {
		t.Fatalf("add: %d %s", w.Code, w.Body)
	}
	repo, _ := e.St.RepoBySlug(context.Background(), "dev")
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		ss, err := e.St.ListSources(context.Background(), repo.ID)
		if err == nil && len(ss) == 1 && ss[0].LastRun != nil {
			return
		}
	}
	t.Fatal("source was not run after being added")
}

func TestSourceOnLaterStageRejected(t *testing.T) {
	e := newEnv(t)
	(&Repos{Store: e.St, DataDir: t.TempDir()}).Routes(e.Mux)
	(&Sources{Store: e.St}).Routes(e.Mux)
	e.Do("POST", "/api/repos", `{"slug":"dev","name":"Dev","mode":"public"}`)
	e.Do("POST", "/api/repos", `{"slug":"test","name":"Test","mode":"public","previous":"dev"}`)
	if w := e.Do("POST", "/api/repos/test/sources", `{"kind":"plex"}`); w.Code != 409 {
		t.Fatalf("later stage: %d %s", w.Code, w.Body)
	}
	if w := e.Do("POST", "/api/repos/dev/sources", `{"kind":"plex"}`); w.Code != 201 {
		t.Fatalf("first stage plex: %d %s", w.Code, w.Body)
	}
	if w := e.Do("POST", "/api/repos/dev/sources", `{"kind":"github","spec":"https://github.com/o/n"}`); w.Code != 201 {
		t.Fatalf("github url: %d %s", w.Code, w.Body)
	}
	if w := e.Do("POST", "/api/repos/dev/sources", `{"kind":"github","spec":"not a repo"}`); w.Code != 400 {
		t.Fatalf("bad spec: %d", w.Code)
	}
	if w := e.Do("POST", "/api/repos/dev/sources", `{"kind":"ftp","spec":"x"}`); w.Code != 400 {
		t.Fatalf("bad kind: %d", w.Code)
	}
}
