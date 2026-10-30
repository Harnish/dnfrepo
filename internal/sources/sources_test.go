package sources

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func ghServer(t *testing.T, tag string, assets ...string) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/releases/latest") {
			http.NotFound(w, r)
			return
		}
		type asset struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		}
		var as []asset
		for _, a := range assets {
			as = append(as, asset{a, srv.URL + "/dl/" + a})
		}
		json.NewEncoder(w).Encode(map[string]any{"tag_name": tag, "assets": as})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestLatestGitHubPicksAMD64RPM(t *testing.T) {
	srv := ghServer(t, "v0.4.1", "tool_arm64.rpm", "tool_amd64.deb", "tool_amd64.rpm", "tool_x86_64.rpm")
	rel, err := LatestGitHub(context.Background(), srv.Client(), srv.URL, "", "https://github.com/o/tool")
	if err != nil {
		t.Fatal(err)
	}
	if rel.Filename != "tool_amd64-0.4.1.rpm" || rel.URL != srv.URL+"/dl/tool_amd64.rpm" {
		t.Fatalf("rel=%+v", rel)
	}
}

func TestLatestGitHubNoRPMErrors(t *testing.T) {
	srv := ghServer(t, "v1.0.0", "tool_amd64.deb", "tool_arm64.rpm")
	if _, err := LatestGitHub(context.Background(), srv.Client(), srv.URL, "", "o/tool"); err == nil {
		t.Fatal("expected error")
	}
}

func TestLatestGitHubSendsToken(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Authorization")
		w.Write([]byte(`{"tag_name":"v1.0","assets":[]}`))
	}))
	defer srv.Close()
	LatestGitHub(context.Background(), srv.Client(), srv.URL, "tok123", "o/tool")
	if got != "Bearer tok123" {
		t.Fatalf("auth=%q", got)
	}
}

func TestLatestPlexPicksRedhatX8664(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"computer":{"Linux":{"releases":[
		 {"build":"linux-x86","distro":"redhat","url":"https://d.plex.tv/x/plexmediaserver-1.43.5.i686.rpm"},
		 {"build":"linux-x86_64","distro":"debian","url":"https://d.plex.tv/x/plexmediaserver_1.43.5_amd64.deb"},
		 {"build":"linux-x86_64","distro":"redhat","url":"https://d.plex.tv/x/plexmediaserver-1.43.5.x86_64.rpm"}]}}}`)
	}))
	defer srv.Close()
	rel, err := LatestPlex(context.Background(), srv.Client(), srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if rel.Filename != "plexmediaserver-1.43.5.x86_64.rpm" || !strings.HasSuffix(rel.URL, "x86_64.rpm") {
		t.Fatalf("rel=%+v", rel)
	}
}
