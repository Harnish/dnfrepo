package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// ImageMirror follows one upstream image tag into the embedded registry.
type ImageMirror struct {
	ID                       int64
	Upstream, Tag, LocalName string
	Platforms                []string // os/arch[/variant]; empty = all
	Keep                     int
	Username                 string
	Secret                   []byte // sealed token; nil = none
	UpstreamDigest           string
	LastRun                  *time.Time
	LastStatus, LastError    string
	CreatedAt                time.Time
}

// ImageVersion is one dated tag a mirror created (the retention ledger).
type ImageVersion struct {
	MirrorID                    int64
	Tag, Digest, UpstreamDigest string
	CreatedAt                   time.Time
}

var ErrMirrorExists = errors.New("an image mirror for that name and tag already exists")

const imCols = `id, upstream, tag, local_name, platforms, keep, username, secret, upstream_digest, last_run, last_status, last_error, created_at`

func scanIM(row pgx.Row) (ImageMirror, error) {
	var m ImageMirror
	err := row.Scan(&m.ID, &m.Upstream, &m.Tag, &m.LocalName, &m.Platforms, &m.Keep, &m.Username, &m.Secret,
		&m.UpstreamDigest, &m.LastRun, &m.LastStatus, &m.LastError, &m.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return m, ErrNotFound
	}
	if m.Platforms == nil {
		m.Platforms = []string{}
	}
	return m, err
}

func plats(p []string) []string {
	if p == nil {
		return []string{}
	}
	return p
}

func (s *Store) AddImageMirror(ctx context.Context, m ImageMirror) (ImageMirror, error) {
	out, err := scanIM(s.pool.QueryRow(ctx, `INSERT INTO registry_mirrors(upstream, tag, local_name, platforms, keep, username, secret)
		VALUES($1,$2,$3,$4,$5,$6,$7) RETURNING `+imCols, m.Upstream, m.Tag, m.LocalName, plats(m.Platforms), m.Keep, m.Username, m.Secret))
	if pgCode(err) == "23505" {
		return ImageMirror{}, ErrMirrorExists
	}
	return out, err
}

func (s *Store) ImageMirrorByID(ctx context.Context, id int64) (ImageMirror, error) {
	return scanIM(s.pool.QueryRow(ctx, `SELECT `+imCols+` FROM registry_mirrors WHERE id=$1`, id))
}

func (s *Store) ListImageMirrors(ctx context.Context) ([]ImageMirror, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+imCols+` FROM registry_mirrors ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ImageMirror
	for rows.Next() {
		m, err := scanIM(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// UpdateImageMirror sets the editable fields. username nil keeps the stored credentials;
// non-nil replaces username and secret. clearDigest forces the next run to copy.
func (s *Store) UpdateImageMirror(ctx context.Context, id int64, platforms []string, keep int, username *string, secret []byte, clearDigest bool) error {
	tag, err := s.pool.Exec(ctx, `UPDATE registry_mirrors SET platforms=$2, keep=$3,
		username = CASE WHEN $4::boolean THEN $5 ELSE username END,
		secret   = CASE WHEN $4::boolean THEN $6 ELSE secret END,
		upstream_digest = CASE WHEN $7 THEN '' ELSE upstream_digest END
		WHERE id=$1`, id, plats(platforms), keep, username != nil, deref(username), secret, clearDigest)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func (s *Store) DeleteImageMirror(ctx context.Context, id int64) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM registry_mirrors WHERE id=$1`, id)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

func (s *Store) RecordImageMirrorRun(ctx context.Context, id int64, status, errMsg string) error {
	_, err := s.pool.Exec(ctx, `UPDATE registry_mirrors SET last_run=now(), last_status=$2, last_error=$3 WHERE id=$1`, id, status, errMsg)
	return err
}

// AddImageVersion records a dated tag and sets the mirror's upstream digest, in one transaction.
func (s *Store) AddImageVersion(ctx context.Context, v ImageVersion, upstreamDigest string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `INSERT INTO registry_mirror_versions(mirror_id, tag, digest, upstream_digest) VALUES($1,$2,$3,$4)`,
		v.MirrorID, v.Tag, v.Digest, upstreamDigest); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE registry_mirrors SET upstream_digest=$2 WHERE id=$1`, v.MirrorID, upstreamDigest); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ImageVersions lists a mirror's dated tags, newest first.
func (s *Store) ImageVersions(ctx context.Context, mirrorID int64) ([]ImageVersion, error) {
	rows, err := s.pool.Query(ctx, `SELECT mirror_id, tag, digest, upstream_digest, created_at FROM registry_mirror_versions
		WHERE mirror_id=$1 ORDER BY created_at DESC, tag DESC`, mirrorID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ImageVersion
	for rows.Next() {
		var v ImageVersion
		if err := rows.Scan(&v.MirrorID, &v.Tag, &v.Digest, &v.UpstreamDigest, &v.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Store) DeleteImageVersion(ctx context.Context, mirrorID int64, tag string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM registry_mirror_versions WHERE mirror_id=$1 AND tag=$2`, mirrorID, tag)
	return err
}

// EnsureRegistryRepoMode inserts a settings row with mode if the name has none; an existing row is kept.
func (s *Store) EnsureRegistryRepoMode(ctx context.Context, name string, mode AccessMode) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO registry_repos(name, mode) VALUES($1,$2) ON CONFLICT DO NOTHING`, name, string(mode))
	return err
}
