// Package imagemirror copies upstream container images into the embedded registry.
package imagemirror

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/distribution/reference"
	"github.com/opencontainers/go-digest"
)

// Ref is an upstream image tag. Host is the API host (docker.io is served by registry-1.docker.io).
type Ref struct {
	Domain, Host, Repo, Tag string
}

// ParseRef normalizes "[registry/]repo[:tag]" (bare names get docker.io/library/, no tag means
// latest). Digest references are rejected: there is nothing to follow.
func ParseRef(s string) (Ref, error) {
	n, err := reference.ParseNormalizedNamed(strings.TrimSpace(s))
	if err != nil {
		return Ref{}, fmt.Errorf("invalid image reference %q: %w", s, err)
	}
	if _, ok := n.(reference.Digested); ok {
		return Ref{}, fmt.Errorf("%q is pinned to a digest; mirror a tag instead", s)
	}
	t := reference.TagNameOnly(n).(reference.Tagged)
	d := reference.Domain(n)
	host := d
	if d == "docker.io" {
		host = "registry-1.docker.io"
	}
	return Ref{Domain: d, Host: host, Repo: reference.Path(n), Tag: t.Tag()}, nil
}

// Name is the normalized image name without tag, e.g. docker.io/plexinc/pms-docker.
func (r Ref) Name() string             { return r.Domain + "/" + r.Repo }
func (r Ref) String() string           { return r.Name() + ":" + r.Tag }
func (r Ref) DefaultLocalName() string { return r.Repo }

// Media types we pull. Schema1 is refused.
const (
	mtOCIIndex     = "application/vnd.oci.image.index.v1+json"
	mtOCIManifest  = "application/vnd.oci.image.manifest.v1+json"
	mtDockerList   = "application/vnd.docker.distribution.manifest.list.v2+json"
	mtDockerSchema = "application/vnd.docker.distribution.manifest.v2+json"
)

var accept = strings.Join([]string{mtOCIIndex, mtOCIManifest, mtDockerList, mtDockerSchema}, ", ")

var ErrAuth = errors.New("upstream auth failed")

// Client pulls from one upstream within a run. Username/Token are optional credentials; the
// bearer token obtained from a registry's token realm is cached on the Client.
type Client struct {
	HTTP            *http.Client
	Username, Token string
	Scheme          string // "" = https (tests use http)

	bearer string
	basic  bool
}

func (c *Client) http() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 15 * time.Minute}
}

func (c *Client) url(r Ref, path string) string {
	s := c.Scheme
	if s == "" {
		s = "https"
	}
	return s + "://" + r.Host + "/v2/" + r.Repo + "/" + path
}

var challengeRE = regexp.MustCompile(`(\w+)="([^"]*)"`)

// authorize answers a 401 challenge: fetch a bearer token, or switch to Basic.
func (c *Client) authorize(ctx context.Context, r Ref, challenge string) error {
	scheme, params, _ := strings.Cut(challenge, " ")
	switch strings.ToLower(scheme) {
	case "basic":
		if c.Username == "" {
			return fmt.Errorf("%w: upstream requires credentials", ErrAuth)
		}
		c.basic = true
		return nil
	case "bearer":
	default:
		return fmt.Errorf("%w: unsupported challenge %q", ErrAuth, scheme)
	}
	p := map[string]string{}
	for _, m := range challengeRE.FindAllStringSubmatch(params, -1) {
		p[m[1]] = m[2]
	}
	if p["realm"] == "" {
		return fmt.Errorf("%w: bearer challenge without realm", ErrAuth)
	}
	q := url.Values{}
	if p["service"] != "" {
		q.Set("service", p["service"])
	}
	scope := p["scope"]
	if scope == "" {
		scope = "repository:" + r.Repo + ":pull"
	}
	q.Set("scope", scope)
	sep := "?"
	if strings.Contains(p["realm"], "?") {
		sep = "&"
	}
	req, err := http.NewRequestWithContext(ctx, "GET", p["realm"]+sep+q.Encode(), nil)
	if err != nil {
		return err
	}
	if c.Username != "" {
		req.SetBasicAuth(c.Username, c.Token)
	}
	resp, err := c.http().Do(req)
	if err != nil {
		return fmt.Errorf("%w: token endpoint: %v", ErrAuth, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("%w: token endpoint returned %s", ErrAuth, resp.Status)
	}
	var raw map[string]any
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return fmt.Errorf("%w: bad token response", ErrAuth)
	}
	c.bearer, _ = raw["token"].(string)
	if c.bearer == "" {
		c.bearer, _ = raw["access_token"].(string)
	}
	if c.bearer == "" {
		return fmt.Errorf("%w: empty token", ErrAuth)
	}
	return nil
}

// do sends a request, answering one auth challenge. The caller closes the body.
func (c *Client) do(ctx context.Context, r Ref, method, path, accepts string) (*http.Response, error) {
	for try := 0; ; try++ {
		req, err := http.NewRequestWithContext(ctx, method, c.url(r, path), nil)
		if err != nil {
			return nil, err
		}
		if accepts != "" {
			req.Header.Set("Accept", accepts)
		}
		if c.bearer != "" {
			req.Header.Set("Authorization", "Bearer "+c.bearer)
		} else if c.basic {
			req.SetBasicAuth(c.Username, c.Token)
		}
		resp, err := c.http().Do(req)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != 401 {
			return resp, nil
		}
		resp.Body.Close()
		if try > 0 {
			return nil, fmt.Errorf("%w: %s %s refused", ErrAuth, method, r.Name())
		}
		if err := c.authorize(ctx, r, resp.Header.Get("WWW-Authenticate")); err != nil {
			return nil, err
		}
	}
}

func mediaType(resp *http.Response) (string, error) {
	mt, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if strings.HasPrefix(mt, "application/vnd.docker.distribution.manifest.v1") {
		return "", fmt.Errorf("schema1 manifests are not supported")
	}
	return mt, nil
}

func statusErr(what string, resp *http.Response) error {
	return fmt.Errorf("upstream %s: %s", what, resp.Status)
}

// Head returns the digest the upstream tag points at.
func (c *Client) Head(ctx context.Context, r Ref) (string, error) {
	resp, err := c.do(ctx, r, "HEAD", "manifests/"+r.Tag, accept)
	if err != nil {
		return "", err
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", statusErr(r.String(), resp)
	}
	if _, err := mediaType(resp); err != nil {
		return "", err
	}
	if d := resp.Header.Get("Docker-Content-Digest"); strings.HasPrefix(d, "sha256:") {
		return d, nil
	}
	_, b, err := c.Manifest(ctx, r, r.Tag) // registry without the digest header
	if err != nil {
		return "", err
	}
	return sha(b), nil
}

var tagRE = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$`)

// checkDigest accepts only well-formed sha256 digests. Digests come from upstream manifests
// (untrusted) and end up in request URLs and staging file names.
func checkDigest(dg string) error {
	d, err := digest.Parse(dg)
	if err != nil || d.Algorithm() != digest.SHA256 {
		return fmt.Errorf("invalid digest %q", dg)
	}
	return nil
}

// Manifest fetches a manifest by tag or digest; a digest reference is verified.
func (c *Client) Manifest(ctx context.Context, r Ref, ref string) (string, []byte, error) {
	if strings.Contains(ref, ":") {
		if err := checkDigest(ref); err != nil {
			return "", nil, err
		}
	} else if !tagRE.MatchString(ref) {
		return "", nil, fmt.Errorf("invalid tag %q", ref)
	}
	resp, err := c.do(ctx, r, "GET", "manifests/"+ref, accept)
	if err != nil {
		return "", nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", nil, statusErr("manifest "+ref, resp)
	}
	mt, err := mediaType(resp)
	if err != nil {
		return "", nil, err
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return "", nil, err
	}
	if strings.HasPrefix(ref, "sha256:") && sha(b) != ref {
		return "", nil, fmt.Errorf("manifest %s: digest mismatch", ref)
	}
	return mt, b, nil
}

// Blob downloads blob dg to dst, verifying its sha256; dst is removed on any failure.
func (c *Client) Blob(ctx context.Context, r Ref, dg, dst string) (n int64, err error) {
	if err := checkDigest(dg); err != nil {
		return 0, err
	}
	resp, err := c.do(ctx, r, "GET", "blobs/"+dg, "")
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return 0, statusErr("blob "+dg, resp)
	}
	f, err := os.Create(dst)
	if err != nil {
		return 0, err
	}
	defer func() {
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			os.Remove(dst)
		}
	}()
	h := sha256.New()
	if n, err = io.Copy(io.MultiWriter(f, h), resp.Body); err != nil {
		return 0, err
	}
	if got := "sha256:" + hex.EncodeToString(h.Sum(nil)); got != dg {
		return 0, fmt.Errorf("blob %s: digest mismatch (got %s)", dg, got)
	}
	return n, nil
}

func sha(b []byte) string { h := sha256.Sum256(b); return "sha256:" + hex.EncodeToString(h[:]) }
