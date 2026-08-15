package rules_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/headroom-project/headroom/internal/extract"
	"github.com/headroom-project/headroom/internal/graph"
	"github.com/headroom-project/headroom/internal/plan"
	"github.com/headroom-project/headroom/internal/rules"
)

const fixtureCrossRepo = "../../fixtures/05-cross-repo/plan.json"

func payloadOf(t *testing.T, path, salt string) extract.Payload {
	t.Helper()
	f, err := plan.Load(path)
	if err != nil {
		t.Fatalf("load %s: %v", path, err)
	}
	g := graph.Build(f)
	return extract.NewRedactor(salt).Build(f, g, nil, "test")
}

// A service repository owns nothing underneath it, so on its own its plan is a
// handful of resources floating in nothing. The dangling edges are what make it
// joinable to the repository that does own the network.
func TestExternalReferencesAreCollected(t *testing.T) {
	p := payloadOf(t, fixtureCrossRepo, "org-salt")

	kinds := map[string]int{}
	for _, ref := range p.External {
		kinds[ref.Kind]++
	}
	for _, want := range []string{"vpc", "rtb", "nat"} {
		if kinds[want] == 0 {
			t.Errorf("no external reference of kind %q; the plan points at one and does not manage it", want)
		}
	}
}

// The join only works if the same cloud resource hashes to the same value no
// matter which attribute, resource or repository mentions it.
func TestSameCloudIDHashesToTheSameXID(t *testing.T) {
	p := payloadOf(t, fixtureCrossRepo, "org-salt")

	byKind := map[string][]string{}
	for _, ref := range p.External {
		byKind[ref.Kind] = append(byKind[ref.Kind], ref.XID)
	}
	vpcs := byKind["vpc"]
	if len(vpcs) < 2 {
		t.Fatalf("expected the VPC to be referenced from two resources, got %d", len(vpcs))
	}
	for _, xid := range vpcs[1:] {
		if xid != vpcs[0] {
			t.Errorf("the same VPC produced two different hashes (%s and %s), so the two repositories would never join", vpcs[0], xid)
		}
	}
}

// Different organizations must never collide, or one customer's graph would
// stitch itself onto another's.
func TestSaltIsolatesOrganizations(t *testing.T) {
	a := payloadOf(t, fixtureCrossRepo, "org-a")
	b := payloadOf(t, fixtureCrossRepo, "org-b")

	if len(a.External) == 0 {
		t.Fatal("no external references to compare")
	}
	if a.External[0].XID == b.External[0].XID {
		t.Error("two different salts produced the same xid, so tenants would stitch onto each other")
	}
}

func TestRemoteStateDependencyIsRecorded(t *testing.T) {
	p := payloadOf(t, fixtureCrossRepo, "org-salt")

	if len(p.RemoteStates) != 1 {
		t.Fatalf("remote states = %d, want 1", len(p.RemoteStates))
	}
	if p.RemoteStates[0].Backend != "local" {
		t.Errorf("backend = %q, want local", p.RemoteStates[0].Backend)
	}
	if p.RemoteStates[0].XID == "" {
		t.Error("the remote state has no hash, so nothing can point at it")
	}
}

// Stitching widened what the payload reads. The privacy guarantee has to hold
// at the new width too: identifiers travel as hashes or they do not travel.
func TestStitchingNeverLeaksRawIdentifiers(t *testing.T) {
	p := payloadOf(t, fixtureCrossRepo, "org-salt")

	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	body := string(raw)

	for _, needle := range []string{
		"vpc-0aaaaaaaaaaaaaaa1",
		"rtb-0aaaaaaaaaaaaaaa2",
		"nat-0aaaaaaaaaaaaaaa3",
		"ami-0000000000000dead",
		"network.tfstate",
		"aws_subnet.app",
		"mock_access_key",
	} {
		if strings.Contains(body, needle) {
			t.Errorf("payload leaks %q", needle)
		}
	}
}

// Nothing about stitching should change what the rules say about a plan.
func TestCrossRepoPlanStillAnalysesCleanly(t *testing.T) {
	f, err := plan.Load(fixtureCrossRepo)
	if err != nil {
		t.Fatalf("load fixture: %v", err)
	}
	c := mustCatalog(t)
	findings := rules.Run(f, graph.Build(f), c, rules.DefaultOptions())

	if find(findings, "R7") == nil {
		t.Error("R7 missed the t3.medium in a plan whose network lives elsewhere")
	}
	for _, got := range findings {
		if got.Rule == "R2" {
			t.Errorf("R2 fired on a subnet with no workload placed in it by this plan: %s", got.Summary)
		}
	}
}
