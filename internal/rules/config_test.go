package rules_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/headroom-project/headroom/internal/config"
	"github.com/headroom-project/headroom/internal/graph"
	"github.com/headroom-project/headroom/internal/plan"
	"github.com/headroom-project/headroom/internal/rules"
)

func withConfig(t *testing.T, fixture, cfgPath string, now time.Time) []rules.Finding {
	t.Helper()
	f, err := plan.Load(fixture)
	if err != nil {
		t.Fatalf("load fixture: %v", err)
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	c := mustCatalog(t)

	opt := rules.DefaultOptions()
	opt.Config = cfg
	opt.Now = now
	return rules.Run(f, graph.Build(f), c, opt)
}

const orgConfig = "testdata/org.yaml"

var beforeExpiry = time.Date(2026, 8, 14, 0, 0, 0, 0, time.UTC)

func TestDisabledRuleProducesNothing(t *testing.T) {
	findings := withConfig(t, fixtureVMs, orgConfig, beforeExpiry)
	if got := find(findings, "R4"); got != nil {
		t.Error("R4 is disabled in the config and still produced a finding")
	}
	// Turning one rule off must not silence the rest.
	if got := find(findings, "R5"); got == nil {
		t.Error("disabling R4 also silenced R5")
	}
}

func TestSeverityOverrideApplies(t *testing.T) {
	findings := withConfig(t, fixture, orgConfig, beforeExpiry)
	got := find(findings, "R2")
	if got == nil {
		t.Fatal("R2 disappeared")
	}
	if got.Severity != rules.SeverityInfo {
		t.Errorf("severity = %q, want info from the config override", got.Severity)
	}
}

// An exception silences a finding only while it is still valid, and only for
// the resource it names.
func TestValidExceptionSuppresses(t *testing.T) {
	findings := withConfig(t, fixtureVMs, orgConfig, beforeExpiry)
	if got := find(findings, "R7"); got != nil {
		t.Errorf("R7 fired despite a valid exception: %s", got.Summary)
	}
}

// The day it expires, the finding comes back carrying the reason that was given
// at the time. Debt that lapses quietly is debt nobody pays.
func TestExpiredExceptionRestoresTheFindingWithItsReason(t *testing.T) {
	afterExpiry := time.Date(2027, 3, 1, 0, 0, 0, 0, time.UTC)
	got := find(withConfig(t, fixtureVMs, orgConfig, afterExpiry), "R7")
	if got == nil {
		t.Fatal("R7 stayed suppressed by an exception that expired in 2026")
	}
	joined := strings.Join(got.Detail, " ")
	if !strings.Contains(joined, "expired on 2026-12-31") {
		t.Error("the restored finding does not say the exception expired")
	}
	if !strings.Contains(joined, "Sandbox account") {
		t.Error("the restored finding does not carry the reason that was given")
	}
}

func TestCustomRuleFires(t *testing.T) {
	got := find(withConfig(t, fixtureVMs, orgConfig, beforeExpiry), "ORG-001")
	if got == nil {
		t.Fatal("ORG-001 missed three gp2 volumes")
	}
	if got.Severity != rules.SeverityCritical {
		t.Errorf("severity = %q, want critical", got.Severity)
	}
	if !strings.Contains(got.Summary, "aws_ebs_volume.data") || !strings.Contains(got.Summary, "20 GiB") {
		t.Errorf("summary did not render its placeholders: %q", got.Summary)
	}
	if !strings.Contains(strings.Join(got.Detail, " "), "org.yaml") {
		t.Error("a custom finding must say it came from this organization's config, not from a headroom default")
	}
}

// cidr_prefix comparisons exist so "a /28 is too small" is expressible without
// asking anyone to write netmask arithmetic in YAML.
func TestCustomCIDRPrefixOperator(t *testing.T) {
	if got := find(withConfig(t, fixture, orgConfig, beforeExpiry), "ORG-002"); got == nil {
		t.Error("ORG-002 missed a /28 subnet")
	}
	if got := find(withConfig(t, fixtureVMs, orgConfig, beforeExpiry), "ORG-002"); got != nil {
		t.Errorf("ORG-002 fired on /24 subnets: %s", got.Summary)
	}
}

// Attribute paths have to reach into a nested block, because that is where half
// of what matters lives.
func TestCustomRuleReadsNestedBlock(t *testing.T) {
	if got := find(withConfig(t, fixtureVMs, orgConfig, beforeExpiry), "ORG-003"); got != nil {
		t.Errorf("ORG-003 fired on gp3 root volumes: %s", got.Summary)
	}
}

// A declared ceiling beats the catalog default, and it beats it with full
// confidence: a human stated the fact instead of the tool guessing at it.
func TestDeclaredCeilingOverridesTheCatalog(t *testing.T) {
	// The catalog puts db.t3.medium postgres at ~450, and 800 connections
	// against it is critical. The config declares 2000, so the same plan is fine.
	if got := find(withConfig(t, fixture, orgConfig, beforeExpiry), "R1"); got != nil {
		t.Errorf("R1 still fires against a declared ceiling of 2000: %s", got.Summary)
	}
}

func TestConfigRejectsBadInput(t *testing.T) {
	cases := map[string]string{
		"missing version": `
rules:
  R1:
    enabled: false
`,
		"unknown field": `
version: 1
ruels:
  R1:
    enabled: false
`,
		"bad severity": `
version: 1
rules:
  R1:
    severity: blocker
`,
		"exception without reason": `
version: 1
exceptions:
  - rule: R1
    resource: aws_db_instance.main
    expires: 2026-12-31
`,
		"exception without expiry": `
version: 1
exceptions:
  - rule: R1
    resource: aws_db_instance.main
    reason: "later"
`,
		"custom id in the builtin namespace": `
version: 1
custom:
  - id: R9
    title: mine
    match:
      type: aws_subnet
`,
		"condition with no operator": `
version: 1
custom:
  - id: ORG-X
    title: mine
    match:
      type: aws_subnet
      where:
        - attr: cidr_block
`,
	}

	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "headroom.yaml")
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatalf("write: %v", err)
			}
			if _, err := config.Load(path); err == nil {
				t.Error("config was accepted; a silent misconfiguration is a silent policy change")
			}
		})
	}
}

func TestNoConfigChangesNothing(t *testing.T) {
	f, err := plan.Load(fixtureVMs)
	if err != nil {
		t.Fatalf("load fixture: %v", err)
	}
	plain := rules.Run(f, graph.Build(f), mustCatalog(t), rules.DefaultOptions())

	if find(plain, "R4") == nil {
		t.Error("R4 is missing without any config, so the disable test proves nothing")
	}
	if find(plain, "ORG-001") != nil {
		t.Error("a custom rule fired with no config loaded")
	}
}
