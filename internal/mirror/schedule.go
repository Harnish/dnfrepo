package mirror

import (
	"errors"
	"fmt"
	"slices"
	"time"
	_ "time/tzdata" // the runtime image (fedora-minimal) may lack /usr/share/zoneinfo

	"dnfrepo/internal/store"
)

// ValidateSchedule checks a schedule before it is stored. A manual schedule
// needs nothing else; the others need a valid HH:MM and a loadable timezone.
func ValidateSchedule(sc store.Schedule) error {
	switch sc.Frequency {
	case "manual":
		return nil
	case "daily", "weekly", "monthly":
	default:
		return fmt.Errorf("unknown frequency %q", sc.Frequency)
	}
	if _, _, err := parseHHMM(sc.TimeOfDay); err != nil {
		return err
	}
	if _, err := time.LoadLocation(sc.Timezone); err != nil || sc.Timezone == "" {
		return fmt.Errorf("unknown timezone %q", sc.Timezone)
	}
	switch sc.Frequency {
	case "weekly":
		if len(sc.Weekdays) == 0 {
			return errors.New("weekly schedule needs at least one weekday")
		}
		for _, d := range sc.Weekdays {
			if d < 0 || d > 6 {
				return fmt.Errorf("weekday %d out of range 0-6", d)
			}
		}
	case "monthly":
		if sc.DayOfMonth < 1 || sc.DayOfMonth > 31 {
			return fmt.Errorf("day of month %d out of range 1-31", sc.DayOfMonth)
		}
	}
	return nil
}

func parseHHMM(s string) (h, m int, err error) {
	t, err := time.Parse("15:04", s)
	if err != nil || len(s) != 5 {
		return 0, 0, fmt.Errorf("time of day %q is not HH:MM", s)
	}
	return t.Hour(), t.Minute(), nil
}

// NextRun returns the first scheduled instant strictly after `after`, or nil
// for a manual schedule. A local time skipped by a DST jump runs at the first
// instant after the gap; a local time that happens twice runs at the first.
func NextRun(sc store.Schedule, after time.Time) (*time.Time, error) {
	if sc.Frequency == "manual" {
		return nil, nil
	}
	if err := ValidateSchedule(sc); err != nil {
		return nil, err
	}
	h, mi, _ := parseHHMM(sc.TimeOfDay)
	loc, _ := time.LoadLocation(sc.Timezone)
	a := after.In(loc)
	for i := 0; i <= 400; i++ {
		day := time.Date(a.Year(), a.Month(), a.Day()+i, 12, 0, 0, 0, time.UTC) // calendar arithmetic only
		y, mo, d := day.Date()
		switch sc.Frequency {
		case "weekly":
			if !slices.Contains(sc.Weekdays, int(day.Weekday())) {
				continue
			}
		case "monthly":
			last := time.Date(y, mo+1, 0, 12, 0, 0, 0, time.UTC).Day()
			if d != min(sc.DayOfMonth, last) {
				continue
			}
		}
		if t := localInstant(y, mo, d, h, mi, loc); t.After(after) {
			return &t, nil
		}
	}
	return nil, errors.New("no run time found within a year")
}

// localInstant is time.Date with DST pinned down: a wall time in a gap
// becomes the instant the gap ends, an ambiguous one its first occurrence.
func localInstant(y int, mo time.Month, d, h, mi int, loc *time.Location) time.Time {
	t := time.Date(y, mo, d, h, mi, 0, 0, loc)
	want := time.Date(y, mo, d, h, mi, 0, 0, time.UTC)
	got := time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), 0, 0, time.UTC)
	start, end := t.ZoneBounds()
	if !got.Equal(want) { // in a gap: Go moved the wall clock to one side of it
		if got.Before(want) {
			return end
		}
		return start
	}
	if !start.IsZero() { // the same wall time an offset earlier, in the previous zone?
		_, prevOff := start.Add(-time.Second).Zone()
		_, off := t.Zone()
		if c := t.Add(-time.Duration(prevOff-off) * time.Second); prevOff > off && c.Before(start) {
			return c
		}
	}
	return t
}
