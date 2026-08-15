package rules

import (
	"fmt"

	"github.com/headroom-project/headroom/internal/graph"
	"github.com/headroom-project/headroom/internal/plan"
)

// egressWorkloads are the resource types that reach the internet through a
// route table, and therefore inherit whatever the NAT gateway can or cannot do.
var egressWorkloads = []string{
	"aws_ecs_service",
	"aws_instance",
	"aws_autoscaling_group",
	"aws_lambda_function",
	"aws_eks_node_group",
}

// ruleNATEgress measures the blast radius of a NAT gateway: how many subnets,
// availability zones and workloads depend on this one device for outbound
// traffic.
//
// A single NAT is frequently a deliberate cost decision rather than a mistake,
// so this rule never calls it critical. What it does is make the trade explicit,
// because the cost saving is visible on the bill and the blast radius is not.
func ruleNATEgress(f *plan.File, g *graph.Graph, opt Options) []Finding {
	var out []Finding
	nats := f.ByType("aws_nat_gateway")

	for _, nat := range nats {
		addr := plan.Base(nat.Address)

		served := servedSubnets(g, addr)
		if len(served) == 0 {
			continue
		}

		// One aws_subnet block with for_each over three zones is one address
		// and three real subnets. Counting addresses here would let the widest
		// blast radius in the plan look like the narrowest.
		subnetCount := 0
		azs := map[string]bool{}
		for _, subnet := range served {
			subnetCount += g.InstanceCount(subnet)
			for _, values := range g.Instances(subnet) {
				if az := plan.Str(values, "availability_zone"); az != "" {
					azs[az] = true
				}
			}
		}

		var workloads []string
		workloadCount := 0
		seen := map[string]bool{}
		for _, subnet := range served {
			for _, t := range egressWorkloads {
				for _, w := range g.ReferrersOfType(subnet, t) {
					if !seen[w] {
						seen[w] = true
						workloads = append(workloads, w)
						workloadCount += g.InstanceCount(w)
					}
				}
			}
		}

		if subnetCount < 2 && workloadCount < 2 {
			continue
		}

		natAZ := "unknown"
		if subnets := g.RefsOfType(addr, "aws_subnet"); len(subnets) > 0 {
			if az := plan.Str(g.Values(subnets[0]), "availability_zone"); az != "" {
				natAZ = az
			}
		}

		detail := []string{
			fmt.Sprintf("%s lives in %s and is the default route for %d subnets across %d availability zones.",
				addr, natAZ, subnetCount, len(azs)),
		}
		if len(workloads) > 0 {
			detail = append(detail, fmt.Sprintf("Workloads behind it: %s.", join(workloads)))
		}
		if len(azs) > 1 {
			detail = append(detail,
				fmt.Sprintf("Losing %s takes outbound traffic down in every one of those zones at once, so multi-AZ placement buys less availability than it looks like it does.", natAZ))
			detail = append(detail,
				"Traffic from the other zones also crosses an availability zone boundary on every request, which is billed on top of the NAT processing charge.")
		}
		if len(nats) == 1 {
			detail = append(detail, "This is the only NAT gateway in the plan.")
		}
		detail = append(detail,
			"One NAT per zone removes both the shared failure and the cross-zone transfer, at the price of one gateway per zone. This rule states the trade, it does not decide it.")

		out = append(out, Finding{
			Rule:     "R4",
			Severity: SeverityWarning,
			Title:    "Concentrated egress: one NAT gateway for the whole environment",
			Summary: fmt.Sprintf(
				"%d subnets in %d availability zones, carrying %d workloads, route their outbound traffic through %s alone.",
				subnetCount, len(azs), workloadCount, addr),
			Detail:     detail,
			Confidence: "high",
			Resources:  append([]string{addr}, served...),
			Metrics: map[string]int{
				"subnets_served": subnetCount,
				"azs_served":     len(azs),
				"workloads":      workloadCount,
				"nat_gateways":   len(nats),
			},
		})
	}
	return out
}

// servedSubnets follows nat gateway -> route -> route table -> association ->
// subnet, covering both the inline route block and the standalone aws_route
// resource.
func servedSubnets(g *graph.Graph, nat string) []string {
	tables := map[string]bool{}

	for _, rt := range g.ReferrersOfType(nat, "aws_route_table") {
		if contains(g.ReferencesIn(rt, "aws_nat_gateway", "route"), nat) {
			tables[rt] = true
		}
	}
	for _, route := range g.ReferrersOfType(nat, "aws_route") {
		if !contains(g.ReferencesIn(route, "aws_nat_gateway", "nat_gateway_id"), nat) {
			continue
		}
		for _, rt := range g.RefsOfType(route, "aws_route_table") {
			tables[rt] = true
		}
	}

	seen := map[string]bool{}
	var subnets []string
	for rt := range tables {
		for _, assoc := range g.ReferrersOfType(rt, "aws_route_table_association") {
			for _, subnet := range g.RefsOfType(assoc, "aws_subnet") {
				if !seen[subnet] {
					seen[subnet] = true
					subnets = append(subnets, subnet)
				}
			}
		}
	}
	sortStrings(subnets)
	return subnets
}

func join(items []string) string {
	out := ""
	for i, s := range items {
		if i > 0 {
			out += ", "
		}
		out += s
	}
	return out
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}
