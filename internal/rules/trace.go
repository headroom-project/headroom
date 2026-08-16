package rules

import "sort"

// A rule that stays quiet is making a claim, and until now it made it without
// evidence. Two very different things printed the same blank report: "this
// infrastructure has headroom" and "I could not ground a single number".
//
// Seven of seven real production plans came back empty during the run that
// produced this file, and every one of them was the second kind. The report
// told those users to run --explain, which did not exist.
//
// Trace is what makes the second kind visible. Rules record why they let a
// resource go, and nothing else about the analysis changes: this is a
// description of the run, never an input to it.

// Skip is one resource a rule looked at and could not use.
type Skip struct {
	Rule     string
	Resource string
	Reason   string
}

// Trace collects skips and, after Run, how many findings each rule produced. A
// nil Trace is valid and records nothing, so no rule needs to check.
type Trace struct {
	skips []Skip
	found map[string]int
	ran   bool
}

func NewTrace() *Trace { return &Trace{found: map[string]int{}} }

// Skip records that a rule could not use a resource, and why. The reason is
// written for the person reading the report, so it says what was missing rather
// than which branch was taken.
func (t *Trace) Skip(rule, resource, reason string) {
	if t == nil {
		return
	}
	t.skips = append(t.skips, Skip{Rule: rule, Resource: resource, Reason: reason})
}

func (t *Trace) Skips() []Skip {
	if t == nil {
		return nil
	}
	return t.skips
}

// Findings is how many findings each rule produced, which is the other half of
// the picture: a rule that fired and also skipped something is a different
// story from a rule that skipped everything.
func (t *Trace) Findings() map[string]int {
	if t == nil {
		return nil
	}
	return t.found
}

// Rules lists every rule the trace knows about, whether it fired or skipped.
func (t *Trace) Rules() []string {
	if t == nil {
		return nil
	}
	seen := map[string]bool{}
	for r := range t.found {
		seen[r] = true
	}
	for _, s := range t.skips {
		seen[s.Rule] = true
	}
	out := make([]string, 0, len(seen))
	for r := range seen {
		out = append(out, r)
	}
	sort.Strings(out)
	return out
}

// Ran reports whether a trace was attached to the run at all, so the caller can
// tell "nothing was skipped" from "nobody was recording".
func (t *Trace) Ran() bool { return t != nil && t.ran }

func (t *Trace) record(findings []Finding) {
	if t == nil {
		return
	}
	t.ran = true
	for _, f := range findings {
		t.found[f.Rule]++
	}
}

// quoteOrNone renders a value that may be missing, so a skip reason never reads
// "the size  has no encoded ceiling" when the plan simply did not state one.
func quoteOrNone(v string) string {
	if v == "" {
		return "(none stated in the plan)"
	}
	return `"` + v + `"`
}
