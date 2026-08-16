// Package graph derives the resource reference graph from the configuration
// section of a terraform plan.
//
// The dependency graph terraform exposes tells you who *references* whom. What
// capacity analysis needs is who *talks* to whom, and those are not the same
// thing. The reference graph is the substrate: rules walk it to reconstruct the
// real topology (a security group ingress rule pointing at another security
// group is a declared, trustworthy network edge, and it does not depend on
// anyone having written depends_on).
package graph

import (
	"strings"

	"github.com/headroom-project/headroom/internal/plan"
)

type Graph struct {
	refs    map[string]map[string]bool
	revRefs map[string]map[string]bool
	types   map[string]string
	values  map[string]map[string]any
	exprs   map[string]map[string]any

	// Edges are declared once per resource block, but a block with for_each
	// becomes many real resources with different sizes and zones. Topology is
	// keyed by the base address; capacity has to count the instances.
	instances map[string][]map[string]any

	// Module outputs, keyed by the address a caller writes
	// ("module.compute.vm_id"), holding the addresses the output returns. An
	// output that returns another module's output is normal, so resolving one
	// is a walk and not a lookup.
	outputs map[string][]string

	// Every output a module call declares, keyed by the call. When a module is
	// indexed dynamically, terraform records the reference as the bare call
	// ("module.sql_vms") and the output name never appears, so the only way back
	// to a resource is through all of them.
	moduleOuts map[string][]string

	// What a resource iterates over, per resource. A block that says each.value
	// is naming an element of this collection, and without it the reference is
	// unresolvable at the block level even though the plan states it plainly.
	iter map[string][]string
}

func Build(f *plan.File) *Graph {
	g := &Graph{
		refs:       map[string]map[string]bool{},
		revRefs:    map[string]map[string]bool{},
		types:      map[string]string{},
		values:     map[string]map[string]any{},
		exprs:      map[string]map[string]any{},
		instances:  map[string][]map[string]any{},
		outputs:    map[string][]string{},
		iter:       map[string][]string{},
		moduleOuts: map[string][]string{},
	}

	for _, r := range f.Resources() {
		addr := plan.Base(r.Address)
		g.types[addr] = r.Type
		g.instances[addr] = append(g.instances[addr], r.Values)
		if _, seen := g.values[addr]; !seen {
			g.values[addr] = r.Values
		}
	}

	// Outputs are indexed before any edge is drawn, because an edge that lands
	// on one has to be expanded on the spot.
	for _, o := range f.ConfigOutputs() {
		call := strings.TrimSuffix(o.ModulePrefix, ".")
		g.moduleOuts[call] = append(g.moduleOuts[call], o.Address)
		for _, ref := range o.References {
			to := normalize(ref, o.ModulePrefix)
			if to == "" || to == o.Address {
				continue
			}
			g.outputs[o.Address] = append(g.outputs[o.Address], to)
		}
	}

	for _, r := range f.ConfigResources() {
		from := plan.Base(r.Address)
		g.types[from] = r.Type
		g.exprs[from] = r.Expressions
		modulePrefix := modulePrefixOf(from)
		iterRefs := collectReferences(r.ForEachExpression)
		iterRefs = append(iterRefs, collectReferences(r.CountExpression)...)
		for _, ref := range iterRefs {
			to := normalize(ref, modulePrefix)
			if to == "" || to == from {
				continue
			}
			g.iter[from] = append(g.iter[from], to)
		}

		refs := collectReferences(r.Expressions)
		refs = append(refs, iterRefs...)
		for _, ref := range refs {
			to := normalize(ref, modulePrefix)
			if to == "" || to == from {
				continue
			}
			for _, real := range g.behind(to) {
				if real != from {
					g.addEdge(from, real)
				}
			}
		}
	}
	return g
}

// behind resolves an address that names a module output into the resources the
// output actually returns, following outputs that return other outputs for as
// long as the chain runs. Anything that is not an output comes back unchanged.
//
// This is what lets a rule see across a module boundary. Terraform's plan says
// the attachment references module.compute.vm_id and stops there; the resource
// behind that name is in the module's own outputs, one hop away, and without
// this hop every cross module edge dies on a name that has no type.
func (g *Graph) behind(addr string) []string {
	return g.resolveOutput(addr, map[string]bool{})
}

func (g *Graph) resolveOutput(addr string, seen map[string]bool) []string {
	targets, isOutput := g.outputs[addr]
	if !isOutput {
		// A module indexed dynamically, module.vms[each.value.name].id, is
		// recorded by terraform as a reference to the bare call: the output name
		// is not in the plan at all. Every output of that call is therefore a
		// candidate, and the caller narrows them down by type.
		if outs, isCall := g.moduleOuts[addr]; isCall && !seen[addr] {
			seen[addr] = true
			var out []string
			for _, o := range outs {
				out = append(out, g.resolveOutput(o, seen)...)
			}
			return out
		}
		return []string{addr}
	}
	// An output cannot reach itself, but a plan is input this tool did not
	// produce, so the cycle is guarded rather than assumed away.
	if seen[addr] {
		return nil
	}
	seen[addr] = true

	var out []string
	for _, t := range targets {
		out = append(out, g.resolveOutput(t, seen)...)
	}
	return out
}

func (g *Graph) addEdge(from, to string) {
	if g.refs[from] == nil {
		g.refs[from] = map[string]bool{}
	}
	if g.revRefs[to] == nil {
		g.revRefs[to] = map[string]bool{}
	}
	g.refs[from][to] = true
	g.revRefs[to][from] = true
}

// RefsOfType returns the resources of type t that addr references.
func (g *Graph) RefsOfType(addr, t string) []string {
	return g.filter(g.refs[plan.Base(addr)], t)
}

// ReferrersOfType returns the resources of type t that reference addr. This is
// how a security group finds the workloads attached to it.
func (g *Graph) ReferrersOfType(addr, t string) []string {
	return g.filter(g.revRefs[plan.Base(addr)], t)
}

func (g *Graph) filter(set map[string]bool, t string) []string {
	var out []string
	for addr := range set {
		if g.types[addr] == t {
			out = append(out, addr)
		}
	}
	sortStrings(out)
	return out
}

// ReferencesIn returns the resources of type t referenced from specific blocks
// of a resource. Direction matters for capacity analysis: a security group that
// names another one under "ingress" is receiving traffic from it, while the same
// reference under "egress" means the opposite. Rules that cannot tell the two
// apart produce false positives, and a false positive in a capacity report costs
// the whole customer.
func (g *Graph) ReferencesIn(addr, t string, blocks ...string) []string {
	base := plan.Base(addr)
	exprs := g.exprs[base]
	if exprs == nil {
		return nil
	}
	prefix := modulePrefixOf(base)
	seen := map[string]bool{}
	var out []string
	for _, block := range blocks {
		node, ok := exprs[block]
		if !ok {
			continue
		}
		for _, ref := range collectReferences(node) {
			// A block whose value is each.value or each.key names an element of
			// the collection the resource iterates over, so the reference the
			// plan states lives in for_each and not in the block. Reading only
			// the block leaves the edge unresolved on terraform that is entirely
			// idiomatic: for_each over a map of disks, managed_disk_id =
			// each.value.
			candidates := []string{normalize(ref, prefix)}
			if strings.HasPrefix(ref, "each.") || ref == "each" {
				candidates = g.iter[base]
			}
			for _, candidate := range candidates {
				var matches []string
				found := map[string]bool{}
				for _, to := range g.behind(candidate) {
					if to == "" || to == base || g.types[to] != t || found[to] {
						continue
					}
					// Deduplicated before it is counted, because a module that
					// exports the same resource through two outputs, an id and a
					// name, is one resource and not an ambiguity.
					found[to] = true
					matches = append(matches, to)
				}
				// A bare module call was expanded through every output it
				// declares, so more than one match of the asked for type means
				// the plan does not say which one was meant. Refusing to choose
				// is the whole difference between an edge and a guess.
				if _, isCall := g.moduleOuts[candidate]; isCall && len(matches) > 1 {
					continue
				}
				for _, to := range matches {
					if seen[to] {
						continue
					}
					seen[to] = true
					out = append(out, to)
				}
			}
		}
	}
	sortStrings(out)
	return out
}

// Edges returns every reference edge, used when building the redacted payload.
func (g *Graph) Edges() [][2]string {
	var out [][2]string
	for from, tos := range g.refs {
		for to := range tos {
			if g.Known(to) {
				out = append(out, [2]string{from, to})
			}
		}
	}
	sortPairs(out)
	return out
}

func sortPairs(p [][2]string) {
	for i := 1; i < len(p); i++ {
		for j := i; j > 0; j-- {
			a, b := p[j-1], p[j]
			if a[0] < b[0] || (a[0] == b[0] && a[1] <= b[1]) {
				break
			}
			p[j-1], p[j] = p[j], p[j-1]
		}
	}
}

func (g *Graph) Type(addr string) string { return g.types[plan.Base(addr)] }

func (g *Graph) Values(addr string) map[string]any { return g.values[plan.Base(addr)] }

// Instances returns the values of every real resource a block produced. One
// aws_subnet with for_each over three zones is one address and three subnets,
// and a capacity rule that counts the address instead of the subnets is wrong.
func (g *Graph) Instances(addr string) []map[string]any { return g.instances[plan.Base(addr)] }

func (g *Graph) InstanceCount(addr string) int { return len(g.instances[plan.Base(addr)]) }

// Known reports whether the address exists in planned_values. Configuration can
// reference resources that the plan is not creating.
func (g *Graph) Known(addr string) bool {
	_, ok := g.values[plan.Base(addr)]
	return ok
}

// collectReferences walks an arbitrary expression tree and pulls out every
// "references" array terraform emits. Walking generically instead of decoding
// each block shape keeps the parser working when providers add attributes.
func collectReferences(node any) []string {
	var out []string
	switch v := node.(type) {
	case map[string]any:
		if refs, ok := v["references"].([]any); ok {
			for _, r := range refs {
				if s, ok := r.(string); ok {
					out = append(out, s)
				}
			}
		}
		for key, child := range v {
			if key == "references" {
				continue
			}
			out = append(out, collectReferences(child)...)
		}
	case []any:
		for _, child := range v {
			out = append(out, collectReferences(child)...)
		}
	}
	return out
}

var nonResourcePrefixes = map[string]bool{
	"var": true, "local": true, "each": true, "count": true,
	"path": true, "terraform": true, "self": true,
}

// normalize turns a raw reference such as "aws_security_group.app.id" into the
// resource address "aws_security_group.app", discarding attribute traversals and
// non-resource scopes. References inside a module call are relative, so they
// inherit the caller's module prefix.
func normalize(ref, modulePrefix string) string {
	parts := strings.Split(ref, ".")
	if len(parts) < 2 {
		return ""
	}
	if nonResourcePrefixes[parts[0]] {
		return ""
	}
	switch parts[0] {
	case "data":
		if len(parts) < 3 {
			return ""
		}
		return modulePrefix + strings.Join(parts[:3], ".")
	case "module":
		// module.compute.vm_id names an output, not a resource. Keep the whole
		// address, nested module hops included, so the resource behind it can be
		// resolved from the module's own outputs. Returning "module.compute"
		// here is what used to end every cross module edge: that address has no
		// type, so every type filter dropped it.
		addr := modulePrefix
		for len(parts) >= 2 && parts[0] == "module" {
			addr += "module." + parts[1] + "."
			parts = parts[2:]
		}
		if len(parts) == 0 {
			// A bare "module.x", naming the call rather than one of its
			// outputs. There is nothing behind it to resolve.
			return strings.TrimSuffix(addr, ".")
		}
		return addr + parts[0]
	}
	return modulePrefix + parts[0] + "." + parts[1]
}

// modulePrefixOf extracts "module.db." from "module.db.aws_db_instance.main".
func modulePrefixOf(addr string) string {
	parts := strings.Split(addr, ".")
	prefix := ""
	for len(parts) >= 2 && parts[0] == "module" {
		prefix += "module." + parts[1] + "."
		parts = parts[2:]
	}
	return prefix
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}
