package mirror

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"regexp"
	"strings"

	"dnfrepo/internal/pipeline"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/klauspost/compress/zstd"
	"github.com/xi2/xz"
)

// UpstreamPkg is one package listed in an upstream primary.xml. Every field has
// been validated: Href is a safe relative path and Filename passes
// pipeline.ValidFilename.
type UpstreamPkg struct {
	Name, Arch, Version, Release, Filename, Href, SHA256 string
	Size                                                 int64
	// Deb only: the index component and the verbatim control stanza from the upstream Packages file.
	Component, Control string
}

// Upstream reads a dnf repo at Base (http or https). Username/Token are sent
// as Basic auth. If GPGKey (armored public key) is set, repomd.xml must carry a
// valid detached signature by it; RequireSig makes a missing key an error.
type Upstream struct {
	Base                    string
	Client                  *http.Client
	Username, Token, GPGKey string
	RequireSig              bool
	MaxRPMBytes             int64 // 0 = no limit beyond the declared size

	maxPrimaryBytes int64 // decompressed primary cap; 0 = 1 GiB
}

var ErrChecksum = errors.New("checksum mismatch")

const (
	maxSig  = 64 << 10
	maxDict = 64 << 20 // xz dictionary / zstd window cap for hostile primaries
)

func (u *Upstream) client() *http.Client {
	if u.Client != nil {
		return u.Client
	}
	return http.DefaultClient
}

func (u *Upstream) url(href string) string { return strings.TrimSuffix(u.Base, "/") + "/" + href }

func (u *Upstream) get(ctx context.Context, href string) (io.ReadCloser, error) {
	return get(ctx, u.client(), u.url(href), u.Username, u.Token)
}

func (u *Upstream) fetch(ctx context.Context, href string, max int64) ([]byte, error) {
	return fetchAuth(ctx, u.client(), u.url(href), u.Username, u.Token, max)
}

// Whitelisted characters only: no %, \, :, ?, #, so no escapes, schemes,
// queries or Windows separators; segments are checked for . and .. below.
var hrefRe = regexp.MustCompile(`^[A-Za-z0-9._+~-]+(/[A-Za-z0-9._+~-]+)*$`)

func safeHref(href string) bool {
	if !hrefRe.MatchString(href) {
		return false
	}
	for _, s := range strings.Split(href, "/") {
		if s == "." || s == ".." {
			return false
		}
	}
	return true
}

type checksumXML struct {
	Type  string `xml:"type,attr"`
	Value string `xml:",chardata"`
}

type hrefXML struct {
	Href string `xml:"href,attr"`
}

// Packages fetches repomd.xml (verifying its signature when a key is set), then
// the primary metadata (verified against repomd's sha256), and returns every
// package. Any unsafe or unsupported entry fails the whole call.
func (u *Upstream) Packages(ctx context.Context) ([]UpstreamPkg, error) {
	if !isHTTP(u.Base) {
		return nil, fmt.Errorf("upstream %s: only http and https are supported", redact(u.Base))
	}
	if u.RequireSig && u.GPGKey == "" {
		return nil, errors.New("upstream requires a signature but no GPG key is configured")
	}
	md, err := u.fetch(ctx, "repodata/repomd.xml", maxBody)
	if err != nil {
		return nil, err
	}
	if u.GPGKey != "" {
		asc, err := u.fetch(ctx, "repodata/repomd.xml.asc", maxSig)
		if err != nil {
			return nil, err
		}
		kr, err := openpgp.ReadArmoredKeyRing(strings.NewReader(u.GPGKey))
		if err != nil {
			return nil, fmt.Errorf("upstream GPG key: %w", err)
		}
		if _, err := openpgp.CheckArmoredDetachedSignature(kr, bytes.NewReader(md), bytes.NewReader(asc), nil); err != nil {
			return nil, fmt.Errorf("%s: signature: %w", redact(u.url("repodata/repomd.xml.asc")), err)
		}
	}

	var doc struct {
		Data []struct {
			Type     string      `xml:"type,attr"`
			Checksum checksumXML `xml:"checksum"`
			Location hrefXML     `xml:"location"`
		} `xml:"data"`
	}
	if err := xml.Unmarshal(md, &doc); err != nil {
		return nil, fmt.Errorf("parse repomd.xml: %w", err)
	}
	for _, d := range doc.Data {
		if d.Type == "primary" {
			return u.primary(ctx, d.Location.Href, d.Checksum)
		}
	}
	return nil, errors.New("repomd.xml has no primary metadata")
}

// capReader fails once more than left bytes are read.
type capReader struct {
	r    io.Reader
	left int64
	max  int64
}

func (c *capReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	if c.left -= int64(n); c.left < 0 {
		return n, fmt.Errorf("primary metadata over %d bytes", c.max)
	}
	return n, err
}

func (u *Upstream) primary(ctx context.Context, href string, sum checksumXML) ([]UpstreamPkg, error) {
	if sum.Type != "sha256" {
		return nil, fmt.Errorf("primary metadata: unsupported checksum type %q", sum.Type)
	}
	if !safeHref(href) {
		return nil, errors.New("primary metadata: unsafe location")
	}
	limit := u.maxPrimaryBytes
	if limit == 0 {
		limit = 1 << 30
	}
	body, err := u.get(ctx, href)
	if err != nil {
		return nil, err
	}
	defer body.Close()
	h := sha256.New()
	raw := io.TeeReader(&capReader{r: body, left: limit, max: limit}, h)

	var dr io.Reader
	switch path.Ext(href) {
	case ".gz":
		g, err := gzip.NewReader(raw)
		if err != nil {
			return nil, fmt.Errorf("primary metadata: %w", err)
		}
		dr = g
	case ".zst":
		z, err := zstd.NewReader(raw, zstd.WithDecoderConcurrency(1),
			zstd.WithDecoderMaxWindow(maxDict), zstd.WithDecoderMaxMemory(uint64(limit)))
		if err != nil {
			return nil, fmt.Errorf("primary metadata: %w", err)
		}
		defer z.Close()
		dr = z
	case ".xz":
		// xi2/xz rejects a declared dictionary over maxDict before allocating it;
		// ulikunitz/xz grows its dictionary to whatever the stream declares.
		x, err := xz.NewReader(raw, maxDict)
		if err != nil {
			return nil, fmt.Errorf("primary metadata: %w", err)
		}
		dr = x
	default:
		return nil, errors.New("primary metadata: unsupported compression")
	}

	type pkgXML struct {
		Name     string      `xml:"name"`
		Arch     string      `xml:"arch"`
		Checksum checksumXML `xml:"checksum"`
		Location hrefXML     `xml:"location"`
		Version  struct {
			Ver string `xml:"ver,attr"`
			Rel string `xml:"rel,attr"`
		} `xml:"version"`
		Size struct {
			Package int64 `xml:"package,attr"`
		} `xml:"size"`
	}
	var all []pkgXML
	dec := xml.NewDecoder(&capReader{r: dr, left: limit, max: limit})
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("parse primary metadata: %w", err)
		}
		if se, ok := tok.(xml.StartElement); ok && se.Name.Local == "package" {
			var p pkgXML
			if err := dec.DecodeElement(&p, &se); err != nil {
				return nil, fmt.Errorf("parse primary metadata: %w", err)
			}
			all = append(all, p)
		}
	}
	if _, err := io.Copy(io.Discard, raw); err != nil {
		return nil, fmt.Errorf("primary metadata: %w", err)
	}
	if got := hex.EncodeToString(h.Sum(nil)); !strings.EqualFold(got, strings.TrimSpace(sum.Value)) {
		return nil, fmt.Errorf("%s: %w", redact(u.url(href)), ErrChecksum)
	}

	out := make([]UpstreamPkg, 0, len(all))
	for _, p := range all {
		id := fmt.Sprintf("%q", p.Name+"-"+p.Version.Ver+"-"+p.Version.Rel+"."+p.Arch)
		if p.Checksum.Type != "sha256" {
			return nil, fmt.Errorf("package %s: unsupported checksum type %q", id, p.Checksum.Type)
		}
		sha := strings.ToLower(strings.TrimSpace(p.Checksum.Value))
		if b, err := hex.DecodeString(sha); err != nil || len(b) != sha256.Size {
			return nil, fmt.Errorf("package %s: invalid sha256", id)
		}
		if !safeHref(p.Location.Href) || !pipeline.ValidFilename(path.Base(p.Location.Href)) {
			return nil, fmt.Errorf("package %s: unsafe location", id)
		}
		if p.Size.Package < 0 {
			return nil, fmt.Errorf("package %s: invalid size", id)
		}
		out = append(out, UpstreamPkg{Name: p.Name, Arch: p.Arch, Version: p.Version.Ver, Release: p.Version.Rel,
			Filename: path.Base(p.Location.Href), Href: p.Location.Href, SHA256: sha, Size: p.Size.Package})
	}
	return out, nil
}

// Download streams p into dst, verifying size and sha256. On error dst may hold
// a partial or unverified body; the caller must discard it.
func (u *Upstream) Download(ctx context.Context, p UpstreamPkg, dst io.Writer) error {
	return downloadVerified(ctx, u.client(), u.Base, u.Username, u.Token, p, u.MaxRPMBytes, dst)
}

// downloadVerified fetches base/p.Href into dst, enforcing the declared size and sha256 (and max
// when positive). Shared by the rpm and deb upstreams.
func downloadVerified(ctx context.Context, c *http.Client, base, user, token string, p UpstreamPkg, max int64, dst io.Writer) error {
	if !isHTTP(base) || !safeHref(p.Href) {
		return fmt.Errorf("package %q: unsafe location", p.Filename)
	}
	if max > 0 && p.Size > max {
		return fmt.Errorf("package %q: size %d over limit %d", p.Filename, p.Size, max)
	}
	full := strings.TrimSuffix(base, "/") + "/" + p.Href
	src := redact(full)
	body, err := get(ctx, c, full, user, token)
	if err != nil {
		return err
	}
	defer body.Close()
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(dst, h), io.LimitReader(body, p.Size))
	if err != nil {
		return fmt.Errorf("GET %s: %w", src, err)
	}
	if n != p.Size {
		return fmt.Errorf("GET %s: got %d bytes, want %d: %w", src, n, p.Size, ErrChecksum)
	}
	if extra, _ := io.ReadFull(body, make([]byte, 1)); extra > 0 {
		return fmt.Errorf("GET %s: larger than declared %d bytes: %w", src, p.Size, ErrChecksum)
	}
	if hex.EncodeToString(h.Sum(nil)) != p.SHA256 {
		return fmt.Errorf("GET %s: %w", src, ErrChecksum)
	}
	return nil
}

// packageSource is what a sync reads from: an rpm (*Upstream) or apt (*AptUpstream) upstream.
type packageSource interface {
	Packages(ctx context.Context) ([]UpstreamPkg, error)
	Download(ctx context.Context, p UpstreamPkg, dst io.Writer) error
}
