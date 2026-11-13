package mirror

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"slices"
	"strconv"
	"strings"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/clearsign"
	"github.com/xi2/xz"

	"dnfrepo/internal/debmeta"
	"dnfrepo/internal/pipeline"
)

const (
	maxRelease         = 16 << 20  // Release / InRelease
	maxIndexCompressed = 256 << 20 // one compressed Packages file
	defaultMaxIndex    = 1 << 30   // one decompressed Packages file
)

// AptUpstream reads one suite of an apt archive at Base (http or https): the listed Components and
// Arches (arch "all" packages come along). If GPGKey (armored public key(s)) is set, InRelease, or
// Release plus Release.gpg, must carry a valid signature by one of its keys; RequireSig without a
// key is an error. Every Packages file is checked against the hash in Release before it is parsed.
type AptUpstream struct {
	Base, Suite             string
	Components, Arches      []string
	Client                  *http.Client
	Username, Token, GPGKey string
	RequireSig              bool
	MaxDebBytes             int64 // per-deb cap; 0 = no limit beyond the declared size

	maxIndexBytes int64 // decompressed Packages cap; 0 = 1 GiB
}

func (u *AptUpstream) client() *http.Client {
	if u.Client != nil {
		return u.Client
	}
	return http.DefaultClient
}

func (u *AptUpstream) url(rel string) string { return strings.TrimSuffix(u.Base, "/") + "/" + rel }

func (u *AptUpstream) fetch(ctx context.Context, rel string, max int64) ([]byte, error) {
	return fetchAuth(ctx, u.client(), u.url(rel), u.Username, u.Token, max)
}

type relFile struct {
	sha  string
	size int64
}

// release returns the verified Release text.
func (u *AptUpstream) release(ctx context.Context) ([]byte, error) {
	base := "dists/" + u.Suite + "/"
	var kr openpgp.EntityList
	if u.GPGKey != "" {
		var err error
		if kr, err = openpgp.ReadArmoredKeyRing(strings.NewReader(u.GPGKey)); err != nil {
			return nil, fmt.Errorf("upstream GPG key: %w", err)
		}
	}
	in, inErr := u.fetch(ctx, base+"InRelease", maxRelease)
	if inErr == nil {
		blk, _ := clearsign.Decode(in)
		if blk == nil {
			return nil, errors.New("InRelease is not a clearsigned message")
		}
		if kr != nil {
			if _, err := blk.VerifySignature(kr, nil); err != nil {
				return nil, fmt.Errorf("%s: signature: %w", redact(u.url(base+"InRelease")), err)
			}
		}
		return blk.Bytes, nil
	}
	rel, err := u.fetch(ctx, base+"Release", maxRelease)
	if err != nil {
		return nil, fmt.Errorf("no InRelease (%v) and no Release: %w", inErr, err)
	}
	if kr != nil {
		sig, err := u.fetch(ctx, base+"Release.gpg", maxSig)
		if err != nil {
			return nil, fmt.Errorf("signature: %w", err)
		}
		if _, err := openpgp.CheckArmoredDetachedSignature(kr, bytes.NewReader(rel), bytes.NewReader(sig), nil); err != nil {
			if _, err2 := openpgp.CheckDetachedSignature(kr, bytes.NewReader(rel), bytes.NewReader(sig), nil); err2 != nil {
				return nil, fmt.Errorf("%s: signature: %w", redact(u.url(base+"Release.gpg")), err)
			}
		}
	}
	return rel, nil
}

// Packages returns every wanted package of the suite. An unsafe or malformed entry fails the call.
func (u *AptUpstream) Packages(ctx context.Context) ([]UpstreamPkg, error) {
	if !isHTTP(u.Base) {
		return nil, fmt.Errorf("upstream %s: only http and https are supported", redact(u.Base))
	}
	if u.RequireSig && u.GPGKey == "" {
		return nil, errors.New("upstream requires a signature but no GPG key is configured")
	}
	text, err := u.release(ctx)
	if err != nil {
		return nil, err
	}
	st, err := debmeta.ParseStanzas(bytes.NewReader(text))
	if err != nil || len(st) == 0 {
		return nil, fmt.Errorf("parse Release: %v", err)
	}
	files := map[string]relFile{}
	for _, l := range strings.Split(st[0].Fields["SHA256"], "\n") {
		f := strings.Fields(l)
		if len(f) != 3 {
			continue
		}
		n, err := strconv.ParseInt(f[1], 10, 64)
		if err != nil || n < 0 {
			return nil, fmt.Errorf("Release: bad size in %q", l)
		}
		files[f[2]] = relFile{strings.ToLower(f[0]), n}
	}
	if len(files) == 0 {
		return nil, errors.New("Release lists no SHA256 hashes")
	}
	haveComps, haveArches := strings.Fields(st[0].Fields["Components"]), strings.Fields(st[0].Fields["Architectures"])
	for _, c := range u.Components {
		if !slices.Contains(haveComps, c) {
			return nil, fmt.Errorf("upstream suite %q has no component %q", u.Suite, c)
		}
	}
	for _, a := range u.Arches {
		if !slices.Contains(haveArches, a) {
			return nil, fmt.Errorf("upstream suite %q has no architecture %q", u.Suite, a)
		}
	}

	var out []UpstreamPkg
	for _, c := range u.Components {
		for _, a := range append(slices.Clone(u.Arches), "all") {
			p, ok := indexPath(files, c, a)
			if !ok {
				if a == "all" { // Ubuntu has no binary-all: arch-all packages sit in each arch's index
					continue
				}
				return nil, fmt.Errorf("Release lists no Packages.xz or Packages.gz for %s/%s", c, a)
			}
			pkgs, err := u.index(ctx, c, p, files[p])
			if err != nil {
				return nil, err
			}
			out = append(out, pkgs...)
		}
	}
	return out, nil
}

func indexPath(files map[string]relFile, component, arch string) (string, bool) {
	for _, ext := range []string{".xz", ".gz"} {
		p := component + "/binary-" + arch + "/Packages" + ext
		if _, ok := files[p]; ok {
			return p, true
		}
	}
	return "", false
}

// index fetches one Packages file, verifies it against Release before parsing, and converts it.
func (u *AptUpstream) index(ctx context.Context, component, rel string, want relFile) ([]UpstreamPkg, error) {
	if want.size > maxIndexCompressed {
		return nil, fmt.Errorf("%s: index over %d bytes", rel, maxIndexCompressed)
	}
	full := "dists/" + u.Suite + "/" + rel
	body, err := u.fetch(ctx, full, want.size)
	if err != nil {
		return nil, err
	}
	if h := sha256.Sum256(body); int64(len(body)) != want.size || hex.EncodeToString(h[:]) != want.sha {
		return nil, fmt.Errorf("%s: %w", redact(u.url(full)), ErrChecksum)
	}
	var dr io.Reader
	switch path.Ext(rel) {
	case ".xz":
		x, err := xz.NewReader(bytes.NewReader(body), maxDict)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", rel, err)
		}
		dr = x
	default:
		g, err := gzip.NewReader(bytes.NewReader(body))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", rel, err)
		}
		dr = g
	}
	limit := u.maxIndexBytes
	if limit == 0 {
		limit = defaultMaxIndex
	}
	raw, err := io.ReadAll(io.LimitReader(dr, limit+1))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", rel, err)
	}
	if int64(len(raw)) > limit {
		return nil, fmt.Errorf("%s: decompressed index over %d bytes", rel, limit)
	}
	stanzas, err := debmeta.ParseStanzas(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", rel, err)
	}
	var out []UpstreamPkg
	for _, s := range stanzas {
		f := s.Fields
		arch := f["Architecture"]
		if arch != "all" && !slices.Contains(u.Arches, arch) {
			continue
		}
		id := fmt.Sprintf("%q", f["Package"]+"_"+f["Version"]+"_"+arch)
		if f["Package"] == "" || f["Version"] == "" {
			return nil, fmt.Errorf("%s: package without name or version", rel)
		}
		sha := strings.ToLower(strings.TrimSpace(f["SHA256"]))
		if b, err := hex.DecodeString(sha); err != nil || len(b) != sha256.Size {
			return nil, fmt.Errorf("package %s: invalid SHA256", id)
		}
		fn := f["Filename"]
		if !safeHref(fn) || !pipeline.ValidDebFilename(path.Base(fn)) {
			return nil, fmt.Errorf("package %s: unsafe location", id)
		}
		size, err := strconv.ParseInt(strings.TrimSpace(f["Size"]), 10, 64)
		if err != nil || size <= 0 {
			return nil, fmt.Errorf("package %s: invalid size", id)
		}
		out = append(out, UpstreamPkg{Name: f["Package"], Arch: arch, Version: f["Version"], Filename: path.Base(fn), Href: fn,
			SHA256: sha, Size: size, Component: component, Control: s.Raw})
	}
	return out, nil
}

// Download streams p into dst, verifying size and sha256 (see Upstream.Download).
func (u *AptUpstream) Download(ctx context.Context, p UpstreamPkg, dst io.Writer) error {
	return downloadVerified(ctx, u.client(), u.Base, u.Username, u.Token, p, u.MaxDebBytes, dst)
}
