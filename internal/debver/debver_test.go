package debver

import "testing"

func TestCompare(t *testing.T) {
	// each row: a is older than b
	older := [][2]string{
		{"1.0", "1.1"},
		{"1.0-1", "1.0-2"},
		{"2.0", "1:1.0"},   // epoch wins
		{"1.0~rc1", "1.0"}, // tilde sorts before the end
		{"1.0~rc1", "1.0~rc2"},
		{"1.0", "1.0+b1"}, // plus sorts after the end
		{"1.0", "1.0a"},   // letter after end
		{"1.0a", "1.0.1"}, // letters sort before non-letters
		{"1.9", "1.10"},   // numeric runs compare as numbers
		{"1.0-1", "1.0-1+b1"},
		{"1.0-1~bpo1", "1.0-1"},
		{"2.0-1", "2.0-1ubuntu1"},
		{"1.0-1", "1.0-ubuntu1"}, // letter after digit run beats the digit
		{"1.2.3", "1.2.x"},
		{"1.0+2020", "1.0+git2020"},
		{"1.0", "1.0-1"}, // missing revision is "0"; "0" < "1"
	}
	for _, p := range older {
		if got := Compare(p[0], p[1]); got != -1 {
			t.Errorf("Compare(%q,%q)=%d, want -1", p[0], p[1], got)
		}
		if got := Compare(p[1], p[0]); got != 1 {
			t.Errorf("Compare(%q,%q)=%d, want 1", p[1], p[0], got)
		}
	}
	equal := [][2]string{{"2.0", "2.0-0"}, {"0:1.0", "1.0"}, {"1.0-1", "1.0-1"}, {"1.00", "1.0"}}
	for _, p := range equal {
		if got := Compare(p[0], p[1]); got != 0 {
			t.Errorf("Compare(%q,%q)=%d, want 0", p[0], p[1], got)
		}
	}
}
