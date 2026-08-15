package rules

import (
	"fmt"

	"github.com/headroom-project/headroom/internal/catalog"
	"github.com/headroom-project/headroom/internal/graph"
	"github.com/headroom-project/headroom/internal/plan"
)

// gcpSQLStorage reports a Cloud SQL instance whose disk cannot grow while the
// tier in front of it can.
//
// disk_autoresize defaults to on, so an instance with it off was turned off on
// purpose, usually to stop a runaway query quietly tripling the bill. That is a
// legitimate decision and this rule does not overrule it. What it does is put
// the consequence next to the decision: a full Cloud SQL disk does not degrade,
// it takes the instance down, and the recovery involves growing a disk while the
// application is already failing. The same applies to disk_autoresize_limit,
// which is the same decision with a number attached.
func gcpSQLStorage(f *plan.File, g *graph.Graph, c *catalog.Catalog) []Finding {
	var out []Finding

	for _, db := range f.ByType("google_sql_database_instance") {
		addr := plan.Base(db.Address)
		settings := gcpAt(db.Values, "settings")
		if settings == nil {
			continue
		}

		size, hasSize := plan.Num(settings, "disk_size")
		autoresize, declared := settings["disk_autoresize"].(bool)
		limit, _ := plan.Num(settings, "disk_autoresize_limit")

		frozen := declared && !autoresize
		capped := (!declared || autoresize) && limit > 0
		if !frozen && !capped {
			continue
		}

		// The asymmetry only exists if something in front of it grows. A fixed
		// disk under a fixed workload is a capacity plan, not a finding.
		consumers := gcpSQLConsumers(g, addr)
		var scale []string
		resources := []string{addr}
		for _, consumer := range consumers {
			w, ok := gcpScaleOf(g, c, consumer)
			if !ok {
				continue
			}
			resources = append(resources, consumer)
			scale = append(scale, fmt.Sprintf("%s scales to %d %s (%s)", consumer, w.scale, w.unit, w.how))
		}
		if len(scale) == 0 {
			continue
		}

		detail := append([]string{}, scale...)
		metrics := map[string]int{}
		var summary string

		switch {
		case frozen && hasSize:
			metrics["disk_gib"] = size
			summary = fmt.Sprintf(
				"%s has disk_autoresize off at %d GiB while the workloads in front of it autoscale. The disk is the one part of this database that cannot follow them.",
				addr, size)
			detail = append(detail, fmt.Sprintf("%s: disk_autoresize = false, disk_size = %d GiB.", addr, size))
		case frozen:
			summary = fmt.Sprintf(
				"%s has disk_autoresize off while the workloads in front of it autoscale. The disk is the one part of this database that cannot follow them.", addr)
			detail = append(detail, fmt.Sprintf("%s: disk_autoresize = false.", addr))
		default:
			metrics["disk_autoresize_limit_gib"] = limit
			if hasSize {
				metrics["disk_gib"] = size
			}
			summary = fmt.Sprintf(
				"%s grows its disk automatically but stops at %d GiB, and the workloads in front of it autoscale past that point without noticing.",
				addr, limit)
			detail = append(detail, fmt.Sprintf("%s: disk_autoresize_limit = %d GiB.", addr, limit))
		}

		detail = append(detail,
			"Cloud SQL storage only ever goes up: an instance whose disk has been grown cannot be shrunk again, which is usually the real reason autoresize gets turned off.",
			"A full disk stops the instance rather than slowing it, and the fix has to be applied while the application is already down.",
			"This may be a deliberate cost decision. The rule states the trade, it does not decide it.")

		out = append(out, Finding{
			Rule:       "GC6",
			Severity:   SeverityWarning,
			Title:      "Cloud SQL storage cannot grow while the workload in front of it does",
			Summary:    summary,
			Detail:     detail,
			Confidence: "high",
			Resources:  resources,
			Metrics:    metrics,
		})
	}
	return out
}
