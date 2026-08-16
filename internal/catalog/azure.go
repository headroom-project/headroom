package catalog

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
)

// The Azure tables live in their own file with their own embeds and their own
// lazy parse, so that adding a cloud never means touching catalog.go and two
// people can add two clouds without meeting in a merge conflict.

//go:embed data/azure-postgresql.json
var azPostgresJSON []byte

//go:embed data/azure-mysql.json
var azMySQLJSON []byte

//go:embed data/azure-network.json
var azNetworkJSON []byte

//go:embed data/azure-aks.json
var azAKSJSON []byte

//go:embed data/azure-disks.json
var azDisksJSON []byte

//go:embed data/azure-vm.json
var azVMJSON []byte

// AzureDBLimit is the connection ceiling of one managed database compute size.
// MaxUserConnections is the number that matters to an application: the service
// keeps some of MaxConnections for replication and monitoring.
type AzureDBLimit struct {
	VCores             int `json:"vcores"`
	MemoryGiB          int `json:"memory_gib"`
	MaxConnections     int `json:"max_connections"`
	MaxUserConnections int `json:"max_user_connections"`

	// Filled in from the enclosing table so a caller holding one limit still
	// has everything it needs to cite it.
	Source     string `json:"-"`
	VerifiedAt string `json:"-"`
	Confidence string `json:"-"`
	Notes      string `json:"-"`
	Engine     string `json:"-"`
}

type azDBCatalog struct {
	Source     string                  `json:"source"`
	VerifiedAt string                  `json:"verified_at"`
	Confidence string                  `json:"confidence"`
	Notes      string                  `json:"notes"`
	Reserved   int                     `json:"reserved_connections"`
	SKUs       map[string]AzureDBLimit `json:"skus"`
}

// AzureSubnet is the address arithmetic every Azure subnet obeys.
type AzureSubnet struct {
	ReservedAddresses int    `json:"reserved_addresses"`
	SmallestPrefix    int    `json:"smallest_prefix"`
	LargestPrefix     int    `json:"largest_prefix"`
	Source            string `json:"source"`
	VerifiedAt        string `json:"verified_at"`
	Confidence        string `json:"confidence"`
	Notes             string `json:"notes"`
}

type AzureNATGateway struct {
	PortsPerPublicIP      int    `json:"snat_ports_per_public_ip"`
	MaxPublicIPs          int    `json:"max_public_ips"`
	MaxConnections        int    `json:"max_concurrent_connections"`
	ConnectionsPerIPDest  int    `json:"concurrent_connections_per_ip_same_destination"`
	DefaultTCPIdleMinutes int    `json:"default_tcp_idle_minutes"`
	Source                string `json:"source"`
	VerifiedAt            string `json:"verified_at"`
	Confidence            string `json:"confidence"`
	Notes                 string `json:"notes"`
}

// AzureLBSNAT is Azure's own statement of how many SNAT ports one instance
// needs. It describes Load Balancer rather than NAT Gateway, and it is used
// only as a yardstick: the port inventory divided by the number of instances is
// the same question whichever device does the translation.
type AzureLBSNAT struct {
	PortsPerFrontendIP int `json:"ports_per_frontend_ip"`
	MaxPerInstance     int `json:"max_default_ports_per_instance"`
	MinPerInstance     int `json:"min_default_ports_per_instance"`
	Allocation         []struct {
		PoolMax int `json:"pool_max"`
		Ports   int `json:"ports"`
	} `json:"default_allocation"`
	Source     string `json:"source"`
	VerifiedAt string `json:"verified_at"`
	Confidence string `json:"confidence"`
	Notes      string `json:"notes"`
}

type AzureVPNSKU struct {
	Generation     string `json:"generation"`
	Tunnels        int    `json:"tunnels"`
	P2SIKEv2       int    `json:"p2s_ikev2"`
	P2SSSTP        int    `json:"p2s_sstp"`
	ThroughputMbps int    `json:"throughput_mbps"`
	BGP            bool   `json:"bgp"`
	ZoneRedundant  bool   `json:"zone_redundant"`
	VMsSupported   int    `json:"vms_supported"`

	// Four SKUs exist in both generations at different throughput. Zero means
	// the SKU does not exist in that generation.
	ThroughputMbpsGen1 int `json:"throughput_mbps_gen1"`
	ThroughputMbpsGen2 int `json:"throughput_mbps_gen2"`
	VMsSupportedGen1   int `json:"vms_supported_gen1"`
	VMsSupportedGen2   int `json:"vms_supported_gen2"`

	// ResolvedGeneration is the generation the figures above were taken from,
	// and GenerationDeclared says whether the plan stated it or the catalog
	// picked the conservative side. Both are set by AzureVPNGatewayFor and are
	// empty on the raw lookup.
	ResolvedGeneration string `json:"-"`
	GenerationDeclared bool   `json:"-"`
	DualGeneration     bool   `json:"-"`
	OtherGenThroughput int    `json:"-"`

	Source     string `json:"-"`
	VerifiedAt string `json:"-"`
	Confidence string `json:"-"`
	Notes      string `json:"-"`
	SKUNotes   string `json:"-"`
	Name       string `json:"-"`
}

type azVPNCatalog struct {
	Source     string                 `json:"source"`
	VerifiedAt string                 `json:"verified_at"`
	Confidence string                 `json:"confidence"`
	Notes      string                 `json:"notes"`
	SKUNotes   string                 `json:"sku_notes"`
	SKUs       map[string]AzureVPNSKU `json:"skus"`
}

type azNetCatalog struct {
	Subnet AzureSubnet     `json:"subnet"`
	NAT    AzureNATGateway `json:"nat_gateway"`
	LBSNAT AzureLBSNAT     `json:"load_balancer_snat"`
	VPN    azVPNCatalog    `json:"vpn_gateway"`
}

// AzureAKS carries the pod-address arithmetic. The whole rule turns on
// DefaultMaxPods: a plan that does not set max_pods still commits to one.
type AzureAKS struct {
	MaxPodsHardLimit int            `json:"max_pods_hard_limit"`
	MinPods          int            `json:"min_pods"`
	DefaultMaxPods   map[string]int `json:"default_max_pods"`
	SubnetFormula    string         `json:"subnet_formula"`
	Source           string         `json:"source"`
	VerifiedAt       string         `json:"verified_at"`
	Confidence       string         `json:"confidence"`
	Notes            string         `json:"notes"`
	SurgeNotes       string         `json:"surge_notes"`
}

// AzureDiskTier is one rung of the Premium SSD or Standard SSD size ladder.
// Performance is a step function of size, which is what makes it derivable from
// the terraform at all.
type AzureDiskTier struct {
	Tier      string `json:"tier"`
	SizeGiB   int    `json:"size_gib"`
	IOPS      int    `json:"iops"`
	MBps      int    `json:"mbps"`
	BurstIOPS int    `json:"burst_iops"`
	BurstMBps int    `json:"burst_mbps"`

	Kind       string `json:"-"`
	Source     string `json:"-"`
	VerifiedAt string `json:"-"`
	Confidence string `json:"-"`
	Notes      string `json:"-"`
}

type azDiskCatalog struct {
	Source           string          `json:"source"`
	VerifiedAt       string          `json:"verified_at"`
	Confidence       string          `json:"confidence"`
	Notes            string          `json:"notes"`
	BurstNotes       string          `json:"burst_notes"`
	StandardSSDNotes string          `json:"standard_ssd_notes"`
	PremiumSSD       []AzureDiskTier `json:"premium_ssd"`
	StandardSSD      []AzureDiskTier `json:"standard_ssd"`
}

// AzureVMSize carries both ceilings a VM imposes: how much CPU it sustains, and
// how much disk traffic it can drive. BaselinePct is zero for anything that is
// not burstable.
type AzureVMSize struct {
	Series       string  `json:"series"`
	VCPUs        int     `json:"vcpus"`
	MemoryGiB    float64 `json:"memory_gib"`
	BaselinePct  float64 `json:"baseline_pct"`
	CreditsPerHr int     `json:"credits_per_hour"`
	MaxBanked    int     `json:"max_banked_credits"`
	UncachedIOPS int     `json:"uncached_iops"`
	UncachedMBps float64 `json:"uncached_mbps"`
	BurstIOPS    int     `json:"burst_iops"`
	BurstMBps    int     `json:"burst_mbps"`

	Name       string `json:"-"`
	Source     string `json:"-"`
	VerifiedAt string `json:"-"`
	Confidence string `json:"-"`
	SeriesNote string `json:"-"`
}

type azVMSeries struct {
	Source     string `json:"source"`
	VerifiedAt string `json:"verified_at"`
	Confidence string `json:"confidence"`
	Notes      string `json:"notes"`
}

type azVMCatalog struct {
	Coverage     string                 `json:"coverage"`
	BurstNotes   string                 `json:"burst_notes"`
	StorageNotes string                 `json:"storage_notes"`
	Series       map[string]azVMSeries  `json:"series"`
	Sizes        map[string]AzureVMSize `json:"sizes"`
}

type azureTables struct {
	postgres azDBCatalog
	mysql    azDBCatalog
	network  azNetCatalog
	aks      AzureAKS
	disks    azDiskCatalog
	vms      azVMCatalog

	// Lowercased key back to the catalog's own spelling. Azure accepts a VM
	// size and a gateway sku in any case, so a plan that writes one of them
	// differently is not writing something unknown.
	vmByFold  map[string]string
	vpnByFold map[string]string

	err error
}

// azFoldIndex builds the case insensitive index for a table.
//
// Two entries that differ only in case would make a folded lookup depend on map
// iteration order, so that is a catalog error rather than a coin toss.
func azFoldIndex[V any](m map[string]V, file string) (map[string]string, error) {
	out := make(map[string]string, len(m))
	for k := range m {
		fold := strings.ToLower(k)
		if other, clash := out[fold]; clash {
			return nil, &azCatalogError{file: file, err: fmt.Errorf(
				"%q and %q differ only in case, so a lookup cannot resolve either", other, k)}
		}
		out[fold] = k
	}
	return out, nil
}

// azCanonical resolves what a plan wrote to the key the catalog holds.
//
// Azure does not distinguish case in a VM size or a gateway sku, so neither
// does this: a plan that writes Standard_E32s_V5 names a size the portal
// accepts and the catalog already carries, and refusing it means the rule goes
// quiet over a spelling. The catalog's own key comes back, so a finding quotes
// the spelling the vendor documentation uses rather than the one that happened
// to be typed.
func azCanonical[V any](name string, table map[string]V, fold map[string]string) (string, bool) {
	if _, ok := table[name]; ok {
		return name, true
	}
	canonical, ok := fold[strings.ToLower(name)]
	return canonical, ok
}

var (
	azOnce   sync.Once
	azLoaded azureTables
)

// loadAzure parses the Azure tables on first use. A malformed table is recorded
// rather than panicked on: the AWS rules must keep working even if someone
// breaks a JSON file in this directory.
func loadAzure() *azureTables {
	azOnce.Do(func() {
		t := &azLoaded
		for _, step := range []struct {
			raw  []byte
			into any
			name string
		}{
			{azPostgresJSON, &t.postgres, "azure-postgresql.json"},
			{azMySQLJSON, &t.mysql, "azure-mysql.json"},
			{azNetworkJSON, &t.network, "azure-network.json"},
			{azAKSJSON, &t.aks, "azure-aks.json"},
			{azDisksJSON, &t.disks, "azure-disks.json"},
			{azVMJSON, &t.vms, "azure-vm.json"},
		} {
			if err := json.Unmarshal(step.raw, step.into); err != nil {
				t.err = &azCatalogError{file: step.name, err: err}
				return
			}
		}

		var err error
		if t.vmByFold, err = azFoldIndex(t.vms.Sizes, "azure-vm.json"); err != nil {
			t.err = err
			return
		}
		if t.vpnByFold, err = azFoldIndex(t.network.VPN.SKUs, "azure-network.json"); err != nil {
			t.err = err
			return
		}
	})
	return &azLoaded
}

type azCatalogError struct {
	file string
	err  error
}

func (e *azCatalogError) Error() string {
	return "catalog " + e.file + " is malformed: " + e.err.Error()
}
func (e *azCatalogError) Unwrap() error { return e.err }

// AzureCatalogError reports a broken Azure table, so a rule can say what it
// skipped instead of silently reporting nothing.
func (c *Catalog) AzureCatalogError() error { return loadAzure().err }

// azureComputeSize turns a flexible server sku_name into the compute size the
// Microsoft limit tables are keyed by. "GP_Standard_D4ds_v5" is tier GP over
// size D4ds_v5, and only the size decides the connection ceiling.
func azureComputeSize(sku string) string {
	s := strings.TrimSpace(sku)
	for _, tier := range []string{"B_", "GP_", "MO_"} {
		s = strings.TrimPrefix(s, tier)
	}
	return strings.TrimPrefix(s, "Standard_")
}

// AzurePostgresConnections returns the connection ceiling a freshly created
// PostgreSQL flexible server gets for a sku_name. An unknown size yields
// ok=false and the caller must stay silent: interpolating between two rows of
// the table would be a guess.
func (c *Catalog) AzurePostgresConnections(sku string) (AzureDBLimit, bool) {
	return azDBLookup(&loadAzure().postgres, sku, "postgresql")
}

// AzureMySQLConnections is the same for MySQL flexible server. The number is a
// default rather than a hard ceiling there, because MySQL lets max_connections
// be raised, and the rule has to say so.
func (c *Catalog) AzureMySQLConnections(sku string) (AzureDBLimit, bool) {
	return azDBLookup(&loadAzure().mysql, sku, "mysql")
}

func azDBLookup(t *azDBCatalog, sku, engine string) (AzureDBLimit, bool) {
	limit, ok := t.SKUs[azureComputeSize(sku)]
	if !ok {
		return AzureDBLimit{}, false
	}
	limit.Source, limit.VerifiedAt = t.Source, t.VerifiedAt
	limit.Confidence, limit.Notes, limit.Engine = t.Confidence, t.Notes, engine
	return limit, true
}

// AzureReservedConnections is how many of the documented connections the
// service keeps for itself.
func (c *Catalog) AzureReservedConnections(engine string) int {
	if engine == "mysql" {
		return loadAzure().mysql.Reserved
	}
	return loadAzure().postgres.Reserved
}

func (c *Catalog) AzureSubnet() AzureSubnet { return loadAzure().network.Subnet }

func (c *Catalog) AzureNATGateway() AzureNATGateway { return loadAzure().network.NAT }

func (c *Catalog) AzureLBSNAT() AzureLBSNAT { return loadAzure().network.LBSNAT }

func (c *Catalog) AzureAKS() AzureAKS { return loadAzure().aks }

// AzureVPNGateway returns a gateway SKU's tunnel count and aggregate
// throughput.
//
// Four SKUs (VpnGw2, VpnGw3, VpnGw2AZ, VpnGw3AZ) exist in both Generation1 and
// Generation2 with different throughput, and ThroughputMbps carries the
// Generation2 figure, which is the higher of the two. That is NOT conservative,
// whatever this comment used to claim: on a Generation1 gateway it overstates
// the headroom by up to 2x. Callers must say that the number assumes
// Generation2.
//
// Prefer AzureVPNGatewayFor, which resolves the generation. This raw lookup
// stays for callers that only want the tunnel count, which does not vary by
// generation.
func (c *Catalog) AzureVPNGateway(sku string) (AzureVPNSKU, bool) {
	t := loadAzure().network.VPN
	name, ok := azCanonical(strings.TrimSpace(sku), t.SKUs, loadAzure().vpnByFold)
	if !ok {
		return AzureVPNSKU{}, false
	}
	entry := t.SKUs[name]
	entry.Name = name
	entry.Source, entry.VerifiedAt = t.Source, t.VerifiedAt
	entry.Confidence, entry.Notes, entry.SKUNotes = t.Confidence, t.Notes, t.SKUNotes
	return entry, true
}

// AzureVPNGatewayFor resolves a SKU against the generation the plan declares.
//
// generation is the value of azurerm_virtual_network_gateway's "generation"
// argument, which extract already allowlists, so the plan does carry the answer.
// Pass "" when it is absent.
//
// When the SKU exists in one generation only, the encoded figure is the figure.
// When it exists in both and the plan says which, that generation's figures are
// used. When it exists in both and the plan is silent, the Generation1 figures
// are used, which is the lower pair. That direction is deliberate: overstating
// a throughput ceiling promises headroom that is not there, and this rule quotes
// throughput rather than thresholding on it, so the conservative side costs
// nothing but a smaller number in the sentence.
func (c *Catalog) AzureVPNGatewayFor(sku, generation string) (AzureVPNSKU, bool) {
	entry, ok := c.AzureVPNGateway(sku)
	if !ok {
		return entry, false
	}

	gen1, gen2 := entry.ThroughputMbpsGen1, entry.ThroughputMbpsGen2
	if gen1 <= 0 || gen2 <= 0 {
		// Single-generation SKU. Report which one it is, so the finding can
		// say the number is not generation-dependent rather than stay quiet.
		switch {
		case gen1 > 0:
			entry.ResolvedGeneration = "Generation1"
		case gen2 > 0:
			entry.ResolvedGeneration = "Generation2"
		}
		entry.GenerationDeclared = true
		return entry, true
	}

	entry.DualGeneration = true
	switch strings.ToLower(strings.TrimSpace(generation)) {
	case "generation1":
		entry.ResolvedGeneration, entry.GenerationDeclared = "Generation1", true
	case "generation2":
		entry.ResolvedGeneration, entry.GenerationDeclared = "Generation2", true
	default:
		// Absent, or "None", which azurerm accepts and which decides nothing.
		entry.ResolvedGeneration, entry.GenerationDeclared = "Generation1", false
	}

	if entry.ResolvedGeneration == "Generation2" {
		entry.ThroughputMbps, entry.OtherGenThroughput = gen2, gen1
		if entry.VMsSupportedGen2 > 0 {
			entry.VMsSupported = entry.VMsSupportedGen2
		}
	} else {
		entry.ThroughputMbps, entry.OtherGenThroughput = gen1, gen2
		if entry.VMsSupportedGen1 > 0 {
			entry.VMsSupported = entry.VMsSupportedGen1
		}
	}
	return entry, true
}

// AzureDiskTierFor maps a provisioned size onto the tier Azure will actually
// bill and serve. Azure rounds up to the next offered size, so 200 GiB of
// Standard SSD is an E15 with E15 performance, not a proportional slice.
func (c *Catalog) AzureDiskTierFor(accountType string, sizeGiB int) (AzureDiskTier, bool) {
	t := loadAzure().disks
	var ladder []AzureDiskTier
	var kind string

	switch strings.ToLower(strings.TrimSpace(accountType)) {
	case "premium_lrs", "premium_zrs":
		ladder, kind = t.PremiumSSD, "Premium SSD"
	case "standardssd_lrs", "standardssd_zrs":
		ladder, kind = t.StandardSSD, "Standard SSD"
	default:
		// Premium SSD v2, Ultra Disk and Standard HDD are deliberately absent:
		// the first two carry their own IOPS on the resource, and the third has
		// no performance claim worth checking.
		return AzureDiskTier{}, false
	}
	if sizeGiB <= 0 {
		return AzureDiskTier{}, false
	}

	for _, tier := range ladder {
		if sizeGiB <= tier.SizeGiB {
			tier.Kind = kind
			tier.Source, tier.VerifiedAt, tier.Confidence = t.Source, t.VerifiedAt, t.Confidence
			tier.Notes = t.Notes
			if kind == "Standard SSD" {
				tier.Notes = t.StandardSSDNotes
			}
			return tier, true
		}
	}
	return AzureDiskTier{}, false
}

func (c *Catalog) AzureDiskBurstNotes() string { return loadAzure().disks.BurstNotes }

// AzureVMSizeOf returns what a VM size sustains and what it can drive. The
// table is deliberately partial, so an unknown size means the rule stays quiet.
func (c *Catalog) AzureVMSizeOf(size string) (AzureVMSize, bool) {
	t := loadAzure().vms
	name, ok := azCanonical(strings.TrimSpace(size), t.Sizes, loadAzure().vmByFold)
	if !ok {
		return AzureVMSize{}, false
	}
	entry := t.Sizes[name]
	entry.Name = name
	if series, known := t.Series[entry.Series]; known {
		entry.Source, entry.VerifiedAt = series.Source, series.VerifiedAt
		entry.Confidence, entry.SeriesNote = series.Confidence, series.Notes
	}
	return entry, true
}

func (c *Catalog) AzureBurstNotes() string { return loadAzure().vms.BurstNotes }

func (c *Catalog) AzureStorageNotes() string { return loadAzure().vms.StorageNotes }

func (c *Catalog) AzureVMCoverage() string { return loadAzure().vms.Coverage }
