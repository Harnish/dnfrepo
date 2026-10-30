package api

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
)

func TestRepoUserTokenShownOnceAndGrants(t *testing.T) {
	e := newEnv(t)
	(&RepoUsers{Store: e.St}).Routes(e.Mux)
	(&Repos{Store: e.St, DataDir: t.TempDir()}).Routes(e.Mux)
	e.Do("POST", "/api/repos", `{"slug":"dev","name":"Dev","mode":"users"}`)
	w := e.Do("POST", "/api/repo-users", `{"name":"alice"}`)
	if w.Code != 201 {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	var created struct {
		ID    int64
		Token string
	}
	json.Unmarshal(w.Body.Bytes(), &created)
	if created.Token == "" {
		t.Fatal("no token on create")
	}
	if l := e.Do("GET", "/api/repo-users", ""); strings.Contains(l.Body.String(), created.Token) {
		t.Fatal("token leaked in list")
	}
	if w := e.Do("POST", "/api/repo-users", `{"name":"a:b"}`); w.Code != 400 {
		t.Fatalf("colon name: %d", w.Code)
	}
	if w := e.Do("PUT", "/api/repos/dev/users", `{"user_ids":[`+strconv.FormatInt(created.ID, 10)+`]}`); w.Code != 200 {
		t.Fatalf("grant: %d %s", w.Code, w.Body)
	}
	if w := e.DoAnon("GET", "/api/repo-users", ""); w.Code != 401 {
		t.Fatalf("anon: %d", w.Code)
	}
}
