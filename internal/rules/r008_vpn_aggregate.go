package rules

import (
	"fmt"

	"github.com/headroom-project/headroom/internal/catalog"
	"github.com/headroom-project/headroom/internal/graph"
	"github.com/headroom-project/headroom/internal/plan"
)

// ruleVPNAggregate catches a topology that looks like it scales and does not.
//
// Attaching a second Site-to-Site VPN connection to a virtual private gateway
// buys redundancy and never buys bandwidth, because a VGW does not do ECMP.
// Nothing in the terraform says so: two connections read as twice the pipe, and
// the ceiling stays at one tunnel.
func ruleVPNAggregate(f *plan.File, g *graph.Graph, c *catalog.Catalog) []Finding {
	var out []Finding
	ecmp := c.ECMP()
	s2s := c.SiteToSiteVPN()

	for _, vgw := range f.ByType("aws_vpn_gateway") {
		addr := plan.Base(vgw.Address)

		conns := g.ReferrersOfType(addr, "aws_vpn_connection")
		total := 0
		for _, conn := range conns {
			total += g.InstanceCount(conn)
		}
		if total < 2 {
			continue
		}

		out = append(out, Finding{
			Rule:     "R8",
			Severity: SeverityWarning,
			Title:    "Two VPN connections on a virtual private gateway do not add bandwidth",
			Summary: fmt.Sprintf(
				"%d Site-to-Site VPN connections terminate on %s. A virtual private gateway does not support ECMP, so the aggregate ceiling stays at one tunnel, around %.2f Gbps, no matter how many connections are attached.",
				total, addr, s2s.MaxGbpsPerTunnel),
			Detail: []string{
				fmt.Sprintf("Connections attached: %s.", join(conns)),
				ecmp.Notes,
				s2s.Notes,
				"What the second connection does buy is a separate carrier path, which is real availability. This finding is about capacity planning, not about whether the design is wrong.",
				"Raising the ceiling means a transit gateway with ECMP across tunnels, or Direct Connect. Adding connections to the VGW does not.",
			},
			Confidence: ecmp.Confidence,
			Resources:  append([]string{addr}, conns...),
			Source:     ecmp.Source,
			Metrics: map[string]int{
				"connections":         total,
				"tunnels_per_conn":    s2s.TunnelsPerConnection,
				"aggregate_mbps_ceil": int(s2s.MaxGbpsPerTunnel * 1000),
			},
		})
	}
	return out
}
