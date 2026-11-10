package store

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"regexp"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type AccessMode string

const (
	ModePublic AccessMode = "public"
	ModeIPs    AccessMode = "ips"
	ModeUsers  AccessMode = "users"
)

func (m AccessMode) Valid() bool { return m == ModePublic || m == ModeIPs || m == ModeUsers }

var (
	ErrNotFound = errors.New("not found")
	ErrBadSlug  = errors.New("slug must match ^[a-z0-9][a-z0-9-]{0,62}$")
	ErrChain    = errors.New("invalid pipeline link")
	ErrInChain  = errors.New("repo is part of a pipeline; unlink it first")
	ErrExists   = errors.New("already exists")
	ErrBadKeep  = errors.New("keep limits must be >= 0")

	ErrHasContent = errors.New("repo already has packages or sources; only an empty repo can be linked behind another")
	ErrLaterStage = errors.New("repo is a later pipeline stage")
)

var slugRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

func ValidSlug(s string) bool { return slugRe.MatchString(s) }

type Repo struct {
	ID         int64
	Slug, Name string
	Mode       AccessMode
	PreviousID *int64
	Keep       int // default max versions kept per package name+arch; 0 = unlimited
}

const repoCols = `id, slug, name, mode, previous_id, keep`

func scanRepo(row pgx.Row) (Repo, error) {
	var r Repo
	var mode string
	err := row.Scan(&r.ID, &r.Slug, &r.Name, &mode, &r.PreviousID, &r.Keep)
	r.Mode = AccessMode(mode)
	if errors.Is(err, pgx.ErrNoRows) {
		return r, ErrNotFound
	}
	return r, err
}

func pgCode(err error) string {
	var pe *pgconn.PgError
	if errors.As(err, &pe) {
		return pe.Code
	}
	return ""
}

func (s *Store) CreateRepo(ctx context.Context, slug, name string, mode AccessMode, previousID *int64) (Repo, error) {
	if !ValidSlug(slug) {
		return Repo{}, ErrBadSlug
	}
	if !mode.Valid() {
		return Repo{}, fmt.Errorf("invalid mode %q", mode)
	}
	r, err := scanRepo(s.pool.QueryRow(ctx,
		`INSERT INTO repos(slug,name,mode,previous_id) VALUES($1,$2,$3,$4) RETURNING `+repoCols, slug, name, string(mode), previousID))
	switch pgCode(err) {
	case "23505": // unique: slug taken, or previous already has a next
		if previousID != nil {
			var n int
			s.pool.QueryRow(ctx, `SELECT count(*) FROM repos WHERE slug=$1`, slug).Scan(&n)
			if n == 0 {
				return Repo{}, ErrChain
			}
		}
		return Repo{}, ErrExists
	case "23503": // previous does not exist
		return Repo{}, ErrChain
	}
	return r, err
}

func (s *Store) RepoBySlug(ctx context.Context, slug string) (Repo, error) {
	return scanRepo(s.pool.QueryRow(ctx, `SELECT `+repoCols+` FROM repos WHERE slug=$1`, slug))
}

func (s *Store) RepoByID(ctx context.Context, id int64) (Repo, error) {
	return scanRepo(s.pool.QueryRow(ctx, `SELECT `+repoCols+` FROM repos WHERE id=$1`, id))
}

func (s *Store) ListRepos(ctx context.Context) ([]Repo, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+repoCols+` FROM repos ORDER BY slug`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Repo
	for rows.Next() {
		r, err := scanRepo(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// NextRepo returns the repo promoted into from id, or nil at the end of a chain.
func (s *Store) NextRepo(ctx context.Context, id int64) (*Repo, error) {
	r, err := scanRepo(s.pool.QueryRow(ctx, `SELECT `+repoCols+` FROM repos WHERE previous_id=$1`, id))
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}

func (s *Store) UpdateRepo(ctx context.Context, id int64, name string, mode AccessMode) error {
	if !mode.Valid() {
		return fmt.Errorf("invalid mode %q", mode)
	}
	tag, err := s.pool.Exec(ctx, `UPDATE repos SET name=$2, mode=$3 WHERE id=$1`, id, name, string(mode))
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// SetPrevious links (or, with nil, unlinks) a repo into a pipeline. Linking is allowed
// only for a repo with no packages and no sources, so nothing can become a later-stage
// package without having been promoted. All link changes are serialized.
func (s *Store) SetPrevious(ctx context.Context, id int64, previousID *int64) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(7001)`); err != nil {
		return err
	}
	var locked int64
	if err := tx.QueryRow(ctx, `SELECT id FROM repos WHERE id=$1 FOR UPDATE`, id).Scan(&locked); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if previousID != nil {
		var mirror bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM mirrors WHERE repo_id=$1)`, id).Scan(&mirror); err != nil {
			return err
		}
		if mirror {
			return ErrMirrorRepo
		}
		var n int
		if err := tx.QueryRow(ctx, `SELECT (SELECT count(*) FROM packages WHERE repo_id=$1) + (SELECT count(*) FROM sources WHERE repo_id=$1)`, id).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return ErrHasContent
		}
		// walk up from the candidate; reaching id means a cycle
		for cur := previousID; cur != nil; {
			if *cur == id {
				return ErrChain
			}
			var up *int64
			if err := tx.QueryRow(ctx, `SELECT previous_id FROM repos WHERE id=$1`, *cur).Scan(&up); err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					return ErrChain
				}
				return err
			}
			cur = up
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE repos SET previous_id=$2 WHERE id=$1`, id, previousID); err != nil {
		if pgCode(err) == "23505" {
			return ErrChain
		}
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) SetRepoIPs(ctx context.Context, id int64, prefixes []netip.Prefix) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `DELETE FROM repo_ips WHERE repo_id=$1`, id); err != nil {
		return err
	}
	for _, p := range prefixes {
		if _, err := tx.Exec(ctx, `INSERT INTO repo_ips(repo_id,prefix) VALUES($1,$2) ON CONFLICT DO NOTHING`, id, p.Masked().String()); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (s *Store) RepoIPs(ctx context.Context, id int64) ([]netip.Prefix, error) {
	rows, err := s.pool.Query(ctx, `SELECT prefix::text FROM repo_ips WHERE repo_id=$1 ORDER BY prefix`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []netip.Prefix
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return nil, err
		}
		p, err := netip.ParsePrefix(t)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// DeleteRepo removes a repo that is not linked into any pipeline.
func (s *Store) DeleteRepo(ctx context.Context, id int64) error {
	r, err := s.RepoByID(ctx, id)
	if err != nil {
		return err
	}
	next, err := s.NextRepo(ctx, id)
	if err != nil {
		return err
	}
	if r.PreviousID != nil || next != nil {
		return ErrInChain
	}
	_, err = s.pool.Exec(ctx, `DELETE FROM repos WHERE id=$1`, id)
	return err
}

// SetRepoKeep replaces a repo's retention limits: keep is the default number of versions kept
// per package name and arch (0 = unlimited); overrides maps a package name to its own limit.
func (s *Store) SetRepoKeep(ctx context.Context, id int64, keep int, overrides map[string]int) error {
	if keep < 0 {
		return ErrBadKeep
	}
	for _, v := range overrides {
		if v < 0 {
			return ErrBadKeep
		}
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, `UPDATE repos SET keep=$2 WHERE id=$1`, id, keep)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	if _, err := tx.Exec(ctx, `DELETE FROM repo_keep_overrides WHERE repo_id=$1`, id); err != nil {
		return err
	}
	for pkg, v := range overrides {
		if _, err := tx.Exec(ctx, `INSERT INTO repo_keep_overrides(repo_id, package, keep) VALUES($1,$2,$3)`, id, pkg, v); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (s *Store) RepoKeep(ctx context.Context, id int64) (int, map[string]int, error) {
	var keep int
	if err := s.pool.QueryRow(ctx, `SELECT keep FROM repos WHERE id=$1`, id).Scan(&keep); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, nil, ErrNotFound
		}
		return 0, nil, err
	}
	rows, err := s.pool.Query(ctx, `SELECT package, keep FROM repo_keep_overrides WHERE repo_id=$1`, id)
	if err != nil {
		return 0, nil, err
	}
	defer rows.Close()
	ov := map[string]int{}
	for rows.Next() {
		var p string
		var k int
		if err := rows.Scan(&p, &k); err != nil {
			return 0, nil, err
		}
		ov[p] = k
	}
	return keep, ov, rows.Err()
}
