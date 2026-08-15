package rules_test

import (
	"testing"

	"github.com/headroom-project/headroom/internal/catalog"
	"github.com/headroom-project/headroom/internal/graph"
	"github.com/headroom-project/headroom/internal/plan"
	"github.com/headroom-project/headroom/internal/rules"
)

const fixtureQueue = "../../fixtures/04-sqs-lambda-scaling/plan.json"

func loadQueue(t *testing.T) *plan.File {
	t.Helper()
	f, err := plan.Load(fixtureQueue)
	if err != nil {
		t.Fatalf("load fixture: %v", err)
	}
	return f
}

func runOn(t *testing.T, f *plan.File) []rules.Finding {
	t.Helper()
	c, err := catalog.Load()
	if err != nil {
		t.Fatalf("load catalog: %v", err)
	}
	return rules.Run(f, graph.Build(f), c, rules.DefaultOptions())
}

func findByTitle(findings []rules.Finding, title string) *rules.Finding {
	for i := range findings {
		if findings[i].Title == title {
			return &findings[i]
		}
	}
	return nil
}

func TestVisibilityShorterThanTimeoutIsCritical(t *testing.T) {
	f := findByTitle(runOn(t, loadQueue(t)), "Queue gives up on a message before the function finishes it")
	if f == nil {
		t.Fatal("R3 accepted a 30s visibility timeout in front of a 60s function timeout")
	}
	if f.Severity != rules.SeverityCritical {
		t.Errorf("severity = %q, want critical: this is guaranteed duplicate processing, not a risk", f.Severity)
	}
	if got := f.Metrics["recommended"]; got != 360 {
		t.Errorf("recommended = %d, want 360 (6x the 60s function timeout)", got)
	}
}

// Raising visibility above the function timeout must silence the critical and
// leave only the softer recommendation, not silence the rule entirely.
func TestVisibilityAboveTimeoutDowngradesToTheRecommendation(t *testing.T) {
	f := loadQueue(t)
	for _, r := range f.ByType("aws_sqs_queue") {
		r.Values["visibility_timeout_seconds"] = float64(90)
	}
	findings := runOn(t, f)

	if got := findByTitle(findings, "Queue gives up on a message before the function finishes it"); got != nil {
		t.Error("the critical still fires with visibility above the function timeout")
	}
	if got := findByTitle(findings, "Visibility timeout leaves no room for retries"); got == nil {
		t.Error("the 6x recommendation went silent at 90s against a 60s timeout")
	}
}

func TestVisibilityAtSixTimesTheTimeoutIsSilent(t *testing.T) {
	f := loadQueue(t)
	for _, r := range f.ByType("aws_sqs_queue") {
		r.Values["visibility_timeout_seconds"] = float64(360)
	}
	for _, got := range runOn(t, f) {
		if got.Rule == "R3" && got.Metrics["visibility_timeout"] > 0 {
			t.Errorf("R3 still complains about visibility at the recommended 6x: %s", got.Title)
		}
	}
}

func TestReservedConcurrencyBelowFiveStarvesThePoller(t *testing.T) {
	f := findByTitle(runOn(t, loadQueue(t)), "Reserved concurrency starves the queue poller itself")
	if f == nil {
		t.Fatal("R3 accepted reserved concurrency of 2 on an SQS-triggered function")
	}
	if got := f.Metrics["minimum"]; got != 5 {
		t.Errorf("minimum = %d, want 5", got)
	}
}

// The cap is typed as a concurrency and lived with as a rate. Translating it is
// the whole value of the check, so the arithmetic gets a test.
func TestConcurrencyCapIsReportedAsADrainRate(t *testing.T) {
	f := findByTitle(runOn(t, loadQueue(t)), "Concurrency cap sets a drain rate nobody wrote down")
	if f == nil {
		t.Fatal("R3 did not translate the concurrency cap into a drain rate")
	}
	// 2 concurrent x 10 per batch / 60s = 0.333/s = 20 per minute.
	if got := f.Metrics["drain_per_min_ceil"]; got != 20 {
		t.Errorf("drain_per_min_ceil = %d, want 20", got)
	}
	if f.Confidence != "medium" {
		t.Errorf("confidence = %q, want medium: the real duration is unknown, only the timeout is", f.Confidence)
	}
}

func TestScalingAsymmetryReportsTheRatio(t *testing.T) {
	f := find(runOn(t, loadQueue(t)), "R6")
	if f == nil {
		t.Fatal("R6 missed a service scaling 20x in front of a fixed database")
	}
	if got := f.Metrics["scale_ratio"]; got != 20 {
		t.Errorf("scale_ratio = %d, want 20 (min 2, max 40)", got)
	}
}

func TestMissingStorageAutoscalingIsReported(t *testing.T) {
	f := findByTitle(runOn(t, loadQueue(t)), "Database storage cannot grow while the workload in front of it does")
	if f == nil {
		t.Fatal("R6 missed allocated_storage with no max_allocated_storage")
	}
	if got := f.Metrics["allocated_gib"]; got != 100 {
		t.Errorf("allocated_gib = %d, want 100", got)
	}
}

func TestStorageAutoscalingEnabledIsSilent(t *testing.T) {
	f := loadQueue(t)
	for _, r := range f.ByType("aws_db_instance") {
		r.Values["max_allocated_storage"] = float64(500)
	}
	if got := findByTitle(runOn(t, f), "Database storage cannot grow while the workload in front of it does"); got != nil {
		t.Error("R6 still complains when max_allocated_storage is set")
	}
}

// A tier that cannot travel far is not an asymmetry worth a line of output.
func TestNarrowScalingRangeIsSilent(t *testing.T) {
	f := loadQueue(t)
	for _, r := range f.ByType("aws_appautoscaling_target") {
		r.Values["max_capacity"] = float64(4)
	}
	for _, got := range runOn(t, f) {
		if got.Rule == "R6" && got.Metrics["scale_ratio"] > 0 {
			t.Errorf("R6 fired on a 2x range: %s", got.Summary)
		}
	}
}
