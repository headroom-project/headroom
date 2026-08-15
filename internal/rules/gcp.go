package rules

import (
	"fmt"
	"net/netip"
	"strings"

	"github.com/headroom-project/headroom/internal/catalog"
	"github.com/headroom-project/headroom/internal/graph"
	"github.com/headroom-project/headroom/internal/plan"
)

func init() { Register(gcpRules) }

// gcpRules is the Google Cloud rule set. The ids are namespaced GC* so that AWS
// (R*) and Azure (AZ*) can grow without anyone renumbering anything.
//
// The GCP ceilings that actually hurt are not the ones with a quota page. They
// are the ones where a number in the terraform silently divides another number
// somewhere else: max_pods_per_node divides a pod CIDR into node slots,
// min_ports_per_vm divides a NAT address into VM slots, a Cloud SQL tier string
// divides memory into connections. None of those divisions is written down in
// the plan, and all three of them are one-way decisions.
func gcpRules(f *plan.File, g *graph.Graph, c *catalog.Catalog, opt Options) []Finding {
	var out []Finding
	out = append(out, gcpSQLConnections(f, g, c, opt)...)
	out = append(out, gcpGKEAddresses(f, g, c, opt)...)
	out = append(out, gcpNATPorts(f, g, c, opt)...)
	out = append(out, gcpDiskPerformance(f, c)...)
	out = append(out, gcpConnectorThroughput(f, g, c, opt)...)
	out = append(out, gcpSQLStorage(f, g, c)...)
	return out
}

// gcpAt walks into terraform's nested block encoding, where a block that appears
// once in HCL is still a one-element list in planned_values. Every capacity
// attribute worth reading on GCP lives one or two blocks deep
// (settings.tier, template.scaling.max_instance_count), so this is the workhorse.
func gcpAt(values map[string]any, path ...string) map[string]any {
	cur := values
	for _, key := range path {
		if cur == nil {
			return nil
		}
		switch v := cur[key].(type) {
		case []any:
			if len(v) == 0 {
				return nil
			}
			m, ok := v[0].(map[string]any)
			if !ok {
				return nil
			}
			cur = m
		case map[string]any:
			cur = v
		default:
			return nil
		}
	}
	return cur
}

// gcpBlocksAt returns every element of a repeated block, which is what a
// subnetwork's secondary ranges and a container's env vars actually are.
func gcpBlocksAt(values map[string]any, path ...string) []map[string]any {
	if len(path) == 0 {
		return nil
	}
	parent := values
	if len(path) > 1 {
		parent = gcpAt(values, path[:len(path)-1]...)
	}
	if parent == nil {
		return nil
	}
	list, ok := parent[path[len(path)-1]].([]any)
	if !ok {
		return nil
	}
	var out []map[string]any
	for _, item := range list {
		if m, ok := item.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

// gcpConsumerTypes are the resource types that scale in front of something
// fixed. Everything in this list has a resolvable maximum, which is the only
// reason it is in the list.
var gcpConsumerTypes = []string{
	"google_cloud_run_v2_service",
	"google_compute_region_instance_group_manager",
	"google_compute_instance_group_manager",
	"google_container_node_pool",
}

// gcpScaleOf resolves the maximum number of instances a GCP workload reaches.
// The declared size is never the ceiling: an autoscaler attached to the group is,
// and on Cloud Run the ceiling exists even when the terraform is silent, because
// Google applies a default maximum of 100 instances to every revision. Treating
// an unconfigured Cloud Run service as a one-instance service is the single
// easiest way to under-report demand on GCP.
func gcpScaleOf(g *graph.Graph, c *catalog.Catalog, addr string) (workload, bool) {
	w := workload{addr: addr}
	values := g.Values(addr)

	switch g.Type(addr) {
	case "google_cloud_run_v2_service":
		w.unit = "instances"
		if n, ok := plan.Num(gcpAt(values, "template", "scaling"), "max_instance_count"); ok && n > 0 {
			w.scale, w.how = n, "max_instance_count"
			return w, true
		}
		if run := c.GCPCloudRun(); run.DefaultMaxInstanceCount > 0 {
			w.scale = run.DefaultMaxInstanceCount
			w.how = fmt.Sprintf("no scaling block, so Cloud Run's default maximum of %d instances applies", run.DefaultMaxInstanceCount)
			return w, true
		}

	case "google_compute_region_instance_group_manager", "google_compute_instance_group_manager":
		w.unit = "instances"
		for _, t := range []string{"google_compute_region_autoscaler", "google_compute_autoscaler"} {
			for _, as := range g.ReferrersOfType(addr, t) {
				if n, ok := plan.Num(gcpAt(g.Values(as), "autoscaling_policy"), "max_replicas"); ok && n > 0 {
					w.scale, w.how = n, "max_replicas of "+as
					return w, true
				}
			}
		}
		if n, ok := plan.Num(values, "target_size"); ok && n > 0 {
			w.scale, w.how = n, "target_size, no autoscaler found"
			return w, true
		}

	case "google_container_node_pool":
		w.unit = "nodes"
		if n, ok := plan.Num(gcpAt(values, "autoscaling"), "max_node_count"); ok && n > 0 {
			w.scale, w.how = n, "max_node_count"
			return w, true
		}
		if n, ok := plan.Num(gcpAt(values, "autoscaling"), "total_max_node_count"); ok && n > 0 {
			w.scale, w.how = n, "total_max_node_count"
			return w, true
		}
		if n, ok := plan.Num(values, "node_count"); ok && n > 0 {
			w.scale, w.how = n, "node_count, no autoscaling block"
			return w, true
		}
	}
	return w, false
}

// gcpNodePoolNodes is the per-location node count of a node pool. A regional
// cluster multiplies the pool's node count by the number of zones, and that
// factor is invisible in the terraform, so it is called out rather than assumed.
func gcpNodePoolNodes(g *graph.Graph, c *catalog.Catalog, pool string) (int, string, bool) {
	w, ok := gcpScaleOf(g, c, pool)
	if !ok {
		return 0, "", false
	}
	return w.scale, w.how, true
}

// gcpPoolSizeOf looks for a declared connection pool size in the environment of
// a Cloud Run container. Only the integer is used and only the integer travels;
// the value itself never leaves the machine, which is why env is read here and
// is not in the extract allowlist.
func gcpPoolSizeOf(g *graph.Graph, addr string, fallback int) (int, string) {
	for _, container := range gcpBlocksAt(g.Values(addr), "template", "containers") {
		for _, env := range gcpBlocksAt(container, "env") {
			name := strings.ToUpper(strings.TrimSpace(plan.Str(env, "name")))
			for _, candidate := range poolEnvNames {
				if name != candidate {
					continue
				}
				if n := atoi(plan.Str(env, "value")); n > 0 {
					return n, plan.Str(env, "name") + "=" + plan.Str(env, "value") + " in " + addr
				}
			}
		}
	}
	return fallback, "assumed"
}

// gcpPrefixAddresses is the total address count of an IPv4 CIDR. GKE carves
// aligned blocks out of secondary ranges, so the arithmetic below is on prefix
// lengths rather than on host counts.
func gcpPrefixAddresses(cidr string) (addresses int, bits int, ok bool) {
	p, err := netip.ParsePrefix(strings.TrimSpace(cidr))
	if err != nil || !p.Addr().Is4() {
		return 0, 0, false
	}
	free := 32 - p.Bits()
	if free > 24 {
		// Wider than a /8 is not something a plan legitimately contains, and
		// shifting by it would overflow the count into nonsense.
		return 0, 0, false
	}
	return 1 << uint(free), p.Bits(), true
}

// gcpSecondaryRange finds a named secondary range on a subnetwork. GKE points at
// its pod and service ranges by name, and the name is a plain string in the
// cluster, so the CIDR only exists on the subnetwork side of the edge.
func gcpSecondaryRange(g *graph.Graph, subnet, name string) (string, bool) {
	if name == "" {
		return "", false
	}
	for _, r := range gcpBlocksAt(g.Values(subnet), "secondary_ip_range") {
		if plan.Str(r, "range_name") == name {
			if cidr := plan.Str(r, "ip_cidr_range"); cidr != "" {
				return cidr, true
			}
		}
	}
	return "", false
}

// gcpPct renders a saturation point the way the AWS rules do.
func gcpPct(ratio float64) int { return int(100/ratio + 0.5) }
