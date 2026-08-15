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
}

func Build(f *plan.File) *Graph {
	g := &Graph{
		refs:      map[string]map[string]bool{},
		revRefs:   map[string]map[string]bool{},
		types:     map[string]string{},
		values:    map[string]map[string]any{},
		exprs:     map[string]map[string]any{},
		instances: map[string][]map[string]any{},
	}

	for _, r := range f.Resources() {
		addr := plan.Base(r.Address)
		g.types[addr] = r.Type
		g.instances[addr] = append(g.instances[addr], r.Values)
		if _, seen := g.values[addr]; !seen {
			g.values[addr] = r.Values
		}
	}

	for _, r := range f.ConfigResources() {
		from := plan.Base(r.Address)
		g.types[from] = r.Type
		g.exprs[from] = r.Expressions
		modulePrefix := modulePrefixOf(from)
		refs := collectReferences(r.Expressions)
		refs = append(refs, collectReferences(r.ForEachExpression)...)
		refs = append(refs, collectReferences(r.CountExpression)...)
		for _, ref := range refs {
			to := normalize(ref, modulePrefix)
			if to == "" || to == from {
				continue
			}
			g.addEdge(from, to)
		}
	}
	return g
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
			to := normalize(ref, prefix)
			if to == "" || to == base || seen[to] || g.types[to] != t {
				continue
			}
			seen[to] = true
			out = append(out, to)
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
		if len(parts) < 2 {
			return ""
		}
		return "module." + parts[1]
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
