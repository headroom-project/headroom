package update

import "testing"

// The parser is a security boundary before it is a comparator. Whatever it
// accepts, Notify prints to a terminal, and the string came off a socket. These
// two tests are the ones that matter most in this file.

func TestParserRejectsAnythingThatCouldReachATerminal(t *testing.T) {
	hostile := []string{
		"v1.0.0\x1b[2J",              // clear the screen
		"v1.0.0\x1b]0;pwned\x07",     // rewrite the window title
		"v1.0.0\nheadroom: uploaded", // forge a line of our own output
		"v1.0.0\rmasked",             // overwrite the line just printed
		"v1.0.0; rm -rf /",
		"v1.0.0 ",
		"v1.0.0\t",
		"../../../etc/passwd",
		"v1.0.0/../../evil",
		"v1.0.0\x00",
		"v1.0.0‮", // right to left override, reverses what follows
	}
	for _, tag := range hostile {
		if v, err := parseSemver(tag); err == nil {
			t.Errorf("parseSemver(%q) accepted it as %v, and Notify prints what this accepts", tag, v)
		}
	}
}

func TestParserRejectsWhatIsSimplyNotAVersion(t *testing.T) {
	bad := []string{
		"",
		"v",
		"dev", // the value a build from source carries
		"latest",
		"1.0",
		"1.0.0.0",
		"1.0.x",
		"v1.-0.0",
		"-1.0.0",
		"1.0.0-",
		"1.0.0-rc..1",
		"1.0.0+",
		"1.0.0+meta!",
		"9999999999999999999.0.0", // more digits than a version has
	}
	for _, tag := range bad {
		if _, err := parseSemver(tag); err == nil {
			t.Errorf("parseSemver(%q) should have failed", tag)
		}
	}
}

func TestParserAcceptsTheTagsGoReleaserActuallyPublishes(t *testing.T) {
	for _, tag := range []string{"v0.1.0", "0.1.0", "v1.2.3", "v0.1.0-rc.1", "v1.0.0+20260816"} {
		if _, err := parseSemver(tag); err != nil {
			t.Errorf("parseSemver(%q) failed: %v", tag, err)
		}
	}
}

func TestOrdering(t *testing.T) {
	cases := []struct {
		a, b string
		want int // -1 a older, 0 equal, 1 a newer
	}{
		{"v0.1.0", "v0.1.0", 0},
		{"v0.1.0", "0.1.0", 0}, // the v is decoration
		{"v0.1.0", "v0.2.0", -1},
		{"v0.2.0", "v0.1.9", 1},
		{"v0.9.0", "v1.0.0", -1},
		{"v1.0.0", "v0.99.99", 1},
		{"v0.1.1", "v0.1.2", -1},

		// The branch that stops a release candidate being told to update to the
		// version it is already ahead of.
		{"v0.2.0-rc.1", "v0.2.0", -1},
		{"v0.2.0", "v0.2.0-rc.1", 1},
		{"v0.2.0-rc.1", "v0.1.9", 1},

		// Numeric identifiers compare as numbers, so rc.11 is after rc.2. String
		// order would get this backwards and suggest a downgrade.
		{"v1.0.0-rc.2", "v1.0.0-rc.11", -1},
		{"v1.0.0-rc.11", "v1.0.0-rc.2", 1},
		{"v1.0.0-alpha", "v1.0.0-beta", -1},
		{"v1.0.0-alpha", "v1.0.0-alpha.1", -1},
		{"v1.0.0-1", "v1.0.0-alpha", -1}, // numeric ranks below alphanumeric

		// Build metadata is not part of the ordering.
		{"v1.0.0+a", "v1.0.0+b", 0},
	}
	for _, c := range cases {
		a, err := parseSemver(c.a)
		if err != nil {
			t.Fatalf("parseSemver(%q): %v", c.a, err)
		}
		b, err := parseSemver(c.b)
		if err != nil {
			t.Fatalf("parseSemver(%q): %v", c.b, err)
		}
		got := sign(compare(a, b))
		if got != c.want {
			t.Errorf("compare(%s, %s) = %d, want %d", c.a, c.b, got, c.want)
		}
		// Antisymmetry, because a comparator that is not antisymmetric will
		// eventually suggest an update in both directions.
		if back := sign(compare(b, a)); back != -c.want {
			t.Errorf("compare(%s, %s) = %d, want %d", c.b, c.a, back, -c.want)
		}
	}
}

func sign(n int) int {
	switch {
	case n < 0:
		return -1
	case n > 0:
		return 1
	default:
		return 0
	}
}
