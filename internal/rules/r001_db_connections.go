package rules

import (
	"fmt"

	"github.com/headroom-project/headroom/internal/catalog"
	"github.com/headroom-project/headroom/internal/graph"
	"github.com/headroom-project/headroom/internal/plan"
)

// ruleDBConnections compares how many connections the workloads in front of a
// database can open at full scale against what the instance class actually
// accepts.
//
// The topology is reconstructed from security groups rather than depends_on: an
// ingress rule on the database security group naming the application security
// group is a declared network edge, and it survives however the code is
// organised.
func ruleDBConnections(f *plan.File, g *graph.Graph, c *catalog.Catalog, opt Options) []Finding {
	var out []Finding

	for _, db := range f.ByType("aws_db_instance") {
		addr := plan.Base(db.Address)
		engine := plan.Str(db.Values, "engine")
		class := plan.Str(db.Values, "instance_class")

		ceiling, formula, ok := c.MaxConnections(engine, class)

		// An operator who knows their parameter group beats a catalog default,
		// and stating it here costs less than granting anyone AWS access to go
		// and read it.
		declaredCeiling := false
		if declared, has := opt.Config.RDSMaxConnections(addr); has {
			ceiling, ok, declaredCeiling = declared, true, true
			formula.Formula = "declared in " + opt.Config.Path()
			formula.Source = opt.Config.Path()
			formula.Notes = "Ceiling stated by this organization, overriding the catalog default for the instance class."
		}

		if !ok {
			if reason, known := c.UnsupportedReason(engine); known {
				out = append(out, Finding{
					Rule:       "R1",
					Severity:   SeverityInfo,
					Title:      "Connection ceiling not evaluated",
					Summary:    fmt.Sprintf("%s runs %s, which has no encoded ceiling yet.", addr, engine),
					Detail:     []string{reason},
					Confidence: "n/a",
					Resources:  []string{addr},
				})
				continue
			}
			out = append(out, Finding{
				Rule:       "R1",
				Severity:   SeverityInfo,
				Title:      "Connection ceiling not evaluated",
				Summary:    fmt.Sprintf("%s: no catalog entry for engine %q on class %q.", addr, engine, class),
				Detail:     []string{"Reporting a number here would be a guess, so the rule stays silent. Add the class to internal/catalog/data/aws-rds.json with its source."},
				Confidence: "n/a",
				Resources:  []string{addr},
			})
			continue
		}

		consumers := dbConsumers(g, addr)
		if len(consumers) == 0 {
			continue
		}

		confidence := "high"
		var detail []string
		var resources []string
		total := 0

		resources = append(resources, addr)
		for _, consumer := range consumers {
			w, ok := scaleOf(g, consumer)
			if !ok {
				continue
			}
			pool, poolHow := poolSizeOf(g, consumer, opt.DefaultPoolSize)
			if poolHow == "assumed" {
				confidence = "medium"
				poolHow = fmt.Sprintf("assumed, no pool env var in the task definition (override with --pool-size)")
			}
			demand := w.scale * pool
			total += demand
			resources = append(resources, consumer)
			detail = append(detail, fmt.Sprintf(
				"%s scales to %d %s (%s) x %d connections per task (%s) = %d connections",
				consumer, w.scale, w.unit, w.how, pool, poolHow, demand))
		}
		if total == 0 {
			continue
		}

		// A custom parameter group can override max_connections, and the plan
		// does not reveal the value, so the catalog default stops being ground
		// truth.
		if pgs := g.RefsOfType(addr, "aws_db_parameter_group"); len(pgs) > 0 && !declaredCeiling {
			confidence = "low"
			detail = append(detail, fmt.Sprintf(
				"%s attaches a custom parameter group (%s); if it overrides max_connections the ceiling below is wrong.",
				addr, pgs[0]))
		}

		detail = append(detail, fmt.Sprintf(
			"%s (%s, %s) accepts ~%d connections by default [%s]",
			addr, class, engine, ceiling, formula.Formula))
		if formula.Notes != "" {
			detail = append(detail, "Catalog note: "+formula.Notes)
		}

		ratio := float64(total) / float64(ceiling)
		metrics := map[string]int{
			"demand":       total,
			"ceiling":      ceiling,
			"break_at_pct": int(100/ratio + 0.5),
		}
		switch {
		case ratio > 1:
			out = append(out, Finding{
				Rule:     "R1",
				Severity: SeverityCritical,
				Title:    "Scale asymmetry: application outgrows the database",
				Summary: fmt.Sprintf(
					"At full scale the workloads in front of %s open ~%d connections against a ceiling of ~%d. Saturation lands at %.0f%% of the scale this plan already authorises.",
					addr, total, ceiling, 100/ratio),
				Detail:     detail,
				Confidence: confidence,
				Resources:  resources,
				Source:     formula.Source,
				Metrics:    metrics,
			})
		case ratio >= opt.WarnAt:
			out = append(out, Finding{
				Rule:     "R1",
				Severity: SeverityWarning,
				Title:    "Thin connection headroom",
				Summary: fmt.Sprintf(
					"Workloads in front of %s reach ~%d of ~%d connections at full scale (%.0f%% of the ceiling).",
					addr, total, ceiling, ratio*100),
				Detail:     detail,
				Confidence: confidence,
				Resources:  resources,
				Source:     formula.Source,
				Metrics:    metrics,
			})
		}
	}
	return out
}

// dbConsumers walks security groups outward from the database to find the
// workloads allowed to reach it.
func dbConsumers(g *graph.Graph, dbAddr string) []string {
	seen := map[string]bool{}
	var consumers []string

	for _, dbSG := range g.RefsOfType(dbAddr, "aws_security_group") {
		for _, sourceSG := range inboundSources(g, dbSG) {
			for _, consumer := range workloadsAttachedTo(g, sourceSG) {
				if !seen[consumer] {
					seen[consumer] = true
					consumers = append(consumers, consumer)
				}
			}
		}
	}
	return consumers
}

// inboundSources returns the security groups allowed to send traffic into sg,
// covering the three ways terraform expresses it. Direction is read from the
// specific attribute, never from a bare reference, because an egress rule
// pointing the other way would invent traffic that does not exist.
func inboundSources(g *graph.Graph, sg string) []string {
	seen := map[string]bool{}
	var sources []string

	add := func(addrs []string) {
		for _, a := range addrs {
			if a != sg && !seen[a] {
				seen[a] = true
				sources = append(sources, a)
			}
		}
	}

	add(g.ReferencesIn(sg, "aws_security_group", "ingress"))

	for _, rule := range g.ReferrersOfType(sg, "aws_security_group_rule") {
		if plan.Str(g.Values(rule), "type") != "ingress" {
			continue
		}
		if !contains(g.ReferencesIn(rule, "aws_security_group", "security_group_id"), sg) {
			continue
		}
		add(g.ReferencesIn(rule, "aws_security_group", "source_security_group_id"))
	}

	for _, rule := range g.ReferrersOfType(sg, "aws_vpc_security_group_ingress_rule") {
		if !contains(g.ReferencesIn(rule, "aws_security_group", "security_group_id"), sg) {
			continue
		}
		add(g.ReferencesIn(rule, "aws_security_group", "referenced_security_group_id"))
	}

	return sources
}

// workloadsAttachedTo finds the scalable workloads wearing a security group,
// including the launch-template indirection an autoscaling group goes through.
func workloadsAttachedTo(g *graph.Graph, sg string) []string {
	var out []string
	out = append(out, g.ReferrersOfType(sg, "aws_ecs_service")...)
	out = append(out, g.ReferrersOfType(sg, "aws_autoscaling_group")...)

	for _, lt := range g.ReferrersOfType(sg, "aws_launch_template") {
		out = append(out, g.ReferrersOfType(lt, "aws_autoscaling_group")...)
	}
	return out
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}
