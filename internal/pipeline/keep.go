package pipeline

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"dnfrepo/internal/store"
)

// purgeLocked deletes the oldest versions beyond the repo's keep limits (default, or the
// per-package override; 0 = unlimited). Packages are grouped by name and arch and ordered
// with rpm's version comparison. The caller holds the repo lock and rebuilds afterwards.
func (s *Service) purgeLocked(ctx context.Context, repo store.Repo, actor string) ([]string, error) {
	keep, overrides, err := s.Store.RepoKeep(ctx, repo.ID)
	if err != nil {
		return nil, err
	}
	if keep == 0 && len(overrides) == 0 {
		return nil, nil
	}
	pkgs, err := s.Store.ListPackages(ctx, repo.ID)
	if err != nil {
		return nil, err
	}
	groups := map[[2]string][]store.Package{}
	for _, p := range pkgs {
		k := [2]string{p.Name, p.Arch}
		groups[k] = append(groups[k], p)
	}
	var removed []string
	for k, g := range groups {
		limit := keep
		if v, ok := overrides[k[0]]; ok {
			limit = v
		}
		if limit <= 0 || len(g) <= limit {
			continue
		}
		sort.Slice(g, func(i, j int) bool { // newest first
			if c := comparePackage(repo.Kind, g[i].Version, g[i].Release, g[j].Version, g[j].Release); c != 0 {
				return c > 0
			}
			if !g[i].AddedAt.Equal(g[j].AddedAt) {
				return g[i].AddedAt.After(g[j].AddedAt)
			}
			return g[i].Filename > g[j].Filename
		})
		for _, p := range g[limit:] {
			if err := s.Store.DeletePackage(ctx, repo.ID, p.Filename); err != nil {
				return removed, err
			}
			if err := os.Remove(filepath.Join(s.Publisher.RepoDir(repo.Slug), p.Filename)); err != nil && !os.IsNotExist(err) {
				return removed, err
			}
			s.Store.AddAudit(ctx, repo.ID, actor, "purged", p.Filename)
			removed = append(removed, p.Filename)
		}
	}
	sort.Strings(removed)
	return removed, nil
}

// ApplyKeep enforces the repo's current keep limits now and rebuilds if anything was removed.
func (s *Service) ApplyKeep(ctx context.Context, slug, actor string) ([]string, error) {
	defer s.lock(slug)()
	repo, err := s.Store.RepoBySlug(ctx, slug)
	if err != nil {
		return nil, err
	}
	removed, err := s.purgeLocked(ctx, repo, actor)
	if err != nil {
		return removed, err
	}
	if len(removed) > 0 {
		if err := s.rebuild(ctx, slug); err != nil {
			return removed, fmt.Errorf("purged but metadata rebuild failed: %w", err)
		}
	}
	return removed, nil
}
