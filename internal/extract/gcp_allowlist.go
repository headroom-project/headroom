package extract

// gcpAllowlist is the complete set of attributes the product is allowed to read
// on Google Cloud resources. It merges into the shared allowlist from init() so
// that adding a cloud never means editing extract.go.
//
// Everything here is a capacity number or a shape. Nothing here can carry a
// secret or customer content, and the omissions are deliberate rather than
// accidental:
//
//   - metadata and metadata_startup_script on instances and templates are the
//     single largest secret-bearing surface on GCP, and they carry no capacity
//     information at all.
//   - labels, resource_labels, effective_labels and terraform_labels are
//     customer content: cost centres, owner emails, ticket numbers.
//   - Cloud Run container env is read locally to derive a connection pool size
//     and never travels; only the resulting integer leaves, the same treatment
//     container_definitions gets on AWS.
//   - names, self links, project ids and DNS names are identity, not capacity.
//
// Most capacity attributes on GCP live inside nested blocks, so the entries
// below use dotted paths. A dotted key is still one explicit entry in the same
// allowlist: the picker walks into the block and copies that one scalar, and
// everything else in the block stays unread.
var gcpAllowlist = map[string][]string{
	"google_sql_database_instance": {
		"database_version", "region", "instance_type",
		"settings.tier", "settings.availability_type", "settings.disk_size",
		"settings.disk_type", "settings.disk_autoresize", "settings.disk_autoresize_limit",
	},

	"google_container_cluster": {
		"location", "initial_node_count", "default_max_pods_per_node", "enable_autopilot",
		"networking_mode", "cluster_ipv4_cidr", "datapath_provider",
		"ip_allocation_policy.cluster_ipv4_cidr_block", "ip_allocation_policy.services_ipv4_cidr_block",
	},
	"google_container_node_pool": {
		"location", "node_count", "max_pods_per_node",
		"autoscaling.min_node_count", "autoscaling.max_node_count",
		"node_config.machine_type", "node_config.disk_type", "node_config.disk_size_gb",
	},

	"google_compute_network":    {"auto_create_subnetworks", "mtu"},
	"google_compute_subnetwork": {"ip_cidr_range", "region", "purpose", "role", "stack_type"},

	"google_compute_router":     {"region"},
	"google_compute_router_nat": {"region", "type", "nat_ip_allocate_option", "source_subnetwork_ip_ranges_to_nat", "min_ports_per_vm", "max_ports_per_vm", "enable_dynamic_port_allocation", "enable_endpoint_independent_mapping"},
	"google_compute_address":    {"region", "address_type", "network_tier"},

	"google_cloud_run_v2_service": {
		"location", "ingress", "launch_stage",
		"template.scaling.max_instance_count", "template.scaling.min_instance_count",
	},
	"google_cloud_run_service":        {"location"},
	"google_cloudfunctions2_function": {"location"},
	"google_cloudfunctions_function":  {"region", "available_memory_mb", "timeout", "max_instances"},

	"google_vpc_access_connector": {"region", "machine_type", "min_instances", "max_instances", "min_throughput", "max_throughput", "ip_cidr_range"},

	"google_compute_instance":          {"machine_type", "zone"},
	"google_compute_instance_template": {"machine_type", "region"},

	"google_compute_instance_group_manager":        {"target_size", "zone"},
	"google_compute_region_instance_group_manager": {"target_size", "region", "distribution_policy_target_shape"},
	"google_compute_autoscaler":                    {"zone"},
	"google_compute_region_autoscaler":             {"region"},

	"google_compute_disk":           {"type", "size", "zone", "provisioned_iops", "provisioned_throughput"},
	"google_compute_region_disk":    {"type", "size", "region"},
	"google_redis_instance":         {"tier", "memory_size_gb", "redis_version", "replica_count", "read_replicas_mode"},
	"google_compute_global_address": {"purpose", "address_type", "prefix_length"},
}

func init() {
	for k, v := range gcpAllowlist {
		allowlist[k] = v
	}
}
