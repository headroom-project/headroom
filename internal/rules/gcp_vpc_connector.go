package rules

import (
	"fmt"

	"github.com/headroom-project/headroom/internal/catalog"
	"github.com/headroom-project/headroom/internal/graph"
	"github.com/headroom-project/headroom/internal/plan"
)

// gcpConnectorConsumerTypes are the serverless products that route into a VPC
// through a connector and that autoscale while the connector does not.
var gcpConnectorConsumerTypes = []string{
	"google_cloud_run_v2_service",
	"google_cloud_run_service",
	"google_cloudfunctions2_function",
	"google_cloudfunctions_function",
}

// gcpConnectorThroughput states what a Serverless VPC Access connector can carry
// and how thin that gets once the services behind it are at full scale.
//
// The connector is the narrowest fixed thing in a serverless GCP architecture
// and the least visible: every packet a Cloud Run service sends to a private
// address crosses it, its machine type and instance range are set once at
// creation, and max_instances cannot go past ten however much traffic arrives.
// Meanwhile Cloud Run is at a hundred instances by default. Two f1-micros in
// front of a hundred instances is a decision nobody made.
//
// It never reports critical, for two honest reasons: Google publishes the
// throughput as an estimated range rather than a guarantee, and terraform says
// nothing about how much traffic any of this actually moves. The rule quotes the
// low end of the range and states the ratio.
func gcpConnectorThroughput(f *plan.File, g *graph.Graph, c *catalog.Catalog, opt Options) []Finding {
	var out []Finding
	cat := c.GCPConnector()

	for _, conn := range f.ByType("google_vpc_access_connector") {
		addr := plan.Base(conn.Address)

		machine := plan.Str(conn.Values, "machine_type")
		spec, known := c.GCPConnectorMachine(machine)
		if !known {
			continue
		}

		maxInstances, how := cat.DefaultMaxInstances, fmt.Sprintf("max_instances not set, so the default of %d applies", cat.DefaultMaxInstances)
		if n, ok := plan.Num(conn.Values, "max_instances"); ok && n > 0 {
			maxInstances, how = n, fmt.Sprintf("max_instances = %d", n)
		}
		if maxInstances <= 0 {
			continue
		}

		consumers, demand, detail := gcpConnectorConsumers(g, c, addr)
		if demand == 0 {
			continue
		}

		// The existing scale-ratio knob is exactly the right question here: how
		// far can the front tier travel relative to a back tier that cannot move.
		ratio := float64(demand) / float64(maxInstances)
		if ratio < opt.ScaleRatioWarn {
			continue
		}

		low := maxInstances * spec.MinMbps
		high := maxInstances * spec.MaxMbps
		perConsumer := low / demand

		detail = append(detail, fmt.Sprintf(
			"%s is %d x %s (%s), which Google estimates at %d to %d Mbps per instance: %d to %d Mbps for everything behind it.",
			addr, maxInstances, machine, how, spec.MinMbps, spec.MaxMbps, low, high))
		detail = append(detail, fmt.Sprintf(
			"Spread across %d consumer instances at full scale that is about %d Mbps each at the low end of the estimate.",
			demand, perConsumer))
		detail = append(detail, cat.SizingNotes)
		detail = append(detail, fmt.Sprintf(
			"max_instances cannot go past %d on any connector, so the ceiling above is close to the product's ceiling and not just this connector's.",
			cat.MaxInstancesCeiling))
		detail = append(detail, cat.Notes)

		out = append(out, Finding{
			Rule:     "GC5",
			Severity: SeverityWarning,
			Title:    "Serverless VPC connector is the fixed tier under an autoscaling one",
			Summary: fmt.Sprintf(
				"%d serverless instances reach the VPC through %s at full scale, and the connector stops at %d %s instances carrying an estimated %d to %d Mbps in total. The consumer scales %.0fx further than the thing it depends on.",
				demand, addr, maxInstances, machine, low, high, ratio),
			Detail:     detail,
			Confidence: cat.Confidence,
			Resources:  append([]string{addr}, consumers...),
			Source:     cat.Source,
			Metrics: map[string]int{
				"consumer_instances":  demand,
				"connector_instances": maxInstances,
				"aggregate_mbps_low":  low,
				"aggregate_mbps_high": high,
				"mbps_per_consumer":   perConsumer,
				"scale_ratio":         int(ratio),
			},
		})
	}
	return out
}

func gcpConnectorConsumers(g *graph.Graph, c *catalog.Catalog, conn string) (addrs []string, demand int, detail []string) {
	for _, t := range gcpConnectorConsumerTypes {
		for _, consumer := range g.ReferrersOfType(conn, t) {
			w, ok := gcpScaleOf(g, c, consumer)
			if !ok {
				// A consumer whose ceiling cannot be resolved still crosses the
				// connector, but counting it as zero is the honest choice: the
				// finding understates rather than invents.
				continue
			}
			addrs = append(addrs, consumer)
			demand += w.scale
			detail = append(detail, fmt.Sprintf(
				"%s scales to %d %s (%s), and every private-range packet it sends crosses %s",
				consumer, w.scale, w.unit, w.how, conn))
		}
	}
	sortStrings(addrs)
	return addrs, demand, detail
}
