package store

import (
	"context"
	"errors"
	"regexp"

	"github.com/jackc/pgx/v5"
)

const (
	KindRPM = "rpm"
	KindDeb = "deb"
)

var ErrBadDeb = errors.New("deb repo needs a valid suite, at least one component and at least one real architecture (not only 'all')")

var (
	suiteRe = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{0,62}$`)
	wordRe  = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`) // component and architecture names
)

func ValidSuite(s string) bool { return suiteRe.MatchString(s) }

// DebRepo is the apt-specific settings of a deb repo.
type DebRepo struct {
	RepoID             int64
	Suite              string
	Components, Arches []string
}

// normalize fills the default suite and validates d.
func (d DebRepo) normalize(slug string) (DebRepo, error) {
	if d.Suite == "" {
		d.Suite = slug
	}
	if !ValidSuite(d.Suite) || len(d.Components) == 0 {
		return d, ErrBadDeb
	}
	for _, c := range d.Components {
		if !wordRe.MatchString(c) {
			return d, ErrBadDeb
		}
	}
	real := false
	for _, a := range d.Arches {
		if !wordRe.MatchString(a) {
			return d, ErrBadDeb
		}
		real = real || a != "all"
	}
	if !real {
		return d, ErrBadDeb
	}
	return d, nil
}

// Validate reports ErrBadDeb unless d is a usable setting; unlike creation an empty suite is invalid.
func (d DebRepo) Validate() error {
	if d.Suite == "" {
		return ErrBadDeb
	}
	_, err := d.normalize("")
	return err
}

// CreateDebRepo creates a repo of kind deb together with its apt settings, in one transaction.
// An empty d.Suite means the slug.
func (s *Store) CreateDebRepo(ctx context.Context, slug, name string, mode AccessMode, previousID *int64, d DebRepo) (Repo, error) {
	d, err := d.normalize(slug)
	if err != nil {
		return Repo{}, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Repo{}, err
	}
	defer tx.Rollback(ctx)
	r, err := s.createRepo(ctx, tx, KindDeb, slug, name, mode, previousID)
	if err != nil {
		return Repo{}, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO deb_repos(repo_id, suite, components, arches) VALUES($1,$2,$3,$4)`,
		r.ID, d.Suite, nn(d.Components), nn(d.Arches)); err != nil {
		return Repo{}, err
	}
	return r, tx.Commit(ctx)
}

// DebRepoByID returns the apt settings of a deb repo; ErrNotFound for an rpm repo.
func (s *Store) DebRepoByID(ctx context.Context, repoID int64) (DebRepo, error) {
	var d DebRepo
	err := s.pool.QueryRow(ctx, `SELECT repo_id, suite, components, arches FROM deb_repos WHERE repo_id=$1`, repoID).
		Scan(&d.RepoID, &d.Suite, &d.Components, &d.Arches)
	if errors.Is(err, pgx.ErrNoRows) {
		return d, ErrNotFound
	}
	return d, err
}

// SetDebRepo replaces the apt settings of an existing deb repo.
func (s *Store) SetDebRepo(ctx context.Context, repoID int64, d DebRepo) error {
	r, err := s.RepoByID(ctx, repoID)
	if err != nil {
		return err
	}
	d, err = d.normalize(r.Slug)
	if err != nil {
		return err
	}
	tag, err := s.pool.Exec(ctx, `UPDATE deb_repos SET suite=$2, components=$3, arches=$4 WHERE repo_id=$1`,
		repoID, d.Suite, nn(d.Components), nn(d.Arches))
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}
