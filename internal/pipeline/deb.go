package pipeline

import (
	"crypto/md5"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"slices"

	"dnfrepo/internal/debmeta"
	"dnfrepo/internal/debver"
	"dnfrepo/internal/rpmver"
	"dnfrepo/internal/store"
)

var (
	ErrWrongKind        = errors.New("file type does not match the repo kind (.rpm for rpm repos, .deb for deb repos)")
	ErrBadArch          = errors.New("package architecture is not accepted by this repo")
	ErrFilenameMismatch = errors.New("filename does not match the package's control data (want <Package>_<version without epoch>_<Arch>.deb)")
	ErrDebIncompatible  = errors.New("target stage does not accept the package's component or architecture")
)

var debFilenameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.+~_-]*\.deb$`)

// ValidDebFilename reports whether name is an acceptable deb file name (no path).
func ValidDebFilename(name string) bool { return debFilenameRe.MatchString(name) }

// validPackageFilename accepts a well-formed rpm or deb name; the repo kind decides which one applies.
func validPackageFilename(name string) bool {
	return filenameRe.MatchString(name) || debFilenameRe.MatchString(name)
}

// comparePackage orders two versions of one package the way the repo's package manager does.
func comparePackage(kind, verA, relA, verB, relB string) int {
	if kind == store.KindDeb {
		return debver.Compare(verA, verB)
	}
	return rpmver.ComparePackage(verA, relA, verB, relB)
}

// debAccepts reports whether the deb repo takes a package of that component and architecture.
func debAccepts(d store.DebRepo, component, arch string) bool {
	return slices.Contains(d.Components, component) && (arch == "all" || slices.Contains(d.Arches, arch))
}

// md5sha1 returns the hex MD5 and SHA1 of the file at path (apt indexes list both).
func md5sha1(path string) (string, string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", "", err
	}
	defer f.Close()
	m, s := md5.New(), sha1.New()
	if _, err := io.Copy(io.MultiWriter(m, s), f); err != nil {
		return "", "", err
	}
	return hex.EncodeToString(m.Sum(nil)), hex.EncodeToString(s.Sum(nil)), nil
}

// debRow builds the package row (identity and index data) of an uploaded deb: the control data
// is authoritative, so the filename must match it and the arch must be one the repo takes.
func debRow(path, filename string, d store.DebRepo) (store.Package, error) {
	c, err := debmeta.Parse(path)
	if err != nil {
		return store.Package{}, err
	}
	if debmeta.Filename(c) != filename {
		return store.Package{}, fmt.Errorf("%w: %s", ErrFilenameMismatch, filename)
	}
	if c.Arch != "all" && !slices.Contains(d.Arches, c.Arch) {
		return store.Package{}, fmt.Errorf("%w: %s", ErrBadArch, c.Arch)
	}
	md, sh, err := md5sha1(path)
	if err != nil {
		return store.Package{}, err
	}
	return store.Package{Name: c.Package, Version: c.Version, Arch: c.Arch, Component: d.Components[0],
		Control: c.Stanza, MD5: md, SHA1: sh}, nil
}

// stagedDebRow builds the row of a mirrored deb: identity from the file, component and control
// stanza from the upstream index (the file's own control data when none was given).
func stagedDebRow(f StagedFile, d store.DebRepo) (store.Package, error) {
	c, err := debmeta.Parse(f.Path)
	if err != nil {
		return store.Package{}, fmt.Errorf("%s: %w", f.Filename, err)
	}
	if !debAccepts(d, f.Component, c.Arch) {
		return store.Package{}, fmt.Errorf("%w: %s (%s, %s)", ErrDebIncompatible, f.Filename, f.Component, c.Arch)
	}
	md, sh, err := md5sha1(f.Path)
	if err != nil {
		return store.Package{}, err
	}
	ctl := f.Control
	if ctl == "" {
		ctl = c.Stanza
	}
	return store.Package{Name: c.Package, Version: c.Version, Arch: c.Arch, Component: f.Component, Control: ctl, MD5: md, SHA1: sh}, nil
}
