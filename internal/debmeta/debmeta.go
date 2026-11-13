// Package debmeta reads package identity from .deb files and parses Debian control stanzas.
package debmeta

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/klauspost/compress/zstd"
	"github.com/xi2/xz"
)

var ErrNotDeb = errors.New("not a valid deb file")

const (
	maxControl    = 1 << 20  // the control file itself
	maxControlTar = 16 << 20 // decompressed control.tar
	maxMember     = 64 << 20 // compressed control.tar member
	maxDict       = 64 << 20
	maxLine       = 1 << 20
)

// Control is the identity of a deb. Stanza is the control file verbatim, trailing newline trimmed.
type Control struct{ Package, Version, Arch, Stanza string }

// Filename is the conventional pool file name: <Package>_<version without epoch>_<Arch>.deb.
func Filename(c Control) string {
	v := c.Version
	if i := strings.IndexByte(v, ':'); i >= 0 {
		v = v[i+1:]
	}
	return c.Package + "_" + v + "_" + c.Arch + ".deb"
}

// Parse reads the control data of the deb at path. Any malformed input is ErrNotDeb.
func Parse(path string) (Control, error) {
	f, err := os.Open(path)
	if err != nil {
		return Control{}, err
	}
	defer f.Close()
	r := bufio.NewReader(f)
	magic := make([]byte, 8)
	if _, err := io.ReadFull(r, magic); err != nil || string(magic) != "!<arch>\n" {
		return Control{}, ErrNotDeb
	}
	for {
		hdr := make([]byte, 60)
		if _, err := io.ReadFull(r, hdr); err != nil {
			return Control{}, ErrNotDeb // ran out before finding a control member
		}
		if string(hdr[58:60]) != "`\n" {
			return Control{}, ErrNotDeb
		}
		name := strings.TrimRight(strings.TrimRight(string(hdr[:16]), " "), "/")
		size, err := strconv.ParseInt(strings.TrimSpace(string(hdr[48:58])), 10, 64)
		if err != nil || size < 0 {
			return Control{}, ErrNotDeb
		}
		if strings.HasPrefix(name, "control.tar") {
			if size > maxMember {
				return Control{}, ErrNotDeb
			}
			body := make([]byte, size)
			if _, err := io.ReadFull(r, body); err != nil {
				return Control{}, ErrNotDeb
			}
			return fromTar(name, body)
		}
		skip := size + size%2
		if _, err := io.CopyN(io.Discard, r, skip); err != nil {
			return Control{}, ErrNotDeb
		}
	}
}

func fromTar(name string, body []byte) (Control, error) {
	var zr io.Reader
	switch strings.TrimPrefix(name, "control.tar") {
	case "":
		zr = bytes.NewReader(body)
	case ".gz":
		g, err := gzip.NewReader(bytes.NewReader(body))
		if err != nil {
			return Control{}, ErrNotDeb
		}
		zr = g
	case ".xz":
		x, err := xz.NewReader(bytes.NewReader(body), maxDict)
		if err != nil {
			return Control{}, ErrNotDeb
		}
		zr = x
	case ".zst":
		z, err := zstd.NewReader(bytes.NewReader(body), zstd.WithDecoderMaxWindow(maxDict))
		if err != nil {
			return Control{}, ErrNotDeb
		}
		defer z.Close()
		zr = z
	default:
		return Control{}, ErrNotDeb
	}
	tr := tar.NewReader(io.LimitReader(zr, maxControlTar))
	for {
		h, err := tr.Next()
		if err != nil {
			return Control{}, ErrNotDeb
		}
		if h.Name != "./control" && h.Name != "control" {
			continue
		}
		b, err := io.ReadAll(io.LimitReader(tr, maxControl+1))
		if err != nil || len(b) > maxControl {
			return Control{}, ErrNotDeb
		}
		st, err := ParseStanzas(bytes.NewReader(b))
		if err != nil || len(st) == 0 {
			return Control{}, ErrNotDeb
		}
		c := Control{Package: st[0].Fields["Package"], Version: st[0].Fields["Version"],
			Arch: st[0].Fields["Architecture"], Stanza: strings.TrimRight(string(b), "\n")}
		if c.Package == "" || c.Version == "" || c.Arch == "" {
			return Control{}, ErrNotDeb
		}
		return c, nil
	}
}

// Stanza is one blank-line separated block of "Field: value" lines. Fields joins continuation
// lines with "\n"; Raw is the block verbatim.
type Stanza struct {
	Fields map[string]string
	Raw    string
}

// ParseStanzas splits r into stanzas. A line over 1 MiB is an error.
func ParseStanzas(r io.Reader) ([]Stanza, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), maxLine)
	var out []Stanza
	var raw []string
	var fields map[string]string
	last := ""
	flush := func() {
		if len(raw) > 0 {
			out = append(out, Stanza{Fields: fields, Raw: strings.Join(raw, "\n")})
		}
		raw, fields, last = nil, nil, ""
	}
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.TrimSpace(line) == "":
			flush()
		case strings.HasPrefix(line, "#"):
		case line[0] == ' ' || line[0] == '\t':
			if last == "" {
				return nil, fmt.Errorf("continuation line without a field: %q", line)
			}
			raw = append(raw, line)
			fields[last] += "\n" + line
		default:
			k, v, ok := strings.Cut(line, ":")
			if !ok || k == "" {
				return nil, fmt.Errorf("malformed control line: %q", line)
			}
			if fields == nil {
				fields = map[string]string{}
			}
			raw = append(raw, line)
			last = k
			fields[k] = strings.TrimSpace(v)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	flush()
	return out, nil
}
