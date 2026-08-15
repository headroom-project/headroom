package rules_test

import (
	"testing"

	"github.com/headroom-project/headroom/internal/catalog"
	"github.com/headroom-project/headroom/internal/graph"
	"github.com/headroom-project/headroom/internal/plan"
	"github.com/headroom-project/headroom/internal/rules"
)

const fixtureVMs = "../../fixtures/02-ec2-nat-ebs/plan.json"

func analyzeVMs(t *testing.T) []rules.Finding {
	t.Helper()
	f, err := plan.Load(fixtureVMs)
	if err != nil {
		t.Fatalf("load fixture: %v", err)
	}
	c, err := catalog.Load()
	if err != nil {
		t.Fatalf("load catalog: %v", err)
	}
	return rules.Run(f, graph.Build(f), c, rules.DefaultOptions())
}

// A route table association written with for_each carries its subnet dependency
// in `each.value`, so the only place the edge exists is the for_each expression
// itself. Dropping those expressions silently disconnects the graph and the rule
// goes quiet on infrastructure that is genuinely concentrated, which is the
// worst failure this tool has: confident silence.
func TestForEachEdgesSurviveIntoTheGraph(t *testing.T) {
	f := find(analyzeVMs(t), "R4")
	if f == nil {
		t.Fatal("R4 went silent: the nat -> route table -> association -> subnet chain broke")
	}
	if got := f.Metrics["subnets_served"]; got != 3 {
		t.Errorf("subnets_served = %d, want 3: one for_each block is three real subnets", got)
	}
	if got := f.Metrics["azs_served"]; got != 3 {
		t.Errorf("azs_served = %d, want 3", got)
	}
	if got := f.Metrics["workloads"]; got != 3 {
		t.Errorf("workloads = %d, want 3 instances behind the gateway", got)
	}
	if f.Severity != rules.SeverityWarning {
		t.Errorf("severity = %q, want warning: a single NAT is usually a deliberate cost decision, not a defect", f.Severity)
	}
}

func TestGP2BurstCliffIsReportedOncePerBlock(t *testing.T) {
	var gp2 *rules.Finding
	for _, f := range analyzeVMs(t) {
		if f.Rule == "R5" && f.Severity == rules.SeverityWarning {
			gp2 = &f
			break
		}
	}
	if gp2 == nil {
		t.Fatal("R5 missed three 20 GiB gp2 volumes")
	}
	if gp2.Instances != 3 {
		t.Errorf("instances = %d, want 3 collapsed into one finding", gp2.Instances)
	}
	if got := gp2.Metrics["baseline_iops"]; got != 100 {
		t.Errorf("baseline_iops = %d, want 100 (20 GiB x 3 IOPS, floored at 100)", got)
	}
}

func TestGP3RatioViolationIsCritical(t *testing.T) {
	var gp3 *rules.Finding
	for _, f := range analyzeVMs(t) {
		if f.Rule == "R5" && f.Severity == rules.SeverityCritical {
			gp3 = &f
			break
		}
	}
	if gp3 == nil {
		t.Fatal("R5 accepted 8000 IOPS on a 10 GiB gp3 volume, which AWS rejects at create time")
	}
	if got := gp3.Metrics["iops"]; got != 8000 {
		t.Errorf("iops = %d, want 8000", got)
	}
}

// The VM fixture has no database and no awsvpc tasks, so the container rules
// must produce nothing at all. A rule that fires on infrastructure it does not
// understand is worse than a rule that does not exist.
func TestContainerRulesStaySilentOnVMInfrastructure(t *testing.T) {
	for _, f := range analyzeVMs(t) {
		if f.Rule == "R1" || f.Rule == "R2" {
			t.Errorf("%s fired on a plan with no database and no awsvpc tasks: %s", f.Rule, f.Summary)
		}
	}
}
