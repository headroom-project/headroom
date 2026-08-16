package update

import (
	"fmt"
	"strconv"
	"strings"
)

// A small semantic version, and a deliberately strict parser.
//
// Strict is the whole point, and not for tidiness. The release tag is the only
// string in the update notice that is not a constant in this package: it comes
// off a socket, from a server this tool does not control, and it ends up
// printed to somebody's terminal. So the parser is the boundary. It accepts
// digits, dots, hyphens, plus signs and ASCII letters and nothing else, which
// means a tag that parses cannot carry an ANSI escape, a control character, a
// newline or a terminal control sequence into that terminal. Notify prints
// nothing this file has not accepted first.
//
// golang.org/x/mod/semver would do the ordering in one import. It stays out for
// two reasons. The README makes an argument out of this module having a single
// dependency, and more to the point that package answers "which is older" and
// never "is this safe to print", which is the question that actually matters
// here.
type semver struct {
	major, minor, patch int
	pre                 []string // dot separated prerelease identifiers, if any
}

// parseSemver reads vMAJOR.MINOR.PATCH, with an optional prerelease and an
// optional build metadata suffix that is validated and then discarded.
func parseSemver(s string) (semver, error) {
	raw := s
	s = strings.TrimPrefix(s, "v")
	if s == "" {
		return semver{}, fmt.Errorf("version %q has no digits", raw)
	}

	// Build metadata is ignored when ordering versions and this tool has no use
	// for it, but it is still checked, because it is still part of a tag that
	// might get printed.
	if i := strings.IndexByte(s, '+'); i >= 0 {
		if !identsOK(s[i+1:]) {
			return semver{}, fmt.Errorf("version %q has unusable build metadata", raw)
		}
		s = s[:i]
	}

	core := s
	var pre []string
	if i := strings.IndexByte(s, '-'); i >= 0 {
		core = s[:i]
		if !identsOK(s[i+1:]) {
			return semver{}, fmt.Errorf("version %q has an unusable prerelease", raw)
		}
		pre = strings.Split(s[i+1:], ".")
	}

	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return semver{}, fmt.Errorf("version %q is not MAJOR.MINOR.PATCH", raw)
	}
	var n [3]int
	for i, p := range parts {
		if p == "" || !allDigits(p) {
			return semver{}, fmt.Errorf("version %q has a non numeric component %q", raw, p)
		}
		// A tag of five hundred digits is not a version, it is an attempt.
		// Rejecting it here also keeps compare free of overflow.
		v, err := strconv.Atoi(p)
		if err != nil {
			return semver{}, fmt.Errorf("version %q has an unreadable component %q", raw, p)
		}
		n[i] = v
	}
	return semver{major: n[0], minor: n[1], patch: n[2], pre: pre}, nil
}

// compare orders two versions: negative when a is older, zero when they order
// equally, positive when a is newer.
func compare(a, b semver) int {
	if c := cmpInt(a.major, b.major); c != 0 {
		return c
	}
	if c := cmpInt(a.minor, b.minor); c != 0 {
		return c
	}
	if c := cmpInt(a.patch, b.patch); c != 0 {
		return c
	}

	// A prerelease comes before the release it leads to, so v0.2.0 is newer
	// than v0.2.0-rc.1 and v0.2.0-rc.1 is newer than v0.1.9. This is the branch
	// that stops somebody running a release candidate from being told to
	// "update" to the version they are already ahead of.
	switch {
	case len(a.pre) == 0 && len(b.pre) == 0:
		return 0
	case len(a.pre) == 0:
		return 1
	case len(b.pre) == 0:
		return -1
	}
	for i := 0; i < len(a.pre) && i < len(b.pre); i++ {
		if c := cmpIdent(a.pre[i], b.pre[i]); c != 0 {
			return c
		}
	}
	// Equal so far, so the one with more identifiers is the later one:
	// rc.1.1 comes after rc.1.
	return cmpInt(len(a.pre), len(b.pre))
}

// cmpIdent applies the semver rule for prerelease identifiers: an all numeric
// identifier is compared as a number and always sorts below one that is not, so
// rc.11 is newer than rc.2 rather than older by string order.
func cmpIdent(x, y string) int {
	xn, xNum := asNumber(x)
	yn, yNum := asNumber(y)
	switch {
	case xNum && yNum:
		return cmpInt(xn, yn)
	case xNum:
		return -1
	case yNum:
		return 1
	default:
		return strings.Compare(x, y)
	}
}

// asNumber reports an all digit identifier as its value. A long run of digits
// is treated as not a number rather than overflowing, which is the safe answer:
// it falls back to string comparison and orders something, instead of wrapping
// negative and claiming a version is older than it is.
func asNumber(s string) (int, bool) {
	if s == "" || !allDigits(s) || len(s) > 15 {
		return 0, false
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, false
	}
	return n, true
}

// identsOK validates a dot separated list of prerelease or build identifiers.
// Empty is not allowed anywhere: neither "1.0.0-" nor "1.0.0-rc..1".
func identsOK(s string) bool {
	if s == "" {
		return false
	}
	for _, id := range strings.Split(s, ".") {
		if id == "" {
			return false
		}
		for i := 0; i < len(id); i++ {
			c := id[i]
			switch {
			case c >= '0' && c <= '9':
			case c >= 'a' && c <= 'z':
			case c >= 'A' && c <= 'Z':
			case c == '-':
			default:
				return false
			}
		}
	}
	return true
}

func allDigits(s string) bool {
	if len(s) == 0 || len(s) > 15 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

func cmpInt(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}
