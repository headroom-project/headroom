package rules

import (
	"fmt"
	"math"
	"net/netip"
	"strings"

	"github.com/headroom-project/headroom/internal/catalog"
	"github.com/headroom-project/headroom/internal/graph"
	"github.com/headroom-project/headroom/internal/plan"
)

func init() { Register(azureRules) }

// azureRules is the Azure half of the catalog. The shape of every finding is
// the same as the AWS set, because the underlying statement is the same:
// something in front scales and something behind it does not. What changes is
// where the ceiling is written down, and on Azure it is written down in more
// places, because the platform sells performance in fixed tiers rather than
// deriving it from size.
func azureRules(f *plan.File, g *graph.Graph, c *catalog.Catalog, opt Options) []Finding {
	var out []Finding
	out = append(out, azDatabaseConnections(f, g, c, opt)...)
	out = append(out, azAKSSubnetIPs(f, g, c, opt)...)
	out = append(out, azNATSNATPorts(f, g, c, opt)...)
	out = append(out, azVPNGatewayThroughput(f, g, c)...)
	out = append(out, azDiskVMAsymmetry(f, g, c)...)
	out = append(out, azBurstableCPU(f, c)...)
	return out
}

// azWorkload is a scalable consumer whose ceiling the plan actually states.
// Azure hides the number in three different places depending on the service,
// and an autoscale setting attached to the resource always outranks the
// declared instance count, exactly as an autoscaling target does on AWS.
type azWorkload struct {
	addr  string
	scale int
	unit  string
	how   string
}

// azHostedApps are the app-shaped resources that sit on a service plan. Their
// scale belongs to the plan underneath, not to themselves.
var azHostedApps = []string{
	"azurerm_linux_web_app",
	"azurerm_windows_web_app",
	"azurerm_linux_function_app",
	"azurerm_windows_function_app",
}

// azScalableWorkloads are the resources that carry their own instance count.
var azScalableWorkloads = []string{
	"azurerm_linux_virtual_machine_scale_set",
	"azurerm_windows_virtual_machine_scale_set",
	"azurerm_orchestrated_virtual_machine_scale_set",
	"azurerm_container_app",
	"azurerm_service_plan",
}

// azScaleOf resolves the maximum number of instances a workload can reach.
func azScaleOf(g *graph.Graph, addr string) (azWorkload, bool) {
	w := azWorkload{addr: addr}

	switch g.Type(addr) {
	case "azurerm_service_plan":
		w.unit = "workers"
		if max, from, ok := azAutoscaleMax(g, addr); ok {
			w.scale, w.how = max, "maximum capacity of "+from
			return w, true
		}
		if n, ok := plan.Num(g.Values(addr), "worker_count"); ok && n > 0 {
			w.scale, w.how = n, "worker_count, no autoscale setting found"
			return w, true
		}

	case "azurerm_linux_virtual_machine_scale_set",
		"azurerm_windows_virtual_machine_scale_set",
		"azurerm_orchestrated_virtual_machine_scale_set":
		w.unit = "instances"
		if max, from, ok := azAutoscaleMax(g, addr); ok {
			w.scale, w.how = max, "maximum capacity of "+from
			return w, true
		}
		for _, key := range []string{"instances", "sku_capacity"} {
			if n, ok := plan.Num(g.Values(addr), key); ok && n > 0 {
				w.scale, w.how = n, key+", no autoscale setting found"
				return w, true
			}
		}

	case "azurerm_container_app":
		w.unit = "replicas"
		if n, ok := azContainerAppMaxReplicas(g.Values(addr)); ok {
			w.scale, w.how = n, "max_replicas in the template"
			return w, true
		}
	}
	return w, false
}

// azAutoscaleMax reads the ceiling out of an autoscale setting pointed at this
// resource. Direction is read from target_resource_id specifically: the same
// setting also names resources under metric_trigger, and a metric on a
// different resource is not a scaling relationship.
func azAutoscaleMax(g *graph.Graph, addr string) (int, string, bool) {
	best, from := 0, ""

	for _, setting := range g.ReferrersOfType(addr, "azurerm_monitor_autoscale_setting") {
		if !contains(g.ReferencesIn(setting, g.Type(addr), "target_resource_id"), plan.Base(addr)) {
			continue
		}
		profiles, ok := g.Values(setting)["profile"].([]any)
		if !ok {
			continue
		}
		// A setting can hold several profiles (a weekday one, a weekend one).
		// The ceiling is the largest of them, because any of them can be the
		// active one when the traffic arrives.
		for _, item := range profiles {
			profile, ok := item.(map[string]any)
			if !ok {
				continue
			}
			caps, ok := profile["capacity"].([]any)
			if !ok {
				continue
			}
			for _, capItem := range caps {
				capacity, ok := capItem.(map[string]any)
				if !ok {
					continue
				}
				if max, ok := plan.Num(capacity, "maximum"); ok && max > best {
					best, from = max, setting
				}
			}
		}
	}
	return best, from, best > 0
}

func azContainerAppMaxReplicas(values map[string]any) (int, bool) {
	templates, ok := values["template"].([]any)
	if !ok {
		return 0, false
	}
	for _, item := range templates {
		template, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if n, ok := plan.Num(template, "max_replicas"); ok && n > 0 {
			return n, true
		}
	}
	return 0, false
}

// azPoolSizeOf looks for a declared connection pool size in the settings of an
// app. Same idea and the same environment variable names as the ECS task
// definition on AWS, because it is the same application code being deployed;
// only the place the platform puts the variable changes.
//
// Nothing read here is ever uploaded: the derived integer is, the settings map
// is not, which is why app_settings is absent from the extract allowlist.
func azPoolSizeOf(g *graph.Graph, addr string, fallback int) (int, string) {
	values := g.Values(addr)

	if settings, ok := values["app_settings"].(map[string]any); ok {
		for key, raw := range settings {
			value, ok := raw.(string)
			if !ok {
				continue
			}
			if n := azMatchPoolEnv(key, value); n > 0 {
				return n, key + "=" + value + " in " + addr
			}
		}
	}

	// Container apps put the same thing under template.container.env.
	if templates, ok := values["template"].([]any); ok {
		for _, item := range templates {
			template, ok := item.(map[string]any)
			if !ok {
				continue
			}
			containers, ok := template["container"].([]any)
			if !ok {
				continue
			}
			for _, ct := range containers {
				container, ok := ct.(map[string]any)
				if !ok {
					continue
				}
				envs, ok := container["env"].([]any)
				if !ok {
					continue
				}
				for _, e := range envs {
					env, ok := e.(map[string]any)
					if !ok {
						continue
					}
					name, value := plan.Str(env, "name"), plan.Str(env, "value")
					if n := azMatchPoolEnv(name, value); n > 0 {
						return n, name + "=" + value + " in " + addr
					}
				}
			}
		}
	}

	return fallback, "assumed"
}

func azMatchPoolEnv(name, value string) int {
	upper := strings.ToUpper(strings.TrimSpace(name))
	for _, candidate := range poolEnvNames {
		if upper == candidate {
			return atoi(value)
		}
	}
	return 0
}

// azSubnetPrefixes returns every CIDR a subnet block declares. Azure allows
// several prefixes on one subnet, and every one of them contributes addresses.
func azSubnetPrefixes(values map[string]any) []string {
	raw, ok := values["address_prefixes"].([]any)
	if !ok {
		return nil
	}
	var out []string
	for _, item := range raw {
		if s, ok := item.(string); ok && s != "" {
			out = append(out, s)
		}
	}
	return out
}

// azUsableIPs is the number of addresses a subnet can actually hand out. Azure
// reserves five in every subnet, the same count as AWS for entirely different
// reasons: network, gateway, two for the DNS mapping, and broadcast.
func azUsableIPs(g *graph.Graph, subnet string, reserved int) (int, string, bool) {
	total, cidr := 0, ""

	for _, values := range g.Instances(subnet) {
		for _, prefix := range azSubnetPrefixes(values) {
			p, err := netip.ParsePrefix(prefix)
			if err != nil || !p.Addr().Is4() {
				continue
			}
			bits := 32 - p.Bits()
			if bits > 24 {
				return 0, "", false
			}
			size := 1 << uint(bits)
			if size <= reserved {
				continue
			}
			total += size - reserved
			if cidr == "" {
				cidr = prefix
			} else {
				cidr += " + " + prefix
			}
		}
	}
	return total, cidr, total > 0
}

// azNodePool is one AKS node pool flattened into the two numbers that matter to
// an address plan: how many nodes it can reach, and how many pods each node
// reserves addresses for.
type azNodePool struct {
	owner      string
	name       string
	maxNodes   int
	maxPods    int
	podsHow    string
	nodesHow   string
	subnet     string
	podSubnet  bool
	unresolved string
}

// azNodePools flattens a cluster's inline default_node_pool and every attached
// azurerm_kubernetes_cluster_node_pool.
func azNodePools(f *plan.File, g *graph.Graph, c *catalog.Catalog, cluster string, mode string) []azNodePool {
	aks := c.AzureAKS()
	defaultPods := aks.DefaultMaxPods[mode]

	var out []azNodePool

	for _, values := range g.Instances(cluster) {
		pools, ok := values["default_node_pool"].([]any)
		if !ok {
			continue
		}
		for _, item := range pools {
			pool, ok := item.(map[string]any)
			if !ok {
				continue
			}
			p := azPoolFrom(pool, cluster, defaultPods)
			p.subnet, p.podSubnet, p.unresolved = azPoolSubnet(g, cluster, pool, "default_node_pool")
			out = append(out, p)
		}
	}

	for _, pool := range g.ReferrersOfType(cluster, "azurerm_kubernetes_cluster_node_pool") {
		for _, values := range g.Instances(pool) {
			p := azPoolFrom(values, pool, defaultPods)
			p.subnet, p.podSubnet, p.unresolved = azPoolSubnet(g, pool, values, "vnet_subnet_id", "pod_subnet_id")
			out = append(out, p)
		}
	}
	return out
}

// azFlag reads a boolean that the provider has spelled more than one way,
// preferring the first name given. An absent flag and a false flag are the same
// thing here: the feature is off.
func azFlag(values map[string]any, names ...string) bool {
	for _, name := range names {
		if v, ok := values[name].(bool); ok {
			return v
		}
	}
	return false
}

func azPoolFrom(values map[string]any, owner string, defaultPods int) azNodePool {
	p := azNodePool{owner: owner, name: plan.Str(values, "name")}

	// azurerm 4.0 renamed this flag from enable_auto_scaling. Both names are
	// read, the newer one first, because the two providers describe the same
	// cluster and a plan cannot be worth a different verdict for having been
	// generated by the older one. Reading only the 4.x name made a 3.x plan fall
	// through to node_count and then say "no autoscaling" about a pool that is
	// autoscaling, which is worse than silence: it is the tool asserting the
	// opposite of what the plan states.
	autoscaling := azFlag(values, "auto_scaling_enabled", "enable_auto_scaling")
	if max, ok := plan.Num(values, "max_count"); ok && autoscaling && max > 0 {
		p.maxNodes, p.nodesHow = max, "max_count with autoscaling enabled"
	} else if n, ok := plan.Num(values, "node_count"); ok && n > 0 {
		p.maxNodes, p.nodesHow = n, "node_count, no autoscaling"
	}

	if pods, ok := plan.Num(values, "max_pods"); ok && pods > 0 {
		p.maxPods, p.podsHow = pods, "max_pods"
	} else if defaultPods > 0 {
		p.maxPods, p.podsHow = defaultPods, "the default for this network plugin, not declared in the plan"
	}
	return p
}

// azPoolSubnet resolves which subnet a pool draws from. A pool that sets
// pod_subnet_id splits its demand across two subnets, and the reference graph
// is keyed per block rather than per attribute, so the two cannot be told
// apart: the pool is reported as unresolved instead of guessed at.
func azPoolSubnet(g *graph.Graph, owner string, values map[string]any, blocks ...string) (string, bool, string) {
	if id, ok := values["pod_subnet_id"]; ok && id != nil && id != "" {
		return "", true, "sets pod_subnet_id, so nodes and pods draw from two different subnets and this plan cannot say which is which"
	}
	subnets := g.ReferencesIn(owner, "azurerm_subnet", blocks...)
	if len(subnets) == 0 {
		return "", false, "does not reference a subnet in this plan"
	}
	if len(subnets) > 1 {
		return "", false, "references more than one subnet from the same block"
	}
	return subnets[0], false, ""
}

// azNetworkMode names the address model a cluster uses, which decides whether
// pods take addresses out of the virtual network at all.
func azNetworkMode(values map[string]any) (mode string, flat bool) {
	profiles, ok := values["network_profile"].([]any)
	if !ok {
		return "", false
	}
	for _, item := range profiles {
		profile, ok := item.(map[string]any)
		if !ok {
			continue
		}
		plugin := strings.ToLower(plan.Str(profile, "network_plugin"))
		pluginMode := strings.ToLower(plan.Str(profile, "network_plugin_mode"))

		switch {
		case plugin == "azure" && pluginMode == "overlay":
			return "azure_overlay", false
		case plugin == "azure":
			return "azure_node_subnet", true
		case plugin == "kubenet" || plugin == "none":
			return "kubenet", false
		}
	}
	return "", false
}

// azVMSizeOfWorkload finds the VM size behind a resource, following the node
// pool and scale set indirections.
func azVMSizeOf(values map[string]any) string {
	for _, key := range []string{"size", "vm_size", "sku"} {
		if s := plan.Str(values, key); s != "" {
			return s
		}
	}
	return ""
}

func azRound(f float64) int { return int(math.Round(f)) }

// azPct prints a percentage without inventing precision: 20% stays 20%, and
// the B4ms baseline of 22.5% keeps its half.
func azPct(v float64) string {
	if v == math.Trunc(v) {
		return fmt.Sprintf("%.0f%%", v)
	}
	return fmt.Sprintf("%.1f%%", v)
}

// azPlural keeps the prose readable when a count happens to be one. A report
// that says "1 subnet(s)" reads like a bug, and a capacity report is only worth
// anything if a human trusts it.
func azPlural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}
