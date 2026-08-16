package extract

// The Azure half of the allowlist, merged into the same map from its own file
// so that adding a cloud never means editing extract.go.
//
// Same rule as the AWS half, and it is worth restating because Azure gives more
// ways to get it wrong: allowlist, never denylist. Nothing that can carry a
// secret or customer content is here. Specifically absent, and each for a
// reason:
//
//	custom_data, user_data          arbitrary boot scripts, routinely secrets
//	admin_password, admin_ssh_key   credentials
//	app_settings, connection_string azurerm's usual home for connection
//	                                strings and API keys. The connection pool
//	                                size is derived from app_settings locally
//	                                and only the resulting integer travels,
//	                                exactly as container_definitions is
//	                                handled on the AWS side
//	shared_key                      VPN pre-shared keys
//	tags                            free text, and the place organisations put
//	                                owner names and ticket numbers
//	name, fqdn, dns_prefix          identifiers; addresses are hashed and real
//	                                names must not leak back in through an
//	                                attribute
func init() {
	for k, v := range azureAllowlist {
		allowlist[k] = v
	}
}

var azureAllowlist = map[string][]string{
	// Databases: the compute size is the entire ceiling.
	"azurerm_postgresql_flexible_server":               {"sku_name", "version", "storage_mb", "storage_tier", "auto_grow_enabled", "high_availability"},
	"azurerm_mysql_flexible_server":                    {"sku_name", "version", "storage"},
	"azurerm_postgresql_flexible_server_configuration": {"name"},
	"azurerm_mysql_flexible_server_configuration":      {"name"},
	"azurerm_mssql_database":                           {"sku_name", "max_size_gb", "read_scale", "zone_redundant"},

	// Compute and the scale declarations attached to it.
	// The pre split resource, which azurerm 4.0 removed and estates did not.
	// Its size attribute has the older name.
	"azurerm_virtual_machine":                        {"vm_size"},
	"azurerm_linux_virtual_machine":                  {"size"},
	"azurerm_windows_virtual_machine":                {"size"},
	"azurerm_linux_virtual_machine_scale_set":        {"sku", "instances"},
	"azurerm_windows_virtual_machine_scale_set":      {"sku", "instances"},
	"azurerm_orchestrated_virtual_machine_scale_set": {"sku_name", "instances"},
	"azurerm_service_plan":                           {"sku_name", "os_type", "worker_count", "per_site_scaling_enabled", "zone_balancing_enabled"},
	"azurerm_linux_web_app":                          {},
	"azurerm_windows_web_app":                        {},
	"azurerm_linux_function_app":                     {},
	"azurerm_windows_function_app":                   {},
	"azurerm_container_app":                          {"revision_mode"},
	"azurerm_monitor_autoscale_setting":              {"enabled"},

	// Kubernetes: node counts and max_pods are the address plan.
	"azurerm_kubernetes_cluster":           {"sku_tier"},
	"azurerm_kubernetes_cluster_node_pool": {"vm_size", "node_count", "min_count", "max_count", "max_pods", "auto_scaling_enabled", "os_type", "priority"},

	// Network topology and the address arithmetic.
	"azurerm_virtual_network":                          {"address_space"},
	"azurerm_subnet":                                   {"address_prefixes"},
	"azurerm_nat_gateway":                              {"sku_name", "idle_timeout_in_minutes"},
	"azurerm_nat_gateway_public_ip_association":        {},
	"azurerm_nat_gateway_public_ip_prefix_association": {},
	"azurerm_subnet_nat_gateway_association":           {},
	"azurerm_public_ip":                                {"sku", "sku_tier", "allocation_method", "ip_version"},
	"azurerm_public_ip_prefix":                         {"prefix_length", "sku", "ip_version"},
	"azurerm_network_interface":                        {"accelerated_networking_enabled"},
	"azurerm_route_table":                              {},
	"azurerm_subnet_route_table_association":           {},
	"azurerm_lb":                                       {"sku", "sku_tier"},
	"azurerm_lb_outbound_rule":                         {"protocol", "allocated_outbound_ports", "idle_timeout_in_minutes"},
	"azurerm_private_endpoint":                         {},

	// Hybrid connectivity: the SKU carries the throughput, the connections do
	// not add to it.
	"azurerm_virtual_network_gateway":            {"type", "vpn_type", "sku", "generation", "active_active", "enable_bgp"},
	"azurerm_virtual_network_gateway_connection": {"type", "enable_bgp", "connection_protocol", "dpd_timeout_seconds"},
	"azurerm_local_network_gateway":              {},

	// Storage: size decides the tier and the tier decides the performance.
	"azurerm_managed_disk":                         {"storage_account_type", "disk_size_gb", "disk_iops_read_write", "disk_mbps_read_write", "tier", "on_demand_bursting_enabled"},
	"azurerm_virtual_machine_data_disk_attachment": {"caching", "lun"},
}
