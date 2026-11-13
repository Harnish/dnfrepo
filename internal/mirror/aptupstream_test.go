package mirror

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

func aptUp(f *fakeApt, comps, arches []string) *AptUpstream {
	return &AptUpstream{Base: f.URL(), Suite: f.Suite, Components: comps, Arches: arches, Client: f.srv.Client()}
}

func byName(ps []UpstreamPkg) map[string]UpstreamPkg {
	m := map[string]UpstreamPkg{}
	for _, p := range ps {
		m[p.Name] = p
	}
	return m
}

func TestAptPackagesParsesAndFilters(t *testing.T) {
	for _, allIndex := range []bool{false, true} { // Ubuntu-style and Debian-style arch-all placement
		f := newFakeApt(t)
		f.AllIndex = allIndex
		f.Components = []string{"main", "universe"}
		f.Arches = []string{"amd64", "arm64"}
		f.AddDeb("tool", "1:2.0-1", "amd64", "main", []byte("tool-bytes"))
		f.AddDeb("armtool", "1.0-1", "arm64", "main", []byte("arm-bytes"))
		f.AddDeb("data", "3.0", "all", "main", []byte("data-bytes"))
		f.AddDeb("extra", "1.0-1", "amd64", "universe", []byte("extra-bytes")).Extra = "Provides: other\nMulti-Arch: foreign"
		got, err := aptUp(f, []string{"main", "universe"}, []string{"amd64"}).Packages(context.Background())
		if err != nil {
			t.Fatalf("allIndex=%v: %v", allIndex, err)
		}
		m := byName(got)
		if _, has := m["armtool"]; has || m["tool"].Name == "" || m["data"].Name == "" || m["extra"].Name == "" {
			t.Fatalf("allIndex=%v filter: %v", allIndex, got)
		}
		tool := m["tool"]
		if tool.Version != "1:2.0-1" || tool.Release != "" || tool.Arch != "amd64" || tool.Component != "main" ||
			tool.Filename != "tool_2.0-1_amd64.deb" || tool.Href != "pool/main/tool_2.0-1_amd64.deb" ||
			tool.SHA256 != sum([]byte("tool-bytes")) || tool.Size != int64(len("tool-bytes")) {
			t.Fatalf("%+v", tool)
		}
		if !strings.Contains(tool.Control, "Package: tool\nVersion: 1:2.0-1") {
			t.Errorf("control not the verbatim stanza: %q", tool.Control)
		}
		if ex := m["extra"]; ex.Component != "universe" || !strings.Contains(ex.Control, "Multi-Arch: foreign") {
			t.Errorf("%+v", ex)
		}
	}
}

func TestAptGzIndexAccepted(t *testing.T) {
	f := newFakeApt(t)
	f.Compress = "gz"
	f.AddDeb("tool", "1.0", "amd64", "main", []byte("x"))
	got, err := aptUp(f, []string{"main"}, []string{"amd64"}).Packages(context.Background())
	if err != nil || len(got) != 1 {
		t.Fatalf("%v %v", got, err)
	}
}

func TestAptVerifiesSignatures(t *testing.T) {
	other, _ := newEntity(t)
	for name, c := range map[string]struct {
		mode    string
		signer  bool // sign with the wrong key
		key     bool // configure the right key
		require bool
		wantErr string
	}{
		"inrelease ok":            {mode: "inrelease", key: true},
		"release+gpg ok":          {mode: "release", key: true},
		"inrelease wrong key":     {mode: "inrelease", signer: true, key: true, wantErr: "signature"},
		"release+gpg wrong key":   {mode: "release", signer: true, key: true, wantErr: "signature"},
		"unsigned with key":       {mode: "unsigned", key: true, wantErr: "signature"},
		"unsigned without key ok": {mode: "unsigned"},
		"require without key":     {mode: "inrelease", require: true, wantErr: "no GPG key"},
	} {
		f := newFakeApt(t)
		f.Mode = c.mode
		if c.signer {
			f.SignWith = other
		}
		f.AddDeb("tool", "1.0", "amd64", "main", []byte("x"))
		up := aptUp(f, []string{"main"}, []string{"amd64"})
		if c.key {
			up.GPGKey = f.PubKey
		}
		up.RequireSig = c.require
		got, err := up.Packages(context.Background())
		switch {
		case c.wantErr == "" && (err != nil || len(got) != 1):
			t.Errorf("%s: %v %v", name, got, err)
		case c.wantErr != "" && (err == nil || !strings.Contains(err.Error(), c.wantErr)):
			t.Errorf("%s: want error containing %q, got %v", name, c.wantErr, err)
		}
	}
}

func TestAptPackagesHashMismatchFails(t *testing.T) {
	f := newFakeApt(t)
	f.Tamper = true
	f.AddDeb("tool", "1.0", "amd64", "main", []byte("x"))
	_, err := aptUp(f, []string{"main"}, []string{"amd64"}).Packages(context.Background())
	if !errors.Is(err, ErrChecksum) {
		t.Fatalf("got %v", err)
	}
}

func TestAptOversizedIndexFails(t *testing.T) {
	f := newFakeApt(t)
	f.Grow = true
	f.AddDeb("tool", "1.0", "amd64", "main", []byte("x"))
	if _, err := aptUp(f, []string{"main"}, []string{"amd64"}).Packages(context.Background()); err == nil {
		t.Fatal("index longer than Release declares was accepted")
	}
}

func TestAptComponentMissingFromRelease(t *testing.T) {
	f := newFakeApt(t)
	f.AddDeb("tool", "1.0", "amd64", "main", []byte("x"))
	if _, err := aptUp(f, []string{"main", "universe"}, []string{"amd64"}).Packages(context.Background()); err == nil {
		t.Fatal("component the upstream does not have was accepted")
	}
	if _, err := aptUp(f, []string{"main"}, []string{"s390x"}).Packages(context.Background()); err == nil {
		t.Fatal("arch the upstream does not have was accepted")
	}
}

func TestAptHostileFilenameRejected(t *testing.T) {
	for _, pool := range []string{"../../etc/x_1_amd64.deb", "/abs/x_1_amd64.deb", "pool//x_1_amd64.deb", `pool/main/a\b_1_amd64.deb`,
		"pool/main/./x_1_amd64.deb", "pool/main/x_1_amd64.rpm", "pool/main/x 1_amd64.deb", "https://evil.test/x_1_amd64.deb", "pool/main/x%2e_1_amd64.deb"} {
		f := newFakeApt(t)
		f.AddDeb("x", "1", "amd64", "main", []byte("x")).Pool = pool
		if _, err := aptUp(f, []string{"main"}, []string{"amd64"}).Packages(context.Background()); err == nil {
			t.Errorf("Filename %q accepted", pool)
		}
	}
}

func TestAptRejectsBadStanzaFields(t *testing.T) {
	f := newFakeApt(t)
	d := f.AddDeb("x", "1", "amd64", "main", []byte("x"))
	d.Extra = "SHA256: nothex" // a second SHA256 line wins in a map; the parser must reject a non-hex value
	if _, err := aptUp(f, []string{"main"}, []string{"amd64"}).Packages(context.Background()); err == nil {
		t.Fatal("non-hex SHA256 accepted")
	}
}

func TestAptDecompressionCapped(t *testing.T) {
	f := newFakeApt(t)
	f.Bomb = 4 << 20
	f.AddDeb("x", "1", "amd64", "main", []byte("x"))
	up := aptUp(f, []string{"main"}, []string{"amd64"})
	up.maxIndexBytes = 1 << 20
	if _, err := up.Packages(context.Background()); err == nil || !strings.Contains(err.Error(), "over") {
		t.Fatalf("got %v", err)
	}
}

func TestAptDownloadVerifies(t *testing.T) {
	f := newFakeApt(t)
	f.AddDeb("tool", "1.0", "amd64", "main", []byte("tool-bytes"))
	up := aptUp(f, []string{"main"}, []string{"amd64"})
	pkgs, err := up.Packages(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := up.Download(context.Background(), pkgs[0], &buf); err != nil || buf.String() != "tool-bytes" {
		t.Fatalf("%v %q", err, buf.String())
	}
	f.Debs[0].Body = []byte("TOOL-BYTES") // same size, different content
	if err := up.Download(context.Background(), pkgs[0], &bytes.Buffer{}); !errors.Is(err, ErrChecksum) {
		t.Fatalf("tampered body: %v", err)
	}
	bad := pkgs[0]
	bad.Href = "../x.deb"
	if err := up.Download(context.Background(), bad, &bytes.Buffer{}); err == nil {
		t.Fatal("unsafe href downloaded")
	}
}

func TestAptSendsBasicAuth(t *testing.T) {
	f := newFakeApt(t)
	f.Username, f.Password = "u", "tok"
	f.AddDeb("tool", "1.0", "amd64", "main", []byte("x"))
	up := aptUp(f, []string{"main"}, []string{"amd64"})
	if _, err := up.Packages(context.Background()); err == nil {
		t.Fatal("unauthenticated request succeeded")
	}
	up.Username, up.Token = "u", "tok"
	if got, err := up.Packages(context.Background()); err != nil || len(got) != 1 {
		t.Fatalf("%v %v", got, err)
	}
}

func TestAptErrorsDoNotLeakToken(t *testing.T) {
	f := newFakeApt(t)
	f.Username, f.Password = "u", "s3cret"
	up := aptUp(f, []string{"main"}, []string{"amd64"})
	up.Username, up.Token = "u", "wrong"
	_, err := up.Packages(context.Background())
	if err == nil || strings.Contains(err.Error(), "wrong") {
		t.Fatalf("%v", err)
	}
}
