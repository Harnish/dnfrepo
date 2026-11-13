// Package debver compares Debian package versions the way dpkg does (Policy 5.6.12).
package debver

import "strings"

// Compare returns -1, 0, or 1 as a is older than, equal to, or newer than b.
// Versions look like [epoch:]upstream[-revision].
func Compare(a, b string) int {
	ea, ua, ra := split(a)
	eb, ub, rb := split(b)
	if c := compareNum(ea, eb); c != 0 {
		return c
	}
	if c := compareStr(ua, ub); c != 0 {
		return c
	}
	return compareStr(ra, rb)
}

// split cuts a version into epoch, upstream and revision (epoch "0" and revision "" when absent).
func split(v string) (epoch, upstream, rev string) {
	epoch = "0"
	if i := strings.IndexByte(v, ':'); i >= 0 {
		epoch, v = v[:i], v[i+1:]
	}
	if i := strings.LastIndexByte(v, '-'); i >= 0 {
		return epoch, v[:i], v[i+1:]
	}
	return epoch, v, ""
}

func compareNum(a, b string) int {
	a, b = strings.TrimLeft(a, "0"), strings.TrimLeft(b, "0")
	if len(a) != len(b) {
		if len(a) < len(b) {
			return -1
		}
		return 1
	}
	return strings.Compare(a, b)
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }
func isAlpha(c byte) bool { return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') }

// order ranks one non-digit byte: '~' before the end, the end, letters, then everything else.
func order(s string, i int) int {
	if i >= len(s) {
		return 0
	}
	c := s[i]
	switch {
	case isDigit(c):
		return 0 // as dpkg: a digit ends the non-digit run, same rank as the end
	case c == '~':
		return -1
	case isAlpha(c):
		return int(c)
	default:
		return int(c) + 256
	}
}

// compareStr is dpkg's verrevcmp: alternating non-digit and digit runs.
func compareStr(a, b string) int {
	i, j := 0, 0
	for i < len(a) || j < len(b) {
		for (i < len(a) && !isDigit(a[i])) || (j < len(b) && !isDigit(b[j])) {
			oa, ob := order(a, i), order(b, j)
			if oa != ob {
				if oa < ob {
					return -1
				}
				return 1
			}
			i++
			j++
		}
		si := i
		for i < len(a) && isDigit(a[i]) {
			i++
		}
		sj := j
		for j < len(b) && isDigit(b[j]) {
			j++
		}
		if c := compareNum(a[si:i], b[sj:j]); c != 0 {
			return c
		}
	}
	return 0
}
