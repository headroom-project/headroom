package rules

import (
	"fmt"

	"github.com/headroom-project/headroom/internal/catalog"
	"github.com/headroom-project/headroom/internal/plan"
)

// azBurstableCPU reports the sustained CPU a B-series virtual machine actually
// owns, which is a fraction of the vCPUs printed on the size.
//
// R7 exists for the AWS T family and this is not a copy of it, because the
// default is inverted. A T3 with no credit_specification launches unlimited: it
// keeps performing and bills for it, so the ceiling is an invoice. Azure has no
// unlimited mode at all. There is no setting, no surcharge and no escape: when
// the balance reaches zero the machine is throttled to its base percentage and
// stays there for as long as the load lasts. On Azure the ceiling is latency,
// and it arrives without a line on the bill to warn anyone.
func azBurstableCPU(f *plan.File, c *catalog.Catalog) []Finding {
	var out []Finding

	for _, vmType := range []string{
		"azurerm_linux_virtual_machine",
		"azurerm_windows_virtual_machine",
		"azurerm_linux_virtual_machine_scale_set",
		"azurerm_windows_virtual_machine_scale_set",
	} {
		for _, vm := range f.ByType(vmType) {
			addr := plan.Base(vm.Address)

			size, ok := c.AzureVMSizeOf(azVMSizeOf(vm.Values))
			if !ok || size.BaselinePct == 0 {
				continue
			}

			baselineVCPUs := float64(size.VCPUs) * size.BaselinePct / 100
			// Credits are vCPU-minutes: the bank divided by what a fully busy
			// machine spends above baseline is how long a burst can last.
			burnPerMinute := float64(size.VCPUs) * (1 - size.BaselinePct/100)
			burstMinutes := 0
			if burnPerMinute > 0 {
				burstMinutes = azRound(float64(size.MaxBanked) / burnPerMinute)
			}

			out = append(out, Finding{
				Rule:     "AZ6",
				Severity: SeverityWarning,
				Title:    "Burstable VM throttles to baseline once its credits run out",
				Summary: fmt.Sprintf(
					"%s runs a %s: %d vCPUs on paper, %.1f sustained. Load above that spends banked credits, and when the bank is empty Azure throttles the machine to %s until the load stops. B-series has no unlimited mode to buy your way out of it.",
					addr, size.Name, size.VCPUs, baselineVCPUs, azPct(size.BaselinePct)),
				Detail: []string{
					fmt.Sprintf("Base CPU performance is %s of the whole VM, so %d vCPUs sustain the equivalent of %.1f.", azPct(size.BaselinePct), size.VCPUs, baselineVCPUs),
					fmt.Sprintf("It banks %d credits per hour up to a maximum of %d, which is a full-throttle burst of roughly %d minutes from a full bank before the throttle lands.", size.CreditsPerHr, size.MaxBanked, burstMinutes),
					"A fresh VM starts with a bank of credits, so the first hours after a deployment and every load test on a rebuilt environment run above baseline and look fine. The throttle shows up later, under the load that matters.",
					"Catalog note: " + c.AzureBurstNotes(),
					"If the workload is genuinely spiky, B-series is the right and much cheaper answer and this finding is informational. If it is steady, a same-size D or F size costs a predictable amount and never throttles.",
				},
				Confidence: size.Confidence,
				Resources:  []string{addr},
				Source:     size.Source,
				Metrics: map[string]int{
					"vcpus":              size.VCPUs,
					"baseline_pct":       azRound(size.BaselinePct),
					"baseline_vcpu_x10":  azRound(baselineVCPUs * 10),
					"credits_per_hour":   size.CreditsPerHr,
					"max_banked_credits": size.MaxBanked,
					"burst_minutes":      burstMinutes,
				},
			})
		}
	}
	return out
}
