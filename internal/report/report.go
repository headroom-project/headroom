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
//
// noColor forces plain output. When it is false the decision is still made by
// wantColour, which honours NO_COLOR and refuses to write escapes into a pipe.
func Text(w io.Writer, findings []rules.Finding, planPath string, noColor bool) {
	write(w, findings, planPath, wantColour(w, noColor))
}

// Coloured writes the same report with the colour decision handed in rather
// than read off the environment.
//
// It exists for a caller that is not a process. The WebAssembly build has no
// terminal, no NO_COLOR and no pipe to inspect, and it wants the escapes
// because the page it draws into renders them. Making that caller set an
// environment variable to reach the coloured path would put a global mutation
// in the middle of a library, and it would leave the colour of a report
// depending on the order two callers ran in.
//
// Text and Coloured share one body, so the bytes they emit for the same
// decision cannot drift apart.
func Coloured(w io.Writer, findings []rules.Finding, planPath string, colour bool) {
	write(w, findings, planPath, colour)
}

func write(w io.Writer, findings []rules.Finding, planPath string, colour bool) {
	p := palette{on: colour}

	fmt.Fprintf(w, "%s  %s\n\n", p.s(styBold, "headroom"), p.s(styDim, planPath))

	if len(findings) == 0 {
		fmt.Fprintln(w, "No capacity ceiling reached by the workloads in this plan.")
		fmt.Fprintln(w, p.s(styDim, "Silence means the rules found nothing they could ground, not that the"))
		fmt.Fprintln(w, p.s(styDim, "infrastructure is safe: run with --explain to see what was skipped."))
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
			suffix = p.s(styDim, fmt.Sprintf("  (x%d instances)", f.Instances))
		}
		fmt.Fprintf(w, "%s  %s %s%s\n",
			p.severity(f.Severity, label[f.Severity]),
			p.s(styDim, "["+f.Rule+"]"),
			p.s(styBold, f.Title),
			suffix)

		for _, line := range wrap(f.Summary, 76) {
			fmt.Fprintf(w, "  %s\n", p.tokens(line))
		}
		if len(f.Detail) > 0 {
			fmt.Fprintln(w)
			for _, d := range f.Detail {
				for j, line := range wrap(d, 74) {
					prefix := "    - "
					if j > 0 {
						prefix = "      "
					}
					fmt.Fprintf(w, "%s%s\n", prefix, p.tokens(line))
				}
			}
		}
		fmt.Fprintf(w, "\n  %s", p.s(styDim, "confidence: "+f.Confidence))
		if f.Source != "" {
			fmt.Fprintf(w, "%s", p.s(styDim, "  |  source: "+f.Source))
		}
		fmt.Fprintln(w)
	}

	fmt.Fprintf(w, "\n%s, %s, %s\n",
		p.severity(rules.SeverityCritical, fmt.Sprintf("%d critical", critical)),
		p.severity(rules.SeverityWarning, fmt.Sprintf("%d warning", warning)),
		p.s(styBold, fmt.Sprintf("%d total", len(findings))))
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
