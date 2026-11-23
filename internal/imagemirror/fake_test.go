package imagemirror

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/opencontainers/go-digest"
	v1 "github.com/opencontainers/image-spec/specs-go/v1"
)

type stored struct {
	mt      string
	payload []byte
}

// fakeUp is a minimal registry v2 pull server: manifests by tag or digest, blobs, and
// optionally a Bearer token realm (Docker Hub style) or plain Basic auth.
type fakeUp struct {
	srv        *httptest.Server
	mu         sync.Mutex
	manifests  map[string]map[string]stored // repo -> tag|digest
	blobs      map[string][]byte
	bearer     bool
	user, pass string
	blobGets   int
	serial     int // makes each built image unique
}

func newFake(t *testing.T) *fakeUp {
	f := &fakeUp{manifests: map[string]map[string]stored{}, blobs: map[string][]byte{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

// host is the fake's host:port, usable as a ref prefix ("<host>/a/b:latest").
func (f *fakeUp) host() string { return strings.TrimPrefix(f.srv.URL, "http://") }

func digestOf(b []byte) string { h := sha256.Sum256(b); return "sha256:" + hex.EncodeToString(h[:]) }

func (f *fakeUp) authorized(w http.ResponseWriter, r *http.Request, repo string) bool {
	if f.bearer {
		if r.Header.Get("Authorization") == "Bearer tok-"+repo {
			return true
		}
		w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer realm="%s/token",service="fake",scope="repository:%s:pull"`, f.srv.URL, repo))
		w.WriteHeader(401)
		return false
	}
	if f.user == "" {
		return true
	}
	if u, p, ok := r.BasicAuth(); ok && u == f.user && p == f.pass {
		return true
	}
	w.Header().Set("WWW-Authenticate", `Basic realm="fake"`)
	w.WriteHeader(401)
	return false
}

func (f *fakeUp) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.URL.Path == "/token" {
		if f.user != "" {
			if u, p, ok := r.BasicAuth(); !ok || u != f.user || p != f.pass {
				w.WriteHeader(401)
				return
			}
		}
		repo := strings.TrimSuffix(strings.TrimPrefix(r.URL.Query().Get("scope"), "repository:"), ":pull")
		json.NewEncoder(w).Encode(map[string]string{"token": "tok-" + repo})
		return
	}
	p := strings.TrimPrefix(r.URL.Path, "/v2/")
	if i := strings.Index(p, "/manifests/"); i >= 0 {
		repo, ref := p[:i], p[i+len("/manifests/"):]
		if !f.authorized(w, r, repo) {
			return
		}
		m, ok := f.manifests[repo][ref]
		if !ok {
			w.WriteHeader(404)
			return
		}
		w.Header().Set("Content-Type", m.mt)
		w.Header().Set("Docker-Content-Digest", digestOf(m.payload))
		if r.Method == "GET" {
			w.Write(m.payload)
		}
		return
	}
	if i := strings.Index(p, "/blobs/"); i >= 0 {
		repo, dg := p[:i], p[i+len("/blobs/"):]
		if !f.authorized(w, r, repo) {
			return
		}
		b, ok := f.blobs[dg]
		if !ok {
			w.WriteHeader(404)
			return
		}
		f.blobGets++
		w.Write(b)
		return
	}
	w.WriteHeader(404)
}

func (f *fakeUp) gets() int { f.mu.Lock(); defer f.mu.Unlock(); return f.blobGets }

func (f *fakeUp) putBlob(b []byte) v1.Descriptor {
	d := digestOf(b)
	f.blobs[d] = b
	return v1.Descriptor{Digest: digestFrom(d), Size: int64(len(b))}
}

func (f *fakeUp) putManifest(repo, ref, mt string, b []byte) {
	if f.manifests[repo] == nil {
		f.manifests[repo] = map[string]stored{}
	}
	f.manifests[repo][ref] = stored{mt, b}
	f.manifests[repo][digestOf(b)] = stored{mt, b}
}

// image builds a single-platform OCI manifest in repo (by digest only) and returns its descriptor.
func (f *fakeUp) image(repo, os, arch, variant string) v1.Descriptor {
	f.serial++
	cfg := f.putBlob([]byte(fmt.Sprintf(`{"architecture":%q,"os":%q,"variant":%q,"n":%d}`, arch, os, variant, f.serial)))
	cfg.MediaType = v1.MediaTypeImageConfig
	layer := f.putBlob([]byte(fmt.Sprintf("layer %s/%s/%s %d", os, arch, variant, f.serial)))
	layer.MediaType = v1.MediaTypeImageLayerGzip
	m := v1.Manifest{MediaType: v1.MediaTypeImageManifest, Config: cfg, Layers: []v1.Descriptor{layer}}
	m.SchemaVersion = 2
	b, _ := json.Marshal(m)
	f.putManifest(repo, digestOf(b), v1.MediaTypeImageManifest, b)
	return v1.Descriptor{MediaType: v1.MediaTypeImageManifest, Digest: digestFrom(digestOf(b)), Size: int64(len(b))}
}

// fakeIndex publishes repo:tag as an OCI index of the given platforms ("os/arch[/variant]") plus
// one buildx-style attestation entry, and returns the index digest.
func (f *fakeUp) fakeIndex(t *testing.T, repo, tag string, platforms ...string) string {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	idx := v1.Index{MediaType: v1.MediaTypeImageIndex}
	idx.SchemaVersion = 2
	for _, p := range platforms {
		parts := strings.Split(p, "/")
		variant := ""
		if len(parts) == 3 {
			variant = parts[2]
		}
		d := f.image(repo, parts[0], parts[1], variant)
		d.Platform = &v1.Platform{OS: parts[0], Architecture: parts[1], Variant: variant}
		idx.Manifests = append(idx.Manifests, d)
	}
	att := f.image(repo, "unknown", "unknown", "")
	att.Platform = &v1.Platform{OS: "unknown", Architecture: "unknown"}
	att.Annotations = map[string]string{"vnd.docker.reference.type": "attestation-manifest", "vnd.docker.reference.digest": idx.Manifests[0].Digest.String()}
	idx.Manifests = append(idx.Manifests, att)
	b, _ := json.Marshal(idx)
	f.putManifest(repo, tag, v1.MediaTypeImageIndex, b)
	return digestOf(b)
}

// fakeSingle publishes repo:tag as a single-platform manifest and returns its digest.
func (f *fakeUp) fakeSingle(t *testing.T, repo, tag string) string {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	d := f.image(repo, "linux", "amd64", "")
	s := f.manifests[repo][d.Digest.String()]
	f.manifests[repo][tag] = s
	return d.Digest.String()
}

func digestFrom(s string) digest.Digest { return digest.Digest(s) }
