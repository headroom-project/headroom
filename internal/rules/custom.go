package rules

import (
	"fmt"
	"net/netip"
	"regexp"
	"strconv"
	"strings"

	"github.com/headroom-project/headroom/internal/config"
	"github.com/headroom-project/headroom/internal/plan"
)

// Custom rules are assertions, not capacity maths.
//
// The distinction is the whole reason they are declarative. "gp2 is banned
// here" is a policy an organization owns and should be able to write in a
// afternoon. "a db.t3.medium running postgres accepts about 450 connections" is
// a curated fact, and a ceiling that nobody verified is worse than no ceiling at
// all, so those stay in code with a source next to them.
//
// The operator set is fixed on purpose. A config file that can compute anything
// is a program, and a program in a config file is a debugging problem shipped to
// the customer.
func runCustom(f *plan.File, opt Options) []Finding {
	if opt.Config == nil || len(opt.Config.Custom) == 0 {
		return nil
	}

	var out []Finding
	for _, rule := range opt.Config.Custom {
		severity := rule.Severity
		if severity == "" {
			severity = SeverityWarning
		}

		for _, res := range f.ByType(rule.Match.Type) {
			if !matchesAll(res.Values, rule.Match.Where) {
				continue
			}
			addr := plan.Base(res.Address)

			summary := rule.Summary
			if summary == "" {
				summary = fmt.Sprintf("%s matches %s.", addr, rule.ID)
			}

			detail := make([]string, 0, len(rule.Detail)+1)
			for _, line := range rule.Detail {
				detail = append(detail, render(line, addr, res))
			}
			detail = append(detail, fmt.Sprintf("Declared in %s as a rule of this organization, not a headroom default.", opt.Config.Path()))

			out = append(out, Finding{
				Rule:       rule.ID,
				Severity:   severity,
				Title:      rule.Title,
				Summary:    render(summary, addr, res),
				Detail:     detail,
				Confidence: "high",
				Resources:  []string{addr},
				Source:     rule.Source,
			})
		}
	}
	return out
}

func matchesAll(values map[string]any, conds []config.Condition) bool {
	for _, cond := range conds {
		if !matches(values, cond) {
			return false
		}
	}
	return true
}

// matches evaluates one condition. An attribute that resolves to several values
// (a list, or a nested block repeated) matches when any single value does, which
// is what someone writing "any subnet smaller than a /24" expects.
func matches(values map[string]any, cond config.Condition) bool {
	found := readPath(values, cond.Attr)

	if cond.Exists != nil {
		return *cond.Exists == (len(found) > 0)
	}
	if len(found) == 0 {
		return false
	}

	for _, raw := range found {
		if matchesValue(raw, cond) {
			return true
		}
	}
	return false
}

func matchesValue(raw any, cond config.Condition) bool {
	text := asText(raw)

	switch {
	case cond.Equals != nil:
		return text == *cond.Equals
	case cond.NotEquals != nil:
		return text != *cond.NotEquals
	case len(cond.In) > 0:
		return containsString(cond.In, text)
	case len(cond.NotIn) > 0:
		return !containsString(cond.NotIn, text)
	case cond.Matches != "":
		re, err := regexp.Compile(cond.Matches)
		return err == nil && re.MatchString(text)
	case cond.NotMatches != "":
		re, err := regexp.Compile(cond.NotMatches)
		return err == nil && !re.MatchString(text)
	}

	if n, ok := asNumber(raw); ok {
		switch {
		case cond.GT != nil:
			return n > *cond.GT
		case cond.GTE != nil:
			return n >= *cond.GTE
		case cond.LT != nil:
			return n < *cond.LT
		case cond.LTE != nil:
			return n <= *cond.LTE
		}
	}

	if bits, ok := cidrPrefix(text); ok {
		switch {
		case cond.PrefixGT != nil:
			return bits > *cond.PrefixGT
		case cond.PrefixGTE != nil:
			return bits >= *cond.PrefixGTE
		case cond.PrefixLT != nil:
			return bits < *cond.PrefixLT
		case cond.PrefixLTE != nil:
			return bits <= *cond.PrefixLTE
		}
	}
	return false
}

// readPath resolves "attr" or "block.attr", walking into lists on the way.
func readPath(values map[string]any, path string) []any {
	head, rest, nested := strings.Cut(path, ".")
	node, ok := values[head]
	if !ok || node == nil {
		return nil
	}
	if !nested {
		return flatten(node)
	}
	var out []any
	for _, child := range flatten(node) {
		if m, ok := child.(map[string]any); ok {
			out = append(out, readPath(m, rest)...)
		}
	}
	return out
}

func flatten(node any) []any {
	if list, ok := node.([]any); ok {
		var out []any
		for _, item := range list {
			out = append(out, flatten(item)...)
		}
		return out
	}
	return []any{node}
}

func asText(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case bool:
		return strconv.FormatBool(t)
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	}
	return ""
}

func asNumber(v any) (float64, bool) {
	switch t := v.(type) {
	case float64:
		return t, true
	case string:
		n, err := strconv.ParseFloat(t, 64)
		return n, err == nil
	}
	return 0, false
}

func cidrPrefix(v string) (int, bool) {
	p, err := netip.ParsePrefix(v)
	if err != nil {
		return 0, false
	}
	return p.Bits(), true
}

func containsString(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

var placeholder = regexp.MustCompile(`\{\{\s*([a-zA-Z_][a-zA-Z0-9_.:]*)\s*\}\}`)

// render fills {{address}}, {{type}} and {{attr:name}} so a rule can name the
// thing it found instead of describing it in the abstract.
func render(text, addr string, res plan.Resource) string {
	return placeholder.ReplaceAllStringFunc(text, func(match string) string {
		key := strings.TrimSpace(placeholder.FindStringSubmatch(match)[1])
		switch {
		case key == "address":
			return addr
		case key == "type":
			return res.Type
		case strings.HasPrefix(key, "attr:"):
			values := readPath(res.Values, strings.TrimPrefix(key, "attr:"))
			if len(values) == 0 {
				return "unknown"
			}
			return asText(values[0])
		}
		return match
	})
}
