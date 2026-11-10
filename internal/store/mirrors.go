package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

var (
	ErrMirrorRepo  = errors.New("repo is a mirror") // uploads, sources, linking refused
	ErrNotEligible = errors.New("only an empty first-stage repo with no sources can become a mirror")
	ErrBusy        = errors.New("sync already running")
)

// Schedule: Frequency is "manual", "daily", "weekly" or "monthly"; TimeOfDay is "HH:MM"; Weekdays 0=Sunday.
type Schedule struct {
	Frequency, TimeOfDay, Timezone string
	Weekdays                       []int
	DayOfMonth                     int
	Paused                         bool
}

type Mirror struct {
	RepoID                   int64
	UpstreamURL, Username    string
	TokenEnc                 []byte
	GPGKey                   string
	RequireSig               bool
	Arches, Include, Exclude []string
	KeepLatest               int
	Schedule                 Schedule
	State                    string // "idle" or "running"
	LastStatus, LastError    string
	LastRun, NextRun         *time.Time
}

const mirrorCols = `repo_id, upstream_url, username, token_enc, gpg_key, require_sig, arches, include_pat, exclude_pat,
	keep_latest, frequency, time_of_day, timezone, weekdays, day_of_month, paused, state, last_run, last_status, last_error, next_run`

func scanMirror(row pgx.Row) (Mirror, error) {
	var m Mirror
	err := row.Scan(&m.RepoID, &m.UpstreamURL, &m.Username, &m.TokenEnc, &m.GPGKey, &m.RequireSig, &m.Arches, &m.Include, &m.Exclude,
		&m.KeepLatest, &m.Schedule.Frequency, &m.Schedule.TimeOfDay, &m.Schedule.Timezone, &m.Schedule.Weekdays, &m.Schedule.DayOfMonth,
		&m.Schedule.Paused, &m.State, &m.LastRun, &m.LastStatus, &m.LastError, &m.NextRun)
	if errors.Is(err, pgx.ErrNoRows) {
		return m, ErrNotFound
	}
	return m, err
}

// nn keeps nil slices from becoming SQL NULL (columns are NOT NULL).
func nn[T any](v []T) []T {
	if v == nil {
		return []T{}
	}
	return v
}

// SetMirror upserts the config fields (never state or last_*). Creating a mirror requires an
// empty first-stage repo with no sources; the repo row is locked FOR UPDATE so a concurrent
// upload, AddSource or SetPrevious cannot slip content in meanwhile.
func (s *Store) SetMirror(ctx context.Context, m Mirror) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var prev *int64
	if err := tx.QueryRow(ctx, `SELECT previous_id FROM repos WHERE id=$1 FOR UPDATE`, m.RepoID).Scan(&prev); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM mirrors WHERE repo_id=$1)`, m.RepoID).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		var n int
		if err := tx.QueryRow(ctx, `SELECT (SELECT count(*) FROM packages WHERE repo_id=$1) + (SELECT count(*) FROM sources WHERE repo_id=$1)`, m.RepoID).Scan(&n); err != nil {
			return err
		}
		if prev != nil || n > 0 {
			return ErrNotEligible
		}
	}
	sc := m.Schedule
	if _, err := tx.Exec(ctx, `
		INSERT INTO mirrors(repo_id, upstream_url, username, token_enc, gpg_key, require_sig, arches, include_pat, exclude_pat,
			keep_latest, frequency, time_of_day, timezone, weekdays, day_of_month, paused, next_run)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)
		ON CONFLICT (repo_id) DO UPDATE SET upstream_url=$2, username=$3, token_enc=$4, gpg_key=$5, require_sig=$6, arches=$7,
			include_pat=$8, exclude_pat=$9, keep_latest=$10, frequency=$11, time_of_day=$12, timezone=$13, weekdays=$14,
			day_of_month=$15, paused=$16, next_run=$17`,
		m.RepoID, m.UpstreamURL, m.Username, m.TokenEnc, m.GPGKey, m.RequireSig, nn(m.Arches), nn(m.Include), nn(m.Exclude),
		m.KeepLatest, sc.Frequency, sc.TimeOfDay, sc.Timezone, nn(sc.Weekdays), sc.DayOfMonth, sc.Paused, m.NextRun); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) MirrorByRepo(ctx context.Context, repoID int64) (Mirror, error) {
	return scanMirror(s.pool.QueryRow(ctx, `SELECT `+mirrorCols+` FROM mirrors WHERE repo_id=$1`, repoID))
}

// DeleteMirror removes an idle mirror's config: ErrBusy while a sync runs, ErrNotFound if not a mirror.
func (s *Store) DeleteMirror(ctx context.Context, repoID int64) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM mirrors WHERE repo_id=$1 AND state='idle'`, repoID)
	if err != nil || tag.RowsAffected() == 1 {
		return err
	}
	if ok, err := s.IsMirror(ctx, repoID); err != nil {
		return err
	} else if !ok {
		return ErrNotFound
	}
	return ErrBusy
}

func (s *Store) IsMirror(ctx context.Context, repoID int64) (bool, error) {
	var ok bool
	err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM mirrors WHERE repo_id=$1)`, repoID).Scan(&ok)
	return ok, err
}

// ClaimMirror moves idle to running: ErrBusy if already running, ErrNotFound if not a mirror.
func (s *Store) ClaimMirror(ctx context.Context, repoID int64) error {
	tag, err := s.pool.Exec(ctx, `UPDATE mirrors SET state='running' WHERE repo_id=$1 AND state='idle'`, repoID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 1 {
		return nil
	}
	if ok, err := s.IsMirror(ctx, repoID); err != nil {
		return err
	} else if !ok {
		return ErrNotFound
	}
	return ErrBusy
}

// ClaimDueMirror atomically claims one idle, unpaused, scheduled (non-manual) mirror whose
// next_run is due or missing (a lost next_run self-heals by running now); nil if none.
func (s *Store) ClaimDueMirror(ctx context.Context, now time.Time) (*Mirror, error) {
	m, err := scanMirror(s.pool.QueryRow(ctx, `
		UPDATE mirrors SET state='running' WHERE repo_id=(
			SELECT repo_id FROM mirrors WHERE state='idle' AND NOT paused AND frequency<>'manual' AND (next_run IS NULL OR next_run<=$1)
			ORDER BY next_run NULLS FIRST LIMIT 1 FOR UPDATE SKIP LOCKED)
		RETURNING `+mirrorCols, now))
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &m, nil
}

// FinishMirror ends a run: running to idle, last_run=now().
func (s *Store) FinishMirror(ctx context.Context, repoID int64, status, errMsg string, next *time.Time) error {
	tag, err := s.pool.Exec(ctx, `UPDATE mirrors SET state='idle', last_run=now(), last_status=$2, last_error=$3, next_run=$4 WHERE repo_id=$1`,
		repoID, status, errMsg, next)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// InterruptRunning resets runs orphaned by a restart and returns how many there were.
func (s *Store) InterruptRunning(ctx context.Context) (int, error) {
	tag, err := s.pool.Exec(ctx, `UPDATE mirrors SET state='idle', last_status='interrupted' WHERE state='running'`)
	return int(tag.RowsAffected()), err
}
