package rules

import (
	"fmt"

	"github.com/headroom-project/headroom/internal/catalog"
	"github.com/headroom-project/headroom/internal/graph"
	"github.com/headroom-project/headroom/internal/plan"
)

// gcpServicesWarnBelow is headroom's own reporting threshold, not a Google
// number: below a /22 the services secondary range is materially smaller than
// the range GKE would have picked, and it cannot be changed for the life of the
// cluster. It is a judgement call, so the finding never goes past warning and
// says where the number came from.
const gcpServicesWarnBelow = 1024

// gcpGKEAddresses is the headline GCP rule: three separate address ranges feed
// one cluster, they run out at completely different node counts, and only one of
// them looks like it is about node counts.
//
// The pod range is the one that surprises people. GKE does not hand out pod
// addresses one at a time; it carves an aligned block per node, always at least
// twice max_pods_per_node, so at the default 110 pods a node costs a /24. A
// /21 pod range therefore holds eight nodes, not two thousand pods' worth of
// them, and the node pool next to it happily autoscales to thirty. Nothing in
// terraform relates the two numbers, none of the three ranges can be resized
// after the cluster exists, and the failure mode is nodes that simply refuse to
// join under load.
func gcpGKEAddresses(f *plan.File, g *graph.Graph, c *catalog.Catalog, opt Options) []Finding {
	var out []Finding
	podCat := c.GCPPodRange()
	nodeCat := c.GCPNodeRange()
	svcCat := c.GCPServicesRange()

	for _, cluster := range f.ByType("google_container_cluster") {
		addr := plan.Base(cluster.Address)

		subnets := g.RefsOfType(addr, "google_compute_subnetwork")
		if len(subnets) == 0 {
			continue
		}
		subnet := subnets[0]
		alloc := gcpAt(cluster.Values, "ip_allocation_policy")

		clusterMaxPods, hasClusterDefault := plan.Num(cluster.Values, "default_max_pods_per_node")

		// Demand is measured in addresses, not nodes, because two node pools
		// with different max_pods_per_node consume the same range at different
		// rates and averaging them would understate the larger one.
		podAddresses, nodeDemand := 0, 0
		blockNetmask, uniformBlocks := 0, true
		var pools []string
		var detail []string

		for _, pool := range g.ReferrersOfType(addr, "google_container_node_pool") {
			nodes, how, known := gcpNodePoolNodes(g, c, pool)
			if !known {
				continue
			}
			maxPods, source := podCat.DefaultMaxPodsPerNode, "GKE default"
			if hasClusterDefault && clusterMaxPods > 0 {
				maxPods, source = clusterMaxPods, "default_max_pods_per_node on "+addr
			}
			if n, ok := plan.Num(g.Values(pool), "max_pods_per_node"); ok && n > 0 {
				maxPods, source = n, "max_pods_per_node"
			}
			netmask, ok := c.GCPPodBlockNetmask(maxPods)
			if !ok {
				continue
			}
			block := 1 << uint(32-netmask)

			if blockNetmask == 0 {
				blockNetmask = netmask
			} else if blockNetmask != netmask {
				uniformBlocks = false
			}
			pools = append(pools, pool)
			nodeDemand += nodes
			podAddresses += nodes * block

			detail = append(detail, fmt.Sprintf(
				"%s scales to %d nodes (%s) at %d pods per node (%s), and GKE reserves a /%d of %d pod addresses per node = %d addresses",
				pool, nodes, how, maxPods, source, netmask, block, nodes*block))
		}
		if len(pools) == 0 {
			continue
		}
		resources := append([]string{addr, subnet}, pools...)

		// 1. The pod secondary range against the blocks the nodes will claim.
		podName := plan.Str(alloc, "cluster_secondary_range_name")
		podCIDR, found := gcpSecondaryRange(g, subnet, podName)
		if !found {
			podCIDR = plan.Str(alloc, "cluster_ipv4_cidr_block")
			found = podCIDR != ""
			podName = "cluster_ipv4_cidr_block"
		}
		if total, _, ok := gcpPrefixAddresses(podCIDR); ok && found && podAddresses > 0 {
			ratio := float64(podAddresses) / float64(total)
			capacityNodes := 0
			nodeText := ""
			if uniformBlocks {
				capacityNodes = total / (1 << uint(32-blockNetmask))
				nodeText = fmt.Sprintf(" That is room for %d nodes at a /%d each, against the %d this plan authorises.",
					capacityNodes, blockNetmask, nodeDemand)
			}

			podDetail := append([]string{}, detail...)
			podDetail = append(podDetail, fmt.Sprintf(
				"The %q secondary range on %s is %s: %d addresses in total.", podName, subnet, podCIDR, total))
			podDetail = append(podDetail, podCat.Notes)
			podDetail = append(podDetail, podCat.ResizeNotes)

			metrics := map[string]int{
				"pod_range_addresses": total,
				"pod_addresses_used":  podAddresses,
				"node_demand":         nodeDemand,
				"node_capacity":       capacityNodes,
				"break_at_pct":        gcpPct(ratio),
			}
			switch {
			case ratio > 1:
				out = append(out, Finding{
					Rule:     "GC2",
					Severity: SeverityCritical,
					Title:    "GKE pod range runs out of addresses before the cluster stops scaling",
					Summary: fmt.Sprintf(
						"The pod secondary range behind %s is %s and the node pools in front of it need %d of its %d addresses at full scale.%s Nodes stop joining at %.0f%% of the authorised scale, and the range cannot be resized afterwards.",
						addr, podCIDR, podAddresses, total, nodeText, 100/ratio),
					Detail:     podDetail,
					Confidence: podCat.Confidence,
					Resources:  resources,
					Source:     podCat.Source,
					Metrics:    metrics,
				})
			case ratio >= opt.WarnAt:
				out = append(out, Finding{
					Rule:     "GC2",
					Severity: SeverityWarning,
					Title:    "Thin pod address headroom in GKE",
					Summary: fmt.Sprintf(
						"The pod secondary range behind %s (%s) reaches %d of %d addresses at full scale (%.0f%%).%s A surge upgrade adds a node before it removes one, so the last node of a rollout has nowhere to go.",
						addr, podCIDR, podAddresses, total, ratio*100, nodeText),
					Detail:     podDetail,
					Confidence: podCat.Confidence,
					Resources:  resources,
					Source:     podCat.Source,
					Metrics:    metrics,
				})
			}
		}

		// 2. The subnet's primary range, where the nodes themselves live.
		if total, bits, ok := gcpPrefixAddresses(plan.Str(g.Values(subnet), "ip_cidr_range")); ok && nodeDemand > 0 {
			usable := total - nodeCat.ReservedAddresses
			if usable > 0 {
				ratio := float64(nodeDemand) / float64(usable)
				nodeDetail := append([]string{}, detail...)
				nodeDetail = append(nodeDetail, fmt.Sprintf(
					"%s is a /%d: %d addresses, %d usable after the four Google Cloud reserves in every primary range.",
					subnet, bits, total, usable))
				nodeDetail = append(nodeDetail, nodeCat.Notes)

				metrics := map[string]int{
					"node_demand":  nodeDemand,
					"usable":       usable,
					"break_at_pct": gcpPct(ratio),
				}
				switch {
				case ratio > 1:
					out = append(out, Finding{
						Rule:     "GC2",
						Severity: SeverityCritical,
						Title:    "GKE node subnet runs out of addresses before the cluster stops scaling",
						Summary: fmt.Sprintf(
							"%s offers %d usable addresses and the node pools of %s need %d at full scale. Node creation starts failing at %.0f%% of the authorised scale, and everything else sharing the subnet is competing for the same addresses.",
							subnet, usable, addr, nodeDemand, 100/ratio),
						Detail:     nodeDetail,
						Confidence: nodeCat.Confidence,
						Resources:  resources,
						Source:     nodeCat.Source,
						Metrics:    metrics,
					})
				case ratio >= opt.WarnAt:
					out = append(out, Finding{
						Rule:     "GC2",
						Severity: SeverityWarning,
						Title:    "Thin node address headroom in the GKE subnet",
						Summary: fmt.Sprintf(
							"The node pools of %s reach %d of the %d usable addresses in %s at full scale (%.0f%%), before anything else in the subnet is counted.",
							addr, nodeDemand, usable, subnet, ratio*100),
						Detail:     nodeDetail,
						Confidence: nodeCat.Confidence,
						Resources:  resources,
						Source:     nodeCat.Source,
						Metrics:    metrics,
					})
				}
			}
		}

		// 3. The services range, which has no demand side in terraform at all.
		// The number is stated rather than compared, so it never goes critical.
		svcName := plan.Str(alloc, "services_secondary_range_name")
		svcCIDR, svcFound := gcpSecondaryRange(g, subnet, svcName)
		if !svcFound {
			svcCIDR = plan.Str(alloc, "services_ipv4_cidr_block")
			svcFound = svcCIDR != ""
		}
		if total, _, ok := gcpPrefixAddresses(svcCIDR); ok && svcFound && total < gcpServicesWarnBelow {
			out = append(out, Finding{
				Rule:     "GC2",
				Severity: SeverityWarning,
				Title:    "GKE service range is fixed and small",
				Summary: fmt.Sprintf(
					"The services secondary range behind %s is %s, so the cluster can ever hold %d Services against the %d GKE would have given it. Every ClusterIP Service spends one, and the range cannot be changed without rebuilding the cluster.",
					addr, svcCIDR, total, svcCat.DefaultMaxServices),
				Detail: []string{
					svcCat.Notes,
					fmt.Sprintf("A team on Helm charts reaches %d Services faster than it expects: each chart brings several, and headless and per-shard Services count too.", total),
					fmt.Sprintf("This one is a judgement call, not a Google limit: headroom reports service ranges holding fewer than %d addresses because the ceiling is permanent, and the design may well be deliberate.", gcpServicesWarnBelow),
				},
				Confidence: svcCat.Confidence,
				Resources:  []string{addr, subnet},
				Source:     svcCat.Source,
				Metrics: map[string]int{
					"max_services":     total,
					"default_services": svcCat.DefaultMaxServices,
				},
			})
		}
	}
	return out
}
