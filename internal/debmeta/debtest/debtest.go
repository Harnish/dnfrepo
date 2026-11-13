// Package debtest builds tiny valid .deb files for tests.
package debtest

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"sort"
	"testing"

	"github.com/klauspost/compress/zstd"
)

// Spec describes a test deb. Compress is "" or "gz" (gzip) or "zst" (zstd) for control.tar.
type Spec struct {
	Package, Version, Arch string
	Fields                 map[string]string // extra control fields
	Compress               string
}

// Control returns the control file text Build embeds (no trailing newline).
func Control(s Spec) string {
	var b bytes.Buffer
	fmt.Fprintf(&b, "Package: %s\nVersion: %s\nArchitecture: %s\nMaintainer: Test <t@example.com>\nDescription: test package\n", s.Package, s.Version, s.Arch)
	keys := make([]string, 0, len(s.Fields))
	for k := range s.Fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(&b, "%s: %s\n", k, s.Fields[k])
	}
	return string(bytes.TrimRight(b.Bytes(), "\n"))
}

func tarOf(name string, body []byte) []byte {
	var b bytes.Buffer
	tw := tar.NewWriter(&b)
	if name != "" {
		tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body))})
		tw.Write(body)
	}
	tw.Close()
	return b.Bytes()
}

func compress(t testing.TB, kind string, data []byte) (string, []byte) {
	t.Helper()
	var b bytes.Buffer
	switch kind {
	case "", "gz":
		w := gzip.NewWriter(&b)
		w.Write(data)
		w.Close()
		return ".gz", b.Bytes()
	case "zst":
		w, err := zstd.NewWriter(&b)
		if err != nil {
			t.Fatal(err)
		}
		w.Write(data)
		w.Close()
		return ".zst", b.Bytes()
	}
	t.Fatalf("debtest: unknown compression %q", kind)
	return "", nil
}

func member(name string, body []byte) []byte {
	var b bytes.Buffer
	fmt.Fprintf(&b, "%-16s%-12d%-6d%-6d%-8s%-10d`\n", name, 0, 0, 0, "100644", len(body))
	b.Write(body)
	if len(body)%2 == 1 {
		b.WriteByte('\n')
	}
	return b.Bytes()
}

// Build returns the bytes of a valid deb.
func Build(t testing.TB, s Spec) []byte {
	t.Helper()
	ext, ctrl := compress(t, s.Compress, tarOf("./control", []byte(Control(s)+"\n")))
	_, data := compress(t, "gz", tarOf("", nil))
	var out bytes.Buffer
	out.WriteString("!<arch>\n")
	out.Write(member("debian-binary", []byte("2.0\n")))
	out.Write(member("control.tar"+ext, ctrl))
	out.Write(member("data.tar.gz", data))
	return out.Bytes()
}
