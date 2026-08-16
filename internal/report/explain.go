package report

import (
	"fmt"
	"io"
	"sort"

	"github.com/headroom-project/headroom/internal/rules"
)

// Explain writes what each rule did with this plan: what it reported, what it
// looked at and could not use, and why.
//
// It exists because the empty report has always been ambiguous. "No capacity
// ceiling reached" and "no rule could ground a single number" print the same
// blank page, and in a run against seven real production plans every one of the
// seven was the second kind. The report told the reader to run --explain, and
// --explain did not exist, which is the worst of both: a promise of an answer
// and no way to get it.
//
// This is a description of the analysis, not part of it. Nothing here changes a
// verdict, and it goes to stderr so a pipeline reading the report on stdout is
// unaffected.
func Explain(w io.Writer, t *rules.Trace, findings []rules.Finding) {
	if !t.Ran() {
		return
	}

	fmt.Fprintln(w, "\nexplain: what each rule did with this plan")

	byRule := map[string][]rules.Skip{}
	for _, s := range t.Skips() {
		byRule[s.Rule] = append(byRule[s.Rule], s)
	}
	found := t.Findings()

	names := t.Rules()
	if len(names) == 0 {
		fmt.Fprintln(w, "\n  No rule found anything it recognises in this plan. That is not a")
		fmt.Fprintln(w, "  verdict on the infrastructure: it means nothing here is of a resource")
		fmt.Fprintln(w, "  type any rule looks at.")
		return
	}

	for _, rule := range names {
		fmt.Fprintf(w, "\n%s  %s\n", rule, plural(found[rule], "finding", "findings"))

		skips := byRule[rule]
		if len(skips) == 0 {
			if found[rule] == 0 {
				fmt.Fprintln(w, "    Nothing recorded. The rule ran and had nothing to say about this")
				fmt.Fprintln(w, "    plan, and it did not say why: that is a gap in this report, not")
				fmt.Fprintln(w, "    evidence that the plan is clean.")
			}
			continue
		}

		sort.SliceStable(skips, func(i, j int) bool { return skips[i].Resource < skips[j].Resource })
		for _, s := range skips {
			fmt.Fprintf(w, "    %s\n", s.Resource)
			fmt.Fprintf(w, "      %s\n", s.Reason)
		}
	}

	if len(findings) == 0 {
		fmt.Fprintln(w, "\n  Nothing was reported. Every line above is a resource a rule looked at")
		fmt.Fprintln(w, "  and let go, and a reason that says whether the resource has headroom")
		fmt.Fprintln(w, "  or the rule could not tell.")
	}
}

func plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}
