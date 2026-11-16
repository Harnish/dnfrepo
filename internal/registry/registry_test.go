package registry

import (
	"testing"

	"dnfrepo/internal/store"
)

func TestAnonymousPushRefused(t *testing.T) {
	r := newTestReg(t)
	if _, st := pushImage(t, r.Srv.URL, "app", "v1", "", ""); st != 401 {
		t.Fatalf("anonymous push status %d", st)
	}
	resp, _ := do(t, "POST", r.Srv.URL+"/v2/app/blobs/uploads/", "", "", nil, nil)
	if resp.StatusCode != 401 || resp.Header.Get("WWW-Authenticate") == "" {
		t.Fatalf("%d %q", resp.StatusCode, resp.Header.Get("WWW-Authenticate"))
	}
}

func TestPushThenAnonPullOfPublicRepo(t *testing.T) {
	r := newTestReg(t)
	dg, st := pushImage(t, r.Srv.URL, "app", "v1", "ci", "ptok")
	if st != 0 {
		t.Fatalf("push status %d", st)
	}
	resp, _ := do(t, "GET", r.Srv.URL+"/v2/app/manifests/v1", "", "", nil, ociAccept)
	if resp.StatusCode != 200 || resp.Header.Get("Docker-Content-Digest") != dg {
		t.Fatalf("manifest %d %q", resp.StatusCode, resp.Header.Get("Docker-Content-Digest"))
	}
	resp, _ = do(t, "GET", r.Srv.URL+"/v2/app/blobs/"+sha([]byte("layer-appv1amd64")), "", "", nil, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("blob %d", resp.StatusCode)
	}
}

func TestDeleteManifestRefusedForPushToken(t *testing.T) {
	r := newTestReg(t)
	dg, _ := pushImage(t, r.Srv.URL, "app", "v1", "ci", "ptok")
	resp, _ := do(t, "DELETE", r.Srv.URL+"/v2/app/manifests/"+dg, "ci", "ptok", nil, nil)
	if resp.StatusCode != 401 {
		t.Fatalf("delete status %d", resp.StatusCode)
	}
	resp, _ = do(t, "GET", r.Srv.URL+"/v2/app/manifests/v1", "", "", nil, ociAccept)
	if resp.StatusCode != 200 {
		t.Fatalf("image gone: %d", resp.StatusCode)
	}
}

func TestUsersRepoAnonPull401(t *testing.T) {
	r := newTestReg(t)
	r.F.rows["priv"] = store.RegistryRepo{Name: "priv", Mode: store.ModeUsers, Exists: true}
	pushImage(t, r.Srv.URL, "priv", "v1", "ci", "ptok")
	priv, _ := do(t, "GET", r.Srv.URL+"/v2/priv/manifests/v1", "", "", nil, ociAccept)
	ghost, _ := do(t, "GET", r.Srv.URL+"/v2/ghost/manifests/v1", "", "", nil, ociAccept)
	if priv.StatusCode != 401 || ghost.StatusCode != 401 {
		t.Fatalf("priv %d ghost %d", priv.StatusCode, ghost.StatusCode)
	}
	if priv.Header.Get("WWW-Authenticate") != ghost.Header.Get("WWW-Authenticate") {
		t.Fatal("challenge differs between private and unknown names")
	}
}

func TestUsersRepoGrantedUserPulls(t *testing.T) {
	r := newTestReg(t)
	r.F.rows["priv"] = store.RegistryRepo{Name: "priv", Mode: store.ModeUsers, Exists: true}
	r.F.users["priv|alice"] = "utok"
	pushImage(t, r.Srv.URL, "priv", "v1", "ci", "ptok")
	if resp, _ := do(t, "GET", r.Srv.URL+"/v2/priv/manifests/v1", "alice", "utok", nil, ociAccept); resp.StatusCode != 200 {
		t.Fatalf("granted user %d", resp.StatusCode)
	}
	if resp, _ := do(t, "GET", r.Srv.URL+"/v2/priv/manifests/v1", "alice", "bad", nil, ociAccept); resp.StatusCode != 401 {
		t.Fatalf("bad token %d", resp.StatusCode)
	}
	if _, st := pushImage(t, r.Srv.URL, "priv", "v2", "alice", "utok"); st != 401 {
		t.Fatalf("user token pushed: %d", st)
	}
}

func TestPushCreatesDefaultSettingsRow(t *testing.T) {
	r := newTestReg(t)
	pushImage(t, r.Srv.URL, "fresh/img", "v1", "ci", "ptok")
	if row := r.F.rows["fresh/img"]; !row.Exists || row.Mode != store.ModePublic || row.Listed {
		t.Fatalf("%+v", row)
	}
}

func TestNestedRepoName(t *testing.T) {
	r := newTestReg(t)
	if _, st := pushImage(t, r.Srv.URL, "a/b/c", "v1", "ci", "ptok"); st != 0 {
		t.Fatalf("push %d", st)
	}
	if resp, _ := do(t, "GET", r.Srv.URL+"/v2/a/b/c/manifests/v1", "", "", nil, ociAccept); resp.StatusCode != 200 {
		t.Fatalf("pull %d", resp.StatusCode)
	}
	// "a/b" is only a parent directory, not an image
	if resp, _ := do(t, "GET", r.Srv.URL+"/v2/a/b/manifests/v1", "", "", nil, ociAccept); resp.StatusCode != 401 {
		t.Fatalf("parent dir treated as an image: %d", resp.StatusCode)
	}
}

var ociAccept = map[string]string{"Accept": "application/vnd.oci.image.manifest.v1+json, application/vnd.oci.image.index.v1+json"}
