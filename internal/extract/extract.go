// Package extract turns a parsed plan into the redacted payload that may leave
// the machine.
//
// The rule is allowlist, never denylist. Terraform plan JSON can contain
// sensitive values, so instead of trying to strip secrets out (which leaks the
// first time a provider adds an attribute nobody thought about), only the
// handful of attributes that matter for capacity is ever read. Everything else
// is never touched, so it cannot escape.
//
// `headroom analyze --dry-run` prints exactly what this package produces. That
// output is the answer to half of any customer security questionnaire.
package extract

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"github.com/headroom-project/headroom/internal/graph"
	"github.com/headroom-project/headroom/internal/plan"
	"github.com/headroom-project/headroom/internal/rules"
)

const SchemaVersion = "1"

// allowlist is the complete set of attributes the product is allowed to read
// per resource type. Adding a line here is a deliberate act with a privacy
// consequence, which is exactly why it lives in one visible place.
//
// aws_ecs_task_definition deliberately excludes container_definitions: the pool
// size is derived from it locally and only the resulting integer travels.
var allowlist = map[string][]string{
	"aws_db_instance":                   {"engine", "engine_version", "instance_class", "allocated_storage", "max_allocated_storage", "iops", "storage_type", "multi_az"},
	"aws_rds_cluster":                   {"engine", "engine_version", "engine_mode"},
	"aws_ecs_service":                   {"desired_count", "launch_type"},
	"aws_ecs_task_definition":           {"cpu", "memory", "network_mode"},
	"aws_appautoscaling_target":         {"min_capacity", "max_capacity", "scalable_dimension"},
	"aws_autoscaling_group":             {"min_size", "max_size", "desired_capacity"},
	"aws_launch_template":               {"instance_type"},
	"aws_instance":                      {"instance_type"},
	"aws_subnet":                        {"cidr_block", "availability_zone"},
	"aws_vpc":                           {"cidr_block"},
	"aws_security_group":                {},
	"aws_nat_gateway":                   {},
	"aws_route_table":                   {},
	"aws_route":                         {"destination_cidr_block"},
	"aws_route_table_association":       {},
	"aws_eks_node_group":                {"instance_types", "capacity_type"},
	"aws_vpn_gateway":                   {},
	"aws_vpn_connection":                {"type", "static_routes_only"},
	"aws_lambda_function":               {"memory_size", "timeout", "reserved_concurrent_executions", "architectures"},
	"aws_sqs_queue":                     {"visibility_timeout_seconds", "fifo_queue", "message_retention_seconds"},
	"aws_lambda_event_source_mapping":   {"batch_size", "maximum_batching_window_in_seconds"},
	"aws_elasticache_replication_group": {"node_type", "num_cache_clusters"},
	"aws_ebs_volume":                    {"type", "size", "iops", "throughput"},
	"aws_elasticache_cluster":           {"node_type", "num_cache_nodes", "engine"},
	"aws_db_parameter_group":            {"family"},
}

const maxStringLen = 64

type Node struct {
	ID   string `json:"id"`
	Type string `json:"type"`
	// XID is the hashed real cloud identifier, present only for resources that
	// already exist. It is the same value in every repository that touches the
	// same resource, and it is what lets the backend stitch plans together.
	XID   string         `json:"xid,omitempty"`
	Attrs map[string]any `json:"attrs,omitempty"`
}

type Finding struct {
	Rule       string         `json:"rule"`
	Severity   string         `json:"severity"`
	Confidence string         `json:"confidence"`
	Metrics    map[string]int `json:"metrics,omitempty"`
	Resources  []string       `json:"resources,omitempty"`
	Source     string         `json:"source,omitempty"`
}

type Payload struct {
	SchemaVersion    string      `json:"schema_version"`
	GeneratedBy      string      `json:"generated_by"`
	TerraformVersion string      `json:"terraform_version"`
	Salted           bool        `json:"salted"`
	Nodes            []Node      `json:"nodes"`
	Edges            [][2]string `json:"edges"`
	Findings         []Finding   `json:"findings"`

	// External and RemoteStates are the dangling edges of this repository.
	// Alone they say "something out there", and matched against another plan's
	// nodes they become the seams of the macro graph.
	External     []ExternalRef `json:"external,omitempty"`
	RemoteStates []RemoteState `json:"remote_states,omitempty"`
}

type Redactor struct{ salt string }

// NewRedactor takes a per-organization salt. Without one, resource addresses are
// low-entropy enough ("aws_db_instance.main") that a plain hash is reversible by
// guessing, so the salt is what makes the hashing worth anything.
func NewRedactor(salt string) *Redactor { return &Redactor{salt: salt} }

func (r *Redactor) ID(address string) string {
	sum := sha256.Sum256([]byte(r.salt + "\x00" + plan.Base(address)))
	return hex.EncodeToString(sum[:])[:12]
}

func (r *Redactor) Salted() bool { return r.salt != "" }

func (r *Redactor) Build(f *plan.File, g *graph.Graph, findings []rules.Finding, version string) Payload {
	p := Payload{
		SchemaVersion:    SchemaVersion,
		GeneratedBy:      "headroom/" + version,
		TerraformVersion: f.TerraformVersion,
		Salted:           r.Salted(),
		Nodes:            []Node{},
		Edges:            [][2]string{},
		Findings:         []Finding{},
	}

	own := ownIDs(f)

	included := map[string]bool{}
	for _, res := range f.Resources() {
		keys, ok := allowlist[res.Type]
		if !ok {
			continue
		}
		addr := plan.Base(res.Address)
		if included[addr] {
			continue
		}
		included[addr] = true
		node := Node{
			ID:    r.ID(addr),
			Type:  res.Type,
			Attrs: pick(res.Values, keys),
		}
		if id, exists := own[addr]; exists {
			node.XID = r.CloudID(id)
		}
		p.Nodes = append(p.Nodes, node)
	}

	p.External = r.externalRefs(f, own)
	p.RemoteStates = r.remoteStates(f)

	for _, e := range g.Edges() {
		if included[e[0]] && included[e[1]] {
			p.Edges = append(p.Edges, [2]string{r.ID(e[0]), r.ID(e[1])})
		}
	}

	for _, f := range findings {
		out := Finding{
			Rule:       f.Rule,
			Severity:   f.Severity,
			Confidence: f.Confidence,
			Metrics:    f.Metrics,
			Source:     f.Source,
		}
		for _, res := range f.Resources {
			if included[plan.Base(res)] {
				out.Resources = append(out.Resources, r.ID(res))
			}
		}
		p.Findings = append(p.Findings, out)
	}

	return p
}

// pick copies only allowlisted scalar attributes, truncating strings so a value
// nobody anticipated cannot smuggle a payload out inside an expected field.
//
// A key may address one or more nested blocks with dots ("settings.tier"). On
// AWS most capacity attributes sit at the top level; on Azure and GCP almost
// none of them do, and without paths the graph the backend receives would be
// thinner for those clouds than for AWS. The guarantee is unchanged: a dotted
// key is still one explicit entry in the same allowlist.
func pick(values map[string]any, keys []string) map[string]any {
	out := map[string]any{}
	for _, k := range keys {
		if strings.Contains(k, ".") {
			if v, ok := pickPath(values, k); ok {
				out[k] = v
			}
			continue
		}
		v, ok := values[k]
		if !ok || v == nil {
			continue
		}
		switch t := v.(type) {
		case string:
			if len(t) > maxStringLen {
				t = t[:maxStringLen]
			}
			out[k] = t
		case float64, bool:
			out[k] = t
		case []any:
			var flat []any
			for _, item := range t {
				if s, ok := item.(string); ok && len(s) <= maxStringLen {
					flat = append(flat, s)
				}
			}
			if len(flat) > 0 {
				out[k] = flat
			}
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// pickPath walks a dotted key into nested blocks and returns the scalars it
// finds, applying the same truncation the flat path does. A single value comes
// back as a scalar so the payload does not sprout one-element arrays.
func pickPath(values map[string]any, path string) (any, bool) {
	head, rest, nested := strings.Cut(path, ".")
	node, ok := values[head]
	if !ok || node == nil {
		return nil, false
	}

	if !nested {
		var found []any
		for _, leaf := range flattenAny(node) {
			switch t := leaf.(type) {
			case string:
				if len(t) > maxStringLen {
					t = t[:maxStringLen]
				}
				found = append(found, t)
			case float64, bool:
				found = append(found, t)
			}
		}
		switch len(found) {
		case 0:
			return nil, false
		case 1:
			return found[0], true
		default:
			return found, true
		}
	}

	var collected []any
	for _, child := range flattenAny(node) {
		m, ok := child.(map[string]any)
		if !ok {
			continue
		}
		if v, ok := pickPath(m, rest); ok {
			collected = append(collected, v)
		}
	}
	switch len(collected) {
	case 0:
		return nil, false
	case 1:
		return collected[0], true
	default:
		return collected, true
	}
}

func flattenAny(node any) []any {
	if list, ok := node.([]any); ok {
		var out []any
		for _, item := range list {
			out = append(out, flattenAny(item)...)
		}
		return out
	}
	return []any{node}
}
