package mirror

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

const fedoraRepo = `[fedora]
name=Fedora $releasever - $basearch
# a comment
; another comment
#baseurl=http://download.example/pub/fedora/linux/releases/$releasever/Everything/$basearch/os/
metalink=https://mirrors.fedoraproject.org/metalink?repo=fedora-$releasever&arch=$basearch
enabled=1
countme=1
metadata_expire=7d
repo_gpgcheck=0
type=rpm
gpgcheck=1
gpgkey=file:///etc/pki/rpm-gpg/RPM-GPG-KEY-fedora-$releasever-$basearch
skip_if_unavailable=False

[fedora-debuginfo]
name=Fedora $releasever - $basearch - Debug
metalink = https://mirrors.fedoraproject.org/metalink?repo=fedora-debug-$releasever&arch=$basearch
enabled=0
gpgkey=file:///etc/pki/rpm-gpg/RPM-GPG-KEY-fedora-$releasever-$basearch
`

func TestParseFedoraRepoFile(t *testing.T) {
	got, err := ParseRepoFile(fedoraRepo)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID != "fedora" || got[1].ID != "fedora-debuginfo" {
		t.Fatalf("sections: %+v", got)
	}
	f := got[0]
	if f.Name != "Fedora $releasever - $basearch" || f.BaseURL != "" || !f.Enabled || f.RepoGPGCheck ||
		f.Metalink != "https://mirrors.fedoraproject.org/metalink?repo=fedora-$releasever&arch=$basearch" ||
		f.GPGKey != "file:///etc/pki/rpm-gpg/RPM-GPG-KEY-fedora-$releasever-$basearch" {
		t.Fatalf("fedora: %+v", f)
	}
	if d := got[1]; d.Enabled || d.Metalink == "" {
		t.Fatalf("debuginfo: %+v", d)
	}
}

func TestParseDnfrepoUsersModeFile(t *testing.T) {
	got, err := ParseRepoFile(`[mine]
name = Mine
baseurl = https://repo.example/r/mine/stable/
    https://other.example/ignored/
username = bob
password = s3cret
repo_gpgcheck = 1
gpgkey = https://repo.example/r/mine/gpg.key
`)
	if err != nil || len(got) != 1 {
		t.Fatal(got, err)
	}
	want := RepoSection{ID: "mine", Name: "Mine", BaseURL: "https://repo.example/r/mine/stable/", GPGKey: "https://repo.example/r/mine/gpg.key",
		RepoGPGCheck: true, Enabled: true, Username: "bob", Password: "s3cret"}
	if got[0] != want {
		t.Fatalf("got %+v", got[0])
	}
}

func TestParseRejectsFileWithoutURL(t *testing.T) {
	if _, err := ParseRepoFile("[a]\nname=x\nenabled=1\n"); err == nil {
		t.Fatal("want error")
	}
}

func TestParseSkipsSectionsWithoutURL(t *testing.T) {
	got, err := ParseRepoFile("[a]\nname=x\n[b]\nbaseurl=http://x/\n")
	if err != nil || len(got) != 1 || got[0].ID != "b" {
		t.Fatal(got, err)
	}
}

func TestVariablesAndResolve(t *testing.T) {
	s := "https://x/$releasever/${basearch}/$$literal/$releasever"
	if v := Variables(s); !reflect.DeepEqual(v, []string{"basearch", "releasever"}) {
		t.Fatalf("vars %v", v)
	}
	out, missing := ResolveVars(s, map[string]string{"releasever": "42"})
	if out != "https://x/42/${basearch}/$$literal/42" || !reflect.DeepEqual(missing, []string{"basearch"}) {
		t.Fatalf("%q %v", out, missing)
	}
	out, missing = ResolveVars(s, map[string]string{"releasever": "42", "basearch": "x86_64"})
	if out != "https://x/42/x86_64/$$literal/42" || len(missing) != 0 {
		t.Fatalf("%q %v", out, missing)
	}
}

func TestResolveMetalinkPicksFirstRepomdURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<?xml version="1.0"?><metalink><files><file name="repomd.xml"><resources>
<url protocol="https" type="https">https://a.example/other/thing.rpm</url>
<url protocol="https" type="https">https://m1.example/fedora/42/x86_64/os/repodata/repomd.xml</url>
<url protocol="https" type="https">https://m2.example/pub/repodata/repomd.xml</url>
</resources></file></files></metalink>`)
	}))
	defer srv.Close()
	got, err := ResolveMetalink(context.Background(), srv.Client(), srv.URL)
	if err != nil || got != "https://m1.example/fedora/42/x86_64/os/" {
		t.Fatal(got, err)
	}
	bad := httptest.NewServer(http.NotFoundHandler())
	defer bad.Close()
	if _, err := ResolveMetalink(context.Background(), bad.Client(), bad.URL); err == nil {
		t.Fatal("want error on 404")
	}
}

func TestResolveMirrorList(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "# c\n\n  http://first.example/os/  \nhttp://second/\n")
	}))
	defer srv.Close()
	got, err := ResolveMirrorList(context.Background(), srv.Client(), srv.URL)
	if err != nil || got != "http://first.example/os/" {
		t.Fatal(got, err)
	}
}

func TestParseBaseURLContinuationMultiURL(t *testing.T) {
	got, err := ParseRepoFile("[a]\nbaseurl=\n  https://a/x/ https://b/y/\n")
	if err != nil || got[0].BaseURL != "https://a/x/" {
		t.Fatal(got, err)
	}
}

func TestFetchErrorsRedactURL(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	u := "http://user:pass@" + srv.Listener.Addr().String() + "/m?token=SECRET"
	_, err := ResolveMetalink(context.Background(), srv.Client(), u)
	if err == nil || strings.Contains(err.Error(), "SECRET") || strings.Contains(err.Error(), "pass") {
		t.Fatalf("non-200: %v", err)
	}
	srv.Close()
	for _, f := range []func() error{
		func() error { _, e := ResolveMetalink(context.Background(), srv.Client(), u); return e },
		func() error { _, e := ResolveMirrorList(context.Background(), srv.Client(), u); return e },
	} {
		if err := f(); err == nil || strings.Contains(err.Error(), "SECRET") || strings.Contains(err.Error(), "pass") {
			t.Fatalf("conn error: %v", err)
		}
	}
}

func TestResolveMetalinkHTTPOnly(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<?xml version="1.0" encoding="utf-8"?>
<metalink version="3.0" xmlns="http://www.metalinker.org/" xmlns:mm0="http://fedoraproject.org/metalink/ns">
<files><file name="repomd.xml"><mm0:timestamp>1700000000</mm0:timestamp><resources maxconnections="1">
<url protocol="rsync" type="rsync">rsync://r.example/fedora/repodata/repomd.xml</url>
<url protocol="https" type="https">https://m1.example/os/repodata/repomd.xml</url>
</resources></file></files></metalink>`)
	}))
	defer srv.Close()
	got, err := ResolveMetalink(context.Background(), srv.Client(), srv.URL)
	if err != nil || got != "https://m1.example/os/" {
		t.Fatal(got, err)
	}
}

func TestResolveMirrorListHTTPOnly(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "rsync://x/\nftp://y/\nhttps://ok/os/\n")
	}))
	defer srv.Close()
	got, err := ResolveMirrorList(context.Background(), srv.Client(), srv.URL)
	if err != nil || got != "https://ok/os/" {
		t.Fatal(got, err)
	}
}
