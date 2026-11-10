package mirror

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/armor"
	"github.com/ProtonMail/go-crypto/openpgp/packet"
	"github.com/klauspost/compress/zstd"
	"github.com/ulikunitz/xz"
)

type fakeOpts struct {
	Compression        string // "gz" (default), "zst" or "xz"
	Sign               bool   // serve repodata/repomd.xml.asc
	Username, Password string // require Basic auth when set
}

type fakePkg struct {
	Name, Ver, Rel, Arch string
	Href                 string // defaults to Packages/<n>/<filename>
	Body                 []byte
}

func (p *fakePkg) Filename() string {
	return fmt.Sprintf("%s-%s-%s.%s.rpm", p.Name, p.Ver, p.Rel, p.Arch)
}

// fakeUpstream serves a dnf repo built from its current state on every request,
// so tests can change fields between calls.
type fakeUpstream struct {
	t    *testing.T
	opts fakeOpts
	srv  *httptest.Server
	key  *openpgp.Entity

	PubKey        string // armored public key matching the .asc
	Pkgs          []*fakePkg
	ChecksumType  string            // default sha256; applies to repomd and primary
	TamperPrimary bool              // serve a primary that differs from repomd's checksum
	BadSig        bool              // sign different bytes than repomd.xml
	Override      map[string][]byte // URL path (no leading slash) -> body served instead
	Dropped       map[string]bool   // URL path (no leading slash) -> 404
	Requests      []*http.Request
}

func newFakeUpstream(t *testing.T, opts fakeOpts) *fakeUpstream {
	t.Helper()
	if opts.Compression == "" {
		opts.Compression = "gz"
	}
	e, err := openpgp.NewEntity("fake", "", "fake@example.test", &packet.Config{Algorithm: packet.PubKeyAlgoEdDSA})
	if err != nil {
		t.Fatal(err)
	}
	var pub bytes.Buffer
	w, _ := armor.Encode(&pub, openpgp.PublicKeyType, nil)
	if err := e.Serialize(w); err != nil {
		t.Fatal(err)
	}
	w.Close()
	f := &fakeUpstream{t: t, opts: opts, key: e, PubKey: pub.String(), ChecksumType: "sha256",
		Override: map[string][]byte{}, Dropped: map[string]bool{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

// URL is the repo base URL, with a trailing slash.
func (f *fakeUpstream) URL() string { return f.srv.URL + "/" }

func (f *fakeUpstream) AddPkg(name, ver, rel, arch string, body []byte) *fakePkg {
	p := &fakePkg{Name: name, Ver: ver, Rel: rel, Arch: arch, Body: body}
	p.Href = "Packages/" + name[:1] + "/" + p.Filename()
	f.Pkgs = append(f.Pkgs, p)
	return p
}

// Corrupt serves p with its last byte flipped (same size, wrong sha256).
func (f *fakeUpstream) Corrupt(p *fakePkg) {
	b := bytes.Clone(p.Body)
	b[len(b)-1] ^= 0xff
	f.Override[p.Href] = b
}

// Drop makes path (no leading slash) return 404.
func (f *fakeUpstream) Drop(path string) { f.Dropped[path] = true }

func (f *fakeUpstream) primaryHref() string { return "repodata/primary.xml." + f.opts.Compression }

func sum(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

func (f *fakeUpstream) primaryXML(pkgs []*fakePkg) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, `<?xml version="1.0" encoding="UTF-8"?>
<metadata xmlns="http://linux.duke.edu/metadata/common" xmlns:rpm="http://linux.duke.edu/metadata/rpm" packages="%d">
`, len(pkgs))
	for _, p := range pkgs {
		fmt.Fprintf(&b, `<package type="rpm">
  <name>%s</name>
  <arch>%s</arch>
  <version epoch="0" ver="%s" rel="%s"/>
  <checksum type="%s" pkgid="YES">%s</checksum>
  <summary>fake</summary>
  <size package="%d" installed="1" archive="1"/>
  <location href="%s"/>
  <format><rpm:license>MIT</rpm:license></format>
</package>
`, p.Name, p.Arch, p.Ver, p.Rel, f.ChecksumType, sum(p.Body), len(p.Body), xmlAttr(p.Href))
	}
	b.WriteString("</metadata>\n")
	return []byte(b.String())
}

func xmlAttr(s string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

func (f *fakeUpstream) compress(raw []byte) []byte {
	var buf bytes.Buffer
	switch f.opts.Compression {
	case "gz":
		w := gzip.NewWriter(&buf)
		w.Write(raw)
		w.Close()
	case "zst":
		w, _ := zstd.NewWriter(&buf)
		w.Write(raw)
		w.Close()
	case "xz":
		w, err := xz.NewWriter(&buf)
		if err != nil {
			f.t.Fatal(err)
		}
		w.Write(raw)
		w.Close()
	}
	return buf.Bytes()
}

func (f *fakeUpstream) primary() []byte { return f.compress(f.primaryXML(f.Pkgs)) }

func (f *fakeUpstream) repomd() []byte {
	p := f.primary()
	return []byte(fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<repomd xmlns="http://linux.duke.edu/metadata/repo" xmlns:rpm="http://linux.duke.edu/metadata/rpm">
  <revision>1</revision>
  <data type="filelists">
    <checksum type="sha256">%s</checksum>
    <location href="repodata/filelists.xml.gz"/>
  </data>
  <data type="primary">
    <checksum type="%s">%s</checksum>
    <open-checksum type="sha256">%s</open-checksum>
    <location href="%s"/>
    <size>%d</size>
  </data>
</repomd>
`, strings.Repeat("0", 64), f.ChecksumType, sum(p), sum(f.primaryXML(f.Pkgs)), f.primaryHref(), len(p)))
}

func (f *fakeUpstream) serve(w http.ResponseWriter, r *http.Request) {
	f.Requests = append(f.Requests, r)
	if f.opts.Username != "" {
		if u, p, ok := r.BasicAuth(); !ok || u != f.opts.Username || p != f.opts.Password {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
	}
	path := strings.TrimPrefix(r.URL.Path, "/")
	if f.Dropped[path] {
		http.NotFound(w, r)
		return
	}
	if b, ok := f.Override[path]; ok {
		w.Write(b)
		return
	}
	switch path {
	case "repodata/repomd.xml":
		w.Write(f.repomd())
		return
	case "repodata/repomd.xml.asc":
		if !f.opts.Sign {
			http.NotFound(w, r)
			return
		}
		signed := f.repomd()
		if f.BadSig {
			signed = append(signed, '\n')
		}
		var sig bytes.Buffer
		if err := openpgp.ArmoredDetachSign(&sig, f.key, bytes.NewReader(signed), nil); err != nil {
			f.t.Error(err)
		}
		w.Write(sig.Bytes())
		return
	case f.primaryHref():
		if f.TamperPrimary {
			extra := &fakePkg{Name: "evil", Ver: "1", Rel: "1", Arch: "x86_64", Href: "Packages/e/evil-1-1.x86_64.rpm", Body: []byte("x")}
			w.Write(f.compress(f.primaryXML(append(append([]*fakePkg{}, f.Pkgs...), extra))))
			return
		}
		w.Write(f.primary())
		return
	}
	for _, p := range f.Pkgs {
		if p.Href == path {
			w.Write(p.Body)
			return
		}
	}
	http.NotFound(w, r)
}
