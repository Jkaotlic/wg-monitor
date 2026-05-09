package upstream

import (
	"strings"

	"golang.org/x/mod/semver"
)

// SoftwareNewerThan returns true if `candidate` is strictly newer than
// `installed` in semver order. Returns false if either input is empty or
// not parseable as semver — false-positive update warnings are worse than
// missed ones.
//
// Leading "v" or "V" is stripped before comparison; the canonical "v" prefix
// required by golang.org/x/mod/semver is added back internally.
func SoftwareNewerThan(installed, candidate string) bool {
	if installed == "" || candidate == "" {
		return false
	}
	i := normalize(installed)
	c := normalize(candidate)
	if !semver.IsValid(i) || !semver.IsValid(c) {
		return false
	}
	return semver.Compare(c, i) > 0
}

func normalize(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "v")
	s = strings.TrimPrefix(s, "V")
	return "v" + s
}

// FirmwareNewerThan compares Keenetic firmware versions. KeeneticOS uses a
// dotted format that is not strict semver: it can include uppercase letter
// segments (e.g. "5.00.C.11.0-0") and arbitrary length. Strategy:
//
//  1. Split both on '.'.
//  2. Compare segments pairwise: numeric vs numeric → integer compare;
//     anything else → byte-wise compare.
//  3. If all shared segments equal, longer wins.
//
// Returns false on empty input.
func FirmwareNewerThan(installed, candidate string) bool {
	if installed == "" || candidate == "" {
		return false
	}
	a := strings.Split(installed, ".")
	b := strings.Split(candidate, ".")
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		if cmp := compareSeg(a[i], b[i]); cmp != 0 {
			return cmp < 0 // a < b means installed < candidate → candidate newer
		}
	}
	return len(b) > len(a)
}

func compareSeg(a, b string) int {
	ai, aerr := parsePosInt(a)
	bi, berr := parsePosInt(b)
	if aerr == nil && berr == nil {
		switch {
		case ai < bi:
			return -1
		case ai > bi:
			return 1
		default:
			return 0
		}
	}
	return strings.Compare(a, b)
}

// parsePosInt is a stripped-down strconv.Atoi that returns an error for
// anything but a non-empty all-digit string. Avoids strconv's wider error
// space and lets compareSeg fall through to lex compare when either side
// has letters or punctuation.
func parsePosInt(s string) (int, error) {
	if s == "" {
		return 0, errNotPosInt
	}
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, errNotPosInt
		}
		n = n*10 + int(r-'0')
	}
	return n, nil
}

var errNotPosInt = newErr("not a positive integer")

type sentinelErr string

func (e sentinelErr) Error() string { return string(e) }
func newErr(s string) error         { return sentinelErr(s) }
