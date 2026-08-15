package extract

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"

	"github.com/headroom-project/headroom/internal/plan"
)

// One plan is one repository, and one repository is never the whole picture.
// Networking lives in one repo, the platform in another, each service in its
// own, and the edges between them are invisible inside any single plan.
//
// Two things make stitching possible without touching the customer's account:
//
//   - prior_state carries real cloud identifiers for everything that already
//     exists, so the repository that created a VPC knows its vpc- id.
//   - data sources are resolved at plan time, so the repository that merely
//     consumes that VPC also ends up with the same vpc- id in its plan.
//
// The same id in two plans is the join. Both sides are hashed with the
// organization salt, so the server can match them without ever learning what
// they are.
//
// Everything read here goes through its own explicit allowlist, for the same
// reason the capacity attributes do: a denylist over plan JSON leaks the first
// time a provider adds a field nobody anticipated.

// crossRefAttrs are the attributes that carry a reference to a resource this
// plan does not own. Dotted names address one level of nested block.
var crossRefAttrs = map[string][]string{
	"aws_subnet":                  {"vpc_id"},
	"aws_security_group":          {"vpc_id"},
	"aws_internet_gateway":        {"vpc_id"},
	"aws_route_table":             {"vpc_id"},
	"aws_route":                   {"route_table_id", "nat_gateway_id", "gateway_id", "transit_gateway_id"},
	"aws_route_table_association": {"subnet_id", "route_table_id"},
	"aws_nat_gateway":             {"subnet_id", "allocation_id"},
	"aws_instance":                {"subnet_id", "vpc_security_group_ids"},
	"aws_launch_template":         {"vpc_security_group_ids"},
	"aws_autoscaling_group":       {"vpc_zone_identifier", "target_group_arns"},
	"aws_db_instance":             {"vpc_security_group_ids", "db_subnet_group_name"},
	"aws_db_subnet_group":         {"subnet_ids"},
	"aws_ecs_service":             {"cluster", "network_configuration.subnets", "network_configuration.security_groups"},
	"aws_lambda_function":         {"vpc_config.subnet_ids", "vpc_config.security_group_ids"},
	"aws_vpn_gateway":             {"vpc_id"},
	"aws_vpn_connection":          {"customer_gateway_id", "vpn_gateway_id", "transit_gateway_id"},
	"aws_volume_attachment":       {"volume_id", "instance_id"},
	"aws_lb":                      {"subnets", "security_groups"},
	"aws_elasticache_cluster":     {"subnet_group_name", "security_group_ids"},
}

// awsID matches the identifier shapes AWS hands out. Matching the shape rather
// than the attribute name keeps a stale attribute from silently sending a value
// that is not an id at all.
var awsID = regexp.MustCompile(`^(vpc|subnet|sg|rtb|igw|nat|vgw|cgw|vpn|eni|vol|snap|ami|i|acl|pcx|tgw|eipalloc|lt|tgw-attach)-[0-9a-f]{8,17}$`)

var awsARN = regexp.MustCompile(`^arn:aws[a-z-]*:[a-z0-9-]+:[a-z0-9-]*:[0-9]*:`)

// ExternalRef is a resource this plan points at but does not manage. On its own
// it is a dangling edge; matched against another plan's node it becomes the
// seam between two repositories.
type ExternalRef struct {
	XID       string `json:"xid"`
	FromID    string `json:"from"`
	Attribute string `json:"attribute"`
	Kind      string `json:"kind"`
}

// RemoteState is a declared dependency on another repository's state. It is the
// strongest cross-repository edge available, because it survives even when no
// identifier is resolvable yet.
type RemoteState struct {
	XID     string `json:"xid"`
	Backend string `json:"backend"`
}

// CloudID hashes a real AWS identifier. The domain byte keeps it from ever
// colliding with the hash of a terraform address.
func (r *Redactor) CloudID(id string) string {
	sum := sha256.Sum256([]byte(r.salt + "\x01aws:" + strings.TrimSpace(id)))
	return hex.EncodeToString(sum[:])[:12]
}

// ownIDs maps each managed resource to the cloud identifier it already has.
// Resources the plan is about to create are absent, which is correct: they have
// no id yet, so nothing can join on them until the next run.
func ownIDs(f *plan.File) map[string]string {
	out := map[string]string{}
	for _, r := range f.PriorResources() {
		id := plan.Str(r.Values, "id")
		if id == "" || !looksLikeCloudID(id) {
			continue
		}
		out[plan.Base(r.Address)] = id
	}
	return out
}

func looksLikeCloudID(v string) bool {
	return awsID.MatchString(v) || awsARN.MatchString(v)
}

func kindOf(v string) string {
	if awsARN.MatchString(v) {
		parts := strings.Split(v, ":")
		if len(parts) > 2 {
			return parts[2]
		}
		return "arn"
	}
	if i := strings.Index(v, "-"); i > 0 {
		return v[:i]
	}
	return "unknown"
}

// externalRefs collects identifiers this plan points at and does not own.
func (r *Redactor) externalRefs(f *plan.File, own map[string]string) []ExternalRef {
	ownedValues := map[string]bool{}
	for _, id := range own {
		ownedValues[id] = true
	}

	seen := map[string]bool{}
	var out []ExternalRef

	for _, res := range f.Resources() {
		attrs, ok := crossRefAttrs[res.Type]
		if !ok {
			continue
		}
		addr := plan.Base(res.Address)
		for _, attr := range attrs {
			for _, value := range readPath(res.Values, attr) {
				if !looksLikeCloudID(value) || ownedValues[value] {
					continue
				}
				key := addr + "|" + attr + "|" + value
				if seen[key] {
					continue
				}
				seen[key] = true
				out = append(out, ExternalRef{
					XID:       r.CloudID(value),
					FromID:    r.ID(addr),
					Attribute: attr,
					Kind:      kindOf(value),
				})
			}
		}
	}
	return out
}

// readPath resolves "attr" or "block.attr", flattening lists on the way, and
// returns only string leaves.
func readPath(values map[string]any, path string) []string {
	head, rest, nested := strings.Cut(path, ".")
	node, ok := values[head]
	if !ok || node == nil {
		return nil
	}
	if !nested {
		return flattenStrings(node)
	}
	var out []string
	for _, child := range asMaps(node) {
		out = append(out, readPath(child, rest)...)
	}
	return out
}

func asMaps(node any) []map[string]any {
	switch v := node.(type) {
	case map[string]any:
		return []map[string]any{v}
	case []any:
		var out []map[string]any
		for _, item := range v {
			if m, ok := item.(map[string]any); ok {
				out = append(out, m)
			}
		}
		return out
	}
	return nil
}

func flattenStrings(node any) []string {
	switch v := node.(type) {
	case string:
		return []string{v}
	case []any:
		var out []string
		for _, item := range v {
			out = append(out, flattenStrings(item)...)
		}
		return out
	}
	return nil
}

// remoteStates records every terraform_remote_state data source, hashed by the
// state key it reads, so two repositories pointing at the same upstream state
// resolve to the same node without either of them naming a bucket.
func (r *Redactor) remoteStates(f *plan.File) []RemoteState {
	seen := map[string]bool{}
	var out []RemoteState

	for _, ds := range f.DataSources() {
		if ds.Type != "terraform_remote_state" {
			continue
		}
		backend := constantString(ds.Expressions["backend"])
		key := remoteStateKey(ds.Expressions["config"])
		if key == "" {
			continue
		}
		xid := r.CloudID("tfstate:" + backend + ":" + key)
		if seen[xid] {
			continue
		}
		seen[xid] = true
		out = append(out, RemoteState{XID: xid, Backend: backend})
	}
	return out
}

// remoteStateKey pulls the state path out of a backend config block. Only the
// locator is read, never credentials or role ARNs that may sit beside it.
func remoteStateKey(node any) string {
	expr, ok := node.(map[string]any)
	if !ok {
		return ""
	}
	cfg, ok := expr["constant_value"].(map[string]any)
	if !ok {
		return ""
	}
	for _, field := range []string{"key", "path", "prefix", "name", "workspaces"} {
		if v, ok := cfg[field].(string); ok && v != "" {
			return field + "=" + v
		}
	}
	return ""
}

func constantString(node any) string {
	expr, ok := node.(map[string]any)
	if !ok {
		return ""
	}
	if v, ok := expr["constant_value"].(string); ok {
		return v
	}
	return ""
}
