package rules_test

import (
	"testing"

	"github.com/headroom-project/headroom/internal/catalog"
	"github.com/headroom-project/headroom/internal/graph"
	"github.com/headroom-project/headroom/internal/plan"
	"github.com/headroom-project/headroom/internal/rules"
)

const fixtureHybrid = "../../fixtures/03-vpn-burstable/plan.json"

func analyzeHybrid(t *testing.T) []rules.Finding {
	t.Helper()
	f, err := plan.Load(fixtureHybrid)
	if err != nil {
		t.Fatalf("load fixture: %v", err)
	}
	c, err := catalog.Load()
	if err != nil {
		t.Fatalf("load catalog: %v", err)
	}
	return rules.Run(f, graph.Build(f), c, rules.DefaultOptions())
}

// A t3.xlarge with no credit_specification launches unlimited, so the ceiling
// is a bill rather than a throttle. Getting the mode backwards would invert the
// advice, which is worse than staying quiet.
func TestBurstableDefaultsToUnlimitedOnT3(t *testing.T) {
	f := find(analyzeHybrid(t), "R7")
	if f == nil {
		t.Fatal("R7 missed a t3.xlarge")
	}
	if got := f.Metrics["vcpus"]; got != 4 {
		t.Errorf("vcpus = %d, want 4", got)
	}
	if got := f.Metrics["baseline_pct"]; got != 40 {
		t.Errorf("baseline_pct = %d, want 40", got)
	}
	if f.Title != "Burstable instance in unlimited mode: the ceiling is the invoice" {
		t.Errorf("title = %q: with no credit_specification a T3 defaults to unlimited, not standard", f.Title)
	}
}

func TestExplicitStandardModeChangesTheAdvice(t *testing.T) {
	f, err := plan.Load(fixtureHybrid)
	if err != nil {
		t.Fatalf("load fixture: %v", err)
	}
	for _, r := range f.ByType("aws_instance") {
		r.Values["credit_specification"] = []any{
			map[string]any{"cpu_credits": "standard"},
		}
	}
	c, _ := catalog.Load()

	got := find(rules.Run(f, graph.Build(f), c, rules.DefaultOptions()), "R7")
	if got == nil {
		t.Fatal("R7 went silent on an explicit standard-mode instance")
	}
	if got.Title != "Burstable instance throttles to baseline once credits run out" {
		t.Errorf("title = %q, want the throttling variant", got.Title)
	}
}

// Two connections on a virtual private gateway is redundancy, not bandwidth.
func TestVPNConnectionsDoNotAggregateOnAVGW(t *testing.T) {
	f := find(analyzeHybrid(t), "R8")
	if f == nil {
		t.Fatal("R8 missed two VPN connections on one virtual private gateway")
	}
	if got := f.Metrics["connections"]; got != 2 {
		t.Errorf("connections = %d, want 2", got)
	}
	if f.Severity != rules.SeverityWarning {
		t.Errorf("severity = %q, want warning: the design is a valid availability choice", f.Severity)
	}
}

// A single connection creates no false expectation of aggregation, so the rule
// has nothing to say.
func TestSingleVPNConnectionIsSilent(t *testing.T) {
	f, err := plan.Load(fixtureHybrid)
	if err != nil {
		t.Fatalf("load fixture: %v", err)
	}
	var kept []plan.Resource
	dropped := false
	for _, r := range f.PlannedValues.RootModule.Resources {
		if r.Type == "aws_vpn_connection" && !dropped {
			dropped = true
			continue
		}
		kept = append(kept, r)
	}
	f.PlannedValues.RootModule.Resources = kept
	c, _ := catalog.Load()

	if got := find(rules.Run(f, graph.Build(f), c, rules.DefaultOptions()), "R8"); got != nil {
		t.Errorf("R8 fired on a single VPN connection: %s", got.Summary)
	}
}
