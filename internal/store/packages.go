package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

type Package struct {
	ID, RepoID                       int64
	Filename, Name, Version, Release string
	Arch, SHA256                     string
	Size                             int64
	AddedAt                          time.Time
	OriginBatchID                    *int64 // sync batch that brought it in; follows the package through promotion
}

type AuditEvent struct {
	ID                    int64
	RepoID                int64
	Actor, Action, Detail string
	At                    time.Time
}

const pkgCols = `id, repo_id, filename, name, version, release, arch, sha256, size, added_at, origin_batch_id`

func scanPkg(row pgx.Row) (Package, error) {
	var p Package
	err := row.Scan(&p.ID, &p.RepoID, &p.Filename, &p.Name, &p.Version, &p.Release, &p.Arch, &p.SHA256, &p.Size, &p.AddedAt, &p.OriginBatchID)
	if errors.Is(err, pgx.ErrNoRows) {
		return p, ErrNotFound
	}
	return p, err
}

// AddPackage inserts p (RepoID, Filename, metadata, SHA256, Size set); returns ErrExists on a duplicate filename.
func (s *Store) AddPackage(ctx context.Context, p Package) (Package, error) {
	out, err := scanPkg(s.pool.QueryRow(ctx, `
		INSERT INTO packages(repo_id, filename, name, version, release, arch, sha256, size)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8) RETURNING `+pkgCols,
		p.RepoID, p.Filename, p.Name, p.Version, p.Release, p.Arch, p.SHA256, p.Size))
	if pgCode(err) == "23505" {
		return Package{}, ErrExists
	}
	return out, err
}

func (s *Store) ListPackages(ctx context.Context, repoID int64) ([]Package, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+pkgCols+` FROM packages WHERE repo_id=$1 ORDER BY name, added_at DESC, filename`, repoID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Package
	for rows.Next() {
		p, err := scanPkg(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) PackageByFilename(ctx context.Context, repoID int64, filename string) (Package, error) {
	return scanPkg(s.pool.QueryRow(ctx, `SELECT `+pkgCols+` FROM packages WHERE repo_id=$1 AND filename=$2`, repoID, filename))
}

func (s *Store) DeletePackage(ctx context.Context, repoID int64, filename string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM packages WHERE repo_id=$1 AND filename=$2`, repoID, filename)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

func (s *Store) AddAudit(ctx context.Context, repoID int64, actor, action, detail string) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO audit_events(repo_id, actor, action, detail) VALUES($1,$2,$3,$4)`, repoID, actor, action, detail)
	return err
}

// ListAudit returns the newest events first.
func (s *Store) ListAudit(ctx context.Context, repoID int64, limit int) ([]AuditEvent, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, repo_id, actor, action, detail, at FROM audit_events WHERE repo_id=$1 ORDER BY id DESC LIMIT $2`, repoID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AuditEvent
	for rows.Next() {
		var e AuditEvent
		if err := rows.Scan(&e.ID, &e.RepoID, &e.Actor, &e.Action, &e.Detail, &e.At); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// AddPackagesAudited inserts pkgs into repoID and one audit event per package, in one transaction.
// detail[i] is the audit detail for pkgs[i].
func (s *Store) AddPackagesAudited(ctx context.Context, repoID int64, pkgs []Package, actor, action string, detail []string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	for i, p := range pkgs {
		_, err := tx.Exec(ctx, `
			INSERT INTO packages(repo_id, filename, name, version, release, arch, sha256, size, origin_batch_id)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, repoID, p.Filename, p.Name, p.Version, p.Release, p.Arch, p.SHA256, p.Size, p.OriginBatchID)
		if pgCode(err) == "23505" {
			return ErrExists
		}
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO audit_events(repo_id, actor, action, detail) VALUES($1,$2,$3,$4)`, repoID, actor, action, detail[i]); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// AddPackageFirstStage is AddPackage for uploads: it fails with ErrLaterStage unless the
// repo is a first stage, and holds a share lock on the repo row so a concurrent SetPrevious
// cannot link the repo behind another one while the insert is in flight.
func (s *Store) AddPackageFirstStage(ctx context.Context, p Package) (Package, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Package{}, err
	}
	defer tx.Rollback(ctx)
	var prev *int64
	if err := tx.QueryRow(ctx, `SELECT previous_id FROM repos WHERE id=$1 FOR SHARE`, p.RepoID).Scan(&prev); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Package{}, ErrNotFound
		}
		return Package{}, err
	}
	if prev != nil {
		return Package{}, ErrLaterStage
	}
	out, err := scanPkg(tx.QueryRow(ctx, `
		INSERT INTO packages(repo_id, filename, name, version, release, arch, sha256, size)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8) RETURNING `+pkgCols,
		p.RepoID, p.Filename, p.Name, p.Version, p.Release, p.Arch, p.SHA256, p.Size))
	if pgCode(err) == "23505" {
		return Package{}, ErrExists
	}
	if err != nil {
		return Package{}, err
	}
	return out, tx.Commit(ctx)
}
