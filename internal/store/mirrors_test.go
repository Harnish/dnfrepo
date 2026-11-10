package store_test

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"dnfrepo/internal/store"
	"dnfrepo/internal/store/storetest"
)

func TestSetMirrorRequiresEmptyFirstStageRepo(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	m := func(id int64) store.Mirror { return store.Mirror{RepoID: id, UpstreamURL: "https://u/"} }

	withPkg := mustRepo(t, s, "withpkg", nil)
	if _, err := s.AddPackage(ctx, store.Package{RepoID: withPkg.ID, Filename: "a-1-1.x86_64.rpm", Name: "a", Version: "1", Release: "1", Arch: "x86_64", SHA256: "x", Size: 1}); err != nil {
		t.Fatal(err)
	}
	withSrc := mustRepo(t, s, "withsrc", nil)
	if _, err := s.AddSource(ctx, withSrc.ID, "github", "o/n"); err != nil {
		t.Fatal(err)
	}
	base := mustRepo(t, s, "base", nil)
	later := mustRepo(t, s, "later", &base.ID)
	for _, id := range []int64{withPkg.ID, withSrc.ID, later.ID} {
		if err := s.SetMirror(ctx, m(id)); !errors.Is(err, store.ErrNotEligible) {
			t.Errorf("repo %d: want ErrNotEligible, got %v", id, err)
		}
	}
	empty := mustRepo(t, s, "empty", nil)
	if err := s.SetMirror(ctx, m(empty.ID)); err != nil {
		t.Fatal(err)
	}
	// update of an existing mirror is fine
	if err := s.SetMirror(ctx, m(empty.ID)); err != nil {
		t.Fatal(err)
	}
	if err := s.SetMirror(ctx, m(9999)); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("missing repo: want ErrNotFound, got %v", err)
	}
}

func TestMirrorRoundTrip(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	r := mustRepo(t, s, "m", nil)
	next := time.Now().Add(time.Hour).UTC().Truncate(time.Microsecond)
	in := store.Mirror{
		RepoID: r.ID, UpstreamURL: "https://u/", Username: "bob", GPGKey: "KEY", TokenEnc: []byte{0, 1, 2, 255},
		RequireSig: true, Arches: []string{"x86_64", "noarch"}, Include: []string{"a*"}, Exclude: []string{"b*", "c*"}, KeepLatest: 3,
		Schedule: store.Schedule{Frequency: "weekly", TimeOfDay: "03:30", Timezone: "UTC", Weekdays: []int{0, 3}, DayOfMonth: 5, Paused: true},
		NextRun:  &next,
	}
	if err := s.SetMirror(ctx, in); err != nil {
		t.Fatal(err)
	}
	got, err := s.MirrorByRepo(ctx, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != "idle" || got.LastRun != nil {
		t.Errorf("state=%q lastrun=%v", got.State, got.LastRun)
	}
	if !bytes.Equal(got.TokenEnc, in.TokenEnc) || got.NextRun == nil || !got.NextRun.Equal(next) {
		t.Errorf("token/next mismatch: %v %v", got.TokenEnc, got.NextRun)
	}
	got.State, got.NextRun, in.NextRun, got.TokenEnc, in.TokenEnc = "", nil, nil, nil, nil
	if !reflect.DeepEqual(got, in) {
		t.Errorf("got %+v\nwant %+v", got, in)
	}
	if ok, _ := s.IsMirror(ctx, r.ID); !ok {
		t.Error("IsMirror false")
	}
	if err := s.DeleteMirror(ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.MirrorByRepo(ctx, r.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("after delete: %v", err)
	}
	if ok, _ := s.IsMirror(ctx, r.ID); ok {
		t.Error("IsMirror true after delete")
	}
}

func TestClaimMirrorIsExclusive(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	r := mustRepo(t, s, "m", nil)
	if err := s.ClaimMirror(ctx, r.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
	if err := s.SetMirror(ctx, store.Mirror{RepoID: r.ID, UpstreamURL: "u"}); err != nil {
		t.Fatal(err)
	}
	if err := s.ClaimMirror(ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.ClaimMirror(ctx, r.ID); !errors.Is(err, store.ErrBusy) {
		t.Fatalf("want ErrBusy, got %v", err)
	}
	next := time.Now().Add(time.Hour).UTC().Truncate(time.Microsecond)
	if err := s.FinishMirror(ctx, r.ID, "error", "boom", &next); err != nil {
		t.Fatal(err)
	}
	m, _ := s.MirrorByRepo(ctx, r.ID)
	if m.State != "idle" || m.LastStatus != "error" || m.LastError != "boom" || m.LastRun == nil || m.NextRun == nil || !m.NextRun.Equal(next) {
		t.Errorf("after finish: %+v", m)
	}
	if err := s.ClaimMirror(ctx, r.ID); err != nil {
		t.Fatalf("reclaim: %v", err)
	}
}

func TestClaimDueMirrorSkipsPausedAndFuture(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	now := time.Now()
	past, future := now.Add(-time.Minute), now.Add(time.Hour)
	mk := func(slug string, next *time.Time, paused bool) int64 {
		r := mustRepo(t, s, slug, nil)
		if err := s.SetMirror(ctx, store.Mirror{RepoID: r.ID, UpstreamURL: "u", NextRun: next, Schedule: store.Schedule{Frequency: "daily", Paused: paused}}); err != nil {
			t.Fatal(err)
		}
		return r.ID
	}
	mk("paused", &past, true)
	mk("future", &future, false)
	mk("never", nil, false)
	due := mk("due", &past, false)
	m, err := s.ClaimDueMirror(ctx, now)
	if err != nil || m == nil || m.RepoID != due || m.State != "running" {
		t.Fatalf("got %+v, %v", m, err)
	}
	if m, err := s.ClaimDueMirror(ctx, now); err != nil || m != nil {
		t.Fatalf("second claim: %+v, %v", m, err)
	}
}

func TestInterruptRunning(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	r := mustRepo(t, s, "m", nil)
	s.SetMirror(ctx, store.Mirror{RepoID: r.ID, UpstreamURL: "u"})
	if err := s.ClaimMirror(ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	n, err := s.InterruptRunning(ctx)
	if err != nil || n != 1 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	m, _ := s.MirrorByRepo(ctx, r.ID)
	if m.State != "idle" || m.LastStatus != "interrupted" {
		t.Errorf("%+v", m)
	}
}

func TestMirrorRepoRejectsSourceAndLinking(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	base := mustRepo(t, s, "base", nil)
	r := mustRepo(t, s, "m", nil)
	if err := s.SetMirror(ctx, store.Mirror{RepoID: r.ID, UpstreamURL: "u"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddSource(ctx, r.ID, "github", "o/n"); !errors.Is(err, store.ErrMirrorRepo) {
		t.Errorf("AddSource: %v", err)
	}
	if err := s.SetPrevious(ctx, r.ID, &base.ID); !errors.Is(err, store.ErrMirrorRepo) {
		t.Errorf("SetPrevious: %v", err)
	}
}
