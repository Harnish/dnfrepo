package pipeline

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"dnfrepo/internal/aptindex"
	"dnfrepo/internal/store"
)

// RefreshCache installs a cache repo's new upstream index: it replaces the stored entries, swaps
// in the signed index (rpm: the verified upstream repodata in repodataDir, consumed; deb: an
// archive generated from entries), then removes cached packages the index no longer lists,
// since no client can request them. It returns the removed filenames.
func (s *Service) RefreshCache(ctx context.Context, slug string, entries []store.CacheEntry, repodataDir, actor string) ([]string, error) {
	defer s.lock(slug)()
	repo, err := s.Store.RepoBySlug(ctx, slug)
	if err != nil {
		return nil, err
	}
	if err := s.Store.ReplaceCacheEntries(ctx, repo.ID, entries); err != nil {
		return nil, err
	}
	d := s.RebuildTimeout
	if d <= 0 {
		d = 10 * time.Minute
	}
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), d)
	defer cancel()
	if repo.Kind == store.KindDeb {
		pkgs := make([]aptindex.Package, len(entries))
		for i, e := range entries {
			pkgs[i] = aptindex.Package{Component: e.Component, Arch: e.Arch, Filename: e.Filename, Size: e.Size,
				MD5: e.MD5, SHA1: e.SHA1, SHA256: e.SHA256, Control: e.Control}
		}
		err = s.Publisher.PublishApt(sctx, repo, pkgs)
	} else {
		err = s.Publisher.SwapRepodata(slug, repodataDir)
	}
	if err != nil {
		return nil, err
	}
	gone, err := s.Store.UncachedPackages(sctx, repo.ID)
	if err != nil {
		return nil, err
	}
	for i, f := range gone {
		if err := s.Store.DeletePackage(sctx, repo.ID, f); err != nil {
			return gone[:i], err
		}
		if err := os.Remove(filepath.Join(s.Publisher.RepoDir(slug), f)); err != nil && !os.IsNotExist(err) {
			return gone[:i], err
		}
		s.Store.AddAudit(sctx, repo.ID, actor, "purged", f)
	}
	return gone, nil
}
