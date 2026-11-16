// Package registrytest pushes small fixture images to a registry over HTTP, for other
// packages' tests.
package registrytest

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"testing"
)

func sha(b []byte) string { h := sha256.Sum256(b); return "sha256:" + hex.EncodeToString(h[:]) }

func do(t testing.TB, method, u, user, pass string, body []byte, ctype string) (*http.Response, []byte) {
	t.Helper()
	req, _ := http.NewRequest(method, u, bytes.NewReader(body))
	if user != "" {
		req.SetBasicAuth(user, pass)
	}
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, b
}

func putBlob(t testing.TB, base, repo, user, pass string, data []byte) int {
	t.Helper()
	resp, _ := do(t, "POST", base+"/v2/"+repo+"/blobs/uploads/", user, pass, nil, "")
	if resp.StatusCode != 202 {
		return resp.StatusCode
	}
	loc, _ := url.Parse(resp.Header.Get("Location"))
	q := loc.Query()
	q.Set("digest", sha(data))
	loc.RawQuery = q.Encode()
	resp, _ = do(t, "PUT", loc.String(), user, pass, data, "application/octet-stream")
	return resp.StatusCode
}

// Push pushes a one-layer amd64 OCI image to base (a registry server URL) and returns the
// manifest digest and the first non-2xx status it met (0 when all went well).
func Push(t testing.TB, base, repo, tag, user, pass string) (string, int) {
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
	resp, _ := do(t, "PUT", base+"/v2/"+repo+"/manifests/"+tag, user, pass, m, "application/vnd.oci.image.manifest.v1+json")
	if resp.StatusCode != 201 {
		return "", resp.StatusCode
	}
	return sha(m), 0
}
