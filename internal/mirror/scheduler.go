package mirror

import (
	"context"
	"fmt"
	"log"
	"time"
)

const (
	defaultTimeout = 6 * time.Hour
	defaultTick    = time.Minute
)

// Trigger claims the mirror (store.ErrBusy if a sync is running, store.ErrNotFound
// if the repo is not a mirror) and runs the sync in the background, detached
// from ctx so the request ending does not cancel it.
func (s *Syncer) Trigger(ctx context.Context, repoID int64, actor string) error {
	if err := s.Store.ClaimMirror(ctx, repoID); err != nil {
		return err
	}
	s.spawn(repoID, actor)
	return nil
}

// Start resets runs orphaned by a restart, then claims and starts due mirrors
// now and every Tick until ctx is done. It does not block.
func (s *Syncer) Start(ctx context.Context) {
	if n, err := s.Store.InterruptRunning(ctx); err != nil {
		log.Printf("mirror: reset interrupted syncs: %v", err)
	} else if n > 0 {
		log.Printf("mirror: %d sync(s) interrupted by restart", n)
	}
	tick := s.Tick
	if tick <= 0 {
		tick = defaultTick
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		t := time.NewTicker(tick)
		defer t.Stop()
		for {
			s.claimDue(ctx)
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
}

// claimDue starts every due mirror. A mirror that missed several slots is
// claimed once; its next run is then computed from the time it finishes.
func (s *Syncer) claimDue(ctx context.Context) {
	for ctx.Err() == nil {
		m, err := s.Store.ClaimDueMirror(ctx, time.Now())
		if err != nil {
			log.Printf("mirror: claim due: %v", err)
			return
		}
		if m == nil {
			return
		}
		actor := fmt.Sprintf("mirror:%d", m.RepoID)
		if r, err := s.Store.RepoByID(ctx, m.RepoID); err == nil {
			actor = "mirror:" + r.Slug
		}
		s.spawn(m.RepoID, actor)
	}
}

// spawn runs a claimed mirror's sync in its own goroutine and timeout.
func (s *Syncer) spawn(repoID int64, actor string) {
	d := s.Timeout
	if d <= 0 {
		d = defaultTimeout
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		ctx, cancel := context.WithTimeout(context.Background(), d)
		defer cancel()
		defer func() {
			if r := recover(); r != nil {
				log.Printf("mirror: repo %d: sync panicked: %v", repoID, r)
				bctx, cancel := context.WithTimeout(context.Background(), bookkeepingTimeout)
				defer cancel()
				msg := "internal error: sync panicked"
				if err := s.Store.FinishMirror(bctx, repoID, "error", msg, s.nextRun(bctx, repoID)); err != nil {
					log.Printf("mirror: repo %d: finish: %v", repoID, err)
				}
				s.auditFailed(bctx, repoID, actor, msg)
			}
		}()
		res, err := s.Run(ctx, repoID, actor)
		if err != nil && res.Status != "synced" { // a committed sync already has its synced events
			bctx, cancel := context.WithTimeout(context.Background(), bookkeepingTimeout)
			defer cancel()
			s.auditFailed(bctx, repoID, actor, err.Error()) // Run's error is already scrubbed
		}
	}()
}

func (s *Syncer) auditFailed(ctx context.Context, repoID int64, actor, msg string) {
	if err := s.Store.AddAudit(ctx, repoID, actor, "sync-failed", msg); err != nil {
		log.Printf("mirror: repo %d: audit: %v", repoID, err)
	}
}

// nextRun is the mirror's next scheduled run from now, nil for manual or on error.
func (s *Syncer) nextRun(ctx context.Context, repoID int64) *time.Time {
	m, err := s.Store.MirrorByRepo(ctx, repoID)
	if err != nil {
		return nil
	}
	n, err := NextRun(m.Schedule, time.Now())
	if err != nil {
		log.Printf("mirror: repo %d: schedule: %v", repoID, err)
	}
	return n
}
