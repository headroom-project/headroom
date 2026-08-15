package rules_test

import (
	"strings"
	"testing"

	"github.com/headroom-project/headroom/internal/catalog"
	"github.com/headroom-project/headroom/internal/graph"
	"github.com/headroom-project/headroom/internal/plan"
	"github.com/headroom-project/headroom/internal/rules"
)

const (
	fixtureAzureAKS    = "../../fixtures/azure-01-aks-postgres/plan.json"
	fixtureAzureHybrid = "../../fixtures/azure-02-vm-disk-vpn/plan.json"
)

func analyzeAzure(t *testing.T, path string) []rules.Finding {
	t.Helper()
	f, err := plan.Load(path)
	if err != nil {
		t.Fatalf("load fixture %s: %v", path, err)
	}
	c, err := catalog.Load()
	if err != nil {
		t.Fatalf("load catalog: %v", err)
	}
	if err := c.AzureCatalogError(); err != nil {
		t.Fatalf("load azure catalog: %v", err)
	}
	return rules.Run(f, graph.Build(f), c, rules.DefaultOptions())
}

// findAzure returns the first finding for a rule at a given severity, since a
// rule can legitimately emit both an info note and a real finding in one run.
func findAzure(all []rules.Finding, rule, severity string) *rules.Finding {
	for i := range all {
		if all[i].Rule == rule && all[i].Severity == severity {
			return &all[i]
		}
	}
	return nil
}

// The whole catalog is the product, so a table that will not parse has to fail
// loudly rather than turn every Azure rule silently into a no-op.
func TestAzureCatalogParses(t *testing.T) {
	c, err := catalog.Load()
	if err != nil {
		t.Fatalf("load catalog: %v", err)
	}
	if err := c.AzureCatalogError(); err != nil {
		t.Fatalf("azure tables: %v", err)
	}
}

// The tier prefix on a flexible server sku_name says what you pay, never what
// you get. Only the compute size decides the connection ceiling, so all three
// tiers must resolve through the same table.
func TestAzureSKUNameParsing(t *testing.T) {
	c, _ := catalog.Load()

	for _, tc := range []struct {
		sku  string
		want int
	}{
		{"B_Standard_B1ms", 50},
		{"GP_Standard_D4ds_v5", 1718},
		{"MO_Standard_E8ds_v5", 5000},
		{"Standard_D2s_v3", 859},
	} {
		limit, ok := c.AzurePostgresConnections(tc.sku)
		if !ok {
			t.Errorf("%s: no catalog entry", tc.sku)
			continue
		}
		if limit.MaxConnections != tc.want {
			t.Errorf("%s: max_connections = %d, want %d", tc.sku, limit.MaxConnections, tc.want)
		}
	}

	if _, ok := c.AzurePostgresConnections("GP_Standard_D999ds_v9"); ok {
		t.Error("an unknown compute size resolved: the catalog must refuse to interpolate")
	}
}

// Azure rounds a disk up to the next offered size and serves it at that tier's
// performance, so 200 GiB of Standard SSD is an E15 and not a proportional
// slice of one.
func TestAzureDiskTierRoundsUp(t *testing.T) {
	c, _ := catalog.Load()

	tier, ok := c.AzureDiskTierFor("StandardSSD_LRS", 200)
	if !ok {
		t.Fatal("200 GiB of Standard SSD did not resolve to a tier")
	}
	if tier.Tier != "E15" {
		t.Errorf("tier = %s, want E15", tier.Tier)
	}

	tier, ok = c.AzureDiskTierFor("Premium_LRS", 1024)
	if !ok {
		t.Fatal("1024 GiB of Premium SSD did not resolve to a tier")
	}
	if tier.Tier != "P30" || tier.IOPS != 5000 {
		t.Errorf("tier = %s at %d IOPS, want P30 at 5000", tier.Tier, tier.IOPS)
	}

	// Premium SSD v2 and Ultra state their own IOPS on the resource, so there
	// is deliberately no ladder to look them up in.
	if _, ok := c.AzureDiskTierFor("PremiumV2_LRS", 1024); ok {
		t.Error("PremiumV2_LRS resolved to a fixed tier, which it does not have")
	}
}

// AZ1: 30 workers x 20 connections against a B1ms that accepts 35 user
// connections. The ceiling is the user connections, not the raw max, because
// the service keeps 15 for itself.
func TestAzureDatabaseOutgrownByAppServicePlan(t *testing.T) {
	f := findAzure(analyzeAzure(t, fixtureAzureAKS), "AZ1", rules.SeverityCritical)
	if f == nil {
		t.Fatal("AZ1 missed 600 connections against a B_Standard_B1ms")
	}
	if got := f.Metrics["demand"]; got != 600 {
		t.Errorf("demand = %d, want 600 (30 workers x DB_POOL_SIZE=20)", got)
	}
	if got := f.Metrics["ceiling"]; got != 35 {
		t.Errorf("ceiling = %d, want 35: B1ms allows 50 connections and reserves 15", got)
	}
	if f.Confidence != "high" {
		t.Errorf("confidence = %q, want high: DB_POOL_SIZE is declared in app_settings, not assumed", f.Confidence)
	}
	if f.Source == "" {
		t.Error("no source: every grounded number must carry the page it came from")
	}
}

// A consumer whose scale the plan never states must be named as skipped rather
// than dropped. Silence and "there is nothing here" have to be distinguishable,
// or the report is telling the reader something it does not know.
func TestAzureUnstatedConsumerScaleIsReportedNotDropped(t *testing.T) {
	f, err := plan.Load(fixtureAzureAKS)
	if err != nil {
		t.Fatalf("load fixture: %v", err)
	}
	for _, r := range f.Resources() {
		switch r.Type {
		case "azurerm_service_plan":
			r.Values["worker_count"] = nil
		case "azurerm_monitor_autoscale_setting":
			r.Values["profile"] = []any{}
		}
	}
	c, _ := catalog.Load()
	got := rules.Run(f, graph.Build(f), c, rules.DefaultOptions())

	if f := findAzure(got, "AZ1", rules.SeverityCritical); f != nil {
		t.Errorf("AZ1 reported a demand it could not derive: %s", f.Summary)
	}
	info := findAzure(got, "AZ1", rules.SeverityInfo)
	if info == nil {
		t.Fatal("AZ1 went quiet without saying which consumer it skipped")
	}
	if !strings.Contains(info.Summary, "does not state how many") {
		t.Errorf("info finding does not explain what was skipped: %q", info.Summary)
	}
}

// AZ2 is the one that catches people: the demand is pods, not nodes.
func TestAzureAKSSubnetExhaustedByPods(t *testing.T) {
	f := findAzure(analyzeAzure(t, fixtureAzureAKS), "AZ2", rules.SeverityCritical)
	if f == nil {
		t.Fatal("AZ2 missed 20 nodes x 110 pods in a /24")
	}
	if got := f.Metrics["usable"]; got != 251 {
		t.Errorf("usable = %d, want 251: a /24 is 256 addresses and Azure reserves 5", got)
	}
	// (12 + 8) nodes, each reserving one node address plus 110 pod addresses.
	if got := f.Metrics["demand"]; got != 2220 {
		t.Errorf("demand = %d, want 2220 (20 nodes x (1 + 110))", got)
	}
}

// Overlay hands pods addresses from a CIDR outside the virtual network, so
// there is no subnet to exhaust and the correct output is nothing at all.
func TestAzureOverlayClusterIsSilent(t *testing.T) {
	f, err := plan.Load(fixtureAzureAKS)
	if err != nil {
		t.Fatalf("load fixture: %v", err)
	}
	for _, r := range f.Resources() {
		if r.Type != "azurerm_kubernetes_cluster" {
			continue
		}
		profiles, _ := r.Values["network_profile"].([]any)
		for _, item := range profiles {
			if profile, ok := item.(map[string]any); ok {
				profile["network_plugin_mode"] = "overlay"
			}
		}
	}
	c, _ := catalog.Load()

	got := rules.Run(f, graph.Build(f), c, rules.DefaultOptions())
	if f := findAzure(got, "AZ2", rules.SeverityCritical); f != nil {
		t.Errorf("AZ2 fired on an overlay cluster: %s", f.Summary)
	}
}

// AZ3: one public IP is 64,512 ports, and on Azure CNI the divisor is pods.
func TestAzureNATSNATPortsExhausted(t *testing.T) {
	f := findAzure(analyzeAzure(t, fixtureAzureAKS), "AZ3", rules.SeverityCritical)
	if f == nil {
		t.Fatal("AZ3 missed 2200 pods behind a single public IP")
	}
	if got := f.Metrics["ports_total"]; got != 64512 {
		t.Errorf("ports_total = %d, want 64512 for one public IP", got)
	}
	if got := f.Metrics["endpoints"]; got != 2200 {
		t.Errorf("endpoints = %d, want 2200 (20 nodes x 110 pods)", got)
	}
	if got := f.Metrics["ports_per_endpoint"]; got >= 32 {
		t.Errorf("ports_per_endpoint = %d, want below the 32 Azure allocates for its largest documented pool", got)
	}
}

// Adding public IPs is the one lever that genuinely moves this ceiling, so the
// rule has to respond to it.
func TestAzureNATPortsScaleWithPublicIPs(t *testing.T) {
	f := findAzure(analyzeAzure(t, fixtureAzureAKS), "AZ3", rules.SeverityCritical)
	if f == nil {
		t.Fatal("AZ3 did not fire")
	}
	if f.Metrics["ports_total"] != f.Metrics["public_ips"]*64512 {
		t.Errorf("ports_total %d is not public_ips %d x 64512",
			f.Metrics["ports_total"], f.Metrics["public_ips"])
	}
}

// AZ4: two connections on one gateway is redundancy, not bandwidth. It is a
// warning because the design is frequently correct.
func TestAzureVPNConnectionsShareGatewayBandwidth(t *testing.T) {
	f := findAzure(analyzeAzure(t, fixtureAzureHybrid), "AZ4", rules.SeverityWarning)
	if f == nil {
		t.Fatal("AZ4 missed two IPsec connections on one VpnGw1")
	}
	if got := f.Metrics["connections"]; got != 2 {
		t.Errorf("connections = %d, want 2", got)
	}
	if got := f.Metrics["throughput_mbps"]; got != 650 {
		t.Errorf("throughput_mbps = %d, want 650 for VpnGw1", got)
	}
	if !strings.Contains(f.Summary, "share") {
		t.Errorf("summary does not say the tunnels share the bandwidth: %q", f.Summary)
	}
}

// A single connection creates no false expectation of aggregation, so there is
// nothing to say.
func TestAzureSingleVPNConnectionIsSilent(t *testing.T) {
	f, err := plan.Load(fixtureAzureHybrid)
	if err != nil {
		t.Fatalf("load fixture: %v", err)
	}
	var kept []plan.Resource
	dropped := false
	for _, r := range f.PlannedValues.RootModule.Resources {
		if r.Type == "azurerm_virtual_network_gateway_connection" && !dropped {
			dropped = true
			continue
		}
		kept = append(kept, r)
	}
	f.PlannedValues.RootModule.Resources = kept
	c, _ := catalog.Load()

	if got := findAzure(rules.Run(f, graph.Build(f), c, rules.DefaultOptions()), "AZ4", rules.SeverityWarning); got != nil {
		t.Errorf("AZ4 fired on a single VPN connection: %s", got.Summary)
	}
}

// AZ5: a P30 provisions 5,000 IOPS onto a VM that can drive 1,280.
func TestAzureDiskOutrunsTheVM(t *testing.T) {
	f := findAzure(analyzeAzure(t, fixtureAzureHybrid), "AZ5", rules.SeverityWarning)
	if f == nil {
		t.Fatal("AZ5 missed a P30 attached to a Standard_B2s")
	}
	if got := f.Metrics["disk_iops"]; got != 5000 {
		t.Errorf("disk_iops = %d, want 5000 for a P30", got)
	}
	if got := f.Metrics["vm_iops"]; got != 1280 {
		t.Errorf("vm_iops = %d, want 1280 uncached for Standard_B2s", got)
	}
}

// A cached disk counts against a different, larger VM limit that this catalog
// does not carry, so it must not be measured against the uncached one.
func TestAzureCachedDiskIsNotCounted(t *testing.T) {
	f, err := plan.Load(fixtureAzureHybrid)
	if err != nil {
		t.Fatalf("load fixture: %v", err)
	}
	for _, r := range f.Resources() {
		if r.Type == "azurerm_virtual_machine_data_disk_attachment" {
			r.Values["caching"] = "ReadOnly"
		}
	}
	c, _ := catalog.Load()

	if got := findAzure(rules.Run(f, graph.Build(f), c, rules.DefaultOptions()), "AZ5", rules.SeverityWarning); got != nil {
		t.Errorf("AZ5 measured a cached disk against the uncached VM limit: %s", got.Summary)
	}
}

// AZ6 must not be a copy of R7. On AWS an unset credit mode means unlimited and
// the ceiling is a bill; on Azure there is no unlimited mode and the ceiling is
// a throttle, so the advice is different.
func TestAzureBurstableThrottlesWithNoUnlimitedEscape(t *testing.T) {
	f := findAzure(analyzeAzure(t, fixtureAzureHybrid), "AZ6", rules.SeverityWarning)
	if f == nil {
		t.Fatal("AZ6 missed a Standard_B2s")
	}
	if got := f.Metrics["vcpus"]; got != 2 {
		t.Errorf("vcpus = %d, want 2", got)
	}
	if got := f.Metrics["baseline_pct"]; got != 20 {
		t.Errorf("baseline_pct = %d, want 20 for Standard_B2s", got)
	}
	if got := f.Metrics["credits_per_hour"]; got != 24 {
		t.Errorf("credits_per_hour = %d, want 24", got)
	}
	if !strings.Contains(f.Summary, "no unlimited mode") {
		t.Errorf("summary does not state that Azure has no unlimited mode: %q", f.Summary)
	}
}

// A size outside the catalog has no encoded ceiling, and inventing one is worse
// than saying nothing.
func TestAzureUnknownVMSizeIsSilent(t *testing.T) {
	f, err := plan.Load(fixtureAzureHybrid)
	if err != nil {
		t.Fatalf("load fixture: %v", err)
	}
	for _, r := range f.Resources() {
		if r.Type == "azurerm_linux_virtual_machine" {
			r.Values["size"] = "Standard_NOT_A_REAL_SIZE"
		}
	}
	c, _ := catalog.Load()
	got := rules.Run(f, graph.Build(f), c, rules.DefaultOptions())

	if f := findAzure(got, "AZ6", rules.SeverityWarning); f != nil {
		t.Errorf("AZ6 fired on an unknown VM size: %s", f.Summary)
	}
	if f := findAzure(got, "AZ5", rules.SeverityWarning); f != nil {
		t.Errorf("AZ5 fired on an unknown VM size: %s", f.Summary)
	}
}

// Every Azure finding has to survive being pasted into a team chat with no
// other context, and every number in it has to be traceable.
func TestAzureFindingsAreWellFormed(t *testing.T) {
	for _, path := range []string{fixtureAzureAKS, fixtureAzureHybrid} {
		for _, f := range analyzeAzure(t, path) {
			if !strings.HasPrefix(f.Rule, "AZ") {
				continue
			}
			if f.Title == "" || f.Summary == "" {
				t.Errorf("%s in %s has an empty title or summary", f.Rule, path)
			}
			if f.Confidence == "" {
				t.Errorf("%s in %s has no confidence", f.Rule, path)
			}
			if len(f.Resources) == 0 {
				t.Errorf("%s in %s names no resources", f.Rule, path)
			}
			if f.Severity == rules.SeverityInfo {
				continue
			}
			if len(f.Metrics) == 0 {
				t.Errorf("%s in %s carries no metrics, so the backend cannot re-render it", f.Rule, path)
			}
			if f.Source == "" {
				t.Errorf("%s in %s cites no source", f.Rule, path)
			}
		}
	}
}

// The AWS rules must keep working with the Azure set registered, and neither
// namespace may leak into the other.
func TestAzureRulesDoNotDisturbAWS(t *testing.T) {
	f, err := plan.Load("../../fixtures/01-ecs-rds/plan.json")
	if err != nil {
		t.Skipf("aws fixture unavailable: %v", err)
	}
	c, _ := catalog.Load()
	got := rules.Run(f, graph.Build(f), c, rules.DefaultOptions())

	sawR1 := false
	for _, finding := range got {
		if strings.HasPrefix(finding.Rule, "AZ") {
			t.Errorf("an Azure rule fired on an AWS plan: %s %s", finding.Rule, finding.Summary)
		}
		if finding.Rule == "R1" {
			sawR1 = true
		}
	}
	if !sawR1 {
		t.Error("R1 stopped firing on the AWS fixture once the Azure rules were registered")
	}
}
