package pkgver

import (
	"strconv"
	"strings"
	"unicode"
)

// Compare orders pkg-style versions "[version][_revision][,epoch]".
// It returns -1, 0 or 1. It follows pkg-version(8) closely for the common
// cases (dotted numbers, letter suffixes, alpha/beta/pre/rc/pl markers) but is
// not a byte-for-byte port; use it only to pick the newest of several files.
func Compare(a, b string) int {
	va, ra, ea := splitVersion(a)
	vb, rb, eb := splitVersion(b)
	if c := cmpInt(ea, eb); c != 0 {
		return c
	}
	if c := cmpVer(va, vb); c != 0 {
		return c
	}
	return cmpInt(ra, rb)
}

func splitVersion(v string) (ver string, rev, epoch int) {
	if i := strings.LastIndexByte(v, ','); i >= 0 {
		epoch, _ = strconv.Atoi(v[i+1:])
		v = v[:i]
	}
	if i := strings.LastIndexByte(v, '_'); i >= 0 {
		if n, err := strconv.Atoi(v[i+1:]); err == nil {
			rev = n
			v = v[:i]
		}
	}
	return v, rev, epoch
}

func cmpInt(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

type vtok struct {
	num  int
	str  string
	isNo bool
}

func tokenize(v string) []vtok {
	var out []vtok
	for i := 0; i < len(v); {
		c := rune(v[i])
		switch {
		case unicode.IsDigit(c):
			j := i
			for j < len(v) && unicode.IsDigit(rune(v[j])) {
				j++
			}
			n, _ := strconv.Atoi(v[i:j])
			out = append(out, vtok{num: n, isNo: true})
			i = j
		case unicode.IsLetter(c):
			j := i
			for j < len(v) && unicode.IsLetter(rune(v[j])) {
				j++
			}
			out = append(out, vtok{str: strings.ToLower(v[i:j])})
			i = j
		default:
			i++
		}
	}
	return out
}

func strRank(s string) int {
	switch s {
	case "alpha":
		return -4
	case "beta":
		return -3
	case "pre", "rc":
		return -2
	case "pl", "p":
		return 1
	}
	return 0
}

func cmpVer(a, b string) int {
	ta, tb := tokenize(a), tokenize(b)
	for i := 0; i < len(ta) || i < len(tb); i++ {
		var x, y vtok
		x.isNo, y.isNo = true, true // missing = 0
		if i < len(ta) {
			x = ta[i]
		}
		if i < len(tb) {
			y = tb[i]
		}
		switch {
		case x.isNo && y.isNo:
			if c := cmpInt(x.num, y.num); c != 0 {
				return c
			}
		case !x.isNo && !y.isNo:
			rx, ry := strRank(x.str), strRank(y.str)
			if rx != ry {
				return cmpInt(rx, ry)
			}
			if c := strings.Compare(x.str, y.str); c != 0 {
				return c
			}
		case x.isNo: // number vs letters
			if r := strRank(y.str); r < 0 {
				return 1
			}
			return -1
		default:
			if r := strRank(x.str); r < 0 {
				return -1
			}
			return 1
		}
	}
	return 0
}
