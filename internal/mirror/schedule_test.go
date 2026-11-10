package mirror

import (
	"testing"
	"time"

	"dnfrepo/internal/store"
)

func mustLoc(t *testing.T, name string) *time.Location {
	t.Helper()
	l, err := time.LoadLocation(name)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func next(t *testing.T, sc store.Schedule, after time.Time) time.Time {
	t.Helper()
	n, err := NextRun(sc, after)
	if err != nil || n == nil {
		t.Fatalf("NextRun: %v %v", n, err)
	}
	if !n.After(after) {
		t.Fatalf("next %v not after %v", n, after)
	}
	return *n
}

func TestNextRunDaily(t *testing.T) {
	sc := store.Schedule{Frequency: "daily", TimeOfDay: "03:30", Timezone: "UTC"}
	cases := []struct{ after, want time.Time }{
		{time.Date(2026, 3, 1, 1, 0, 0, 0, time.UTC), time.Date(2026, 3, 1, 3, 30, 0, 0, time.UTC)},
		{time.Date(2026, 3, 1, 3, 30, 0, 0, time.UTC), time.Date(2026, 3, 2, 3, 30, 0, 0, time.UTC)}, // strictly after
		{time.Date(2026, 12, 31, 5, 0, 0, 0, time.UTC), time.Date(2027, 1, 1, 3, 30, 0, 0, time.UTC)},
	}
	for _, c := range cases {
		if got := next(t, sc, c.after); !got.Equal(c.want) {
			t.Errorf("after %v: got %v want %v", c.after, got, c.want)
		}
	}
	// the time of day is local to the schedule's zone
	ny := mustLoc(t, "America/New_York")
	sc.Timezone = "America/New_York"
	if got, want := next(t, sc, time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)), time.Date(2026, 7, 1, 3, 30, 0, 0, ny); !got.Equal(want) {
		t.Errorf("got %v want %v", got, want)
	}
}

func TestNextRunWeeklyPicksNextListedWeekday(t *testing.T) {
	// 2026-03-04 is a Wednesday
	sc := store.Schedule{Frequency: "weekly", TimeOfDay: "12:00", Timezone: "UTC", Weekdays: []int{1, 5}} // Mon, Fri
	if got, want := next(t, sc, time.Date(2026, 3, 4, 0, 0, 0, 0, time.UTC)), time.Date(2026, 3, 6, 12, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Errorf("got %v want %v", got, want)
	}
	if got, want := next(t, sc, time.Date(2026, 3, 6, 12, 0, 0, 0, time.UTC)), time.Date(2026, 3, 9, 12, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Errorf("got %v want %v", got, want)
	}
	sc.Weekdays = []int{0} // Sunday
	if got, want := next(t, sc, time.Date(2026, 3, 4, 0, 0, 0, 0, time.UTC)), time.Date(2026, 3, 8, 12, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Errorf("got %v want %v", got, want)
	}
}

func TestNextRunMonthly(t *testing.T) {
	sc := store.Schedule{Frequency: "monthly", TimeOfDay: "06:15", Timezone: "UTC", DayOfMonth: 10}
	if got, want := next(t, sc, time.Date(2026, 3, 4, 0, 0, 0, 0, time.UTC)), time.Date(2026, 3, 10, 6, 15, 0, 0, time.UTC); !got.Equal(want) {
		t.Errorf("got %v want %v", got, want)
	}
	if got, want := next(t, sc, time.Date(2026, 3, 10, 6, 15, 0, 0, time.UTC)), time.Date(2026, 4, 10, 6, 15, 0, 0, time.UTC); !got.Equal(want) {
		t.Errorf("got %v want %v", got, want)
	}
	if got, want := next(t, sc, time.Date(2026, 12, 20, 0, 0, 0, 0, time.UTC)), time.Date(2027, 1, 10, 6, 15, 0, 0, time.UTC); !got.Equal(want) {
		t.Errorf("got %v want %v", got, want)
	}
}

func TestNextRunMonthlyDay31InShortMonthUsesLastDay(t *testing.T) {
	sc := store.Schedule{Frequency: "monthly", TimeOfDay: "00:00", Timezone: "UTC", DayOfMonth: 31}
	at := time.Date(2026, 1, 31, 0, 0, 0, 0, time.UTC)
	want := []time.Time{
		time.Date(2026, 2, 28, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 4, 30, 0, 0, 0, 0, time.UTC),
	}
	for _, w := range want {
		at = next(t, sc, at)
		if !at.Equal(w) {
			t.Fatalf("got %v want %v", at, w)
		}
	}
	// leap year
	if got, w := next(t, sc, time.Date(2028, 2, 1, 0, 0, 0, 0, time.UTC)), time.Date(2028, 2, 29, 0, 0, 0, 0, time.UTC); !got.Equal(w) {
		t.Errorf("got %v want %v", got, w)
	}
}

func TestNextRunNonexistentLocalTimeSpringForward(t *testing.T) {
	// 2026-03-08 America/New_York: 02:00 EST jumps to 03:00 EDT, so 02:30 does not exist.
	ny := mustLoc(t, "America/New_York")
	sc := store.Schedule{Frequency: "daily", TimeOfDay: "02:30", Timezone: "America/New_York"}
	got := next(t, sc, time.Date(2026, 3, 8, 0, 0, 0, 0, ny))
	if want := time.Date(2026, 3, 8, 7, 0, 0, 0, time.UTC); !got.Equal(want) { // 03:00 EDT, the first valid instant
		t.Fatalf("got %v want %v", got, want)
	}
	// runs once that day: the next is the following day at 02:30 EDT
	if got2, want := next(t, sc, got), time.Date(2026, 3, 9, 2, 30, 0, 0, ny); !got2.Equal(want) {
		t.Fatalf("got %v want %v", got2, want)
	}
	// east of UTC: 2026-03-29 Europe/Berlin 02:00 CET jumps to 03:00 CEST
	sc.Timezone = "Europe/Berlin"
	if got, want := next(t, sc, time.Date(2026, 3, 28, 12, 0, 0, 0, time.UTC)), time.Date(2026, 3, 29, 1, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Fatalf("berlin: got %v want %v", got, want)
	}
}

func TestNextRunAmbiguousLocalTimeFallBackRunsOnce(t *testing.T) {
	// 2026-11-01 America/New_York: 02:00 EDT falls back to 01:00 EST, so 01:30 happens twice.
	ny := mustLoc(t, "America/New_York")
	sc := store.Schedule{Frequency: "daily", TimeOfDay: "01:30", Timezone: "America/New_York"}
	first := time.Date(2026, 11, 1, 5, 30, 0, 0, time.UTC) // 01:30 EDT
	got := next(t, sc, time.Date(2026, 11, 1, 0, 0, 0, 0, ny))
	if !got.Equal(first) {
		t.Fatalf("got %v want first occurrence %v", got, first)
	}
	want := time.Date(2026, 11, 2, 1, 30, 0, 0, ny)
	for _, after := range []time.Time{got, got.Add(30 * time.Minute), first.Add(time.Hour)} { // after the first, during, after the second
		if n := next(t, sc, after); !n.Equal(want) {
			t.Errorf("after %v: got %v want %v", after, n, want)
		}
	}
	// east of UTC (time.Date alone picks the second occurrence here):
	// 2026-10-25 Europe/Berlin 03:00 CEST falls back to 02:00 CET.
	sc = store.Schedule{Frequency: "daily", TimeOfDay: "02:30", Timezone: "Europe/Berlin"}
	if got, want := next(t, sc, time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)), time.Date(2026, 10, 25, 0, 30, 0, 0, time.UTC); !got.Equal(want) {
		t.Fatalf("berlin: got %v want first occurrence %v", got, want)
	}
	if got, want := next(t, sc, time.Date(2026, 10, 25, 0, 30, 0, 0, time.UTC)), time.Date(2026, 10, 26, 1, 30, 0, 0, time.UTC); !got.Equal(want) {
		t.Fatalf("berlin: got %v want %v (once)", got, want)
	}
}

func TestNextRunManualIsNil(t *testing.T) {
	n, err := NextRun(store.Schedule{Frequency: "manual"}, time.Now())
	if err != nil || n != nil {
		t.Fatalf("got %v %v", n, err)
	}
}

func TestValidateScheduleRejects(t *testing.T) {
	ok := []store.Schedule{
		{Frequency: "manual"},
		{Frequency: "daily", TimeOfDay: "23:59", Timezone: "Europe/Berlin"},
		{Frequency: "weekly", TimeOfDay: "00:00", Timezone: "UTC", Weekdays: []int{0, 6}},
		{Frequency: "monthly", TimeOfDay: "12:00", Timezone: "UTC", DayOfMonth: 31},
	}
	for _, sc := range ok {
		if err := ValidateSchedule(sc); err != nil {
			t.Errorf("%+v: %v", sc, err)
		}
	}
	bad := []store.Schedule{
		{Frequency: "hourly", TimeOfDay: "00:00", Timezone: "UTC"},
		{Frequency: "daily", TimeOfDay: "00:00", Timezone: "Mars/Olympus"},
		{Frequency: "daily", TimeOfDay: "25:00", Timezone: "UTC"},
		{Frequency: "daily", TimeOfDay: "12:60", Timezone: "UTC"},
		{Frequency: "daily", TimeOfDay: "", Timezone: "UTC"},
		{Frequency: "weekly", TimeOfDay: "00:00", Timezone: "UTC"},
		{Frequency: "weekly", TimeOfDay: "00:00", Timezone: "UTC", Weekdays: []int{7}},
		{Frequency: "weekly", TimeOfDay: "00:00", Timezone: "UTC", Weekdays: []int{-1}},
		{Frequency: "monthly", TimeOfDay: "00:00", Timezone: "UTC", DayOfMonth: 0},
		{Frequency: "monthly", TimeOfDay: "00:00", Timezone: "UTC", DayOfMonth: 32},
	}
	for _, sc := range bad {
		if err := ValidateSchedule(sc); err == nil {
			t.Errorf("%+v: want error", sc)
		}
	}
}
