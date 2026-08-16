package rules

import (
	"fmt"

	"github.com/headroom-project/headroom/internal/catalog"
	"github.com/headroom-project/headroom/internal/graph"
	"github.com/headroom-project/headroom/internal/plan"
)

// azAKSSubnetIPs compares the addresses an AKS node subnet can hand out against
// how many the cluster will ask for at full scale.
//
// This is the Azure version of R2 and it is much sharper, because the unit of
// consumption is not the node. With Azure CNI in node subnet mode every pod
// gets a real virtual network address, reserved up front per node rather than
// allocated on demand, so the demand is nodes x max_pods and the default
// max_pods is 30 or 110 depending on how the cluster was created. A /24 looks
// generous next to twelve nodes and is exhausted by the first one.
//
// Neither half can be fixed later: a subnet prefix cannot be resized once
// resources sit in it, and max_pods cannot be changed on an existing node pool.
func azAKSSubnetIPs(f *plan.File, g *graph.Graph, c *catalog.Catalog, opt Options) []Finding {
	aks := c.AzureAKS()
	subnet := c.AzureSubnet()

	demand := map[string]int{}
	reasons := map[string][]string{}
	owners := map[string][]string{}

	var out []Finding

	for _, cluster := range f.ByType("azurerm_kubernetes_cluster") {
		addr := plan.Base(cluster.Address)

		mode, flat := azNetworkMode(cluster.Values)
		if mode == "" {
			opt.Trace.Skip("AZ2", addr, "the plan does not state a network plugin, so how pods get addresses is unknown")
			continue
		}
		if !flat {
			opt.Trace.Skip("AZ2", addr, mode+" gives pods addresses from a CIDR outside the virtual network, so no subnet is exhausted by them")
			// Overlay and kubenet hand pods addresses from a CIDR that never
			// touches the virtual network. There is no subnet to exhaust, and
			// saying nothing is the correct answer rather than a gap.
			continue
		}

		for _, pool := range azNodePools(f, g, c, addr, mode) {
			if pool.unresolved != "" {
				out = append(out, Finding{
					Rule:       "AZ2",
					Severity:   SeverityInfo,
					Title:      "Pod address demand not evaluated for a node pool",
					Summary:    fmt.Sprintf("Node pool %q on %s %s.", pool.name, addr, pool.unresolved),
					Detail:     []string{"Attributing the demand to a subnet would be a guess, so the rule stays silent for this pool. The other pools in the cluster are still counted."},
					Confidence: "n/a",
					Resources:  []string{addr, pool.owner},
				})
				continue
			}
			if pool.maxNodes == 0 || pool.maxPods == 0 || pool.subnet == "" {
				continue
			}

			want := pool.maxNodes * (1 + pool.maxPods)
			demand[pool.subnet] += want
			owners[pool.subnet] = append(owners[pool.subnet], pool.owner)
			reasons[pool.subnet] = append(reasons[pool.subnet], fmt.Sprintf(
				"node pool %q reaches %d nodes (%s) x (1 node address + %d pod addresses, %s) = %d addresses",
				pool.name, pool.maxNodes, pool.nodesHow, pool.maxPods, pool.podsHow, want))
		}
	}

	for _, sn := range f.ByType("azurerm_subnet") {
		addr := plan.Base(sn.Address)
		wanted := demand[addr]
		if wanted == 0 {
			continue
		}
		usable, cidr, ok := azUsableIPs(g, addr, subnet.ReservedAddresses)
		if !ok {
			continue
		}

		detail := append([]string{}, reasons[addr]...)
		detail = append(detail,
			fmt.Sprintf("%s is %s: %d usable addresses (Azure reserves %d in every subnet).",
				addr, cidr, usable, subnet.ReservedAddresses),
			"Azure CNI reserves the full max_pods worth of addresses per node the moment the node joins, so the subnet empties at the rate nodes are added and not at the rate pods are scheduled.",
			"Microsoft sizes this as "+aks.SubnetFormula+". The surge nodes are left out above, so the real demand during an upgrade is higher than the number quoted.",
			"Catalog note: "+aks.Notes)

		ratio := float64(wanted) / float64(usable)
		resources := append([]string{addr}, owners[addr]...)
		metrics := map[string]int{
			"demand":       wanted,
			"usable":       usable,
			"reserved":     subnet.ReservedAddresses,
			"break_at_pct": azRound(100 / ratio),
		}

		switch {
		case ratio > 1:
			out = append(out, Finding{
				Rule:     "AZ2",
				Severity: SeverityCritical,
				Title:    "AKS node subnet runs out of addresses before the cluster stops scaling",
				Summary: fmt.Sprintf(
					"%s (%s) offers %d usable addresses and the node pools placed in it reserve ~%d at full scale. Node creation starts failing at %.0f%% of the authorised scale, and neither the subnet prefix nor max_pods can be changed afterwards.",
					addr, cidr, usable, wanted, 100/ratio),
				Detail:     detail,
				Confidence: aks.Confidence,
				Resources:  resources,
				Source:     aks.Source,
				Metrics:    metrics,
			})
		case ratio >= opt.WarnAt:
			out = append(out, Finding{
				Rule:     "AZ2",
				Severity: SeverityWarning,
				Title:    "Thin address headroom in an AKS node subnet",
				Summary: fmt.Sprintf(
					"%s (%s) reaches %d of %d usable addresses at full scale (%.0f%%), which leaves nothing for the surge node a rolling upgrade adds.",
					addr, cidr, wanted, usable, ratio*100),
				Detail:     detail,
				Confidence: aks.Confidence,
				Resources:  resources,
				Source:     aks.Source,
				Metrics:    metrics,
			})
		}
	}
	return out
}
