package aptindex

import (
	"bytes"
	"compress/gzip"
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"strconv"
	"strings"
	"testing"
	"time"
)

var now = time.Date(2026, 11, 13, 10, 0, 0, 0, time.UTC)

func gunzip(t *testing.T, b []byte) string {
	t.Helper()
	r, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

func pkg(name, arch, comp string) Package {
	return Package{Component: comp, Arch: arch, Filename: name + "_1_" + arch + ".deb", Size: 10, MD5: "m5", SHA1: "s1", SHA256: "s256",
		Control: "Package: " + name + "\nVersion: 1\nArchitecture: " + arch}
}

func TestBuildEmptyRepo(t *testing.T) {
	f := Build("stable", []string{"main"}, []string{"amd64", "arm64"}, nil, now)
	for _, a := range []string{"amd64", "arm64"} {
		if p := f["main/binary-"+a+"/Packages"]; len(p) != 0 {
			t.Errorf("%s Packages not empty: %q", a, p)
		}
		if gunzip(t, f["main/binary-"+a+"/Packages.gz"]) != "" {
			t.Errorf("%s Packages.gz not empty", a)
		}
	}
	rel := string(f["Release"])
	for _, want := range []string{"Suite: stable\n", "Codename: stable\n", "Architectures: amd64 arm64\n", "Components: main\n",
		"Date: Fri, 13 Nov 2026 10:00:00 UTC\n", "main/binary-amd64/Packages\n", "main/binary-amd64/Packages.gz\n"} {
		if !strings.Contains(rel, want) {
			t.Errorf("Release lacks %q:\n%s", want, rel)
		}
	}
	if strings.Contains(rel, "Valid-Until") {
		t.Error("Valid-Until must not be set")
	}
}

func TestBuildAllFanOut(t *testing.T) {
	pk := []Package{pkg("tool", "amd64", "main"), pkg("data", "all", "main"), pkg("other", "arm64", "main"), pkg("gone", "s390x", "main")}
	f := Build("s", []string{"main"}, []string{"amd64", "arm64"}, pk, now)
	amd, arm := string(f["main/binary-amd64/Packages"]), string(f["main/binary-arm64/Packages"])
	if !strings.Contains(amd, "Package: tool") || !strings.Contains(amd, "Package: data") || strings.Contains(amd, "Package: other") {
		t.Errorf("amd64:\n%s", amd)
	}
	if !strings.Contains(arm, "Package: other") || !strings.Contains(arm, "Package: data") || strings.Contains(arm, "Package: tool") {
		t.Errorf("arm64:\n%s", arm)
	}
	if strings.Contains(amd+arm, "gone") {
		t.Error("package of an unlisted arch was indexed")
	}
}

func TestBuildFiltersByComponent(t *testing.T) {
	pk := []Package{pkg("a", "amd64", "main"), pkg("b", "amd64", "universe")}
	f := Build("s", []string{"main", "universe"}, []string{"amd64"}, pk, now)
	if m := string(f["main/binary-amd64/Packages"]); !strings.Contains(m, "Package: a") || strings.Contains(m, "Package: b") {
		t.Errorf("main:\n%s", m)
	}
	if u := string(f["universe/binary-amd64/Packages"]); !strings.Contains(u, "Package: b") || strings.Contains(u, "Package: a") {
		t.Errorf("universe:\n%s", u)
	}
}

func TestBuildStanzaFields(t *testing.T) {
	p := pkg("tool", "amd64", "main")
	p.Control += "\nSize: 999\nMD5sum: old\nFilename: pool/x/old.deb\nSHA512: old\nSection: utils"
	got := string(Build("s", []string{"main"}, []string{"amd64"}, []Package{p}, now)["main/binary-amd64/Packages"])
	want := "Package: tool\nVersion: 1\nArchitecture: amd64\nSection: utils\n" +
		"Filename: pool/main/tool_1_amd64.deb\nSize: 10\nMD5sum: m5\nSHA1: s1\nSHA256: s256\n\n"
	if got != want {
		t.Fatalf("got:\n%q\nwant:\n%q", got, want)
	}
}

func TestPackagesSortedAndSeparated(t *testing.T) {
	pk := []Package{pkg("zeta", "amd64", "main"), pkg("alpha", "amd64", "main")}
	got := string(Build("s", []string{"main"}, []string{"amd64"}, pk, now)["main/binary-amd64/Packages"])
	if strings.Index(got, "Package: alpha") > strings.Index(got, "Package: zeta") || strings.Count(got, "\n\n") != 2 {
		t.Fatalf("%q", got)
	}
}

func TestReleaseHashesMatchFiles(t *testing.T) {
	f := Build("s", []string{"main"}, []string{"amd64"}, []Package{pkg("a", "amd64", "main")}, now)
	blocks := map[string]map[string]string{} // header -> path -> hash
	size := map[string]int{}
	var cur string
	for _, l := range strings.Split(string(f["Release"]), "\n") {
		switch {
		case l == "MD5Sum:" || l == "SHA1:" || l == "SHA256:":
			cur = l
			blocks[cur] = map[string]string{}
		case strings.HasPrefix(l, " ") && cur != "":
			parts := strings.Fields(l)
			if len(parts) != 3 {
				t.Fatalf("bad hash line %q", l)
			}
			blocks[cur][parts[2]] = parts[0]
			n, _ := strconv.Atoi(parts[1])
			size[parts[2]] = n
		default:
			cur = ""
		}
	}
	hashes := map[string]func([]byte) string{
		"MD5Sum:": func(b []byte) string { h := md5.Sum(b); return hex.EncodeToString(h[:]) },
		"SHA1:":   func(b []byte) string { h := sha1.Sum(b); return hex.EncodeToString(h[:]) },
		"SHA256:": func(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) },
	}
	for name, sum := range hashes {
		if len(blocks[name]) == 0 {
			t.Fatalf("no %s block", name)
		}
		for path, h := range blocks[name] {
			body, ok := f[path]
			if !ok {
				t.Errorf("%s lists %q which is not in the output", name, path)
				continue
			}
			if sum(body) != h || size[path] != len(body) {
				t.Errorf("%s %s: hash or size wrong", name, path)
			}
		}
	}
	for path := range f {
		if path != "Release" && blocks["SHA256:"][path] == "" {
			t.Errorf("%s missing from Release", path)
		}
	}
}

func TestBuildDeterministic(t *testing.T) {
	pk := []Package{pkg("a", "amd64", "main")}
	a := Build("s", []string{"main"}, []string{"amd64"}, pk, now)
	b := Build("s", []string{"main"}, []string{"amd64"}, pk, now)
	for k := range a {
		if !bytes.Equal(a[k], b[k]) {
			t.Errorf("%s differs between runs", k)
		}
	}
}
