package rules

import (
	"fmt"
	"net/netip"

	"github.com/headroom-project/headroom/internal/graph"
	"github.com/headroom-project/headroom/internal/plan"
)

// ruleSubnetIPs compares the addresses a subnet can hand out against how many
// the workloads placed in it will ask for at full scale.
//
// This is the silent killer: every awsvpc task takes a real IP out of the
// subnet, the CIDR is a one-way decision that cannot be resized after creation,
// and a generated /27 looks perfectly reasonable until the service scales.
func ruleSubnetIPs(f *plan.File, g *graph.Graph, opt Options) []Finding {
	demand := map[string][]string{}
	totals := map[string]int{}

	for _, svc := range f.ByType("aws_ecs_service") {
		addr := plan.Base(svc.Address)
		subnets := g.ReferencesIn(addr, "aws_subnet", "network_configuration")
		if len(subnets) == 0 {
			continue
		}
		w, ok := scaleOf(g, addr)
		if !ok {
			continue
		}
		// ECS spreads tasks across the subnets it is given, so the honest
		// per-subnet number is the even split, not the worst case. Claiming
		// the worst case here would manufacture false positives.
		perSubnet := ceilDiv(w.scale, len(subnets))
		for _, s := range subnets {
			totals[s] += perSubnet
			demand[s] = append(demand[s], fmt.Sprintf(
				"%s scales to %d %s (%s) spread over %d subnets = %d IPs here",
				addr, w.scale, w.unit, w.how, len(subnets), perSubnet))
		}
	}

	var out []Finding
	for _, subnet := range f.ByType("aws_subnet") {
		addr := plan.Base(subnet.Address)
		wanted := totals[addr]
		if wanted == 0 {
			continue
		}
		cidr := plan.Str(subnet.Values, "cidr_block")
		usable, ok := usableIPs(cidr)
		if !ok {
			continue
		}

		detail := append([]string{}, demand[addr]...)
		detail = append(detail, fmt.Sprintf(
			"%s is %s: %d usable addresses (AWS reserves 5 per subnet)", addr, cidr, usable))
		detail = append(detail,
			"Every awsvpc task consumes one address, and a subnet CIDR cannot be resized after creation.")

		ratio := float64(wanted) / float64(usable)
		switch {
		case ratio > 1:
			out = append(out, Finding{
				Rule:     "R2",
				Severity: SeverityCritical,
				Title:    "Subnet runs out of addresses before the service stops scaling",
				Summary: fmt.Sprintf(
					"%s (%s) offers %d usable addresses and the workloads placed in it need ~%d at full scale. Task placement starts failing at %.0f%% of the authorised scale.",
					addr, cidr, usable, wanted, 100/ratio),
				Detail:     detail,
				Confidence: "high",
				Resources:  append([]string{addr}, resourcesOf(demand[addr])...),
				Metrics: map[string]int{
					"demand":       wanted,
					"usable":       usable,
					"break_at_pct": int(100/ratio + 0.5),
				},
			})
		case ratio >= opt.WarnAt:
			out = append(out, Finding{
				Rule:     "R2",
				Severity: SeverityWarning,
				Title:    "Thin address headroom in subnet",
				Summary: fmt.Sprintf(
					"%s (%s) reaches %d of %d usable addresses at full scale (%.0f%%), leaving no room for a deployment that briefly doubles task count.",
					addr, cidr, wanted, usable, ratio*100),
				Detail:     detail,
				Confidence: "high",
				Resources:  append([]string{addr}, resourcesOf(demand[addr])...),
				Metrics: map[string]int{
					"demand":       wanted,
					"usable":       usable,
					"break_at_pct": int(100/ratio + 0.5),
				},
			})
		}
	}
	return out
}

// usableIPs is the subnet size minus the five addresses AWS reserves in every
// subnet: network, VPC router, DNS, future use, and broadcast.
func usableIPs(cidr string) (int, bool) {
	p, err := netip.ParsePrefix(cidr)
	if err != nil || !p.Addr().Is4() {
		return 0, false
	}
	bits := 32 - p.Bits()
	if bits > 24 {
		return 0, false
	}
	total := 1 << uint(bits)
	if total <= 5 {
		return 0, false
	}
	return total - 5, true
}

func resourcesOf(lines []string) []string {
	var out []string
	for _, l := range lines {
		for i, r := range l {
			if r == ' ' {
				out = append(out, l[:i])
				break
			}
		}
	}
	return out
}
