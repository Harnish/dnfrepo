package api

import "testing"

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
