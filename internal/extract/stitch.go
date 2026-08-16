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

	// Azure. The seam is almost always a network that one repository owns and
	// another consumes, which is the arrangement every estate that motivated
	// this actually uses: 380 subnet data source reads against 7 subnet
	// resources, in one of them.
	"azurerm_subnet":                               {"virtual_network_name"},
	"azurerm_network_interface":                    {"ip_configuration.subnet_id", "network_security_group_id"},
	"azurerm_virtual_machine_data_disk_attachment": {"virtual_machine_id", "managed_disk_id"},
	"azurerm_kubernetes_cluster":                   {"default_node_pool.vnet_subnet_id", "default_node_pool.pod_subnet_id"},
	"azurerm_kubernetes_cluster_node_pool":         {"kubernetes_cluster_id", "vnet_subnet_id", "pod_subnet_id"},
	"azurerm_subnet_nat_gateway_association":       {"subnet_id", "nat_gateway_id"},
	"azurerm_nat_gateway_public_ip_association":    {"nat_gateway_id", "public_ip_address_id"},
	"azurerm_virtual_network_gateway_connection":   {"virtual_network_gateway_id", "peer_virtual_network_gateway_id", "local_network_gateway_id"},
	"azurerm_virtual_network_peering":              {"remote_virtual_network_id"},
	"azurerm_private_endpoint":                     {"subnet_id"},
	"azurerm_mssql_virtual_machine":                {"virtual_machine_id"},
	"azurerm_postgresql_flexible_server":           {"delegated_subnet_id", "private_dns_zone_id"},
	"azurerm_mysql_flexible_server":                {"delegated_subnet_id", "private_dns_zone_id"},
	"azurerm_lb_backend_address_pool":              {"loadbalancer_id"},
	"azurerm_managed_disk":                         {"source_resource_id", "disk_encryption_set_id"},

	// Google Cloud.
	"google_compute_subnetwork":        {"network"},
	"google_compute_instance":          {"network_interface.subnetwork", "network_interface.network"},
	"google_compute_instance_template": {"network_interface.subnetwork", "network_interface.network"},
	"google_compute_router":            {"network"},
	"google_compute_router_nat":        {"router"},
	"google_container_cluster":         {"network", "subnetwork"},
	"google_container_node_pool":       {"cluster"},
	"google_sql_database_instance":     {"settings.ip_configuration.private_network"},
	"google_vpc_access_connector":      {"network"},
	"google_compute_forwarding_rule":   {"network", "subnetwork", "backend_service"},
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

// CloudID hashes a real cloud identifier. The domain byte keeps it from ever
// colliding with the hash of a terraform address, and the cloud name keeps a
// subscription id from ever colliding with an AWS resource id.
//
// The prefix used to be the literal "aws:", which was not a decision so much as
// the only cloud that existed when this was written. Nothing outside AWS was
// recognised as an identifier at all, so the cross repository view, which is the
// paid premise of this product, simply did not exist for two of the three clouds
// the rules already cover.
func (r *Redactor) CloudID(id string) string {
	id = strings.TrimSpace(id)
	cloud := cloudOf(id)
	if cloud == "" {
		// Anything unrecognised keeps the original namespace, so identifiers
		// that were already being hashed keep hashing to the same value.
		cloud = "aws"
	}
	sum := sha256.Sum256([]byte(r.salt + "\x01" + cloud + ":" + id))
	return hex.EncodeToString(sum[:])[:12]
}

// azureID matches an ARM resource id. Everything identifying in it, the
// subscription, the resource group and the resource name, is hashed and never
// travels: what the shape buys is the confidence that this is an id at all.
var azureID = regexp.MustCompile(`^/subscriptions/[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}(/|$)`)

// kindShape is the ingest contract's own pattern for a kind, kept here so this
// file cannot emit a value the API would reject. The corpus catches a mismatch,
// and catching it here means the corpus never has to.
var kindShape = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// azureProvider pulls the namespace and type out of an ARM id, which is the one
// part of it that describes a kind of thing rather than somebody's thing.
var azureProvider = regexp.MustCompile(`/providers/([A-Za-z0-9.]+)/([A-Za-z0-9]+)/`)

// gcpID matches a self link or a relative resource name. The bare form,
// "projects/p/global/networks/n", is what the google provider stores for most
// references and the https form is what it stores for the rest.
var gcpID = regexp.MustCompile(`^(https://[a-z0-9.-]+/[a-z0-9]+/[a-z0-9]+/)?projects/[a-z0-9][a-z0-9-]{4,}/(global|regions|zones|locations)/[a-zA-Z0-9-]+(/[a-zA-Z0-9_-]+)+$`)

// cloudOf names the cloud that issued an identifier, or "" when nothing here
// recognises it. Recognition is by shape and never by attribute name, for the
// same reason as everywhere else in this file: a stale attribute must not be
// able to send a value that is not an identifier at all.
func cloudOf(v string) string {
	switch {
	case awsID.MatchString(v) || awsARN.MatchString(v):
		return "aws"
	case azureID.MatchString(v):
		return "azure"
	case gcpID.MatchString(v):
		return "gcp"
	}
	return ""
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
	return cloudOf(v) != ""
}

// kindOf says what sort of thing an identifier points at, and nothing about
// whose. Every value it can return is a type name the provider publishes, never
// a name somebody chose.
func kindOf(v string) string {
	switch cloudOf(v) {
	case "azure":
		if m := azureProvider.FindStringSubmatch(v); m != nil {
			// The ingest contract accepts a kind of [a-z][a-z0-9-]*, and that
			// shape is a control rather than a preference: a dot would mean this
			// could be a terraform address and a colon would mean it could be an
			// ARN. Microsoft.Network/virtualNetworks carries both a dot and a
			// slash, so it is flattened rather than the contract loosened.
			return strings.NewReplacer(".", "-", "/", "-").Replace(strings.ToLower(m[1] + "/" + m[2]))
		}
		return "azure-resource"
	case "gcp":
		// The collection is the second to last segment of both forms:
		// projects/p/global/networks/n ends in networks/n.
		parts := strings.Split(strings.TrimSuffix(v, "/"), "/")
		if len(parts) >= 2 {
			if kind := strings.ToLower(parts[len(parts)-2]); kindShape.MatchString(kind) {
				return kind
			}
		}
		return "gcp-resource"
	}
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
