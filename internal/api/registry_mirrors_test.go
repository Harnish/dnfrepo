package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"dnfrepo/internal/imagemirror"
	"dnfrepo/internal/secretbox"
	"dnfrepo/internal/store"
)

// basicUpstream serves one single-platform manifest at a/b:latest behind Basic auth u:pw.
func basicUpstream(t *testing.T) (host, digest string) {
	t.Helper()
	m := []byte(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"mediaType":"application/vnd.oci.image.config.v1+json","digest":"sha256:` + strings.Repeat("0", 64) + `","size":2},"layers":[]}`)
	h := sha256.Sum256(m)
	dg := "sha256:" + hex.EncodeToString(h[:])
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u, p, ok := r.BasicAuth(); !ok || u != "u" || p != "pw" {
			w.Header().Set("WWW-Authenticate", `Basic realm="x"`)
			w.WriteHeader(401)
			return
		}
		if r.URL.Path != "/v2/a/b/manifests/latest" && r.URL.Path != "/v2/a/b/manifests/"+dg {
			w.WriteHeader(404)
			return
		}
		w.Header().Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")
		w.Header().Set("Docker-Content-Digest", dg)
		w.Write(m)
	}))
	t.Cleanup(srv.Close)
	return strings.TrimPrefix(srv.URL, "http://"), dg
}

func newMirrorEnv(t *testing.T, withKey bool) *regEnv {
	t.Helper()
	r := newRegEnv(t)
	var box *secretbox.Box
	if withKey {
		box, _ = secretbox.New("test-secret-key-test-secret-key!")
	}
	m := &imagemirror.Mirrors{Store: r.St, Box: box, StagingDir: t.TempDir(), Scheme: "http"}
	a := &RegistryAPI{Store: r.St, Mirrors: m, Actor: func(*http.Request) string { return "admin@x.org" }}
	a.mirrorRoutes(r.Mux)
	return r
}

func TestMirrorAPICreateListNoSecret(t *testing.T) {
	r := newMirrorEnv(t, true)
	w := r.Do("POST", "/api/registry/mirrors", `{"upstream":"127.0.0.1:1/plexinc/pms-docker","platforms":["linux/amd64"],"username":"u","token":"s3cret-tok"}`)
	if w.Code != 201 || strings.Contains(w.Body.String(), "s3cret-tok") {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	var row struct {
		ID             int64
		LocalName      string `json:"local_name"`
		Tag            string
		Keep           int
		HasCredentials bool `json:"has_credentials"`
	}
	json.Unmarshal(w.Body.Bytes(), &row)
	if row.LocalName != "plexinc/pms-docker" || row.Tag != "latest" || row.Keep != 3 || !row.HasCredentials {
		t.Fatalf("%+v", row)
	}
	l := r.Do("GET", "/api/registry/mirrors", "")
	if l.Code != 200 || strings.Contains(l.Body.String(), "s3cret-tok") || !strings.Contains(l.Body.String(), `"has_credentials":true`) {
		t.Fatalf("list %d %s", l.Code, l.Body)
	}
	if w := r.Do("DELETE", "/api/registry/mirrors/"+strconv.FormatInt(row.ID, 10), ""); w.Code != 200 {
		t.Fatalf("delete %d", w.Code)
	}
	if w := r.Do("DELETE", "/api/registry/mirrors/"+strconv.FormatInt(row.ID, 10), ""); w.Code != 404 {
		t.Fatalf("delete again %d", w.Code)
	}
}

func TestMirrorAPIValidation(t *testing.T) {
	r := newMirrorEnv(t, true)
	for _, body := range []string{
		`{"upstream":"alpine@sha256:` + strings.Repeat("a", 64) + `"}`,
		`{"upstream":"Bad Ref"}`,
		`{"upstream":"alpine","keep":-1}`,
		`{"upstream":"alpine","local_name":"Bad/Name"}`,
		`{"upstream":"alpine","platforms":["linux"]}`,
		`{"upstream":"alpine","username":"u"}`,
	} {
		if w := r.Do("POST", "/api/registry/mirrors", body); w.Code != 400 {
			t.Errorf("%s: %d %s", body, w.Code, w.Body)
		}
	}
}

func TestMirrorAPIDuplicate409(t *testing.T) {
	r := newMirrorEnv(t, true)
	if w := r.Do("POST", "/api/registry/mirrors", `{"upstream":"127.0.0.1:1/a/b"}`); w.Code != 201 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if w := r.Do("POST", "/api/registry/mirrors", `{"upstream":"127.0.0.2:1/x/y","local_name":"a/b"}`); w.Code != 409 {
		t.Fatalf("dup %d %s", w.Code, w.Body)
	}
}

func TestMirrorAPICredsWithoutKey400(t *testing.T) {
	r := newMirrorEnv(t, false)
	if w := r.Do("POST", "/api/registry/mirrors", `{"upstream":"127.0.0.1:1/a/b","username":"u","token":"t"}`); w.Code != 400 || !strings.Contains(w.Body.String(), "SECRET_KEY") {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
}

func TestMirrorAPIPutPlatformsClearsDigest(t *testing.T) {
	r := newMirrorEnv(t, true)
	w := r.Do("POST", "/api/registry/mirrors", `{"upstream":"127.0.0.1:1/a/b","platforms":["linux/amd64"],"username":"u","token":"t"}`)
	var row struct{ ID int64 }
	json.Unmarshal(w.Body.Bytes(), &row)
	ctx := context.Background()
	if _, err := r.St.ImageMirrorByID(ctx, row.ID); err != nil {
		t.Fatal(err)
	}
	r.St.AddImageVersion(ctx, imageVersion(row.ID), "sha256:up")
	id := strconv.FormatInt(row.ID, 10)
	if w := r.Do("PUT", "/api/registry/mirrors/"+id, `{"platforms":["linux/amd64"],"keep":5}`); w.Code != 200 {
		t.Fatalf("put %d %s", w.Code, w.Body)
	}
	m, _ := r.St.ImageMirrorByID(ctx, row.ID)
	if m.Keep != 5 || m.UpstreamDigest != "sha256:up" || m.Secret == nil {
		t.Fatalf("same platforms: %+v", m)
	}
	if w := r.Do("PUT", "/api/registry/mirrors/"+id, `{"platforms":["linux/arm64"],"keep":5}`); w.Code != 200 {
		t.Fatalf("put %d %s", w.Code, w.Body)
	}
	if m, _ = r.St.ImageMirrorByID(ctx, row.ID); m.UpstreamDigest != "" || m.Secret == nil {
		t.Fatalf("platform change kept digest or lost creds: %+v", m)
	}
	if w := r.Do("PUT", "/api/registry/mirrors/"+id, `{"platforms":[],"keep":5,"username":""}`); w.Code != 200 {
		t.Fatalf("clear creds %d %s", w.Code, w.Body)
	}
	if m, _ = r.St.ImageMirrorByID(ctx, row.ID); m.Secret != nil {
		t.Fatalf("creds not cleared: %+v", m)
	}
}

func TestMirrorAPIInspectByID(t *testing.T) {
	r := newMirrorEnv(t, true)
	host, dg := basicUpstream(t)
	if w := r.Do("POST", "/api/registry/mirrors/inspect", fmt.Sprintf(`{"upstream":%q}`, host+"/a/b")); w.Code != 502 {
		t.Fatalf("no creds: %d %s", w.Code, w.Body)
	}
	w := r.Do("POST", "/api/registry/mirrors", fmt.Sprintf(`{"upstream":%q,"username":"u","token":"pw"}`, host+"/a/b"))
	var row struct{ ID int64 }
	json.Unmarshal(w.Body.Bytes(), &row)
	w = r.Do("POST", "/api/registry/mirrors/inspect", fmt.Sprintf(`{"id":%d}`, row.ID))
	if w.Code != 200 || !strings.Contains(w.Body.String(), dg) || !strings.Contains(w.Body.String(), `"platforms":[]`) {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
}

func imageVersion(id int64) store.ImageVersion {
	return store.ImageVersion{MirrorID: id, Tag: "latest-20261123", Digest: "sha256:l"}
}
