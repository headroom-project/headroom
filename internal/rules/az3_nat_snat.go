package rules

import (
	"fmt"

	"github.com/headroom-project/headroom/internal/catalog"
	"github.com/headroom-project/headroom/internal/graph"
	"github.com/headroom-project/headroom/internal/plan"
)

// azNATSNATPorts divides a NAT gateway's SNAT port inventory by the number of
// things behind it.
//
// This is the best finding in the Azure set because the ceiling is completely
// invisible. Every public IP on the gateway contributes exactly 64,512 ports,
// the subnets attached to it share one inventory, and nothing in the terraform
// mentions any of it. When the ports run out, connections do not slow down:
// individual outbound connections fail, intermittently, under load, to some
// destinations and not others, and the failure looks like the remote service
// being flaky.
//
// The pod arithmetic makes it much worse than it looks. An AKS cluster on Azure
// CNI puts every pod in the subnet, so the divisor is pods and not nodes.
func azNATSNATPorts(f *plan.File, g *graph.Graph, c *catalog.Catalog, opt Options) []Finding {
	nat := c.AzureNATGateway()
	lb := c.AzureLBSNAT()

	var out []Finding

	for _, gateway := range f.ByType("azurerm_nat_gateway") {
		addr := plan.Base(gateway.Address)

		ips, ipDetail := azNATAddresses(g, addr)
		if ips == 0 {
			out = append(out, Finding{
				Rule:       "AZ3",
				Severity:   SeverityInfo,
				Title:      "SNAT port inventory not evaluated",
				Summary:    fmt.Sprintf("%s has no public IP or public IP prefix attached in this plan, so its port inventory is not derivable here.", addr),
				Detail:     []string{fmt.Sprintf("Each attached public IP would contribute %d SNAT ports. If the address is attached from another repository, this gateway's real ceiling is in that plan.", nat.PortsPerPublicIP)},
				Confidence: "n/a",
				Resources:  []string{addr},
			})
			continue
		}

		subnets := azNATSubnets(g, addr)
		if len(subnets) == 0 {
			continue
		}

		endpoints := 0
		var detail []string
		resources := []string{addr}

		for _, subnet := range subnets {
			count, lines, owners := azSubnetEndpoints(f, g, c, subnet)
			endpoints += count
			detail = append(detail, lines...)
			resources = append(resources, owners...)
		}
		if endpoints == 0 {
			continue
		}

		ports := ips * nat.PortsPerPublicIP
		perEndpoint := ports / endpoints

		detail = append([]string{ipDetail}, detail...)
		detail = append(detail,
			fmt.Sprintf("%d SNAT ports shared by ~%d endpoints across %s is ~%d ports each.",
				ports, endpoints, azPlural(len(subnets), "subnet", "subnets"), perEndpoint),
			fmt.Sprintf("For scale, Azure Load Balancer preallocates %d ports per instance for a backend pool of 50 and never less than %d even for a pool of a thousand. Ports on a NAT gateway are shared on demand rather than preallocated, which is strictly better, but the inventory is still the inventory.",
				lb.MaxPerInstance, lb.MinPerInstance),
			fmt.Sprintf("A closed connection holds its port for %d seconds after a FIN before it can be reused to the same destination, and an idle one holds it for the %d minute idle timeout, so a service making many short-lived outbound calls consumes far more than one port per concurrent request.", 65, nat.DefaultTCPIdleMinutes),
			"Catalog note: "+nat.Notes)

		// The honest lever, and the one nobody reaches for, because the
		// symptom never points at the gateway.
		remedy := fmt.Sprintf(
			"Each extra public IP adds another %d ports, up to %d addresses. Private Link for Azure PaaS destinations removes the flows from SNAT entirely, which is usually the larger win.",
			nat.PortsPerPublicIP, nat.MaxPublicIPs)

		metrics := map[string]int{
			"ports_total":        ports,
			"ports_per_endpoint": perEndpoint,
			"endpoints":          endpoints,
			"public_ips":         ips,
			"subnets":            len(subnets),
		}

		switch {
		case perEndpoint < lb.MinPerInstance:
			out = append(out, Finding{
				Rule:     "AZ3",
				Severity: SeverityCritical,
				Title:    "NAT gateway runs out of SNAT ports before the workloads stop scaling",
				Summary: fmt.Sprintf(
					"%s carries %d SNAT ports from %s, and at full scale ~%d endpoints sit behind it: ~%d ports each, below the %d that Azure allocates per instance even for its largest documented backend pool. Outbound connections start failing intermittently well before that.",
					addr, ports, azPlural(ips, "public IP", "public IPs"), endpoints, perEndpoint, lb.MinPerInstance),
				Detail:     append(detail, remedy),
				Confidence: nat.Confidence,
				Resources:  resources,
				Source:     nat.Source,
				Metrics:    metrics,
			})
		case perEndpoint < lb.MaxPerInstance:
			out = append(out, Finding{
				Rule:     "AZ3",
				Severity: SeverityWarning,
				Title:    "Thin SNAT port headroom behind a NAT gateway",
				Summary: fmt.Sprintf(
					"%s carries %d SNAT ports and ~%d endpoints at full scale, which is ~%d ports each against the %d Azure hands one instance in a small backend pool. Chatty outbound traffic will feel this before anything else in the plan breaks.",
					addr, ports, endpoints, perEndpoint, lb.MaxPerInstance),
				Detail:     append(detail, remedy),
				Confidence: nat.Confidence,
				Resources:  resources,
				Source:     nat.Source,
				Metrics:    metrics,
			})
		}
	}
	return out
}

// azNATAddresses counts the public addresses feeding a gateway's port
// inventory, covering both a plain public IP and a prefix, where the whole
// prefix is consumed.
func azNATAddresses(g *graph.Graph, gateway string) (int, string) {
	ips := 0
	var parts []string

	for _, assoc := range g.ReferrersOfType(gateway, "azurerm_nat_gateway_public_ip_association") {
		if !contains(g.ReferencesIn(assoc, "azurerm_nat_gateway", "nat_gateway_id"), gateway) {
			continue
		}
		for _, pip := range g.ReferencesIn(assoc, "azurerm_public_ip", "public_ip_address_id") {
			n := g.InstanceCount(pip)
			ips += n
			parts = append(parts, fmt.Sprintf("%s (%s)", pip, azPlural(n, "address", "addresses")))
		}
	}

	for _, assoc := range g.ReferrersOfType(gateway, "azurerm_nat_gateway_public_ip_prefix_association") {
		if !contains(g.ReferencesIn(assoc, "azurerm_nat_gateway", "nat_gateway_id"), gateway) {
			continue
		}
		for _, prefix := range g.ReferencesIn(assoc, "azurerm_public_ip_prefix", "public_ip_prefix_id") {
			for _, values := range g.Instances(prefix) {
				bits, ok := plan.Num(values, "prefix_length")
				if !ok || bits < 0 || bits > 32 {
					continue
				}
				n := 1 << uint(32-bits)
				ips += n
				parts = append(parts, fmt.Sprintf("%s (/%d, %d addresses)", prefix, bits, n))
			}
		}
	}

	if ips == 0 {
		return 0, ""
	}
	return ips, "Port inventory comes from " + join(parts) + "."
}

func azNATSubnets(g *graph.Graph, gateway string) []string {
	seen := map[string]bool{}
	var out []string

	for _, assoc := range g.ReferrersOfType(gateway, "azurerm_subnet_nat_gateway_association") {
		if !contains(g.ReferencesIn(assoc, "azurerm_nat_gateway", "nat_gateway_id"), gateway) {
			continue
		}
		for _, subnet := range g.ReferencesIn(assoc, "azurerm_subnet", "subnet_id") {
			if !seen[subnet] {
				seen[subnet] = true
				out = append(out, subnet)
			}
		}
	}
	return out
}

// azSubnetEndpoints counts the things in a subnet that will make outbound
// connections. A pod is an endpoint, not a share of one: on Azure CNI it has
// its own address in this subnet and its own flows.
func azSubnetEndpoints(f *plan.File, g *graph.Graph, c *catalog.Catalog, subnet string) (int, []string, []string) {
	total := 0
	var lines, owners []string

	for _, cluster := range f.ByType("azurerm_kubernetes_cluster") {
		addr := plan.Base(cluster.Address)
		mode, flat := azNetworkMode(cluster.Values)
		if mode == "" {
			continue
		}
		for _, pool := range azNodePools(f, g, c, addr, mode) {
			if pool.subnet != subnet || pool.maxNodes == 0 {
				continue
			}
			count, how := pool.maxNodes, fmt.Sprintf("%d nodes", pool.maxNodes)
			if flat && pool.maxPods > 0 {
				count = pool.maxNodes * pool.maxPods
				how = fmt.Sprintf("%d nodes x %d pods, each with its own address in the subnet", pool.maxNodes, pool.maxPods)
			}
			total += count
			owners = append(owners, addr)
			lines = append(lines, fmt.Sprintf("%s node pool %q in %s: %s = %d endpoints", addr, pool.name, subnet, how, count))
		}
	}

	for _, wType := range azScalableWorkloads {
		if wType == "azurerm_service_plan" || wType == "azurerm_container_app" {
			continue
		}
		for _, workload := range g.ReferrersOfType(subnet, wType) {
			w, ok := azScaleOf(g, workload)
			if !ok {
				continue
			}
			total += w.scale
			owners = append(owners, workload)
			lines = append(lines, fmt.Sprintf("%s in %s scales to %d %s (%s) = %d endpoints",
				workload, subnet, w.scale, w.unit, w.how, w.scale))
		}
	}

	nics := 0
	for _, nic := range g.ReferrersOfType(subnet, "azurerm_network_interface") {
		nics += g.InstanceCount(nic)
		owners = append(owners, nic)
	}
	if nics > 0 {
		total += nics
		lines = append(lines, fmt.Sprintf("%d standalone network interface(s) in %s", nics, subnet))
	}

	return total, lines, owners
}
