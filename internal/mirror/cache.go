package mirror

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"dnfrepo/internal/debmeta"
	"dnfrepo/internal/store"
)

// refresh installs upstream's current index in a cache repo without downloading any package:
// rpm copies the verified repodata, deb regenerates the archive from the verified indexes.
// Filters do not apply: clients see everything upstream lists.
func (s *Syncer) refresh(ctx context.Context, repo store.Repo, up packageSource, actor string) (Result, error) {
	var (
		all []UpstreamPkg
		dir string
		err error
	)
	if rpm, ok := up.(*Upstream); ok {
		if s.StagingDir == "" {
			return Result{}, errors.New("mirror staging dir is not configured")
		}
		parent := filepath.Join(s.StagingDir, strconv.FormatInt(repo.ID, 10))
		if err := os.MkdirAll(parent, 0o700); err != nil {
			return Result{}, fmt.Errorf("staging dir: %w", err)
		}
		dir = filepath.Join(parent, "repodata-"+strconv.FormatInt(time.Now().UnixNano(), 10))
		if err := os.Mkdir(dir, 0o700); err != nil {
			return Result{}, fmt.Errorf("staging dir: %w", err)
		}
		defer os.RemoveAll(dir) // no-op once the swap consumed it
		all, err = rpm.FetchRepodata(ctx, dir)
	} else {
		all, err = up.Packages(ctx)
	}
	if err != nil {
		return Result{}, err
	}
	entries, err := cacheEntries(repo.Kind, all)
	if err != nil {
		return Result{}, err
	}
	if _, err := s.Pipeline.RefreshCache(ctx, repo.Slug, entries, dir, actor); err != nil {
		return Result{}, err
	}
	return Result{Status: "refreshed"}, nil
}

// cacheEntries maps an upstream index to cache entries. A client requests an rpm by its
// upstream location (served verbatim in repodata) and a deb by pool/<component>/<file>
// (the regenerated Packages files use the flat pool layout).
func cacheEntries(kind string, pkgs []UpstreamPkg) ([]store.CacheEntry, error) {
	pkgs, err := dedupe(pkgs)
	if err != nil {
		return nil, err
	}
	out := make([]store.CacheEntry, 0, len(pkgs))
	for _, p := range pkgs {
		e := store.CacheEntry{Path: p.Href, Href: p.Href, Filename: p.Filename, Name: p.Name, Arch: p.Arch,
			Version: p.Version, Release: p.Release, SHA256: p.SHA256, Size: p.Size}
		if kind == store.KindDeb {
			e.Path, e.Component, e.Control = "pool/"+p.Component+"/"+p.Filename, p.Component, p.Control
			st, err := debmeta.ParseStanzas(bytes.NewReader([]byte(p.Control)))
			if err != nil || len(st) != 1 {
				return nil, fmt.Errorf("%s: bad control stanza", p.Filename)
			}
			e.MD5, e.SHA1 = st[0].Fields["MD5sum"], st[0].Fields["SHA1"]
		}
		out = append(out, e)
	}
	return out, nil
}
