package rules_test

import "testing"

// These lock the two ceilings that were shipping wrong on 2026-08-14.
//
// Both survived a fully green suite, which is the lesson worth keeping: the
// tests asserted that the code did what the catalog said, and said nothing
// about whether the catalog was true. A number nobody exercises is a number
// nobody checks.

// MariaDB does not share the MySQL formula. The catalog used MariaDB 10.4's
// divisor, so every MariaDB ceiling was reported at twice its real value: the
// one direction that matters, because headroom was promising room that was not
// there.
func TestMariaDBUsesItsOwnDivisorAndCap(t *testing.T) {
	c := mustCatalog(t)

	got, formula, ok := c.MaxConnections("mariadb", "db.r5.large")
	if !ok {
		t.Fatal("mariadb on db.r5.large produced no ceiling")
	}
	if got != 682 {
		t.Errorf("ceiling = %d, want 682 (16 GiB / 25165760). 1365 would mean the MySQL divisor came back", got)
	}
	if formula.Divisor != 25165760 {
		t.Errorf("divisor = %d, want 25165760 for MariaDB 10.5 and above", formula.Divisor)
	}

	// The cap is the other half of the formula and only shows up on classes
	// large enough to pass it.
	huge, _, ok := c.MaxConnections("mariadb", "db.r5.24xlarge")
	if !ok {
		t.Fatal("mariadb on db.r5.24xlarge produced no ceiling")
	}
	if huge != 12000 {
		t.Errorf("ceiling = %d, want the 12000 cap", huge)
	}
}

// MySQL and MariaDB reading the same number would mean the divisors were
// collapsed back together.
func TestMySQLAndMariaDBDoNotAgree(t *testing.T) {
	c := mustCatalog(t)

	mysql, _, okMySQL := c.MaxConnections("mysql", "db.r5.large")
	maria, _, okMaria := c.MaxConnections("mariadb", "db.r5.large")
	if !okMySQL || !okMaria {
		t.Fatal("one of the two engines produced no ceiling")
	}
	if mysql == maria {
		t.Errorf("both engines report %d on the same class; they use different formulas", mysql)
	}
}

// gp3 was capped at the AWS Outposts limits, five times below the real service
// ceiling, so R5 reported a create-time failure on volumes AWS accepts. A false
// positive costs more than a missed ceiling: it teaches people to ignore the
// tool.
func TestGP3AcceptsVolumesAWSActuallyAllows(t *testing.T) {
	c := mustCatalog(t)
	gp3 := c.GP3()

	if gp3.MaxIOPS != 80000 {
		t.Errorf("max IOPS = %d, want 80000. 16000 is the Outposts limit, not the service limit", gp3.MaxIOPS)
	}
	if gp3.MaxThroughputMiB != 2000 {
		t.Errorf("max throughput = %d MiB/s, want 2000. 1000 is the Outposts limit", gp3.MaxThroughputMiB)
	}

	// 20,000 IOPS on 100 GiB is legal: the 500 IOPS per GiB ratio allows
	// 50,000 there, and 20,000 is well under the 80,000 service maximum.
	if 20000 > gp3.MaxIOPSPerGiB*100 {
		t.Error("the ratio check would reject a legal 20000 IOPS volume on 100 GiB")
	}
	if 20000 > gp3.MaxIOPS {
		t.Error("the absolute check would reject a legal 20000 IOPS volume")
	}
}

// R5 silenced gp2 volumes from roughly 334 GiB to 1000 GiB by comparing the
// baseline IOPS against a literal 1000, which is BurstIrrelevantAtGiB, a size in
// GiB reused as an IOPS figure. Those volumes have a real cliff, and it is the
// size range a production database lands in.
func TestGP2CliffIsSilencedOnlyWhenBaselineReachesBurst(t *testing.T) {
	c := mustCatalog(t)
	gp2 := c.GP2()

	// 400 GiB earns 1200 baseline and still bursts to 3000. The literal 1000
	// made this one disappear.
	if got := c.GP2Baseline(400); got != 1200 {
		t.Fatalf("baseline at 400 GiB = %d, want 1200", got)
	}
	if c.GP2Baseline(400) >= gp2.BurstIOPS {
		t.Error("400 GiB is treated as having no cliff, and it has a 2.5x one")
	}
	if 400 >= gp2.BurstIrrelevantAtGiB {
		t.Error("400 GiB is above the size at which burst stops mattering")
	}

	// The genuine silence point is where baseline catches the burst ceiling,
	// which AWS documents as 1000 GiB and the arithmetic agrees.
	if got := c.GP2Baseline(gp2.BurstIrrelevantAtGiB); got < gp2.BurstIOPS {
		t.Errorf("baseline at %d GiB = %d, which is below the %d burst ceiling",
			gp2.BurstIrrelevantAtGiB, got, gp2.BurstIOPS)
	}
}

// AZ4 quoted the Generation2 throughput for every dual-generation SKU, which
// overstates a Generation1 gateway's headroom by up to 2x. The plan carries the
// answer: azurerm_virtual_network_gateway has a "generation" argument and
// extract allowlists it.
func TestAzureVPNGatewayResolvesGeneration(t *testing.T) {
	c := mustCatalog(t)

	gen1, ok := c.AzureVPNGatewayFor("VpnGw3AZ", "Generation1")
	if !ok {
		t.Fatal("VpnGw3AZ is not in the catalog")
	}
	gen2, _ := c.AzureVPNGatewayFor("VpnGw3AZ", "Generation2")
	if gen1.ThroughputMbps == gen2.ThroughputMbps {
		t.Fatalf("both generations report %d Mbps; the generation is being ignored", gen1.ThroughputMbps)
	}
	if gen1.ThroughputMbps > gen2.ThroughputMbps {
		t.Errorf("Generation1 (%d) is quoted above Generation2 (%d)", gen1.ThroughputMbps, gen2.ThroughputMbps)
	}
	if !gen1.GenerationDeclared || !gen1.DualGeneration {
		t.Error("a declared generation on a dual-generation SKU is not being flagged as declared")
	}

	// Undeclared falls to the lower figure on purpose. Overstating a ceiling
	// promises headroom that is not there, which is the direction that burns
	// the customer.
	silent, _ := c.AzureVPNGatewayFor("VpnGw3AZ", "")
	if silent.ThroughputMbps != gen1.ThroughputMbps {
		t.Errorf("undeclared generation quotes %d Mbps, want the Generation1 figure %d",
			silent.ThroughputMbps, gen1.ThroughputMbps)
	}
	if silent.GenerationDeclared {
		t.Error("an absent generation argument is being reported as declared")
	}

	// A single-generation SKU must not be dragged into any of this.
	only, ok := c.AzureVPNGatewayFor("VpnGw1", "")
	if !ok {
		t.Fatal("VpnGw1 is not in the catalog")
	}
	if only.DualGeneration {
		t.Error("VpnGw1 is Generation1 only and is being treated as dual")
	}
	if only.ThroughputMbps != 650 {
		t.Errorf("VpnGw1 = %d Mbps, want 650", only.ThroughputMbps)
	}
}

// The Azure VM table went from four series to ten on 2026-08-16, and eight of
// eleven real stacks in a customer estate had been false negatives purely
// because their size was absent. Absence is a silent failure mode here: the
// rule stays quiet and the report looks clean, so a size disappearing from the
// table can only be caught by counting.
func TestAzureVMTableKeepsItsCoverage(t *testing.T) {
	c := mustCatalog(t)

	// One representative per encoded series, with the uncached pair each one
	// was verified against.
	for _, tc := range []struct {
		size       string
		iops       int
		mbps       float64
		v2IOPS     int
		v2MBps     float64
		wantSeries string
	}{
		{"Standard_B2s", 1280, 15, 0, 0, "Bv1"},
		{"Standard_B4s_v2", 6400, 145, 0, 0, "Bsv2"},
		{"Standard_B4as_v2", 6400, 145, 0, 0, "Basv2"},
		{"Standard_D4s_v5", 6400, 145, 0, 0, "Dsv5"},
		{"Standard_E8s_v4", 12800, 192, 0, 0, "Esv4"},
		{"Standard_E8s_v5", 12800, 290, 0, 0, "Esv5"},
		{"Standard_E8as_v5", 12800, 200, 0, 0, "Easv5"},
		{"Standard_E16s_v6", 25600, 848, 33333, 992, "Esv6"},
		{"Standard_FX8mds_v2", 33000, 1091, 44200, 1200, "FXmdsv2"},
		{"Standard_FX8ms_v2", 33000, 1091, 44200, 1200, "FXmsv2"},
	} {
		size, ok := c.AzureVMSizeOf(tc.size)
		if !ok {
			t.Errorf("%s left the catalog", tc.size)
			continue
		}
		if size.Series != tc.wantSeries {
			t.Errorf("%s: series = %q, want %q", tc.size, size.Series, tc.wantSeries)
		}
		if size.UncachedIOPS != tc.iops || size.UncachedMBps != tc.mbps {
			t.Errorf("%s: uncached = %d IOPS and %.0f MB/s, want %d and %.0f",
				tc.size, size.UncachedIOPS, size.UncachedMBps, tc.iops, tc.mbps)
		}
		// The second pair governs Ultra Disk and Premium SSD v2 and only exists
		// where the series page publishes it. Inventing one would overstate a
		// ceiling; losing one understates it, which manufactures findings.
		if size.UncachedV2IOPS != tc.v2IOPS || size.UncachedV2MBps != tc.v2MBps {
			t.Errorf("%s: v2 uncached = %d IOPS and %.0f MB/s, want %d and %.0f",
				tc.size, size.UncachedV2IOPS, size.UncachedV2MBps, tc.v2IOPS, tc.v2MBps)
		}
		if size.Source == "" || size.VerifiedAt == "" || size.Confidence == "" {
			t.Errorf("%s: entry without source, verified_at or confidence", tc.size)
		}
	}

	// The trap that made this series worth encoding by hand: the AMD burstable
	// is not the Intel one at 32 vCPUs, and every size below that matches.
	amd, _ := c.AzureVMSizeOf("Standard_B32as_v2")
	intel, _ := c.AzureVMSizeOf("Standard_B32s_v2")
	if amd.UncachedIOPS != 25600 || intel.UncachedIOPS != 51200 {
		t.Errorf("B32as_v2 = %d IOPS and B32s_v2 = %d, want 25600 and 51200: copying one series onto the other doubles a ceiling",
			amd.UncachedIOPS, intel.UncachedIOPS)
	}
}

// Every Premium SSD v2 disk gets 3,000 IOPS and 125 MB/s free, and terraform
// leaves both attributes optional, so the common case is a disk that states
// nothing and performs anyway. Reading that as zero is how a rule walks past
// terabytes of storage.
func TestPremiumV2BaselineIsEncodedAndSourced(t *testing.T) {
	c := mustCatalog(t)

	base, ok := c.AzurePremiumV2Baseline()
	if !ok {
		t.Fatal("no Premium SSD v2 baseline in the catalog")
	}
	if base.IOPS != 3000 || base.MBps != 125 {
		t.Errorf("baseline = %d IOPS and %d MB/s, want 3000 and 125", base.IOPS, base.MBps)
	}
	if base.Source == "" || base.VerifiedAt == "" {
		t.Error("the baseline carries no source: every grounded number must name the page it came from")
	}
}
