package rules

import (
	"fmt"

	"github.com/headroom-project/headroom/internal/catalog"
	"github.com/headroom-project/headroom/internal/plan"
)

// ruleBurstableCPU reports the sustained CPU a T family instance actually owns,
// which is a fraction of the vCPUs printed on the instance type.
//
// It is the same shape as the gp2 credit cliff, one layer up, and it is the
// ceiling people are most surprised by, because nothing in the terraform hints
// that a 4 vCPU instance sustains 1.6 of them. What happens when the bucket
// empties depends on a credit mode almost nobody sets, so the default decides.
func ruleBurstableCPU(f *plan.File, c *catalog.Catalog, opt Options) []Finding {
	var out []Finding

	for _, inst := range f.ByType("aws_instance") {
		addr := plan.Base(inst.Address)
		itype := plan.Str(inst.Values, "instance_type")

		b, defaultMode, ok := c.Burstable(itype)
		if !ok {
			continue
		}

		mode, explicit := creditMode(inst.Values)
		if !explicit {
			mode = defaultMode
		}

		baseline := float64(b.VCPUs) * float64(b.BaselinePctPerCPU) / 100
		detail := []string{
			fmt.Sprintf("%s has %d vCPUs with a baseline of %d%% each, so it sustains the equivalent of %.1f vCPUs and earns %d credits per hour.",
				itype, b.VCPUs, b.BaselinePctPerCPU, baseline, b.CreditsPerHour),
		}
		if explicit {
			detail = append(detail, fmt.Sprintf("credit_specification sets cpu_credits = %q.", mode))
		} else {
			detail = append(detail, fmt.Sprintf("No credit_specification block, so this instance launches in %q mode, which is the family default.", mode))
		}
		detail = append(detail, c.CreditModeConsequence(mode))

		var title, summary string
		switch mode {
		case "unlimited":
			title = "Burstable instance in unlimited mode: the ceiling is the invoice"
			summary = fmt.Sprintf(
				"%s runs a %s, which sustains %.1f of its %d vCPUs for free and then keeps going by buying credits. Load above baseline does not slow down, it bills, and nothing in this plan bounds the amount.",
				addr, itype, baseline, b.VCPUs)
			detail = append(detail,
				"If the workload is genuinely steady, a same-size M or C instance costs a predictable amount and never buys a credit. If it is genuinely spiky, unlimited is the right call and this finding is informational.")
		case "standard":
			title = "Burstable instance throttles to baseline once credits run out"
			summary = fmt.Sprintf(
				"%s runs a %s in standard mode: it sustains %.1f of its %d vCPUs, and any load above that drains the credit bucket and then gets throttled to baseline for as long as the load continues.",
				addr, itype, baseline, b.VCPUs)
		default:
			continue
		}

		out = append(out, Finding{
			Rule:       "R7",
			Severity:   SeverityWarning,
			Title:      title,
			Summary:    summary,
			Detail:     detail,
			Confidence: "high",
			Resources:  []string{addr},
			Source:     c.BurstableSource(),
			Metrics: map[string]int{
				"vcpus":            b.VCPUs,
				"baseline_pct":     b.BaselinePctPerCPU,
				"credits_per_hour": b.CreditsPerHour,
			},
		})
	}
	return out
}

// creditMode reads credit_specification, reporting whether the plan states it at
// all. An absent block is not the same as a default one: it means the family
// default silently decides whether the instance throttles or charges.
func creditMode(values map[string]any) (string, bool) {
	list, ok := values["credit_specification"].([]any)
	if !ok {
		return "", false
	}
	for _, item := range list {
		spec, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if mode := plan.Str(spec, "cpu_credits"); mode != "" {
			return mode, true
		}
	}
	return "", false
}
