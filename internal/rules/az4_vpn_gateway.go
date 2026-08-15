package rules

import (
	"fmt"

	"github.com/headroom-project/headroom/internal/catalog"
	"github.com/headroom-project/headroom/internal/graph"
	"github.com/headroom-project/headroom/internal/plan"
)

// azVPNGatewayThroughput catches a topology that looks like it scales and does
// not: several connections terminating on one VPN gateway.
//
// Microsoft states it in one sentence and nobody reads it: "When you create
// multiple connections, all VPN tunnels share the available gateway bandwidth."
// The SKU is the ceiling. A second connection buys a second site and a second
// carrier path, which is real availability, and it buys exactly zero additional
// megabits.
//
// Same shape as R8 on AWS, where two connections on a virtual private gateway
// do not aggregate either. The difference is that on Azure the number is
// printed on the SKU, so the finding can quote it.
func azVPNGatewayThroughput(f *plan.File, g *graph.Graph, c *catalog.Catalog) []Finding {
	var out []Finding

	for _, gateway := range f.ByType("azurerm_virtual_network_gateway") {
		addr := plan.Base(gateway.Address)
		if plan.Str(gateway.Values, "type") != "Vpn" {
			continue
		}
		skuName := plan.Str(gateway.Values, "sku")
		// Four SKUs exist in both generations at different throughput, and the
		// plan says which: azurerm_virtual_network_gateway has a "generation"
		// argument and extract allowlists it. Quoting the Generation2 figure for
		// a Generation1 gateway overstates the headroom by up to 2x, which is
		// the one direction this product cannot afford to be wrong in.
		generation := plan.Str(gateway.Values, "generation")

		sku, ok := c.AzureVPNGatewayFor(skuName, generation)
		if !ok {
			out = append(out, Finding{
				Rule:       "AZ4",
				Severity:   SeverityInfo,
				Title:      "VPN gateway throughput not evaluated",
				Summary:    fmt.Sprintf("%s uses SKU %q, which has no encoded throughput.", addr, skuName),
				Detail:     []string{"Add it to internal/catalog/data/azure-network.json with its source. Quoting a number for an unknown SKU would be a guess."},
				Confidence: "n/a",
				Resources:  []string{addr},
			})
			continue
		}

		conns := g.ReferrersOfType(addr, "azurerm_virtual_network_gateway_connection")
		total := 0
		for _, conn := range conns {
			total += g.InstanceCount(conn)
		}
		if total < 2 {
			continue
		}

		// active_active doubles the tunnels for redundancy, and Microsoft's
		// aggregate benchmark is still the gateway's, not per instance.
		activeActive, _ := gateway.Values["active_active"].(bool)

		detail := []string{
			fmt.Sprintf("Connections terminating here: %s.", join(conns)),
			fmt.Sprintf("%s is rated for %d Mbps aggregate and at most %d site-to-site or VNet-to-VNet tunnels. That %d Mbps is the whole gateway, divided among however many tunnels are up.",
				sku.Name, sku.ThroughputMbps, sku.Tunnels, sku.ThroughputMbps),
			"Catalog note: " + sku.Notes,
			"The second connection does buy a separate site and a separate carrier path, which is real availability. This finding is about capacity planning, not about whether the design is wrong.",
			fmt.Sprintf("Raising the ceiling means a larger SKU, or ExpressRoute. On this SKU the arithmetic is %d Mbps split %d ways, or about %d Mbps per tunnel if they are all busy at once.",
				sku.ThroughputMbps, total, sku.ThroughputMbps/total),
		}
		if sku.DualGeneration {
			if sku.GenerationDeclared {
				detail = append(detail, fmt.Sprintf(
					"%s exists in both generations at different throughput. This plan declares generation = %q, so the %d Mbps above is that generation's figure. The other generation is rated %d Mbps.",
					sku.Name, sku.ResolvedGeneration, sku.ThroughputMbps, sku.OtherGenThroughput))
			} else {
				detail = append(detail, fmt.Sprintf(
					"%s exists in both generations at different throughput and this plan does not set the generation argument, so the figure above is the Generation1 one, %d Mbps, which is the lower of the two. A Generation2 gateway is rated %d Mbps. Set generation explicitly and this finding will quote the number that applies to you.",
					sku.Name, sku.ThroughputMbps, sku.OtherGenThroughput))
			}
		}
		if activeActive {
			detail = append(detail, "active_active is set, which gives each connection two tunnels on two gateway instances. That removes a failover gap; the aggregate throughput benchmark above is still the gateway's.")
		}
		if sku.Name == "Basic" {
			detail = append(detail, "Basic is the legacy SKU: no BGP, no zone redundancy, no IKEv2 or OpenVPN point-to-site, and 100 Mbps. It is almost never the right choice for anything with two sites attached to it.")
		}

		metrics := map[string]int{
			"connections":     total,
			"throughput_mbps": sku.ThroughputMbps,
			"max_tunnels":     sku.Tunnels,
			"mbps_per_tunnel": sku.ThroughputMbps / total,
			"vms_supported":   sku.VMsSupported,
		}

		if total > sku.Tunnels {
			out = append(out, Finding{
				Rule:     "AZ4",
				Severity: SeverityCritical,
				Title:    "More VPN connections than the gateway SKU accepts",
				Summary: fmt.Sprintf(
					"%d connections terminate on %s, which is a %s and accepts at most %d tunnels. The ones past the limit will not come up.",
					total, addr, sku.Name, sku.Tunnels),
				Detail:     detail,
				Confidence: sku.Confidence,
				Resources:  append([]string{addr}, conns...),
				Source:     sku.Source,
				Metrics:    metrics,
			})
			continue
		}

		out = append(out, Finding{
			Rule:     "AZ4",
			Severity: SeverityWarning,
			Title:    "VPN connections on one gateway share its bandwidth, they do not add to it",
			Summary: fmt.Sprintf(
				"%d VPN connections terminate on %s. Every tunnel on a VPN gateway shares the SKU's aggregate throughput, so the ceiling stays at the %d Mbps of a %s no matter how many connections are attached.",
				total, addr, sku.ThroughputMbps, sku.Name),
			Detail:     detail,
			Confidence: sku.Confidence,
			Resources:  append([]string{addr}, conns...),
			Source:     sku.Source,
			Metrics:    metrics,
		})
	}
	return out
}
