package mirror

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"dnfrepo/internal/store"
)

func daily(next *time.Time) func(*store.Mirror) {
	return func(m *store.Mirror) {
		m.Schedule = store.Schedule{Frequency: "daily", TimeOfDay: "03:00", Timezone: "UTC"}
		m.NextRun = next
	}
}

func ago(d time.Duration) *time.Time { t := time.Now().Add(-d); return &t }

func (e *syncEnv) audit(action string) []store.AuditEvent {
	e.t.Helper()
	ev, err := e.st.ListAudit(context.Background(), e.repo.ID, 100)
	if err != nil {
		e.t.Fatal(err)
	}
	var out []store.AuditEvent
	for _, a := range ev {
		if a.Action == action {
			out = append(out, a)
		}
	}
	return out
}

func TestStartMarksInterruptedRunsStopped(t *testing.T) {
	e := newSyncEnv(t, fakeOpts{}, nil)
	if err := e.st.ClaimMirror(context.Background(), e.repo.ID); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	e.s.Start(ctx)
	cancel()
	e.s.wg.Wait()
	if m := e.mirror(); m.State != "idle" || m.LastStatus != "interrupted" {
		t.Fatalf("state=%q status=%q", m.State, m.LastStatus)
	}
}

func TestDueMirrorRunsOnceNotPerMissedSlot(t *testing.T) {
	e := newSyncEnv(t, fakeOpts{}, daily(ago(72*time.Hour))) // three daily slots missed
	e.add("hello-1.2.3-4.noarch")
	ctx := context.Background()
	e.s.claimDue(ctx)
	e.s.wg.Wait()
	m := e.mirror()
	if m.LastStatus != "synced" || m.LastRun == nil || m.State != "idle" {
		t.Fatalf("mirror=%+v", m)
	}
	if m.NextRun == nil || !m.NextRun.After(time.Now()) || m.NextRun.After(time.Now().Add(24*time.Hour)) {
		t.Fatalf("next run %v not the next slot from now", m.NextRun)
	}
	first := *m.LastRun
	e.s.claimDue(ctx)
	e.s.wg.Wait()
	if m := e.mirror(); !m.LastRun.Equal(first) {
		t.Errorf("ran again at %v", m.LastRun)
	}
	if got := e.audit("synced"); len(got) != 1 || got[0].Actor != "mirror:m" {
		t.Errorf("synced audit=%+v", got)
	}
}

func TestStartRunsDueMirror(t *testing.T) {
	e := newSyncEnv(t, fakeOpts{}, daily(ago(time.Minute)))
	e.add("hello-1.2.3-4.noarch")
	e.s.Tick = 10 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	e.s.Start(ctx)
	deadline := time.Now().Add(10 * time.Second)
	for e.mirror().LastRun == nil && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	e.s.wg.Wait()
	if m := e.mirror(); m.LastStatus != "synced" {
		t.Fatalf("mirror=%+v", m)
	}
}

func TestPausedMirrorNeverRuns(t *testing.T) {
	e := newSyncEnv(t, fakeOpts{}, func(m *store.Mirror) {
		daily(ago(time.Hour))(m)
		m.Schedule.Paused = true
	})
	e.add("hello-1.2.3-4.noarch")
	e.s.claimDue(context.Background())
	e.s.wg.Wait()
	if m := e.mirror(); m.LastRun != nil || m.State != "idle" {
		t.Fatalf("paused mirror ran: %+v", m)
	}
}

func TestTriggerWhileRunningReturnsErrBusy(t *testing.T) {
	e := newSyncEnv(t, fakeOpts{}, daily(nil))
	e.add("hello-1.2.3-4.noarch")
	entered, release := make(chan struct{}), make(chan struct{})
	e.s.Client = &http.Client{Transport: rtFunc(func(r *http.Request) (*http.Response, error) {
		if strings.HasSuffix(r.URL.Path, "repomd.xml") {
			close(entered)
			<-release
		}
		return http.DefaultTransport.RoundTrip(r)
	})}
	reqCtx, cancelReq := context.WithCancel(context.Background())
	if err := e.s.Trigger(reqCtx, e.repo.ID, "mirror:m"); err != nil {
		t.Fatal(err)
	}
	<-entered
	cancelReq() // the request ending must not cancel the sync
	if err := e.s.Trigger(context.Background(), e.repo.ID, "mirror:m"); !errors.Is(err, store.ErrBusy) {
		t.Fatalf("second trigger: %v", err)
	}
	close(release)
	e.s.wg.Wait()
	m := e.mirror()
	if m.State != "idle" || m.LastStatus != "synced" {
		t.Fatalf("mirror=%+v", m)
	}
	if m.NextRun == nil || !m.NextRun.After(time.Now()) {
		t.Errorf("next run %v", m.NextRun)
	}
	if err := e.s.Trigger(context.Background(), e.repo.ID+1000, "x"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("unknown repo: %v", err)
	}
}

func TestTriggerFailureWritesSyncFailedAudit(t *testing.T) {
	e := newSyncEnv(t, fakeOpts{}, nil)
	c := e.add("hello-1.2.3-4.noarch")
	e.up.Drop(c.Href)
	if err := e.s.Trigger(context.Background(), e.repo.ID, "mirror:m"); err != nil {
		t.Fatal(err)
	}
	e.s.wg.Wait()
	e.assertUnchanged()
	got := e.audit("sync-failed")
	if len(got) != 1 || got[0].Actor != "mirror:m" || got[0].Detail == "" {
		t.Fatalf("audit=%+v", got)
	}
}

func TestTriggerAppliesTimeout(t *testing.T) {
	e := newSyncEnv(t, fakeOpts{}, nil)
	e.add("hello-1.2.3-4.noarch")
	e.s.Timeout = time.Nanosecond
	if err := e.s.Trigger(context.Background(), e.repo.ID, "mirror:m"); err != nil {
		t.Fatal(err)
	}
	e.s.wg.Wait()
	e.assertUnchanged()
	if m := e.mirror(); !strings.Contains(m.LastError, "deadline") {
		t.Errorf("last error %q", m.LastError)
	}
}

func TestTriggerRecoversPanic(t *testing.T) {
	e := newSyncEnv(t, fakeOpts{}, daily(nil))
	e.add("hello-1.2.3-4.noarch")
	e.s.Client = &http.Client{Transport: rtFunc(func(r *http.Request) (*http.Response, error) { panic("boom") })}
	if err := e.s.Trigger(context.Background(), e.repo.ID, "mirror:m"); err != nil {
		t.Fatal(err)
	}
	e.s.wg.Wait()
	m := e.mirror()
	if m.State != "idle" || m.LastStatus != "error" || m.LastError == "" || m.NextRun == nil {
		t.Fatalf("mirror=%+v", m)
	}
	if got := e.audit("sync-failed"); len(got) != 1 {
		t.Errorf("audit=%+v", got)
	}
}

func TestRunStoresNextRun(t *testing.T) {
	e := newSyncEnv(t, fakeOpts{}, daily(nil))
	if _, err := e.run(context.Background()); err != nil {
		t.Fatal(err)
	}
	m := e.mirror()
	want, _ := NextRun(m.Schedule, time.Now())
	if m.NextRun == nil || m.NextRun.Sub(*want).Abs() > time.Minute {
		t.Fatalf("next run %v want %v", m.NextRun, want)
	}
}

func TestBadScheduleKeepsMirrorScheduled(t *testing.T) {
	// a schedule NextRun cannot use must not null next_run, or the mirror never runs again
	e := newSyncEnv(t, fakeOpts{}, func(m *store.Mirror) {
		daily(nil)(m)
		m.Schedule.Timezone = "Mars/Olympus"
	})
	if _, err := e.run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if m := e.mirror(); m.NextRun == nil || !m.NextRun.After(time.Now()) {
		t.Fatalf("next run %v", m.NextRun)
	}
}

func TestManualMirrorHasNoNextRun(t *testing.T) {
	e := newSyncEnv(t, fakeOpts{}, nil)
	if _, err := e.run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if m := e.mirror(); m.NextRun != nil {
		t.Fatalf("next run %v", m.NextRun)
	}
}
