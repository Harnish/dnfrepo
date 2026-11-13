package api

import (
	"context"
	"dnfrepo/internal/store"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func mkDebRepo(t *testing.T, e *env, slug string) int64 {
	t.Helper()
	if w := e.Do("POST", "/api/repos", `{"slug":"`+slug+`","kind":"deb","mode":"public"}`); w.Code != 201 {
		t.Fatalf("create %s: %d %s", slug, w.Code, w.Body)
	}
	r, err := e.St.RepoBySlug(context.Background(), slug)
	if err != nil {
		t.Fatal(err)
	}
	return r.ID
}

const debMirrorBody = `{"upstream_url":"https://deb.example/debian/","suite":"bookworm","components":["main","contrib"],"arches":["amd64"]}`

func TestPutDebMirror(t *testing.T) {
	e, _, _ := mirrorEnv(t)
	id := mkDebRepo(t, e, "deb")
	putMirror(t, e, "deb", debMirrorBody, 200)
	m := jsonOf(t, e.Do("GET", "/api/repos/deb/mirror", "").Body.Bytes())
	if m["suite"] != "bookworm" || !reflect.DeepEqual(m["components"], []any{"main", "contrib"}) || !reflect.DeepEqual(m["arches"], []any{"amd64"}) {
		t.Fatalf("GET: %v", m)
	}
	d, err := e.St.DebRepoByID(context.Background(), id)
	if err != nil || d.Suite != "bookworm" || !reflect.DeepEqual(d.Components, []string{"main", "contrib"}) || !reflect.DeepEqual(d.Arches, []string{"amd64"}) {
		t.Fatalf("deb_repos: %+v %v", d, err)
	}
}

func TestPutDebMirrorRepoTextIs400(t *testing.T) {
	e, _, _ := mirrorEnv(t)
	mkDebRepo(t, e, "deb")
	putMirror(t, e, "deb", `{"repo_file":"[x]\nbaseurl=https://h/\n","suite":"bookworm","components":["main"],"arches":["amd64"]}`, 400)
}

func TestPutDebMirrorNeedsSuite400(t *testing.T) {
	e, _, _ := mirrorEnv(t)
	mkDebRepo(t, e, "deb")
	putMirror(t, e, "deb", `{"upstream_url":"https://h/d/","components":["main"],"arches":["amd64"]}`, 400)
	putMirror(t, e, "deb", `{"upstream_url":"https://h/d/","suite":"Bad Suite","components":["main"],"arches":["amd64"]}`, 400)
	putMirror(t, e, "deb", `{"upstream_url":"https://h/d/","suite":"x","components":["main"],"arches":["all"]}`, 400)
	if w := e.Do("GET", "/api/repos/deb/mirror", ""); w.Code != 404 {
		t.Fatalf("rejected PUT left a mirror: %d", w.Code)
	}
}

func TestPutDebMirrorOnRepoWithPackages409(t *testing.T) {
	e, _, _ := mirrorEnv(t)
	mkDebRepo(t, e, "deb")
	name, b := debBytes(t, "a", "1.0", "amd64")
	if w := upload(e, "deb", name, b); w.Code != 201 {
		t.Fatalf("upload: %d %s", w.Code, w.Body)
	}
	putMirror(t, e, "deb", debMirrorBody, 409)
}

func TestDebMirrorRejectsUploads409(t *testing.T) {
	e, _, _ := mirrorEnv(t)
	mkDebRepo(t, e, "deb")
	putMirror(t, e, "deb", debMirrorBody, 200)
	name, b := debBytes(t, "a", "1.0", "amd64")
	if w := upload(e, "deb", name, b); w.Code != 409 {
		t.Fatalf("upload to mirror: %d %s", w.Code, w.Body)
	}
}

func TestSyncNowDebMirror202(t *testing.T) {
	e, _, _ := mirrorEnv(t)
	mkDebRepo(t, e, "deb")
	up := httptest.NewServer(http.NotFoundHandler())
	defer up.Close()
	putMirror(t, e, "deb", strings.Replace(debMirrorBody, "https://deb.example/debian/", up.URL+"/", 1), 200)
	if w := e.Do("POST", "/api/repos/deb/mirror/sync", ""); w.Code != 202 {
		t.Fatalf("sync: %d %s", w.Code, w.Body)
	}
}

func fillDebMirror(t *testing.T, e *env, id int64) {
	t.Helper()
	ctx := context.Background()
	bid, err := e.St.CreateBatch(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	p := store.Package{RepoID: id, Filename: "a_1_amd64.deb", Name: "a", Version: "1", Arch: "amd64", SHA256: "s", Size: 3,
		Component: "main", Control: "Package: a", MD5: "m", SHA1: "h"}
	if err := e.St.AddMirrorPackages(ctx, id, bid, []store.Package{p}, "mirror"); err != nil {
		t.Fatal(err)
	}
}

// Changing the suite or dropping a component of a populated mirror would orphan or mix its packages.
func TestPutDebMirrorPopulatedRejectsSuiteChangeAndComponentRemoval(t *testing.T) {
	e, _, _ := mirrorEnv(t)
	id := mkDebRepo(t, e, "deb")
	putMirror(t, e, "deb", debMirrorBody, 200)
	fillDebMirror(t, e, id)
	putMirror(t, e, "deb", strings.Replace(debMirrorBody, "bookworm", "trixie", 1), 409)
	putMirror(t, e, "deb", strings.Replace(debMirrorBody, `["main","contrib"]`, `["main"]`, 1), 409)
	d, _ := e.St.DebRepoByID(context.Background(), id)
	if d.Suite != "bookworm" || len(d.Components) != 2 {
		t.Fatalf("rejected PUT changed settings: %+v", d)
	}
	putMirror(t, e, "deb", strings.Replace(debMirrorBody, `["main","contrib"]`, `["main","contrib","non-free"]`, 1), 200)
}

func TestPutDebMirrorRebuildsWhenSettingsChange(t *testing.T) {
	e, m, _ := mirrorEnv(t)
	mkDebRepo(t, e, "deb")
	var rebuilt []string
	m.OnChange = func(s string) { rebuilt = append(rebuilt, s) }
	putMirror(t, e, "deb", debMirrorBody, 200)
	if !reflect.DeepEqual(rebuilt, []string{"deb"}) {
		t.Fatalf("first save: %v", rebuilt)
	}
	putMirror(t, e, "deb", strings.Replace(debMirrorBody, `"amd64"`, `"amd64","arm64"`, 1), 200)
	if len(rebuilt) != 2 {
		t.Fatalf("arch change: %v", rebuilt)
	}
	putMirror(t, e, "deb", strings.Replace(debMirrorBody, `"amd64"`, `"amd64","arm64"`, 1), 200)
	if len(rebuilt) != 2 {
		t.Fatalf("unchanged save rebuilt: %v", rebuilt)
	}
}
