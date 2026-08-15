package rules_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/headroom-project/headroom/internal/catalog"
	"github.com/headroom-project/headroom/internal/extract"
	"github.com/headroom-project/headroom/internal/graph"
	"github.com/headroom-project/headroom/internal/plan"
	"github.com/headroom-project/headroom/internal/rules"
)

const fixture = "../../fixtures/01-ecs-rds/plan.json"

func analyze(t *testing.T) (*plan.File, *graph.Graph, []rules.Finding) {
	t.Helper()
	f, err := plan.Load(fixture)
	if err != nil {
		t.Fatalf("load fixture: %v", err)
	}
	c, err := catalog.Load()
	if err != nil {
		t.Fatalf("load catalog: %v", err)
	}
	g := graph.Build(f)
	return f, g, rules.Run(f, g, c, rules.DefaultOptions())
}

func mustCatalog(t *testing.T) *catalog.Catalog {
	t.Helper()
	c, err := catalog.Load()
	if err != nil {
		t.Fatalf("load catalog: %v", err)
	}
	return c
}

func find(findings []rules.Finding, rule string) *rules.Finding {
	for i := range findings {
		if findings[i].Rule == rule {
			return &findings[i]
		}
	}
	return nil
}

// The connection ceiling has to come from the autoscaling target and the pool
// size declared in the task definition, not from desired_count and a guess.
// Reading desired_count would report 40 connections against a ceiling of 450
// and call the plan healthy, which is the exact failure this product exists to
// prevent.
func TestR1ReadsTheRealCeilingAndTheRealDemand(t *testing.T) {
	_, _, findings := analyze(t)

	f := find(findings, "R1")
	if f == nil {
		t.Fatal("R1 produced no finding on a plan that saturates its database")
	}
	if f.Severity != rules.SeverityCritical {
		t.Errorf("severity = %q, want critical", f.Severity)
	}
	if got := f.Metrics["demand"]; got != 800 {
		t.Errorf("demand = %d, want 800 (40 tasks from max_capacity x 20 from DB_POOL_SIZE)", got)
	}
	if got := f.Metrics["ceiling"]; got != 450 {
		t.Errorf("ceiling = %d, want 450 (db.t3.medium postgres)", got)
	}
	if got := f.Metrics["break_at_pct"]; got != 56 {
		t.Errorf("break_at_pct = %d, want 56", got)
	}
	if f.Confidence != "high" {
		t.Errorf("confidence = %q, want high: the pool size was declared, nothing was assumed", f.Confidence)
	}
}

func TestR2CountsUsableAddresses(t *testing.T) {
	_, _, findings := analyze(t)

	f := find(findings, "R2")
	if f == nil {
		t.Fatal("R2 produced no finding on two /28 subnets holding 40 awsvpc tasks")
	}
	if got := f.Metrics["usable"]; got != 11 {
		t.Errorf("usable = %d, want 11 (a /28 is 16 addresses, AWS reserves 5)", got)
	}
	if got := f.Metrics["demand"]; got != 20 {
		t.Errorf("demand = %d, want 20 (40 tasks spread over 2 subnets)", got)
	}
}

// Without a pool size in the task definition the rule must still fire, but it
// has to admit that the number is assumed.
func TestAssumedPoolSizeLowersConfidence(t *testing.T) {
	f, err := plan.Load(fixture)
	if err != nil {
		t.Fatalf("load fixture: %v", err)
	}
	for _, r := range f.ByType("aws_ecs_task_definition") {
		delete(r.Values, "container_definitions")
	}
	c, _ := catalog.Load()
	g := graph.Build(f)

	got := find(rules.Run(f, g, c, rules.DefaultOptions()), "R1")
	if got == nil {
		t.Fatal("R1 went silent when the pool size had to be assumed")
	}
	if got.Confidence != "medium" {
		t.Errorf("confidence = %q, want medium when the pool size is assumed", got.Confidence)
	}
}

// The security posture is the product's main sales argument, so it gets a test:
// nothing that identifies the customer's infrastructure may appear in the
// payload. This is the check that has to fail loudly if someone widens the
// allowlist without thinking.
func TestPayloadLeaksNoIdentifiers(t *testing.T) {
	f, g, findings := analyze(t)

	payload := extract.NewRedactor("org-salt").Build(f, g, findings, "test")
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	body := string(raw)

	forbidden := []string{
		"aws_db_instance.main",  // resource addresses
		"aws_ecs_service.api",   //
		"private_a",             // resource names
		"DB_POOL_SIZE",          // task definition environment
		"container_definitions", // the block itself, which can hold secrets
		"mock_access_key",       // provider credentials
		"node:22-alpine",        // image references
	}
	for _, needle := range forbidden {
		if strings.Contains(body, needle) {
			t.Errorf("payload leaks %q", needle)
		}
	}

	if len(payload.Nodes) == 0 || len(payload.Findings) == 0 {
		t.Fatal("payload came out empty, so the test above proves nothing")
	}
	for _, n := range payload.Nodes {
		if len(n.ID) != 12 {
			t.Errorf("node id %q is not a 12 char hash", n.ID)
		}
	}
}

func TestUnsupportedEngineStaysSilentInsteadOfGuessing(t *testing.T) {
	c, err := catalog.Load()
	if err != nil {
		t.Fatalf("load catalog: %v", err)
	}
	if _, _, ok := c.MaxConnections("aurora-postgresql", "db.r6g.large"); ok {
		t.Error("aurora returned an RDS ceiling; its formula is different and encoding the wrong number is worse than encoding none")
	}
	if _, _, ok := c.MaxConnections("postgres", "db.does.not.exist"); ok {
		t.Error("an unknown instance class produced a ceiling")
	}
}
