package rules_test

import (
	"testing"

	"github.com/headroom-project/headroom/internal/catalog"
	"github.com/headroom-project/headroom/internal/plan"
	"github.com/headroom-project/headroom/internal/rules"
)

const fixtureGCP = "../../fixtures/gcp-01-gke-cloudrun-sql/plan.json"

// The whole catalog is the product, so a table that will not parse has to fail
// loudly rather than turn every Google Cloud rule silently into a no-op. This
// exact failure happened during the catalog verification pass: a missing comma
// zeroed the serverless table and the suite stayed green, because the loader
// was discarding the parse error.
func TestGCPCatalogParses(t *testing.T) {
	c, err := catalog.Load()
	if err != nil {
		t.Fatalf("load catalog: %v", err)
	}
	if err := c.GCPCatalogError(); err != nil {
		t.Fatalf("gcp tables: %v", err)
	}
	// A table that parsed but arrived empty is the same outage wearing a
	// different hat, so assert one anchor value per file.
	if len(c.GCPSQLTierNotEncoded()) == 0 && c.GCPSQLTierSource() == "" {
		t.Error("gcp-cloudsql.json parsed to an empty tier table")
	}
	if c.GCPPodRange().DefaultMaxPodsPerNode == 0 {
		t.Error("gcp-gke.json parsed to an empty pod range")
	}
	if c.GCPPublicNAT().PortsPerIP == 0 {
		t.Error("gcp-nat.json parsed to an empty public NAT table")
	}
	if _, ok := c.GCPDisk("pd-balanced"); !ok {
		t.Error("gcp-disk.json parsed without pd-balanced")
	}
	if c.GCPConnector().MaxInstancesCeiling == 0 {
		t.Error("gcp-serverless.json parsed to an empty connector table")
	}
}

func loadGCP(t *testing.T) *plan.File {
	t.Helper()
	f, err := plan.Load(fixtureGCP)
	if err != nil {
		t.Fatalf("load gcp fixture: %v", err)
	}
	return f
}

// gcpBlockIn reaches into terraform's nested block encoding the same way the
// rules do, so a test can move one number and leave the rest of the plan alone.
func gcpBlockIn(t *testing.T, values map[string]any, path ...string) map[string]any {
	t.Helper()
	cur := values
	for _, key := range path {
		list, ok := cur[key].([]any)
		if !ok || len(list) == 0 {
			t.Fatalf("no block %q in %v", key, path)
		}
		cur, ok = list[0].(map[string]any)
		if !ok {
			t.Fatalf("block %q is not an object", key)
		}
	}
	return cur
}

func gcpHas(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

func gcpFindings(t *testing.T, f *plan.File, rule string) []rules.Finding {
	t.Helper()
	var out []rules.Finding
	for _, got := range runOn(t, f) {
		if got.Rule == rule {
			out = append(out, got)
		}
	}
	return out
}

func gcpOne(t *testing.T, f *plan.File, rule, title string) *rules.Finding {
	t.Helper()
	for _, got := range gcpFindings(t, f, rule) {
		if got.Title == title {
			found := got
			return &found
		}
	}
	return nil
}

// GC1 --------------------------------------------------------------------

func TestGCPCloudRunOutgrowsCloudSQL(t *testing.T) {
	f := gcpOne(t, loadGCP(t), "GC1", "Scale asymmetry: application outgrows Cloud SQL")
	if f == nil {
		t.Fatal("GC1 accepted 100 Cloud Run instances at 10 connections each in front of a 400-connection tier")
	}
	if f.Severity != rules.SeverityCritical {
		t.Errorf("severity = %q, want critical", f.Severity)
	}
	if f.Metrics["demand"] != 1000 {
		t.Errorf("demand = %d, want 1000 (100 instances x DB_POOL_SIZE=10)", f.Metrics["demand"])
	}
	if f.Metrics["ceiling"] != 400 {
		t.Errorf("ceiling = %d, want 400 (db-custom-2-7680 is 7680 MB, the 7.5-15 GB band)", f.Metrics["ceiling"])
	}
	if f.Metrics["break_at_pct"] != 40 {
		t.Errorf("break_at_pct = %d, want 40", f.Metrics["break_at_pct"])
	}
}

// A Cloud Run service with no scaling block is not a one-instance service, it is
// a hundred-instance service that happens to be idle. Missing this is the
// easiest way to under-report demand on GCP, so it gets a test of its own.
func TestGCPCloudRunDefaultMaxInstancesApplies(t *testing.T) {
	f := loadGCP(t)
	for _, r := range f.ByType("google_cloud_run_v2_service") {
		delete(gcpBlockIn(t, r.Values, "template"), "scaling")
	}
	got := gcpOne(t, f, "GC1", "Scale asymmetry: application outgrows Cloud SQL")
	if got == nil {
		t.Fatal("GC1 went silent when the scaling block was removed; Cloud Run still defaults to 100 instances")
	}
	if got.Metrics["demand"] != 1000 {
		t.Errorf("demand = %d, want 1000 from the documented default of 100 instances", got.Metrics["demand"])
	}
}

// The declared flag is not an override to be nervous about, it is the answer.
func TestGCPDeclaredMaxConnectionsFlagBeatsTheCatalog(t *testing.T) {
	f := loadGCP(t)
	for _, r := range f.ByType("google_sql_database_instance") {
		s := gcpBlockIn(t, r.Values, "settings")
		s["database_flags"] = []any{map[string]any{"name": "max_connections", "value": "2000"}}
	}
	for _, got := range gcpFindings(t, f, "GC1") {
		if got.Metrics["ceiling"] != 0 && got.Metrics["ceiling"] != 2000 {
			t.Errorf("ceiling = %d, want the declared 2000", got.Metrics["ceiling"])
		}
		if got.Severity == rules.SeverityCritical {
			t.Errorf("GC1 still critical at 1000 of a declared 2000: %s", got.Summary)
		}
	}
}

// MySQL has no published memory-to-connections table. An absent ceiling beats an
// uncertain one, but a silent absence reads as a clean bill of health.
func TestGCPMySQLReportsWhatItSkipped(t *testing.T) {
	f := loadGCP(t)
	for _, r := range f.ByType("google_sql_database_instance") {
		r.Values["database_version"] = "MYSQL_8_0"
	}
	got := gcpOne(t, f, "GC1", "Connection ceiling not evaluated")
	if got == nil {
		t.Fatal("GC1 said nothing at all about a MySQL instance it cannot ground")
	}
	if got.Severity != rules.SeverityInfo {
		t.Errorf("severity = %q, want info", got.Severity)
	}
	for _, other := range gcpFindings(t, f, "GC1") {
		if other.Metrics["ceiling"] > 0 {
			t.Errorf("GC1 invented a MySQL ceiling of %d", other.Metrics["ceiling"])
		}
	}
}

func TestGCPUnknownTierStaysSilent(t *testing.T) {
	f := loadGCP(t)
	for _, r := range f.ByType("google_sql_database_instance") {
		gcpBlockIn(t, r.Values, "settings")["tier"] = "db-n1-standard-4"
	}
	got := gcpOne(t, f, "GC1", "Connection ceiling not evaluated")
	if got == nil {
		t.Fatal("GC1 did not report that db-n1-standard-4 has no encoded memory")
	}
	for _, other := range gcpFindings(t, f, "GC1") {
		if other.Metrics["ceiling"] > 0 {
			t.Errorf("GC1 guessed a ceiling of %d for an unencoded tier", other.Metrics["ceiling"])
		}
	}
}

// GC2 --------------------------------------------------------------------

// The headline number: a /21 pod range at 110 pods per node is eight nodes, not
// two thousand pods' worth of them.
func TestGCPPodRangeIsTheRealNodeCeiling(t *testing.T) {
	f := gcpOne(t, loadGCP(t), "GC2", "GKE pod range runs out of addresses before the cluster stops scaling")
	if f == nil {
		t.Fatal("GC2 accepted a node pool scaling to 30 against a /21 pod range")
	}
	if f.Severity != rules.SeverityCritical {
		t.Errorf("severity = %q, want critical: the nodes cannot be created", f.Severity)
	}
	if f.Metrics["node_capacity"] != 8 {
		t.Errorf("node_capacity = %d, want 8 (a /21 holds eight /24 blocks)", f.Metrics["node_capacity"])
	}
	if f.Metrics["node_demand"] != 30 {
		t.Errorf("node_demand = %d, want 30", f.Metrics["node_demand"])
	}
	if f.Metrics["pod_range_addresses"] != 2048 {
		t.Errorf("pod_range_addresses = %d, want 2048", f.Metrics["pod_range_addresses"])
	}
	if f.Metrics["pod_addresses_used"] != 7680 {
		t.Errorf("pod_addresses_used = %d, want 7680 (30 nodes x 256)", f.Metrics["pod_addresses_used"])
	}
}

// Lowering max_pods_per_node shrinks the per-node block, and that is the fix
// nobody reaches for because the field looks like it is about pod density.
func TestGCPLowerMaxPodsPerNodeFitsMoreNodes(t *testing.T) {
	f := loadGCP(t)
	for _, r := range f.ByType("google_container_cluster") {
		r.Values["default_max_pods_per_node"] = float64(16)
	}
	got := gcpOne(t, f, "GC2", "GKE pod range runs out of addresses before the cluster stops scaling")
	if got != nil {
		t.Fatalf("GC2 still critical at 16 pods per node, where a /21 holds 64 nodes: %s", got.Summary)
	}
}

func TestGCPWidePodRangeIsSilent(t *testing.T) {
	f := loadGCP(t)
	for _, r := range f.ByType("google_compute_subnetwork") {
		for _, raw := range r.Values["secondary_ip_range"].([]any) {
			rng := raw.(map[string]any)
			if rng["range_name"] == "pods" {
				rng["ip_cidr_range"] = "10.16.0.0/14"
			}
		}
	}
	if got := gcpOne(t, f, "GC2", "GKE pod range runs out of addresses before the cluster stops scaling"); got != nil {
		t.Errorf("GC2 fired on a /14 pod range holding 16384 nodes: %s", got.Summary)
	}
}

func TestGCPServiceRangeCeilingIsStatedButNeverCritical(t *testing.T) {
	f := gcpOne(t, loadGCP(t), "GC2", "GKE service range is fixed and small")
	if f == nil {
		t.Fatal("GC2 said nothing about a /24 services range")
	}
	if f.Severity != rules.SeverityWarning {
		t.Errorf("severity = %q, want warning: there is no demand side to compare against", f.Severity)
	}
	if f.Metrics["max_services"] != 256 {
		t.Errorf("max_services = %d, want 256", f.Metrics["max_services"])
	}
}

// GC3 --------------------------------------------------------------------

func TestGCPNATPortsRunOutBeforeTheVMsStopScaling(t *testing.T) {
	f := gcpOne(t, loadGCP(t), "GC3", "Cloud NAT runs out of ports before the workloads stop scaling")
	if f == nil {
		t.Fatal("GC3 accepted 90 VMs behind one NAT address at 1024 ports each")
	}
	if f.Metrics["vm_capacity"] != 63 {
		t.Errorf("vm_capacity = %d, want 63 (64512 / 1024)", f.Metrics["vm_capacity"])
	}
	if f.Metrics["vms"] != 90 {
		t.Errorf("vms = %d, want 90 (30 GKE nodes + 60 managed instances)", f.Metrics["vms"])
	}
	if f.Metrics["nat_ips"] != 1 {
		t.Errorf("nat_ips = %d, want 1", f.Metrics["nat_ips"])
	}
}

// The default 64 ports per VM makes the same gateway serve 1008 VMs, which is
// Google's own worked example.
func TestGCPDefaultPortAllocationIsRoomy(t *testing.T) {
	f := loadGCP(t)
	for _, r := range f.ByType("google_compute_router_nat") {
		delete(r.Values, "min_ports_per_vm")
	}
	for _, got := range gcpFindings(t, f, "GC3") {
		if got.Severity == rules.SeverityCritical {
			t.Errorf("GC3 fired at the default 64 ports per VM, which serves 1008: %s", got.Summary)
		}
		if got.Metrics["vm_capacity"] != 0 && got.Metrics["vm_capacity"] != 1008 {
			t.Errorf("vm_capacity = %d, want 1008", got.Metrics["vm_capacity"])
		}
	}
}

// Automatic address allocation has no fixed ceiling to compare against, so the
// rule must say so rather than stay quiet.
func TestGCPAutoAllocatedNATIsReportedAsNotEvaluated(t *testing.T) {
	f := loadGCP(t)
	for _, r := range f.ByType("google_compute_router_nat") {
		r.Values["nat_ip_allocate_option"] = "AUTO_ONLY"
	}
	got := gcpOne(t, f, "GC3", "NAT port ceiling not evaluated")
	if got == nil {
		t.Fatal("GC3 went silent on an AUTO_ONLY gateway instead of saying what it skipped")
	}
	if got.Severity != rules.SeverityInfo {
		t.Errorf("severity = %q, want info", got.Severity)
	}
	if got.Metrics["vms"] != 90 {
		t.Errorf("vms = %d, want 90", got.Metrics["vms"])
	}
}

// GC4 --------------------------------------------------------------------

func TestGCPPdStandardIOPSComeFromSizeAlone(t *testing.T) {
	var found *rules.Finding
	for _, got := range gcpFindings(t, loadGCP(t), "GC4") {
		if gcpHas(got.Resources, "google_compute_disk.scratch") {
			f := got
			found = &f
		}
	}
	if found == nil {
		t.Fatal("GC4 said nothing about a 20 GiB pd-standard disk")
	}
	if found.Severity != rules.SeverityWarning {
		t.Errorf("severity = %q, want warning: a small pd-standard disk can be the right choice", found.Severity)
	}
	if found.Metrics["read_iops"] != 15 {
		t.Errorf("read_iops = %d, want 15 (20 GiB x 0.75)", found.Metrics["read_iops"])
	}
	if found.Metrics["write_iops"] != 30 {
		t.Errorf("write_iops = %d, want 30 (20 GiB x 1.5)", found.Metrics["write_iops"])
	}
}

func TestGCPBalancedNodeDiskIsSilent(t *testing.T) {
	for _, got := range gcpFindings(t, loadGCP(t), "GC4") {
		if gcpHas(got.Resources, "google_container_node_pool.main") {
			t.Errorf("GC4 complained about a 100 GiB pd-balanced disk at 600 read IOPS: %s", got.Summary)
		}
	}
}

// GC5 --------------------------------------------------------------------

func TestGCPConnectorIsTheFixedTierUnderCloudRun(t *testing.T) {
	f := gcpOne(t, loadGCP(t), "GC5", "Serverless VPC connector is the fixed tier under an autoscaling one")
	if f == nil {
		t.Fatal("GC5 accepted 100 Cloud Run instances behind three f1-micro connector instances")
	}
	if f.Severity != rules.SeverityWarning {
		t.Errorf("severity = %q, want warning: Google publishes the throughput as an estimate", f.Severity)
	}
	if f.Metrics["aggregate_mbps_low"] != 300 {
		t.Errorf("aggregate_mbps_low = %d, want 300 (3 x the 100 Mbps low end)", f.Metrics["aggregate_mbps_low"])
	}
	if f.Metrics["mbps_per_consumer"] != 3 {
		t.Errorf("mbps_per_consumer = %d, want 3", f.Metrics["mbps_per_consumer"])
	}
	if f.Metrics["scale_ratio"] != 33 {
		t.Errorf("scale_ratio = %d, want 33", f.Metrics["scale_ratio"])
	}
}

// An unknown connector machine type has no encoded throughput, and inventing one
// is worse than saying nothing.
func TestGCPUnknownConnectorMachineStaysSilent(t *testing.T) {
	f := loadGCP(t)
	for _, r := range f.ByType("google_vpc_access_connector") {
		r.Values["machine_type"] = "e2-standard-8"
	}
	if got := gcpFindings(t, f, "GC5"); len(got) > 0 {
		t.Errorf("GC5 produced %d findings for an unencoded machine type", len(got))
	}
}

// GC6 --------------------------------------------------------------------

func TestGCPFrozenCloudSQLDiskIsReported(t *testing.T) {
	f := gcpOne(t, loadGCP(t), "GC6", "Cloud SQL storage cannot grow while the workload in front of it does")
	if f == nil {
		t.Fatal("GC6 missed disk_autoresize = false under an autoscaling Cloud Run service")
	}
	if f.Severity != rules.SeverityWarning {
		t.Errorf("severity = %q, want warning: it may be a deliberate cost decision", f.Severity)
	}
	if f.Metrics["disk_gib"] != 20 {
		t.Errorf("disk_gib = %d, want 20", f.Metrics["disk_gib"])
	}
}

func TestGCPAutoresizeEnabledIsSilent(t *testing.T) {
	f := loadGCP(t)
	for _, r := range f.ByType("google_sql_database_instance") {
		gcpBlockIn(t, r.Values, "settings")["disk_autoresize"] = true
	}
	if got := gcpFindings(t, f, "GC6"); len(got) > 0 {
		t.Errorf("GC6 still fires with autoresize on and no limit: %s", got[0].Summary)
	}
}

func TestGCPAutoresizeLimitIsAHardCeiling(t *testing.T) {
	f := loadGCP(t)
	for _, r := range f.ByType("google_sql_database_instance") {
		s := gcpBlockIn(t, r.Values, "settings")
		s["disk_autoresize"] = true
		s["disk_autoresize_limit"] = float64(50)
	}
	got := gcpOne(t, f, "GC6", "Cloud SQL storage cannot grow while the workload in front of it does")
	if got == nil {
		t.Fatal("GC6 missed disk_autoresize_limit, which is the same decision with a number attached")
	}
	if got.Metrics["disk_autoresize_limit_gib"] != 50 {
		t.Errorf("disk_autoresize_limit_gib = %d, want 50", got.Metrics["disk_autoresize_limit_gib"])
	}
}

// Catalog ----------------------------------------------------------------

// Cloud SQL puts the memory in the tier string, which is the one place GCP is
// kinder than AWS. Everything in GC1 rests on parsing it correctly.
func TestGCPTierMemory(t *testing.T) {
	c, err := catalog.Load()
	if err != nil {
		t.Fatalf("load catalog: %v", err)
	}
	cases := []struct {
		tier string
		mb   int
		ok   bool
	}{
		{"db-custom-2-7680", 7680, true},
		{"db-custom-16-104448", 104448, true},
		{"db-f1-micro", 614, true},
		{"db-g1-small", 1700, true},
		{"db-perf-optimized-N-2", 16384, true},
		{"db-perf-optimized-N-96", 786432, true},
		{"db-n1-standard-4", 0, false},
		{"", 0, false},
		{"db-custom-2", 0, false},
	}
	for _, tc := range cases {
		mb, ok := c.GCPSQLTierMemoryMB(tc.tier)
		if ok != tc.ok || mb != tc.mb {
			t.Errorf("GCPSQLTierMemoryMB(%q) = %d, %v; want %d, %v", tc.tier, mb, ok, tc.mb, tc.ok)
		}
	}
}

// The published table has a hole between 1.7 GB and 3.75 GB. Interpolating
// across it would be inventing a number.
func TestGCPUndocumentedMemoryBandIsNotInterpolated(t *testing.T) {
	c, err := catalog.Load()
	if err != nil {
		t.Fatalf("load catalog: %v", err)
	}
	if limit, _, ok := c.GCPSQLMaxConnections("POSTGRES_16", "db-custom-1-2048"); ok {
		t.Errorf("the catalog invented %d connections for 2048 MB, which sits in the documented gap", limit)
	}
	if limit, _, ok := c.GCPSQLMaxConnections("POSTGRES_16", "db-custom-8-61440"); !ok || limit != 800 {
		t.Errorf("60 GB should land in the 60-120 GB band at 800, got %d, %v", limit, ok)
	}
}

// The block table and the published formula have to agree on every row, because
// the whole GKE finding is one exponent away from nonsense.
func TestGCPPodBlockTableMatchesTheFormula(t *testing.T) {
	c, err := catalog.Load()
	if err != nil {
		t.Fatalf("load catalog: %v", err)
	}
	cases := map[int]int{8: 28, 9: 27, 16: 27, 17: 26, 32: 26, 33: 25, 64: 25, 65: 24, 110: 24, 128: 24, 129: 23, 256: 23}
	for pods, want := range cases {
		got, ok := c.GCPPodBlockNetmask(pods)
		if !ok || got != want {
			t.Errorf("GCPPodBlockNetmask(%d) = /%d, %v; want /%d", pods, got, ok, want)
		}
	}
	if _, ok := c.GCPPodBlockNetmask(257); ok {
		t.Error("GKE caps max_pods_per_node at 256; the catalog answered anyway")
	}
}
