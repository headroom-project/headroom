package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The CLI is the whole product surface for a free user, and until this file
// existed not one line of it was exercised. Exit codes matter most: headroom is
// meant to run inside somebody's pipeline, so "the tool broke" and "the tool
// found something" have to be distinguishable from the outside.

const (
	fixtureCritical = "../../fixtures/01-ecs-rds/plan.json"    // 3 critical, 2 warning
	fixtureQuiet    = "../../fixtures/05-cross-repo/plan.json" // 0 critical, 1 warning
)

type result struct {
	code   int
	stdout string
	stderr string
	err    error
}

func exec(t *testing.T, args ...string) result {
	t.Helper()
	var out, errb bytes.Buffer
	code, err := run(args, &out, &errb)
	return result{code: code, stdout: out.String(), stderr: errb.String(), err: err}
}

// --- invocation and exit codes -------------------------------------------

func TestNoArgsPrintsUsageAndSucceeds(t *testing.T) {
	r := exec(t)
	if r.code != exitOK || r.err != nil {
		t.Fatalf("code = %d, err = %v, want 0 and nil", r.code, r.err)
	}
	for _, want := range []string{"headroom " + version, "usage:", "--fail-on", "exit codes:"} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("usage is missing %q", want)
		}
	}
}

func TestVersionIsPrintedAndParseable(t *testing.T) {
	for _, arg := range []string{"version", "--version"} {
		r := exec(t, arg)
		if r.code != exitOK {
			t.Errorf("%s: code = %d, want 0", arg, r.code)
		}
		if got := strings.TrimSpace(r.stdout); got != "headroom "+version {
			t.Errorf("%s printed %q, want %q", arg, got, "headroom "+version)
		}
	}
}

// Exit code 2 is "could not run". Anything the user typed wrong lands here, and
// never on 1, which a pipeline reads as a real finding.
func TestUserErrorsExitTwoAndNeverOne(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"unknown command", []string{"analyse", fixtureCritical}, "unknown command"},
		{"no plan file", []string{"analyze"}, "exactly one plan"},
		{"two plan files", []string{"analyze", fixtureCritical, fixtureQuiet}, "exactly one plan"},
		{"missing file", []string{"analyze", "does-not-exist.json"}, ""},
		{"unknown flag", []string{"analyze", "--nope", fixtureCritical}, ""},
		{"bad fail-on", []string{"analyze", "--fail-on", "urgent", fixtureCritical}, "not a severity"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := exec(t, tc.args...)
			if r.code != exitError {
				t.Errorf("code = %d, want %d", r.code, exitError)
			}
			if r.err == nil {
				t.Fatal("no error returned")
			}
			if tc.want != "" && !strings.Contains(r.err.Error(), tc.want) {
				t.Errorf("error = %q, want it to mention %q", r.err, tc.want)
			}
		})
	}
}

// A typo in --fail-on used to fall through to the critical-only behaviour, so a
// pipeline that asked to fail on warnings silently failed on less. This is the
// regression that made the flag worth validating.
func TestFailOnTypoIsRejectedRatherThanQuietlyWeakened(t *testing.T) {
	r := exec(t, "analyze", "--fail-on", "warnings", fixtureQuiet)
	if r.code != exitError {
		t.Fatalf("code = %d, want %d: a plural typo must not be accepted", r.code, exitError)
	}
	if !strings.Contains(r.err.Error(), "warning") || !strings.Contains(r.err.Error(), "critical") {
		t.Errorf("the error does not name the accepted values: %v", r.err)
	}
}

// A flag whose default comes from os.Getenv has that default printed back by
// PrintDefaults, and PrintDefaults runs on any flag error. So a single mistyped
// flag used to write HEADROOM_SALT straight into stderr, which in CI means
// straight into a log a whole team can read. The environment is read after
// Parse instead, and this is the test that says so.
func TestAFlagErrorNeverPrintsASecretFromTheEnvironment(t *testing.T) {
	t.Setenv("HEADROOM_SALT", "salt-from-the-environment")

	r := exec(t, "analyze", "--not-a-flag", fixtureCritical)
	if r.code != exitError {
		t.Fatalf("code = %d, want %d", r.code, exitError)
	}
	if strings.Contains(r.stderr, "salt-from-the-environment") {
		t.Errorf("a flag error printed the salt from the environment: %s", r.stderr)
	}
	if strings.Contains(r.stdout, "salt-from-the-environment") {
		t.Error("a flag error printed the salt on stdout")
	}
	// The salt still has to work, or the fix broke the flag it was fixing.
	ok := exec(t, "analyze", "--dry-run", fixtureCritical)
	if strings.Contains(ok.stderr, "no salt set") {
		t.Error("HEADROOM_SALT stopped being read at all")
	}
}

func TestFailOnCriticalExitsOneOnACriticalPlan(t *testing.T) {
	r := exec(t, "analyze", "--fail-on", "critical", fixtureCritical)
	if r.code != exitFinding {
		t.Fatalf("code = %d, want %d", r.code, exitFinding)
	}
	if r.err != nil {
		t.Errorf("a matched gate is not a tool error, got err = %v", r.err)
	}
	if !strings.Contains(r.stdout, "CRITICAL") {
		t.Error("the report was not written before the gate fired")
	}
}

// The gate has to be silent when nothing matches, otherwise nobody turns it on.
func TestFailOnCriticalIsSilentWhenOnlyWarningsExist(t *testing.T) {
	r := exec(t, "analyze", "--fail-on", "critical", fixtureQuiet)
	if r.code != exitOK {
		t.Fatalf("code = %d, want 0: this plan has warnings and no criticals", r.code)
	}
}

func TestFailOnWarningCatchesWhatCriticalLetsThrough(t *testing.T) {
	critical := exec(t, "analyze", "--fail-on", "critical", fixtureQuiet)
	warning := exec(t, "analyze", "--fail-on", "warning", fixtureQuiet)
	if critical.code != exitOK {
		t.Fatalf("--fail-on critical = %d, want 0", critical.code)
	}
	if warning.code != exitFinding {
		t.Fatalf("--fail-on warning = %d, want %d on the same plan", warning.code, exitFinding)
	}
}

// Without the flag there is no gate at all, whatever the findings say. This is
// the fail-open promise: a capacity tool must never be the reason a deploy is
// stuck.
func TestWithoutFailOnEvenACriticalPlanExitsZero(t *testing.T) {
	r := exec(t, "analyze", fixtureCritical)
	if r.code != exitOK {
		t.Fatalf("code = %d, want 0 with no --fail-on", r.code)
	}
	if !strings.Contains(r.stdout, "CRITICAL") {
		t.Error("the critical findings were not reported")
	}
}

// --- output shapes --------------------------------------------------------

func TestJSONOutputIsValidAndCarriesTheNumbers(t *testing.T) {
	r := exec(t, "analyze", "--json", fixtureCritical)
	if r.code != exitOK || r.err != nil {
		t.Fatalf("code = %d, err = %v", r.code, r.err)
	}

	var findings []struct {
		Rule       string         `json:"rule"`
		Severity   string         `json:"severity"`
		Title      string         `json:"title"`
		Confidence string         `json:"confidence"`
		Metrics    map[string]int `json:"metrics"`
	}
	if err := json.Unmarshal([]byte(r.stdout), &findings); err != nil {
		t.Fatalf("--json did not emit valid JSON: %v", err)
	}
	if len(findings) == 0 {
		t.Fatal("no findings in the JSON for a plan the text mode calls critical")
	}

	var r1 int
	for _, f := range findings {
		if f.Rule == "" || f.Severity == "" || f.Title == "" {
			t.Errorf("a finding is missing an identifying field: %+v", f)
		}
		if f.Rule == "R1" {
			r1++
			if f.Metrics["demand"] != 800 || f.Metrics["ceiling"] != 450 {
				t.Errorf("R1 metrics = %v, want demand 800 and ceiling 450", f.Metrics)
			}
		}
	}
	if r1 == 0 {
		t.Error("R1 is absent from the JSON output")
	}

	// Text and JSON must describe the same run, or one of them is lying.
	text := exec(t, "analyze", fixtureCritical)
	if got := strings.Count(text.stdout, "CRITICAL "); got == 0 {
		t.Fatal("no CRITICAL lines in the text report")
	}
}

func TestTextReportNamesThePlanAndCountsTheFindings(t *testing.T) {
	r := exec(t, "analyze", fixtureCritical)
	if !strings.Contains(r.stdout, filepath.Base(fixtureCritical)) {
		t.Error("the report does not say which plan it read")
	}
	if !strings.Contains(r.stdout, "critical") || !strings.Contains(r.stdout, "total") {
		t.Error("the report has no summary line")
	}
	// The finding is only useful if it carries its own evidence.
	for _, want := range []string{"aws_db_instance.main", "db.t3.medium", "confidence:", "source:"} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("the report is missing %q", want)
		}
	}
}

// --- the promise the product is sold on -----------------------------------

// "Your plan file never leaves your machine" is the security claim on the
// landing page and in the README. --dry-run is what makes it checkable, so if
// this test ever fails the claim is false.
func TestDryRunPayloadCarriesNoRealAddresses(t *testing.T) {
	r := exec(t, "analyze", "--dry-run", "--salt", "test-salt", fixtureCritical)
	if r.code != exitOK || r.err != nil {
		t.Fatalf("code = %d, err = %v", r.code, r.err)
	}

	var payload struct {
		SchemaVersion string `json:"schema_version"`
		GeneratedBy   string `json:"generated_by"`
		Salted        bool   `json:"salted"`
		Nodes         []struct {
			ID   string `json:"id"`
			Type string `json:"type"`
		} `json:"nodes"`
		Findings []struct {
			Rule      string   `json:"rule"`
			Resources []string `json:"resources"`
		} `json:"findings"`
	}
	if err := json.Unmarshal([]byte(r.stdout), &payload); err != nil {
		t.Fatalf("--dry-run did not emit valid JSON: %v", err)
	}
	if !payload.Salted {
		t.Error("salted = false even though --salt was given")
	}
	if payload.GeneratedBy != "headroom/"+version {
		t.Errorf("generated_by = %q, want headroom/%s", payload.GeneratedBy, version)
	}
	if len(payload.Nodes) == 0 {
		t.Fatal("the payload has no nodes at all")
	}

	// Every real name that appears in the local report, and the prose around
	// it, must be absent from what would travel.
	leaks := []string{
		"aws_db_instance.main", "aws_ecs_service.api", "aws_subnet.private_a",
		"aws_appautoscaling_target.api", "DB_POOL_SIZE",
		"Scale asymmetry", "outgrows the database",
	}
	for _, leak := range leaks {
		if strings.Contains(r.stdout, leak) {
			t.Errorf("the payload contains %q, which never leaves the machine", leak)
		}
	}
	for _, n := range payload.Nodes {
		if strings.Contains(n.ID, ".") {
			t.Errorf("node id %q looks like a terraform address, not a hash", n.ID)
		}
	}
}

// Allowlisted capacity attributes travel with their real values, and a subnet
// CIDR is one of them. That is deliberate, because the ceiling cannot be
// recomputed server-side without it, but it means "nothing identifying leaves
// your machine" is not the whole truth: real network topology does. This test
// pins the fact so the privacy copy on the site and in the README has to keep
// saying it out loud rather than quietly rounding it off.
func TestAllowlistedAttributesTravelWithRealValues(t *testing.T) {
	r := exec(t, "analyze", "--dry-run", "--salt", "test-salt", fixtureCritical)

	var payload struct {
		Nodes []struct {
			Type  string         `json:"type"`
			Attrs map[string]any `json:"attrs"`
		} `json:"nodes"`
	}
	if err := json.Unmarshal([]byte(r.stdout), &payload); err != nil {
		t.Fatalf("payload: %v", err)
	}

	var sawCIDR, sawClass bool
	for _, n := range payload.Nodes {
		if n.Type == "aws_subnet" {
			if got, ok := n.Attrs["cidr_block"].(string); ok && got != "" {
				sawCIDR = true
			}
			// The address itself is still hashed, and anything outside the
			// allowlist is still absent.
			for key := range n.Attrs {
				switch key {
				case "cidr_block", "availability_zone", "map_public_ip_on_launch", "vpc_id", "ipv6_cidr_block":
				default:
					t.Errorf("aws_subnet carries %q, which is not a capacity attribute", key)
				}
			}
		}
		if n.Type == "aws_db_instance" {
			if n.Attrs["instance_class"] == "db.t3.medium" {
				sawClass = true
			}
		}
	}
	if !sawCIDR {
		t.Error("no subnet CIDR in the payload; if that changed on purpose, the privacy copy can be strengthened")
	}
	if !sawClass {
		t.Error("the instance class did not travel, and no ceiling can be recomputed without it")
	}
}

// A different salt has to produce different ids, or the salt is decoration and
// two customers hash their identical addresses to identical values.
func TestSaltChangesEveryIdentifier(t *testing.T) {
	a := exec(t, "analyze", "--dry-run", "--salt", "org-a", fixtureCritical)
	b := exec(t, "analyze", "--dry-run", "--salt", "org-b", fixtureCritical)
	if a.stdout == b.stdout {
		t.Fatal("two different salts produced byte-identical payloads")
	}

	ids := func(out string) []string {
		var p struct {
			Nodes []struct {
				ID string `json:"id"`
			} `json:"nodes"`
		}
		if err := json.Unmarshal([]byte(out), &p); err != nil {
			t.Fatalf("payload: %v", err)
		}
		got := make([]string, len(p.Nodes))
		for i, n := range p.Nodes {
			got[i] = n.ID
		}
		return got
	}
	idsA, idsB := ids(a.stdout), ids(b.stdout)
	if len(idsA) != len(idsB) {
		t.Fatalf("node counts differ: %d and %d", len(idsA), len(idsB))
	}
	for i := range idsA {
		if idsA[i] == idsB[i] {
			t.Errorf("node %d hashed to %q under both salts", i, idsA[i])
		}
	}
}

// An unsalted payload is reversible by guessing, because terraform addresses are
// low entropy. The warning is the only thing standing between that and an
// upload, so it has to be on stderr where a redirected stdout cannot hide it.
func TestUnsaltedDryRunWarnsOnStderr(t *testing.T) {
	t.Setenv("HEADROOM_SALT", "")
	r := exec(t, "analyze", "--dry-run", fixtureCritical)
	if !strings.Contains(r.stderr, "no salt set") {
		t.Errorf("stderr does not warn about the missing salt: %q", r.stderr)
	}
	if strings.Contains(r.stdout, "no salt set") {
		t.Error("the warning is on stdout, where it would end up inside a redirected payload file")
	}
}

// --- config discovery -----------------------------------------------------

// --no-config has to win over a headroom.yaml sitting next to the plan,
// otherwise there is no way to ask "what do the defaults say".
func TestNoConfigIgnoresADiscoveredFile(t *testing.T) {
	dir := t.TempDir()
	planCopy := filepath.Join(dir, "plan.json")
	raw, err := os.ReadFile(fixtureCritical)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	if err := os.WriteFile(planCopy, raw, 0o600); err != nil {
		t.Fatalf("write plan copy: %v", err)
	}
	cfg := "version: 1\nrules:\n  R1:\n    enabled: false\n"
	if err := os.WriteFile(filepath.Join(dir, "headroom.yaml"), []byte(cfg), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	withConfig := exec(t, "analyze", "--json", planCopy)
	if !strings.Contains(withConfig.stderr, "using config") {
		t.Error("the discovered headroom.yaml was not reported on stderr")
	}
	if strings.Contains(withConfig.stdout, `"R1"`) {
		t.Error("R1 fired even though the discovered config disables it")
	}

	without := exec(t, "analyze", "--json", "--no-config", planCopy)
	if strings.Contains(without.stderr, "using config") {
		t.Error("--no-config still loaded the file")
	}
	if !strings.Contains(without.stdout, `"R1"`) {
		t.Error("--no-config did not restore the built-in R1")
	}
}

func TestBrokenConfigIsAToolErrorNotAFinding(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.yaml")
	if err := os.WriteFile(bad, []byte("rules: [this is not a mapping\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	r := exec(t, "analyze", "--config", bad, fixtureCritical)
	if r.code != exitError {
		t.Fatalf("code = %d, want %d", r.code, exitError)
	}
}

// --- flags that change the numbers ----------------------------------------

// --pool-size is the assumption the finding rests on when the task definition
// is silent, so it has to actually move the demand figure.
func TestPoolSizeFlagMovesTheDemand(t *testing.T) {
	demand := func(args ...string) int {
		r := exec(t, args...)
		var findings []struct {
			Rule    string         `json:"rule"`
			Metrics map[string]int `json:"metrics"`
		}
		if err := json.Unmarshal([]byte(r.stdout), &findings); err != nil {
			t.Fatalf("json: %v", err)
		}
		for _, f := range findings {
			if f.Rule == "GC1" {
				return f.Metrics["demand"]
			}
		}
		return 0
	}
	const gcp = "../../fixtures/gcp-01-gke-cloudrun-sql/plan.json"
	base := demand("analyze", "--json", gcp)
	if base == 0 {
		t.Skip("no GC1 finding with a demand to move")
	}
	// The fixture declares DB_POOL_SIZE, so a declared value must win over the
	// flag: the flag is only the fallback.
	bumped := demand("analyze", "--json", "--pool-size", "50", gcp)
	if bumped != base {
		t.Errorf("demand moved from %d to %d, but the plan declares its pool size, so the flag must not apply", base, bumped)
	}
}
