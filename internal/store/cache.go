package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

// CacheEntry is one package of a cache repo's upstream index. Path is what a client requests
// (rpm: the upstream location href; deb: pool/<component>/<filename>); Href is the upstream path.
type CacheEntry struct {
	Path, Href, Filename, Name, Arch, Version, Release, SHA256 string
	Size                                                       int64
	// deb only: index data for the regenerated Packages file.
	Component, Control, MD5, SHA1 string
}

var cacheCols = []string{"repo_id", "path", "href", "filename", "name", "arch", "version", "release", "sha256", "size",
	"component", "control", "md5", "sha1"}

const cacheSelect = `SELECT path, href, filename, name, arch, version, release, sha256, size, component, control, md5, sha1 FROM cache_entries `

func scanCache(row pgx.Row) (CacheEntry, error) {
	var e CacheEntry
	err := row.Scan(&e.Path, &e.Href, &e.Filename, &e.Name, &e.Arch, &e.Version, &e.Release, &e.SHA256, &e.Size,
		&e.Component, &e.Control, &e.MD5, &e.SHA1)
	if errors.Is(err, pgx.ErrNoRows) {
		return e, ErrNotFound
	}
	return e, err
}

// ReplaceCacheEntries swaps repoID's whole index for es in one transaction.
func (s *Store) ReplaceCacheEntries(ctx context.Context, repoID int64, es []CacheEntry) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `DELETE FROM cache_entries WHERE repo_id=$1`, repoID); err != nil {
		return err
	}
	rows := make([][]any, len(es))
	for i, e := range es {
		rows[i] = []any{repoID, e.Path, e.Href, e.Filename, e.Name, e.Arch, e.Version, e.Release, e.SHA256, e.Size,
			e.Component, e.Control, e.MD5, e.SHA1}
	}
	if _, err := tx.CopyFrom(ctx, pgx.Identifier{"cache_entries"}, cacheCols, pgx.CopyFromRows(rows)); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) CacheEntryByPath(ctx context.Context, repoID int64, path string) (CacheEntry, error) {
	return scanCache(s.pool.QueryRow(ctx, cacheSelect+`WHERE repo_id=$1 AND path=$2`, repoID, path))
}

func (s *Store) ListCacheEntries(ctx context.Context, repoID int64) ([]CacheEntry, error) {
	rows, err := s.pool.Query(ctx, cacheSelect+`WHERE repo_id=$1 ORDER BY path`, repoID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CacheEntry
	for rows.Next() {
		e, err := scanCache(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// UncachedPackages lists repoID's package filenames that its current index no longer has.
func (s *Store) UncachedPackages(ctx context.Context, repoID int64) ([]string, error) {
	rows, err := s.pool.Query(ctx, `SELECT p.filename FROM packages p WHERE p.repo_id=$1 AND NOT EXISTS
		(SELECT 1 FROM cache_entries c WHERE c.repo_id=p.repo_id AND c.filename=p.filename) ORDER BY p.filename`, repoID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

// TouchPackage records that filename was just served.
func (s *Store) TouchPackage(ctx context.Context, repoID int64, filename string) error {
	_, err := s.pool.Exec(ctx, `UPDATE packages SET last_pulled=now() WHERE repo_id=$1 AND filename=$2`, repoID, filename)
	return err
}

func (s *Store) IsCacheRepo(ctx context.Context, repoID int64) (bool, error) {
	var ok bool
	err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM mirrors WHERE repo_id=$1 AND mode='cache')`, repoID).Scan(&ok)
	return ok, err
}
