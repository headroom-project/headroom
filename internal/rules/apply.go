package rules

import (
	"fmt"
	"time"
)

// applyConfig is where the organization's say lands on the output.
//
// Order matters: disable first, then except, then reseverity. A rule that is off
// produces nothing to except, and an exception that has expired must not be
// quietly re-suppressed by a severity change.
func applyConfig(findings []Finding, opt Options) []Finding {
	cfg := opt.Config
	if cfg == nil {
		return findings
	}
	now := opt.Now
	if now.IsZero() {
		now = time.Now()
	}

	out := make([]Finding, 0, len(findings))
	for _, f := range findings {
		if !cfg.RuleEnabled(f.Rule) {
			continue
		}

		exception, expired := cfg.Suppressed(f.Rule, f.Resources, now)
		switch {
		case exception != nil && !expired:
			// Silenced on purpose, by someone who wrote down why.
			continue
		case exception != nil && expired:
			// The suppression stops working and says so, rather than lapsing
			// invisibly. Debt that expires quietly is debt nobody pays.
			f.Detail = append(f.Detail, fmt.Sprintf(
				"An exception for this expired on %s and no longer applies. Reason given at the time: %q.",
				exception.Expires, exception.Reason))
		}

		f.Severity = cfg.SeverityFor(f.Rule, f.Severity)
		out = append(out, f)
	}
	return out
}
