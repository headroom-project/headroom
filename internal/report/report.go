package report

import (
	"fmt"
	"io"
	"strings"

	"github.com/headroom-project/headroom/internal/rules"
)

var label = map[string]string{
	rules.SeverityCritical: "CRITICAL",
	rules.SeverityWarning:  "WARNING ",
	rules.SeverityInfo:     "INFO    ",
}

// Text writes the human report. The critical finding has to survive being
// pasted into a team chat with no other context, because that is how the
// product spreads.
func Text(w io.Writer, findings []rules.Finding, planPath string) {
	fmt.Fprintf(w, "headroom  %s\n\n", planPath)

	if len(findings) == 0 {
		fmt.Fprintln(w, "No capacity ceiling reached by the workloads in this plan.")
		fmt.Fprintln(w, "Silence means the rules found nothing they could ground, not that the")
		fmt.Fprintln(w, "infrastructure is safe: run with --explain to see what was skipped.")
		return
	}

	var critical, warning int
	for _, f := range findings {
		switch f.Severity {
		case rules.SeverityCritical:
			critical++
		case rules.SeverityWarning:
			warning++
		}
	}

	for i, f := range findings {
		if i > 0 {
			fmt.Fprintln(w)
		}
		suffix := ""
		if f.Instances > 1 {
			suffix = fmt.Sprintf("  (x%d instances)", f.Instances)
		}
		fmt.Fprintf(w, "%s  [%s] %s%s\n", label[f.Severity], f.Rule, f.Title, suffix)
		for _, line := range wrap(f.Summary, 76) {
			fmt.Fprintf(w, "  %s\n", line)
		}
		if len(f.Detail) > 0 {
			fmt.Fprintln(w)
			for _, d := range f.Detail {
				for j, line := range wrap(d, 74) {
					prefix := "    - "
					if j > 0 {
						prefix = "      "
					}
					fmt.Fprintf(w, "%s%s\n", prefix, line)
				}
			}
		}
		fmt.Fprintf(w, "\n  confidence: %s", f.Confidence)
		if f.Source != "" {
			fmt.Fprintf(w, "  |  source: %s", f.Source)
		}
		fmt.Fprintln(w)
	}

	fmt.Fprintf(w, "\n%d critical, %d warning, %d total\n", critical, warning, len(findings))
}

func wrap(s string, width int) []string {
	words := strings.Fields(s)
	if len(words) == 0 {
		return nil
	}
	var lines []string
	line := words[0]
	for _, word := range words[1:] {
		if len(line)+1+len(word) > width {
			lines = append(lines, line)
			line = word
			continue
		}
		line += " " + word
	}
	return append(lines, line)
}
