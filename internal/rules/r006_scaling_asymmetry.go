package rules

import (
	"fmt"

	"github.com/headroom-project/headroom/internal/graph"
	"github.com/headroom-project/headroom/internal/plan"
)

// fixedProviders are stateful services whose capacity is pinned at apply time.
// None of them is wrong for being fixed; the question is how far the thing in
// front of them is allowed to travel.
var fixedProviders = []string{
	"aws_db_instance",
	"aws_elasticache_cluster",
	"aws_elasticache_replication_group",
}

// ruleScalingAsymmetry is the general form of the whole product: the consumer
// scales, the provider does not.
//
// R1 answers this for connections, where the ceiling is a hard number. Here
// there is no single number, so the finding is the ratio itself. An application
// tier allowed to grow twenty times in front of a database that cannot grow at
// all is a decision, and it should be a stated one.
func ruleScalingAsymmetry(f *plan.File, g *graph.Graph, opt Options) []Finding {
	var out []Finding

	for _, providerType := range fixedProviders {
		for _, provider := range f.ByType(providerType) {
			addr := plan.Base(provider.Address)

			var consumers []string
			var ratios []string
			maxRatio := 0.0

			for _, consumer := range dbConsumers(g, addr) {
				min, max, ok := scalingRange(g, consumer)
				if !ok || max <= min {
					continue
				}
				ratio := float64(max) / float64(min)
				if min == 0 {
					ratio = float64(max)
				}
				if ratio > maxRatio {
					maxRatio = ratio
				}
				consumers = append(consumers, consumer)
				ratios = append(ratios, fmt.Sprintf("%s scales from %d to %d, a %.0fx range.", consumer, min, max, ratio))
			}

			if len(consumers) == 0 {
				continue
			}

			if maxRatio >= opt.ScaleRatioWarn {
				detail := append([]string{}, ratios...)
				detail = append(detail,
					fmt.Sprintf("%s has fixed capacity: nothing in this plan grows it when the tier in front does.", addr))
				if class := plan.Str(provider.Values, "instance_class"); class != "" {
					detail = append(detail, fmt.Sprintf("Instance class stays %s at every point in that range.", class))
				}
				detail = append(detail,
					"The fix is rarely to autoscale the database. It is to know which of the two numbers is the real limit, and to cap the front tier there on purpose instead of by accident.")

				out = append(out, Finding{
					Rule:     "R6",
					Severity: SeverityWarning,
					Title:    "Scale asymmetry: the tier in front grows, the one behind cannot",
					Summary: fmt.Sprintf(
						"Workloads in front of %s are allowed to grow %.0fx while %s stays exactly the same size.",
						addr, maxRatio, addr),
					Detail:     detail,
					Confidence: "high",
					Resources:  append([]string{addr}, consumers...),
					Metrics: map[string]int{
						"scale_ratio": int(maxRatio),
						"consumers":   len(consumers),
					},
				})
			}

			// Storage is the one dimension RDS will grow on its own, and only
			// if asked. Left unasked, a full volume stops writes outright.
			if providerType == "aws_db_instance" {
				allocated, hasAllocated := plan.Num(provider.Values, "allocated_storage")
				maxAllocated, _ := plan.Num(provider.Values, "max_allocated_storage")
				if hasAllocated && allocated > 0 && maxAllocated == 0 {
					out = append(out, Finding{
						Rule:     "R6",
						Severity: SeverityWarning,
						Title:    "Database storage cannot grow while the workload in front of it does",
						Summary: fmt.Sprintf(
							"%s is pinned at %d GiB with no max_allocated_storage, so RDS storage autoscaling is off. A full volume does not degrade, it stops writes.",
							addr, allocated),
						Detail: []string{
							fmt.Sprintf("Workloads in front of it: %s.", join(consumers)),
							"Setting max_allocated_storage turns on storage autoscaling and costs nothing until it is used.",
							"Growing storage manually later is an online operation, but it has a cooldown, so the second emergency in the same day cannot be fixed the same way.",
						},
						Confidence: "high",
						Resources:  []string{addr},
						Metrics: map[string]int{
							"allocated_gib": allocated,
						},
					})
				}
			}
		}
	}
	return out
}

// scalingRange reports how far a workload is allowed to travel, preferring the
// autoscaling policy over the declared size. desired_count is where a service
// starts, not where it stops.
func scalingRange(g *graph.Graph, addr string) (min, max int, ok bool) {
	switch g.Type(addr) {
	case "aws_ecs_service":
		for _, target := range g.ReferrersOfType(addr, "aws_appautoscaling_target") {
			lo, okLo := plan.Num(g.Values(target), "min_capacity")
			hi, okHi := plan.Num(g.Values(target), "max_capacity")
			if okLo && okHi {
				return lo, hi, true
			}
		}
	case "aws_autoscaling_group":
		lo, okLo := plan.Num(g.Values(addr), "min_size")
		hi, okHi := plan.Num(g.Values(addr), "max_size")
		if okLo && okHi {
			return lo, hi, true
		}
	}
	return 0, 0, false
}
