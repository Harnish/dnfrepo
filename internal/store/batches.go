package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

type Batch struct {
	ID, RepoID int64
	StartedAt  time.Time
	FinishedAt *time.Time
	Status     string
	AddedCount int
	Error      string
}

func (s *Store) CreateBatch(ctx context.Context, repoID int64) (int64, error) {
	var id int64
	err := s.pool.QueryRow(ctx, `INSERT INTO sync_batches(repo_id) VALUES($1) RETURNING id`, repoID).Scan(&id)
	return id, err
}

func (s *Store) FinishBatch(ctx context.Context, id int64, status string, added int, errMsg string) error {
	tag, err := s.pool.Exec(ctx, `UPDATE sync_batches SET finished_at=now(), status=$2, added_count=$3, error=$4 WHERE id=$1`, id, status, added, errMsg)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// ListBatches returns the newest batches first, at most 100.
func (s *Store) ListBatches(ctx context.Context, repoID int64) ([]Batch, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, repo_id, started_at, finished_at, status, added_count, error FROM sync_batches WHERE repo_id=$1 ORDER BY id DESC LIMIT 100`, repoID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Batch
	for rows.Next() {
		var b Batch
		if err := rows.Scan(&b.ID, &b.RepoID, &b.StartedAt, &b.FinishedAt, &b.Status, &b.AddedCount, &b.Error); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// BatchFilenames lists the packages in repoID that originated from batchID, sorted.
func (s *Store) BatchFilenames(ctx context.Context, repoID, batchID int64) ([]string, error) {
	rows, err := s.pool.Query(ctx, `SELECT filename FROM packages WHERE repo_id=$1 AND origin_batch_id=$2 ORDER BY filename`, repoID, batchID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var f string
		if err := rows.Scan(&f); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// AddMirrorPackages is the mirror-sync insert path (it does not refuse mirror repos): in one
// transaction it checks repoID is a first stage (ErrLaterStage), inserts pkgs with
// origin_batch_id=batchID and writes one "synced" audit event each. ErrExists on a duplicate filename.
func (s *Store) AddMirrorPackages(ctx context.Context, repoID, batchID int64, pkgs []Package, actor string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var prev *int64
	if err := tx.QueryRow(ctx, `SELECT previous_id FROM repos WHERE id=$1 FOR SHARE`, repoID).Scan(&prev); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if prev != nil {
		return ErrLaterStage
	}
	for _, p := range pkgs {
		_, err := tx.Exec(ctx, `
			INSERT INTO packages(repo_id, filename, name, version, release, arch, sha256, size, origin_batch_id)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, repoID, p.Filename, p.Name, p.Version, p.Release, p.Arch, p.SHA256, p.Size, batchID)
		if pgCode(err) == "23505" {
			return ErrExists
		}
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO audit_events(repo_id, actor, action, detail) VALUES($1,$2,'synced',$3)`, repoID, actor, p.Filename); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
