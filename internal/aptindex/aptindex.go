// Package aptindex generates the metadata of an apt archive (Packages and Release files).
package aptindex

import (
	"bytes"
	"compress/gzip"
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"
)

// Package is one indexed .deb. Control is its control stanza; Filename is the bare file name.
type Package struct {
	Component, Arch, Filename string
	Size                      int64
	MD5, SHA1, SHA256         string
	Control                   string
}

// fields dnfrepo writes itself; any copy in Control (e.g. from an upstream index) is dropped.
var ownFields = []string{"Filename:", "Size:", "MD5sum:", "SHA1:", "SHA256:", "SHA512:"}

func stanza(p Package) string {
	var b strings.Builder
	skip := false // inside a continuation of a dropped field
	for _, l := range strings.Split(p.Control, "\n") {
		if l != "" && (l[0] == ' ' || l[0] == '\t') {
			if !skip {
				b.WriteString(l + "\n")
			}
			continue
		}
		skip = false
		for _, f := range ownFields {
			if strings.HasPrefix(l, f) {
				skip = true
			}
		}
		if !skip && l != "" {
			b.WriteString(l + "\n")
		}
	}
	fmt.Fprintf(&b, "Filename: pool/%s/%s\nSize: %d\nMD5sum: %s\nSHA1: %s\nSHA256: %s\n", p.Component, p.Filename, p.Size, p.MD5, p.SHA1, p.SHA256)
	return b.String()
}

func gz(b []byte) []byte {
	var out bytes.Buffer
	w := gzip.NewWriter(&out) // zero ModTime: output is deterministic
	w.Write(b)
	w.Close()
	return out.Bytes()
}

// Build returns every file of the suite, keyed by path relative to dists/<suite>/: Release (unsigned)
// and, for each component and architecture, binary-<arch>/{Packages,Packages.gz,Release}.
// A package of arch "all" is listed under every architecture; a package whose component or
// architecture is not configured is left out.
// ponytail: stanzas sorted by file name, not by (Package, Version); fine for apt.
func Build(suite string, components, arches []string, pkgs []Package, now time.Time) map[string][]byte {
	files := map[string][]byte{}
	sorted := slices.Clone(pkgs)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Filename < sorted[j].Filename })
	for _, c := range components {
		for _, a := range arches {
			var b strings.Builder
			for _, p := range sorted {
				if p.Component != c || (p.Arch != a && p.Arch != "all") {
					continue
				}
				b.WriteString(stanza(p) + "\n") // each stanza ends with a blank line
			}
			dir := c + "/binary-" + a + "/"
			files[dir+"Packages"] = []byte(b.String())
			files[dir+"Packages.gz"] = gz([]byte(b.String()))
			files[dir+"Release"] = []byte(fmt.Sprintf("Archive: %s\nComponent: %s\nOrigin: dnfrepo\nLabel: dnfrepo\nArchitecture: %s\n", suite, c, a))
		}
	}
	files["Release"] = release(suite, components, arches, files, now)
	return files
}

func release(suite string, components, arches []string, files map[string][]byte, now time.Time) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "Origin: dnfrepo\nLabel: dnfrepo\nSuite: %s\nCodename: %s\nDate: %s\nArchitectures: %s\nComponents: %s\nDescription: dnfrepo %s\n",
		suite, suite, now.UTC().Format("Mon, 02 Jan 2006 15:04:05 UTC"), strings.Join(arches, " "), strings.Join(components, " "), suite)
	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, blk := range []struct {
		name string
		sum  func([]byte) string
	}{
		{"MD5Sum", func(d []byte) string { h := md5.Sum(d); return hex.EncodeToString(h[:]) }},
		{"SHA1", func(d []byte) string { h := sha1.Sum(d); return hex.EncodeToString(h[:]) }},
		{"SHA256", func(d []byte) string { h := sha256.Sum256(d); return hex.EncodeToString(h[:]) }},
	} {
		b.WriteString(blk.name + ":\n")
		for _, p := range paths {
			fmt.Fprintf(&b, " %s %d %s\n", blk.sum(files[p]), len(files[p]), p)
		}
	}
	return []byte(b.String())
}
