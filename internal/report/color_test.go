package report

import (
	"bytes"
	"regexp"
	"strings"
	"testing"

	"github.com/headroom-project/headroom/internal/rules"
)

var sample = []rules.Finding{
	{
		Rule:       "R1",
		Severity:   rules.SeverityCritical,
		Title:      "Scale asymmetry: application outgrows the database",
		Summary:    "At full scale the workloads in front of aws_db_instance.main open ~800 connections against a ceiling of ~450. Saturation lands at 56% of the scale this plan already authorises.",
		Detail:     []string{"aws_db_instance.main (db.t3.medium, postgres) accepts ~450 connections by default"},
		Confidence: "high",
		Source:     "https://docs.aws.amazon.com/AmazonRDS/latest/UserGuide/CHAP_Limits.html",
	},
	{
		Rule:       "R6",
		Severity:   rules.SeverityWarning,
		Title:      "The tier in front grows, the one behind cannot",
		Summary:    "aws_ecs_service.api scales from 2 to 40, a 20x range.",
		Confidence: "high",
	},
}

var reAnsi = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func render(t *testing.T, noColor bool, env map[string]string) string {
	t.Helper()
	for k, v := range env {
		t.Setenv(k, v)
	}
	var b bytes.Buffer
	Text(&b, sample, "fixtures/01-ecs-rds/plan.json", noColor)
	return b.String()
}

// A bytes.Buffer is not a character device, so the report must come out plain
// even with nothing else asked for. This is the case that matters most: it is
// what a redirect to a file and a pipe into grep both look like, and escape
// codes landing in a CI log would be the visible regression.
func TestNotATerminalMeansNoColour(t *testing.T) {
	out := render(t, false, nil)
	if strings.Contains(out, "\x1b[") {
		t.Fatalf("escape codes written to a non-terminal writer:\n%q", out[:200])
	}
}

// FORCE_COLOR is the only way to get escapes out of a buffer, which is what
// makes the rest of these tests possible at all.
func TestForceColourEmitsEscapes(t *testing.T) {
	out := render(t, false, map[string]string{"FORCE_COLOR": "1"})
	if !strings.Contains(out, "\x1b[") {
		t.Fatal("FORCE_COLOR produced no escapes")
	}
}

// The no-color.org convention: set to anything at all, including the empty
// string, means off. And it beats FORCE_COLOR, because a user who has opted out
// globally should not be overridden by a variable a script happened to export.
func TestNoColorEnvWinsOverForceColor(t *testing.T) {
	for _, v := range []string{"", "1", "0", "false"} {
		out := render(t, false, map[string]string{"NO_COLOR": v, "FORCE_COLOR": "1"})
		if strings.Contains(out, "\x1b[") {
			t.Errorf("NO_COLOR=%q still produced escapes", v)
		}
	}
}

// The flag is the last word, because an explicit argument beats an inherited
// environment.
func TestFlagWinsOverEverything(t *testing.T) {
	out := render(t, true, map[string]string{"FORCE_COLOR": "1"})
	if strings.Contains(out, "\x1b[") {
		t.Fatal("--no-color still produced escapes")
	}
}

// The load-bearing property. Colour must never be the only carrier of meaning:
// stripping every escape has to leave exactly the report that would have been
// printed with colour off. If these two ever diverge, something is being said
// in colour alone, and a person piping through `sed` loses it.
func TestStrippingEscapesYieldsThePlainReport(t *testing.T) {
	plain := render(t, true, nil)
	coloured := render(t, false, map[string]string{"FORCE_COLOR": "1"})

	if got := reAnsi.ReplaceAllString(coloured, ""); got != plain {
		t.Errorf("stripped colour output differs from plain output\n--- plain ---\n%s\n--- stripped ---\n%s", plain, got)
	}
}

// Severity is the one distinction colour is allowed to make, so it has to
// actually make it: critical and warning must not share a colour, or the report
// is painted rather than informative.
func TestSeveritiesUseDifferentColours(t *testing.T) {
	out := render(t, false, map[string]string{"FORCE_COLOR": "1"})

	crit := colourOf(t, out, "CRITICAL")
	warn := colourOf(t, out, "WARNING ")
	if crit == "" || warn == "" {
		t.Fatalf("a severity label carries no colour: critical=%q warning=%q", crit, warn)
	}
	if crit == warn {
		t.Errorf("critical and warning are both %q", crit)
	}
	if crit != styRed {
		t.Errorf("critical is %q, want red", crit)
	}
	if warn != styYellow {
		t.Errorf("warning is %q, want yellow", warn)
	}
}

// An escape that opens and never closes bleeds into everything printed after
// it, including the user's own shell prompt.
func TestEveryEscapeIsClosed(t *testing.T) {
	out := render(t, false, map[string]string{"FORCE_COLOR": "1"})
	// A reset is itself an escape, so a balanced report has exactly twice as
	// many escape starts as resets. Comparing starts against resets directly
	// would be counting every reset on both sides of the equation.
	if starts, resets := strings.Count(out, "\x1b["), strings.Count(out, ansiReset); starts != resets*2 {
		t.Errorf("%d escape starts against %d resets: %d opened and never closed",
			starts, resets, starts-resets*2)
	}
	if !strings.HasSuffix(reAnsi.ReplaceAllString(out, ""), "\n") {
		t.Error("the report does not end with a newline")
	}
}

// Wrapping happens before colouring, and wrap only ever breaks at a space, so a
// coloured token can never straddle a line. This asserts the property rather
// than the implementation: no line may contain an unclosed escape.
func TestNoLineCarriesAnUnclosedEscape(t *testing.T) {
	out := render(t, false, map[string]string{"FORCE_COLOR": "1"})
	for i, line := range strings.Split(out, "\n") {
		if starts, resets := strings.Count(line, "\x1b["), strings.Count(line, ansiReset); starts != resets*2 {
			t.Errorf("line %d has an escape crossing the newline: %q", i+1, line)
		}
	}
}

// A version inside an address must not be picked up as a measurement: matching
// addresses first is what prevents "db.t3.medium" turning into "db.t" plus a
// bolded 3.
func TestAddressIsColouredWholeAndNotSplitByTheNumberRule(t *testing.T) {
	out := render(t, false, map[string]string{"FORCE_COLOR": "1"})
	if !strings.Contains(out, styIdent+"db.t3.medium"+ansiReset) {
		t.Error("db.t3.medium was not coloured as a single identifier")
	}
	if !strings.Contains(out, styIdent+"aws_db_instance.main"+ansiReset) {
		t.Error("aws_db_instance.main was not coloured as an identifier")
	}
}

// The numbers are the product, so they carry weight. ~800 and 56% both count.
func TestMeasurementsAreEmphasised(t *testing.T) {
	out := render(t, false, map[string]string{"FORCE_COLOR": "1"})
	for _, want := range []string{styBold + "~800" + ansiReset, styBold + "56%" + ansiReset} {
		if !strings.Contains(out, want) {
			t.Errorf("measurement not emphasised: %q", want)
		}
	}
}

// An empty report is the common case in a healthy repository and it must not
// come out painted.
func TestEmptyReportStaysQuiet(t *testing.T) {
	t.Setenv("FORCE_COLOR", "1")
	var b bytes.Buffer
	Text(&b, nil, "plan.json", false)
	if strings.Contains(b.String(), styRed) || strings.Contains(b.String(), styYellow) {
		t.Error("a report with no findings used a severity colour")
	}
}

// colourOf returns the escape immediately preceding text, or "".
func colourOf(t *testing.T, out, text string) string {
	t.Helper()
	i := strings.Index(out, text)
	if i < 0 {
		t.Fatalf("%q not found in the report", text)
	}
	prefix := out[:i]
	j := strings.LastIndex(prefix, "\x1b[")
	if j < 0 {
		return ""
	}
	esc := prefix[j:]
	if !strings.HasSuffix(esc, "m") {
		return ""
	}
	return esc
}
