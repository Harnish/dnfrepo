package rpmver

import "testing"

func TestCompare(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want int
	}{
		{"1.0", "1.0", 0},
		{"1.0", "2.0", -1},
		{"2.0", "1.0", 1},
		{"1.9", "1.10", -1}, // numeric, not lexical
		{"1.10.0", "1.9.0", 1},
		{"1.0", "1.0.1", -1},
		{"1.0.1", "1.0", 1},
		{"1.0a", "1.0b", -1},
		{"1.0", "1.0a", -1},
		{"1.0~rc1", "1.0", -1}, // tilde sorts before
		{"1.0~rc1", "1.0~rc2", -1},
		{"1.0^git1", "1.0", 1}, // caret sorts after
		{"1.0^git1", "1.0.1", -1},
		{"1.01", "1.1", 0}, // leading zeros ignored
		{"1.2.3", "1.2.3.11029", -1},
		{"10", "9", 1},
		{"1.0a", "1.0.1", -1}, // alpha < numeric
		{"1_0", "1.0", 0},     // separators are equivalent
	} {
		if got := Compare(c.a, c.b); got != c.want {
			t.Errorf("Compare(%q,%q)=%d want %d", c.a, c.b, got, c.want)
		}
		if got := Compare(c.b, c.a); got != -c.want {
			t.Errorf("Compare(%q,%q)=%d want %d (reversed)", c.b, c.a, got, -c.want)
		}
	}
}

func TestComparePackageOrdersVersionThenRelease(t *testing.T) {
	if ComparePackage("1.2.3", "4", "1.2.3", "5") != -1 {
		t.Fatal("release 4 < 5")
	}
	if ComparePackage("1.10.0", "1", "1.9.0", "9") != 1 {
		t.Fatal("version wins over release")
	}
	if ComparePackage("1.0", "1", "1.0", "1") != 0 {
		t.Fatal("equal")
	}
}
