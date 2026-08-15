package rules

import (
	"fmt"

	"github.com/headroom-project/headroom/internal/catalog"
	"github.com/headroom-project/headroom/internal/graph"
	"github.com/headroom-project/headroom/internal/plan"
)

// gcpNATPorts compares the VMs behind a Cloud NAT gateway against the number the
// gateway's addresses can actually serve.
//
// This is the AWS NAT finding's meaner cousin. AWS gives you one number to worry
// about; Cloud NAT gives you two that multiply, and static port allocation
// reserves min_ports_per_vm for every VM at placement time whether the VM opens
// a single connection or not. So the moment someone raises min_ports_per_vm to
// stop connection failures inside a busy VM -- the obvious fix, and the one the
// error message points at -- they divide the number of VMs the gateway can serve
// by the same factor. The failure is not a clean outage either: the VMs that
// arrive after the ports run out simply have no egress, which looks like a bad
// node rather than a bad gateway.
func gcpNATPorts(f *plan.File, g *graph.Graph, c *catalog.Catalog, opt Options) []Finding {
	var out []Finding

	for _, nat := range f.ByType("google_compute_router_nat") {
		addr := plan.Base(nat.Address)

		cat := c.GCPPublicNAT()
		if plan.Str(nat.Values, "type") == "PRIVATE" {
			cat = c.GCPPrivateNAT()
		}
		if cat.PortsPerIP == 0 {
			continue
		}

		subnets := gcpNATSubnets(g, addr, plan.Str(nat.Values, "source_subnetwork_ip_ranges_to_nat"))
		demand, detail, resources := gcpVMsBehind(g, c, subnets)
		if demand == 0 {
			continue
		}

		// AUTO_ONLY lets Google add addresses as the gateway needs them, so
		// there is no fixed ceiling in the plan to compare against. Saying so is
		// the whole output: a silent absence reads as a clean bill of health.
		if plan.Str(nat.Values, "nat_ip_allocate_option") != "MANUAL_ONLY" {
			auto := c.GCPNATAutoAllocation()
			out = append(out, Finding{
				Rule:       "GC3",
				Severity:   SeverityInfo,
				Title:      "NAT port ceiling not evaluated",
				Summary:    fmt.Sprintf("%s allocates its external addresses automatically, so this plan pins no port ceiling for the %d VMs behind it.", addr, demand),
				Detail:     []string{auto.SkipReason},
				Confidence: "n/a",
				Resources:  append([]string{addr}, resources...),
				Source:     auto.Source,
				Metrics:    map[string]int{"vms": demand},
			})
			continue
		}

		ips := 0
		for _, a := range g.RefsOfType(addr, "google_compute_address") {
			ips += g.InstanceCount(a)
		}
		if ips == 0 {
			continue
		}

		minPorts, portsHow := cat.DefaultMinPortsPerVM, fmt.Sprintf("min_ports_per_vm not set, so the default of %d applies", cat.DefaultMinPortsPerVM)
		if n, ok := plan.Num(nat.Values, "min_ports_per_vm"); ok && n > 0 {
			minPorts, portsHow = n, fmt.Sprintf("min_ports_per_vm = %d", n)
		}
		if minPorts <= 0 {
			continue
		}
		multiplier := cat.PortMultiplier
		if multiplier < 1 {
			multiplier = 1
		}
		capacity := ips * cat.PortsPerIP / (minPorts * multiplier)
		if capacity == 0 {
			continue
		}

		natDetail := append([]string{}, detail...)
		plural := "es"
		if ips == 1 {
			plural = ""
		}
		natDetail = append(natDetail, fmt.Sprintf(
			"%s holds %d external address%s x %d ports, at %s = %d VMs.",
			addr, ips, plural, cat.PortsPerIP, portsHow, capacity))
		natDetail = append(natDetail, cat.Notes)
		if n, ok := plan.Num(nat.Values, "min_ports_per_vm"); ok && n > cat.DefaultMinPortsPerVM {
			natDetail = append(natDetail, fmt.Sprintf(
				"Raising min_ports_per_vm from the default %d to %d was a decision about one VM's connection count; it divided the gateway's VM capacity by %d at the same time.",
				cat.DefaultMinPortsPerVM, n, n/cat.DefaultMinPortsPerVM))
		}
		natDetail = append(natDetail,
			"Adding one more external address to the gateway doubles the VM capacity and changes nothing else.")

		ratio := float64(demand) / float64(capacity)
		metrics := map[string]int{
			"vms":              demand,
			"vm_capacity":      capacity,
			"nat_ips":          ips,
			"min_ports_per_vm": minPorts,
			"break_at_pct":     gcpPct(ratio),
		}
		resources = append([]string{addr}, resources...)

		switch {
		case ratio > 1:
			out = append(out, Finding{
				Rule:     "GC3",
				Severity: SeverityCritical,
				Title:    "Cloud NAT runs out of ports before the workloads stop scaling",
				Summary: fmt.Sprintf(
					"%s can serve %d VMs at %d ports each, and the workloads behind it reach %d at full scale. VMs stop getting outbound connectivity at %.0f%% of the scale this plan authorises.",
					addr, capacity, minPorts, demand, 100/ratio),
				Detail:     natDetail,
				Confidence: cat.Confidence,
				Resources:  resources,
				Source:     cat.Source,
				Metrics:    metrics,
			})
		case ratio >= opt.WarnAt:
			out = append(out, Finding{
				Rule:     "GC3",
				Severity: SeverityWarning,
				Title:    "Thin NAT port headroom",
				Summary: fmt.Sprintf(
					"The workloads behind %s reach %d of the %d VMs it can serve at %d ports each (%.0f%%), leaving no room for a rollout that briefly runs old and new nodes together.",
					addr, demand, capacity, minPorts, ratio*100),
				Detail:     natDetail,
				Confidence: cat.Confidence,
				Resources:  resources,
				Source:     cat.Source,
				Metrics:    metrics,
			})
		}
	}
	return out
}

// gcpNATSubnets resolves which subnetworks a gateway actually covers. The
// difference matters: a gateway scoped to one subnetwork is not responsible for
// the VMs in the others, and counting them would invent demand.
func gcpNATSubnets(g *graph.Graph, nat, scope string) []string {
	if scope == "LIST_OF_SUBNETWORKS" {
		return g.ReferencesIn(nat, "google_compute_subnetwork", "subnetwork")
	}
	var out []string
	seen := map[string]bool{}
	for _, router := range g.RefsOfType(nat, "google_compute_router") {
		for _, network := range g.RefsOfType(router, "google_compute_network") {
			for _, subnet := range g.ReferrersOfType(network, "google_compute_subnetwork") {
				if !seen[subnet] {
					seen[subnet] = true
					out = append(out, subnet)
				}
			}
		}
	}
	sortStrings(out)
	return out
}

// gcpVMsBehind counts the real VMs a set of subnetworks will hold at full scale.
// GKE nodes are VMs like any other and they are usually the largest population,
// which is exactly why a gateway sized for the managed instance group next to
// them comes up short.
func gcpVMsBehind(g *graph.Graph, c *catalog.Catalog, subnets []string) (total int, detail []string, resources []string) {
	seen := map[string]bool{}

	add := func(addr string, count int, how string) {
		if seen[addr] || count == 0 {
			return
		}
		seen[addr] = true
		total += count
		resources = append(resources, addr)
		detail = append(detail, how)
	}

	for _, subnet := range subnets {
		for _, cluster := range g.ReferrersOfType(subnet, "google_container_cluster") {
			for _, pool := range g.ReferrersOfType(cluster, "google_container_node_pool") {
				if nodes, how, ok := gcpNodePoolNodes(g, c, pool); ok {
					add(pool, nodes, fmt.Sprintf("%s scales to %d nodes (%s), and every node is a VM behind the gateway", pool, nodes, how))
				}
			}
		}

		for _, tmpl := range g.ReferrersOfType(subnet, "google_compute_instance_template") {
			for _, t := range []string{"google_compute_region_instance_group_manager", "google_compute_instance_group_manager"} {
				for _, mig := range g.ReferrersOfType(tmpl, t) {
					if w, ok := gcpScaleOf(g, c, mig); ok {
						add(mig, w.scale, fmt.Sprintf("%s scales to %d instances (%s)", mig, w.scale, w.how))
					}
				}
			}
		}

		for _, vm := range g.ReferrersOfType(subnet, "google_compute_instance") {
			add(vm, g.InstanceCount(vm), fmt.Sprintf("%s is %d fixed instance(s)", vm, g.InstanceCount(vm)))
		}
	}
	sortStrings(resources)
	return total, detail, resources
}
