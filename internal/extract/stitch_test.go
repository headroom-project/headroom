package extract

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

func sha256Hex(v string) string {
	sum := sha256.Sum256([]byte(v))
	return hex.EncodeToString(sum[:])[:12]
}

// Stitching was written when AWS was the only cloud in the tool, and it stayed
// that way after the Azure and GCP rules landed: the hash prefix was the literal
// "aws:" and nothing outside AWS was recognised as an identifier at all. The
// cross repository view is the paid premise of this product, and for two of the
// three clouds it did not exist.
func TestIdentifiersAreRecognisedOnEveryCloudTheRulesCover(t *testing.T) {
	r := NewRedactor("org-salt")

	for _, tc := range []struct {
		name string
		id   string
		kind string
	}{
		{
			"azure subnet",
			"/subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/rg/providers/Microsoft.Network/virtualNetworks/vnet/subnets/app",
			"microsoft-network-virtualnetworks",
		},
		{
			"gcp network self link",
			"https://www.googleapis.com/compute/v1/projects/example-project/global/networks/prod",
			"networks",
		},
		{
			"gcp relative name",
			"projects/example-project/regions/us-east1/subnetworks/apps",
			"subnetworks",
		},
		{"aws vpc, which must not change", "vpc-0123456789abcdef0", "vpc"},
	} {
		if !looksLikeCloudID(tc.id) {
			t.Errorf("%s: not recognised as an identifier, so it can never join two repositories", tc.name)
			continue
		}
		if got := kindOf(tc.id); got != tc.kind {
			t.Errorf("%s: kind = %q, want %q", tc.name, got, tc.kind)
		}
		if r.CloudID(tc.id) == "" {
			t.Errorf("%s: empty hash", tc.name)
		}
	}
}

// The kind is the only part of an identifier that travels in the clear, so it
// must carry a type published by the provider and never a name somebody chose.
// An ARM id is made almost entirely of names somebody chose.
func TestKindNeverCarriesANameOutOfThePlan(t *testing.T) {
	id := "/subscriptions/11111111-2222-3333-4444-555555555555/resourceGroups/prod-rg-01/" +
		"providers/Microsoft.Network/virtualNetworks/company-vnet/subnets/payments"

	kind := kindOf(id)
	if kind != "microsoft-network-virtualnetworks" {
		t.Fatalf("kind = %q", kind)
	}
	for _, secret := range []string{"prod-rg-01", "company-vnet", "payments", "11111111"} {
		if strings.Contains(kind, secret) {
			t.Errorf("kind %q carries %q out of the plan", kind, secret)
		}
	}
}

// Two clouds must never collide, because an organization's graph is joined on
// these hashes and a collision joins two things that are not the same thing.
func TestEachCloudHashesIntoItsOwnNamespace(t *testing.T) {
	r := NewRedactor("org-salt")

	// The same string, if it could ever be issued by two clouds, must not
	// produce the same hash. Constructed rather than realistic, because the
	// property is about the namespace and not about the shapes.
	azure := "/subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/rg"
	gcp := "projects/example-project/global/networks/prod"
	if r.CloudID(azure) == r.CloudID(gcp) {
		t.Error("an Azure id and a GCP id hashed to the same value")
	}

	// An identifier this file does not recognise keeps the original namespace,
	// so nothing that was already being hashed changes value on upgrade. That
	// matters more than it looks: a stored join that changes hash is a join
	// that silently disappears.
	unknown := "something-nobody-anticipated"
	sum := sha256Hex("org-salt" + "\x01aws:" + unknown)
	if r.CloudID(unknown) != sum {
		t.Errorf("an unrecognised identifier changed namespace: %s, want %s", r.CloudID(unknown), sum)
	}
}

// A salt is what makes any of this worth anything, so the same identifier in two
// organizations must not join.
func TestSaltKeepsOrganizationsApartOnEveryCloud(t *testing.T) {
	id := "/subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/rg/providers/Microsoft.Network/virtualNetworks/vnet"
	if NewRedactor("org-a").CloudID(id) == NewRedactor("org-b").CloudID(id) {
		t.Error("two organizations hashed the same Azure id to the same value")
	}
}
