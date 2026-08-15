package rules

import (
	"fmt"

	"github.com/headroom-project/headroom/internal/catalog"
	"github.com/headroom-project/headroom/internal/graph"
	"github.com/headroom-project/headroom/internal/plan"
)

// gcpSQLConsumerTypes are the workloads that hold connections against a Cloud
// SQL instance and that have a resolvable maximum. GKE workloads are absent on
// purpose: a node pool's node count says nothing about how many pods talk to the
// database, and terraform does not contain the Deployment that would.
var gcpSQLConsumerTypes = []string{
	"google_cloud_run_v2_service",
	"google_compute_region_instance_group_manager",
	"google_compute_instance_group_manager",
}

// gcpSQLConnections compares how many connections the workloads in front of a
// Cloud SQL instance can open at full scale against what the tier actually
// accepts.
//
// The topology is easier than it is on AWS and the asymmetry is worse. Easier,
// because a Cloud Run service that reaches a database has to name it -- for the
// connection name, the private IP or the instance itself -- so the edge is
// declared. Worse, because Cloud Run's default maximum is a hundred instances
// whether or not anyone wrote a scaling block, and a db-custom-2-7680 accepts
// four hundred connections in total. The two numbers live in different files and
// nothing in terraform ever multiplies them.
func gcpSQLConnections(f *plan.File, g *graph.Graph, c *catalog.Catalog, opt Options) []Finding {
	var out []Finding

	for _, db := range f.ByType("google_sql_database_instance") {
		addr := plan.Base(db.Address)
		settings := gcpAt(db.Values, "settings")
		tier := plan.Str(settings, "tier")
		version := plan.Str(db.Values, "database_version")

		consumers := gcpSQLConsumers(g, addr)

		ceiling, engine, ok := c.GCPSQLMaxConnections(version, tier)
		source := engine.Source
		how := fmt.Sprintf("%s (%s, %s) accepts %d connections by default", addr, tier, version, ceiling)
		confidence := engine.Confidence
		if confidence == "" {
			confidence = "medium"
		}

		// A declared max_connections flag is not an override to be nervous
		// about, it is the answer: unlike an RDS parameter group, the value is
		// right there in the plan.
		if declared, found := gcpDeclaredMaxConnections(settings); found {
			ceiling, ok = declared, true
			source = ""
			confidence = "high"
			how = fmt.Sprintf("%s sets the max_connections database flag to %d", addr, declared)
		}

		if !ok {
			if len(consumers) == 0 {
				continue
			}
			out = append(out, gcpSQLSkip(c, addr, version, tier))
			continue
		}
		if len(consumers) == 0 {
			continue
		}

		total := 0
		resources := []string{addr}
		var detail []string
		poolConfidence := confidence

		for _, consumer := range consumers {
			w, known := gcpScaleOf(g, c, consumer)
			if !known {
				continue
			}
			pool, poolHow := gcpPoolSizeOf(g, consumer, opt.DefaultPoolSize)
			if poolHow == "assumed" {
				poolConfidence = "medium"
				poolHow = "assumed, no pool env var on the container (override with --pool-size)"
			}
			demand := w.scale * pool
			total += demand
			resources = append(resources, consumer)
			detail = append(detail, fmt.Sprintf(
				"%s scales to %d %s (%s) x %d connections per instance (%s) = %d connections",
				consumer, w.scale, w.unit, w.how, pool, poolHow, demand))
		}
		if total == 0 {
			continue
		}

		detail = append(detail, how)
		if engine.Notes != "" && source != "" {
			detail = append(detail, "Catalog note: "+engine.Notes)
		}
		detail = append(detail,
			"Cloud SQL has no connection multiplexing of its own. The usual fix is a pooler in front of the instance, not a bigger tier, because the tier buys connections in expensive increments.")

		ratio := float64(total) / float64(ceiling)
		metrics := map[string]int{
			"demand":       total,
			"ceiling":      ceiling,
			"break_at_pct": gcpPct(ratio),
		}

		switch {
		case ratio > 1:
			out = append(out, Finding{
				Rule:     "GC1",
				Severity: SeverityCritical,
				Title:    "Scale asymmetry: application outgrows Cloud SQL",
				Summary: fmt.Sprintf(
					"At full scale the workloads in front of %s open ~%d connections against a ceiling of %d. Saturation lands at %.0f%% of the scale this plan already authorises.",
					addr, total, ceiling, 100/ratio),
				Detail:     detail,
				Confidence: poolConfidence,
				Resources:  resources,
				Source:     source,
				Metrics:    metrics,
			})
		case ratio >= opt.WarnAt:
			out = append(out, Finding{
				Rule:     "GC1",
				Severity: SeverityWarning,
				Title:    "Thin connection headroom on Cloud SQL",
				Summary: fmt.Sprintf(
					"Workloads in front of %s reach ~%d of %d connections at full scale (%.0f%% of the ceiling), which leaves nothing for a deployment that briefly runs two revisions at once.",
					addr, total, ceiling, ratio*100),
				Detail:     detail,
				Confidence: poolConfidence,
				Resources:  resources,
				Source:     source,
				Metrics:    metrics,
			})
		}
	}
	return out
}

// gcpSQLSkip says what was not evaluated and why. An absent ceiling beats an
// uncertain one, but a silent absence is indistinguishable from a clean bill of
// health, so the skip is reported.
func gcpSQLSkip(c *catalog.Catalog, addr, version, tier string) Finding {
	if reason, known := c.GCPSQLUnsupportedReason(version); known {
		return Finding{
			Rule:       "GC1",
			Severity:   SeverityInfo,
			Title:      "Connection ceiling not evaluated",
			Summary:    fmt.Sprintf("%s runs %s, which has no published connection table.", addr, version),
			Detail:     []string{reason},
			Confidence: "n/a",
			Resources:  []string{addr},
		}
	}
	detail := []string{
		"Reporting a number here would be a guess, so the rule stays silent. Add the tier to internal/catalog/data/gcp-cloudsql.json with its source.",
	}
	for pattern, reason := range c.GCPSQLTierNotEncoded() {
		detail = append(detail, pattern+": "+reason)
	}
	sortStrings(detail[1:])
	return Finding{
		Rule:       "GC1",
		Severity:   SeverityInfo,
		Title:      "Connection ceiling not evaluated",
		Summary:    fmt.Sprintf("%s: no catalog entry for tier %q on %s.", addr, tier, version),
		Detail:     detail,
		Confidence: "n/a",
		Resources:  []string{addr},
	}
}

// gcpDeclaredMaxConnections reads an explicit max_connections database flag.
func gcpDeclaredMaxConnections(settings map[string]any) (int, bool) {
	for _, flag := range gcpBlocksAt(settings, "database_flags") {
		if plan.Str(flag, "name") != "max_connections" {
			continue
		}
		if n := atoi(plan.Str(flag, "value")); n > 0 {
			return n, true
		}
	}
	return 0, false
}

func gcpSQLConsumers(g *graph.Graph, addr string) []string {
	seen := map[string]bool{}
	var out []string
	for _, t := range gcpSQLConsumerTypes {
		for _, consumer := range g.ReferrersOfType(addr, t) {
			if !seen[consumer] {
				seen[consumer] = true
				out = append(out, consumer)
			}
		}
	}
	sortStrings(out)
	return out
}
