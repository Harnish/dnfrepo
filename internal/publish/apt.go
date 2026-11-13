package publish

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"dnfrepo/internal/aptindex"
	"dnfrepo/internal/store"
)

// rebuildApt writes a complete signed apt archive for a deb repo into dists.<gen>/<suite>/ and
// repoints the dists symlink at it, so clients see Release, Release.gpg, InRelease and every index
// from one generation. The caller holds the repo lock.
func (p *Publisher) rebuildApt(ctx context.Context, repo store.Repo) error {
	d, err := p.Store.DebRepoByID(ctx, repo.ID)
	if err != nil {
		return err
	}
	rows, err := p.Store.ListPackages(ctx, repo.ID)
	if err != nil {
		return err
	}
	pkgs := make([]aptindex.Package, 0, len(rows))
	for _, r := range rows {
		pkgs = append(pkgs, aptindex.Package{Component: r.Component, Arch: r.Arch, Filename: r.Filename, Size: r.Size,
			MD5: r.MD5, SHA1: r.SHA1, SHA256: r.SHA256, Control: r.Control})
	}
	files := aptindex.Build(d.Suite, d.Components, d.Arches, pkgs, time.Now())

	dir := p.RepoDir(repo.Slug)
	gen := fmt.Sprintf("%d", time.Now().UnixNano())
	root := filepath.Join(dir, "dists."+gen)
	swapped := false
	defer func() {
		if !swapped {
			os.RemoveAll(root)
		}
	}()
	suiteDir := filepath.Join(root, d.Suite)
	for name, body := range files {
		path := filepath.Join(suiteDir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(path, body, 0o644); err != nil {
			return err
		}
	}
	sig, err := p.signer.SignDetached(files["Release"])
	if err != nil {
		return err
	}
	inrel, err := p.signer.ClearSign(files["Release"])
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(suiteDir, "Release.gpg"), sig, 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(suiteDir, "InRelease"), inrel, 0o644); err != nil {
		return err
	}
	tmpLink := filepath.Join(dir, ".link-"+gen)
	if err := os.Symlink("dists."+gen, tmpLink); err != nil {
		return err
	}
	if err := os.Rename(tmpLink, filepath.Join(dir, "dists")); err != nil {
		os.Remove(tmpLink)
		return err
	}
	swapped = true
	return pruneGenerations(dir, "dists.")
}
