package registry

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"dnfrepo/internal/access"
)

// testReg is a registry over a temp dir with a fake policy store and one push token
// "ci"/"ptok".
type testReg struct {
	Reg *Registry
	F   *fakeStore
	Srv *httptest.Server
}

func newTestReg(t *testing.T) *testReg {
	t.Helper()
	f := newFake()
	f.pushes["ci"] = "ptok"
	p := &Policy{Store: f, Throttle: access.NewThrottle()}
	reg, err := New(context.Background(), Config{Root: t.TempDir(), Policy: p})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(reg.FrontHandler())
	t.Cleanup(srv.Close)
	return &testReg{reg, f, srv}
}

func sha(b []byte) string { h := sha256.Sum256(b); return "sha256:" + hex.EncodeToString(h[:]) }

func do(t *testing.T, method, u, user, pass string, body []byte, hdr map[string]string) (*http.Response, []byte) {
	t.Helper()
	req, _ := http.NewRequest(method, u, bytes.NewReader(body))
	if user != "" {
		req.SetBasicAuth(user, pass)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, b
}

func putBlob(t *testing.T, base, repo, user, pass string, data []byte) int {
	t.Helper()
	resp, _ := do(t, "POST", base+"/v2/"+repo+"/blobs/uploads/", user, pass, nil, nil)
	if resp.StatusCode != 202 {
		return resp.StatusCode
	}
	loc, _ := url.Parse(resp.Header.Get("Location"))
	q := loc.Query()
	q.Set("digest", sha(data))
	loc.RawQuery = q.Encode()
	resp, _ = do(t, "PUT", loc.String(), user, pass, data, map[string]string{"Content-Type": "application/octet-stream"})
	return resp.StatusCode
}

// pushImage pushes a one-layer OCI image; it returns the manifest digest and the first
// non-2xx status it met (0 when everything succeeded).
func pushImage(t *testing.T, base, repo, tag, user, pass string) (string, int) {
	t.Helper()
	cfg := []byte(`{"architecture":"amd64","os":"linux","created":"2024-01-02T03:04:05Z","rootfs":{"type":"layers","diff_ids":[]}}`)
	layer := []byte("layer-" + repo + tag)
	for _, b := range [][]byte{cfg, layer} {
		if st := putBlob(t, base, repo, user, pass, b); st != 201 {
			return "", st
		}
	}
	m := []byte(fmt.Sprintf(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json",`+
		`"config":{"mediaType":"application/vnd.oci.image.config.v1+json","digest":%q,"size":%d},`+
		`"layers":[{"mediaType":"application/vnd.oci.image.layer.v1.tar","digest":%q,"size":%d}]}`,
		sha(cfg), len(cfg), sha(layer), len(layer)))
	resp, _ := do(t, "PUT", base+"/v2/"+repo+"/manifests/"+tag, user, pass, m,
		map[string]string{"Content-Type": "application/vnd.oci.image.manifest.v1+json"})
	if resp.StatusCode != 201 {
		return "", resp.StatusCode
	}
	return sha(m), 0
}
