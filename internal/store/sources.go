package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

type Source struct {
	ID, RepoID int64
	Kind, Spec string // Kind is "github" (Spec "owner/name") or "plex"
	LastRun    *time.Time
	LastStatus string
	LastError  string
}

const srcCols = `id, repo_id, kind, spec, last_run, last_status, last_error`

func scanSrc(row pgx.Row) (Source, error) {
	var s Source
	err := row.Scan(&s.ID, &s.RepoID, &s.Kind, &s.Spec, &s.LastRun, &s.LastStatus, &s.LastError)
	if errors.Is(err, pgx.ErrNoRows) {
		return s, ErrNotFound
	}
	return s, err
}

func (s *Store) AddSource(ctx context.Context, repoID int64, kind, spec string) (Source, error) {
	return scanSrc(s.pool.QueryRow(ctx, `INSERT INTO sources(repo_id, kind, spec) VALUES($1,$2,$3) RETURNING `+srcCols, repoID, kind, spec))
}

func (s *Store) SourceByID(ctx context.Context, id int64) (Source, error) {
	return scanSrc(s.pool.QueryRow(ctx, `SELECT `+srcCols+` FROM sources WHERE id=$1`, id))
}

func (s *Store) querySources(ctx context.Context, q string, args ...any) ([]Source, error) {
	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Source
	for rows.Next() {
		x, err := scanSrc(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

func (s *Store) ListSources(ctx context.Context, repoID int64) ([]Source, error) {
	return s.querySources(ctx, `SELECT `+srcCols+` FROM sources WHERE repo_id=$1 ORDER BY id`, repoID)
}

func (s *Store) AllSources(ctx context.Context) ([]Source, error) {
	return s.querySources(ctx, `SELECT `+srcCols+` FROM sources ORDER BY id`)
}

func (s *Store) DeleteSource(ctx context.Context, id int64) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM sources WHERE id=$1`, id)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

func (s *Store) RecordSourceRun(ctx context.Context, id int64, status, errMsg string) error {
	_, err := s.pool.Exec(ctx, `UPDATE sources SET last_run=now(), last_status=$2, last_error=$3 WHERE id=$1`, id, status, errMsg)
	return err
}
