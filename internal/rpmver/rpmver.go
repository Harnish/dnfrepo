// Package rpmver compares rpm version strings the way rpm does (rpmvercmp).
package rpmver

func isDigit(c byte) bool { return c >= '0' && c <= '9' }
func isAlpha(c byte) bool { return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') }
func isAlnum(c byte) bool { return isDigit(c) || isAlpha(c) }

// Compare returns -1, 0, or 1 as a is older than, equal to, or newer than b.
func Compare(a, b string) int {
	if a == b {
		return 0
	}
	i, j := 0, 0
	for i < len(a) || j < len(b) {
		for i < len(a) && !isAlnum(a[i]) && a[i] != '~' && a[i] != '^' {
			i++
		}
		for j < len(b) && !isAlnum(b[j]) && b[j] != '~' && b[j] != '^' {
			j++
		}
		// tilde sorts before everything, even the end of the string
		if (i < len(a) && a[i] == '~') || (j < len(b) && b[j] == '~') {
			if i >= len(a) || a[i] != '~' {
				return 1
			}
			if j >= len(b) || b[j] != '~' {
				return -1
			}
			i++
			j++
			continue
		}
		// caret sorts after the end of the string but before anything else
		if (i < len(a) && a[i] == '^') || (j < len(b) && b[j] == '^') {
			if i >= len(a) {
				return -1
			}
			if j >= len(b) {
				return 1
			}
			if a[i] != '^' {
				return 1
			}
			if b[j] != '^' {
				return -1
			}
			i++
			j++
			continue
		}
		if i >= len(a) || j >= len(b) {
			break
		}
		si, sj := i, j
		numeric := isDigit(a[i])
		if numeric {
			for i < len(a) && isDigit(a[i]) {
				i++
			}
			for j < len(b) && isDigit(b[j]) {
				j++
			}
		} else {
			for i < len(a) && isAlpha(a[i]) {
				i++
			}
			for j < len(b) && isAlpha(b[j]) {
				j++
			}
		}
		segA, segB := a[si:i], b[sj:j]
		if segB == "" { // segments of different kinds: numeric is newer than alpha
			if numeric {
				return 1
			}
			return -1
		}
		if numeric {
			for len(segA) > 0 && segA[0] == '0' {
				segA = segA[1:]
			}
			for len(segB) > 0 && segB[0] == '0' {
				segB = segB[1:]
			}
			if len(segA) != len(segB) {
				if len(segA) > len(segB) {
					return 1
				}
				return -1
			}
		}
		if segA != segB {
			if segA > segB {
				return 1
			}
			return -1
		}
	}
	switch {
	case i >= len(a) && j >= len(b):
		return 0
	case i < len(a):
		return 1
	default:
		return -1
	}
}

// ComparePackage orders two builds of a package by version, then release (epoch is not tracked).
func ComparePackage(verA, relA, verB, relB string) int {
	if c := Compare(verA, verB); c != 0 {
		return c
	}
	return Compare(relA, relB)
}
