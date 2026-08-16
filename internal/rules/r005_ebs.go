package rules

import (
	"fmt"

	"github.com/headroom-project/headroom/internal/catalog"
	"github.com/headroom-project/headroom/internal/plan"
)

type volume struct {
	owner string
	kind  string
	size  int
	iops  int
	tput  int
}

// ruleEBS covers the two ways a volume betrays the workload on top of it: a gp2
// volume that looks fast until its credit bucket runs dry, and a gp3 volume
// asking for a ratio AWS will not grant.
func ruleEBS(f *plan.File, c *catalog.Catalog, opt Options) []Finding {
	var out []Finding

	for _, v := range volumes(f) {
		switch v.kind {
		case "gp2":
			gp2 := c.GP2()
			baseline := c.GP2Baseline(v.size)
			// Above this size the baseline has caught up with the burst
			// ceiling and there is no cliff left to warn about. The second
			// clause compares against the burst ceiling, not against a literal
			// 1000: that literal was BurstIrrelevantAtGiB, which is a size in
			// GiB, wrongly reused as an IOPS figure. It silenced every volume
			// from about 334 GiB to 1000 GiB, which is exactly the range where
			// the cliff is real. A 400 GiB volume earns 1200 baseline IOPS and
			// still bursts to 3000, a 2.5x fall.
			if v.size >= gp2.BurstIrrelevantAtGiB || baseline >= gp2.BurstIOPS {
				opt.Trace.Skip("R5", v.owner, fmt.Sprintf(
					"%d GiB of gp2 earns %d baseline IOPS, which has caught up with the %d IOPS burst ceiling: there is no cliff left",
					v.size, baseline, gp2.BurstIOPS))
				continue
			}
			out = append(out, Finding{
				Rule:     "R5",
				Severity: SeverityWarning,
				Title:    "gp2 volume depends on burst credits it cannot refill",
				Summary: fmt.Sprintf(
					"%s is a %d GiB gp2 volume: it bursts to %d IOPS while credits last and then drops to %d, a %.1fx fall under sustained load.",
					v.owner, v.size, gp2.BurstIOPS, baseline, float64(gp2.BurstIOPS)/float64(baseline)),
				Detail: []string{
					fmt.Sprintf("Baseline is %d IOPS per GiB with a floor of %d, so %d GiB earns %d IOPS.",
						gp2.BaselineIOPSPerGiB, gp2.MinBaselineIOPS, v.size, baseline),
					"The credit bucket refills at the baseline rate, so any load that stays above baseline drains it and never recovers while the load continues. Benchmarks and the first days in production run on credits and look fine.",
					"gp3 delivers 3000 IOPS and 125 MiB/s with no bucket at all, at a lower price per GiB. The usual fix is a type change, not a size change.",
					"Catalog note: " + gp2.Notes,
				},
				Confidence: "high",
				Resources:  []string{v.owner},
				Source:     gp2.Source,
				Metrics: map[string]int{
					"size_gib":      v.size,
					"baseline_iops": baseline,
					"burst_iops":    gp2.BurstIOPS,
				},
			})

		case "gp3":
			gp3 := c.GP3()
			var problems []string
			if v.iops > 0 && v.size > 0 && v.iops > gp3.MaxIOPSPerGiB*v.size {
				problems = append(problems, fmt.Sprintf(
					"%d IOPS requested on %d GiB exceeds the %d IOPS per GiB limit (max here is %d).",
					v.iops, v.size, gp3.MaxIOPSPerGiB, gp3.MaxIOPSPerGiB*v.size))
			}
			if v.iops > gp3.MaxIOPS {
				problems = append(problems, fmt.Sprintf("%d IOPS exceeds the gp3 maximum of %d.", v.iops, gp3.MaxIOPS))
			}
			if v.tput > 0 && v.iops > 0 && float64(v.tput) > gp3.MaxMiBPerIOPS*float64(v.iops) {
				problems = append(problems, fmt.Sprintf(
					"%d MiB/s against %d IOPS exceeds the %.2f MiB/s per IOPS limit (max here is %.0f MiB/s).",
					v.tput, v.iops, gp3.MaxMiBPerIOPS, gp3.MaxMiBPerIOPS*float64(v.iops)))
			}
			if v.tput > gp3.MaxThroughputMiB {
				problems = append(problems, fmt.Sprintf("%d MiB/s exceeds the gp3 maximum of %d.", v.tput, gp3.MaxThroughputMiB))
			}
			if len(problems) == 0 {
				continue
			}
			out = append(out, Finding{
				Rule:     "R5",
				Severity: SeverityCritical,
				Title:    "gp3 volume asks for a ratio AWS will refuse",
				Summary: fmt.Sprintf(
					"%s requests performance outside the gp3 envelope, so this plan fails at apply time rather than at scale.",
					v.owner),
				Detail:     append(problems, "Catalog note: "+gp3.Notes),
				Confidence: "high",
				Resources:  []string{v.owner},
				Source:     gp3.Source,
				Metrics: map[string]int{
					"size_gib":         v.size,
					"iops":             v.iops,
					"throughput_mibps": v.tput,
				},
			})
		}
	}
	return out
}

// volumes collects both standalone volumes and the block devices declared
// inline on an instance, since the same mistake happens in either shape.
func volumes(f *plan.File) []volume {
	var out []volume

	for _, r := range f.ByType("aws_ebs_volume") {
		size, _ := plan.Num(r.Values, "size")
		iops, _ := plan.Num(r.Values, "iops")
		tput, _ := plan.Num(r.Values, "throughput")
		out = append(out, volume{
			owner: plan.Base(r.Address),
			kind:  plan.Str(r.Values, "type"),
			size:  size,
			iops:  iops,
			tput:  tput,
		})
	}

	for _, r := range f.ByType("aws_instance") {
		for _, block := range []string{"root_block_device", "ebs_block_device"} {
			list, ok := r.Values[block].([]any)
			if !ok {
				continue
			}
			for _, item := range list {
				dev, ok := item.(map[string]any)
				if !ok {
					continue
				}
				size, _ := plan.Num(dev, "volume_size")
				iops, _ := plan.Num(dev, "iops")
				tput, _ := plan.Num(dev, "throughput")
				out = append(out, volume{
					owner: fmt.Sprintf("%s (%s)", plan.Base(r.Address), block),
					kind:  plan.Str(dev, "volume_type"),
					size:  size,
					iops:  iops,
					tput:  tput,
				})
			}
		}
	}
	return out
}
