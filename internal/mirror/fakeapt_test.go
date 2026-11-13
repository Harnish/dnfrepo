package mirror

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/armor"
	"github.com/ProtonMail/go-crypto/openpgp/clearsign"
	"github.com/ProtonMail/go-crypto/openpgp/packet"
	"github.com/ulikunitz/xz"
)

type fakeDeb struct {
	Name, Version, Arch, Component string
	Body                           []byte
	Pool                           string // Filename in the index; default pool/<component>/<name>_<version-no-epoch>_<arch>.deb
	Extra                          string // extra control lines
}

func (d *fakeDeb) filename() string {
	v := d.Version
	if i := strings.IndexByte(v, ':'); i >= 0 {
		v = v[i+1:]
	}
	return fmt.Sprintf("%s_%s_%s.deb", d.Name, v, d.Arch)
}

func (d *fakeDeb) path() string {
	if d.Pool != "" {
		return d.Pool
	}
	return "pool/" + d.Component + "/" + d.filename()
}

// fakeApt serves an apt archive built from its current state on every request.
type fakeApt struct {
	t   *testing.T
	srv *httptest.Server
	key *openpgp.Entity

	PubKey     string
	Suite      string
	Components []string // listed in Release
	Arches     []string // listed in Release (an index is served per component and arch)
	Debs       []*fakeDeb
	Mode       string // "inrelease" (default: signed InRelease), "release" (Release + Release.gpg), "unsigned" (Release only)
	Compress   string // "xz" (default) or "gz": which Packages file is listed and served
	AllIndex   bool   // arch-all debs go to binary-all instead of every arch's index
	SignWith   *openpgp.Entity
	Tamper     bool // serve a same-size Packages file that differs from the hash in Release
	Grow       bool // serve a longer Packages file than Release declares
	Bomb       int  // when > 0, every Packages file is that many zero bytes (compressed)
	Username   string
	Password   string
	Requests   []string

	mu sync.Mutex
}

func newEntity(t *testing.T) (*openpgp.Entity, string) {
	t.Helper()
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
	return e, pub.String()
}

func newFakeApt(t *testing.T) *fakeApt {
	t.Helper()
	e, pub := newEntity(t)
	f := &fakeApt{t: t, key: e, PubKey: pub, Suite: "stable", Components: []string{"main"}, Arches: []string{"amd64"}, Compress: "xz"}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeApt) URL() string { return f.srv.URL + "/" }

func (f *fakeApt) AddDeb(name, version, arch, component string, body []byte) *fakeDeb {
	d := &fakeDeb{Name: name, Version: version, Arch: arch, Component: component, Body: body}
	f.Debs = append(f.Debs, d)
	return d
}

func (f *fakeApt) Hits(path string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, r := range f.Requests {
		if r == path {
			n++
		}
	}
	return n
}

func (f *fakeApt) stanza(d *fakeDeb) string {
	s := fmt.Sprintf("Package: %s\nVersion: %s\nArchitecture: %s\nDescription: fake\nFilename: %s\nSize: %d\nSHA256: %s\n", d.Name, d.Version, d.Arch, d.path(), len(d.Body), sum(d.Body))
	if d.Extra != "" {
		s += d.Extra + "\n"
	}
	return s
}

func (f *fakeApt) packagesRaw(component, arch string) []byte {
	var b strings.Builder
	for _, d := range f.Debs {
		if d.Component != component {
			continue
		}
		match := d.Arch == arch || (d.Arch == "all" && arch != "all" && !f.AllIndex) || (d.Arch == "all" && arch == "all" && f.AllIndex)
		if match {
			b.WriteString(f.stanza(d) + "\n")
		}
	}
	return []byte(b.String())
}

func (f *fakeApt) compress(raw []byte) []byte {
	var buf bytes.Buffer
	if f.Compress == "gz" {
		w := gzip.NewWriter(&buf)
		w.Write(raw)
		w.Close()
		return buf.Bytes()
	}
	w, _ := xz.NewWriter(&buf)
	w.Write(raw)
	w.Close()
	return buf.Bytes()
}

func (f *fakeApt) ext() string {
	if f.Compress == "gz" {
		return ".gz"
	}
	return ".xz"
}

// indexes returns path (relative to dists/<suite>/) -> compressed served bytes, plus the
// bytes whose hash Release lists (they differ when Tamper is set).
func (f *fakeApt) indexes() (served, listed map[string][]byte) {
	served, listed = map[string][]byte{}, map[string][]byte{}
	arches := append([]string{}, f.Arches...)
	if f.AllIndex {
		arches = append(arches, "all")
	}
	for _, c := range f.Components {
		for _, a := range arches {
			raw := f.packagesRaw(c, a)
			if f.Bomb > 0 {
				raw = make([]byte, f.Bomb)
			}
			p := c + "/binary-" + a + "/Packages" + f.ext()
			listed[p] = f.compress(raw)
			served[p] = listed[p]
			if f.Tamper {
				b := bytes.Clone(listed[p])
				b[len(b)-1] ^= 0xff
				served[p] = b
			}
			if f.Grow {
				served[p] = f.compress(append(raw, []byte("Package: evil\n\n")...))
			}
		}
	}
	return
}

func (f *fakeApt) release() []byte {
	_, listed := f.indexes()
	var b strings.Builder
	arches := append([]string{}, f.Arches...)
	if f.AllIndex {
		arches = append(arches, "all")
	}
	fmt.Fprintf(&b, "Origin: Fake\nSuite: %s\nCodename: %s\nDate: Fri, 13 Nov 2026 10:00:00 UTC\nArchitectures: %s\nComponents: %s\nSHA256:\n",
		f.Suite, f.Suite, strings.Join(arches, " "), strings.Join(f.Components, " "))
	for p, body := range listed {
		fmt.Fprintf(&b, " %s %d %s\n", sum(body), len(body), p)
	}
	return []byte(b.String())
}

func (f *fakeApt) signer() *openpgp.Entity {
	if f.SignWith != nil {
		return f.SignWith
	}
	return f.key
}

func (f *fakeApt) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.Requests = append(f.Requests, strings.TrimPrefix(r.URL.Path, "/"))
	f.mu.Unlock()
	if f.Username != "" {
		if u, p, ok := r.BasicAuth(); !ok || u != f.Username || p != f.Password {
			w.WriteHeader(401)
			return
		}
	}
	p := strings.TrimPrefix(r.URL.Path, "/")
	base := "dists/" + f.Suite + "/"
	switch {
	case p == base+"InRelease":
		if f.Mode != "" && f.Mode != "inrelease" {
			http.NotFound(w, r)
			return
		}
		var out bytes.Buffer
		cw, err := clearsign.Encode(&out, f.signer().PrivateKey, nil)
		if err != nil {
			f.t.Error(err)
		}
		cw.Write(f.release())
		cw.Close()
		w.Write(out.Bytes())
	case p == base+"Release":
		if f.Mode == "inrelease" || f.Mode == "" {
			http.NotFound(w, r)
			return
		}
		w.Write(f.release())
	case p == base+"Release.gpg":
		if f.Mode != "release" {
			http.NotFound(w, r)
			return
		}
		var out bytes.Buffer
		if err := openpgp.ArmoredDetachSign(&out, f.signer(), bytes.NewReader(f.release()), nil); err != nil {
			f.t.Error(err)
		}
		w.Write(out.Bytes())
	case strings.HasPrefix(p, base):
		served, _ := f.indexes()
		if b, ok := served[strings.TrimPrefix(p, base)]; ok {
			w.Write(b)
			return
		}
		http.NotFound(w, r)
	default:
		for _, d := range f.Debs {
			if d.path() == p {
				w.Write(d.Body)
				return
			}
		}
		http.NotFound(w, r)
	}
}
