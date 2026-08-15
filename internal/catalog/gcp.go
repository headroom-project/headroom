package catalog

import (
	_ "embed"
	"encoding/json"
	"strconv"
	"strings"
	"sync"
)

// The Google Cloud tables live in their own file with their own embeds so that
// adding a cloud never means editing catalog.go, and so two people can extend
// the catalog at once without meeting in a merge conflict. Load() in catalog.go
// knows nothing about them; parsing happens on first use instead.

//go:embed data/gcp-cloudsql.json
var gcpSQLJSON []byte

//go:embed data/gcp-gke.json
var gcpGKEJSON []byte

//go:embed data/gcp-nat.json
var gcpNATJSON []byte

//go:embed data/gcp-disk.json
var gcpDiskJSON []byte

//go:embed data/gcp-serverless.json
var gcpServerlessJSON []byte

// GCPSQLBand is one row of the Cloud SQL memory-to-connections table. Google
// publishes bands rather than a formula, so the catalog carries bands.
type GCPSQLBand struct {
	MinMB int `json:"min_mb"`
	MaxMB int `json:"max_mb"` // exclusive; 0 means unbounded
	Value int `json:"value"`
}

type GCPSQLEngine struct {
	Source     string         `json:"source"`
	VerifiedAt string         `json:"verified_at"`
	Confidence string         `json:"confidence"`
	Formula    string         `json:"formula"`
	Notes      string         `json:"notes"`
	SharedCore map[string]int `json:"shared_core"`
	Bands      []GCPSQLBand   `json:"bands"`
	GapNotes   string         `json:"gap_notes"`
}

type gcpSQLTiers struct {
	Source            string            `json:"_source"`
	VerifiedAt        string            `json:"_verified_at"`
	Confidence        string            `json:"_confidence"`
	CustomPrefix      string            `json:"custom_prefix"`
	SharedCoreMB      map[string]int    `json:"shared_core_mb"`
	PredefinedPerVCPU map[string]int    `json:"predefined_mb_per_vcpu"`
	ExplicitMB        map[string]int    `json:"explicit_mb"`
	NotEncoded        map[string]string `json:"not_encoded"`
}

type gcpSQLCatalog struct {
	MaxConnections     map[string]GCPSQLEngine `json:"max_connections"`
	UnsupportedEngines map[string]string       `json:"unsupported_engines"`
	Tiers              gcpSQLTiers             `json:"tiers"`
}

// GCPPodBlock is one row of the GKE "maximum Pods per node -> CIDR per node"
// table. The block is what makes GKE pod ranges so much smaller than they look.
type GCPPodBlock struct {
	MaxPods   int `json:"max_pods"`
	Netmask   int `json:"netmask"`
	Addresses int `json:"addresses"`
}

type GCPPodRange struct {
	DefaultMaxPodsPerNode int           `json:"default_max_pods_per_node"`
	MaxPodsPerNodeCap     int           `json:"max_pods_per_node_cap"`
	Formula               string        `json:"formula"`
	Source                string        `json:"source"`
	VerifiedAt            string        `json:"verified_at"`
	Confidence            string        `json:"confidence"`
	Notes                 string        `json:"notes"`
	Blocks                []GCPPodBlock `json:"blocks"`
	ResizeNotes           string        `json:"resize_notes"`
}

type GCPNodeRange struct {
	AddressesPerNode  int    `json:"addresses_per_node"`
	ReservedAddresses int    `json:"reserved_addresses"`
	Source            string `json:"source"`
	VerifiedAt        string `json:"verified_at"`
	Confidence        string `json:"confidence"`
	Notes             string `json:"notes"`
}

type GCPServicesRange struct {
	AddressesPerService int    `json:"addresses_per_service"`
	DefaultNetmask      int    `json:"default_netmask"`
	DefaultMaxServices  int    `json:"default_max_services"`
	SmallestNetmask     int    `json:"smallest_netmask"`
	SmallestMaxServices int    `json:"smallest_max_services"`
	Source              string `json:"source"`
	VerifiedAt          string `json:"verified_at"`
	Confidence          string `json:"confidence"`
	Notes               string `json:"notes"`
}

type gcpGKECatalog struct {
	PodRange      GCPPodRange      `json:"pod_range"`
	NodeRange     GCPNodeRange     `json:"node_range"`
	ServicesRange GCPServicesRange `json:"services_range"`
}

type GCPNAT struct {
	PortsPerIP           int    `json:"ports_per_ip"`
	DefaultMinPortsPerVM int    `json:"default_min_ports_per_vm"`
	PortMultiplier       int    `json:"port_multiplier"`
	Formula              string `json:"formula"`
	Source               string `json:"source"`
	VerifiedAt           string `json:"verified_at"`
	Confidence           string `json:"confidence"`
	Notes                string `json:"notes"`
}

type GCPNATAuto struct {
	SkipReason string `json:"skip_reason"`
	Source     string `json:"source"`
	VerifiedAt string `json:"verified_at"`
	Confidence string `json:"confidence"`
}

type gcpNATCatalog struct {
	Public  GCPNAT     `json:"public_nat"`
	Private GCPNAT     `json:"private_nat"`
	Auto    GCPNATAuto `json:"auto_allocation"`
}

type GCPDisk struct {
	ReadIOPSPerGiB      float64 `json:"read_iops_per_gib"`
	WriteIOPSPerGiB     float64 `json:"write_iops_per_gib"`
	ThroughputMiBPerGiB float64 `json:"throughput_mibps_per_gib"`
	Source              string  `json:"source"`
	VerifiedAt          string  `json:"verified_at"`
	Confidence          string  `json:"confidence"`
	Notes               string  `json:"notes"`
}

type GCPDiskVCPUCap struct {
	Encoded    bool   `json:"encoded"`
	SkipReason string `json:"skip_reason"`
	Source     string `json:"source"`
	VerifiedAt string `json:"verified_at"`
	Confidence string `json:"confidence"`
}

type GCPDiskFloor struct {
	ReadIOPS int    `json:"read_iops"`
	Notes    string `json:"notes"`
}

type gcpDiskCatalog struct {
	Types   map[string]GCPDisk `json:"types"`
	VCPUCap GCPDiskVCPUCap     `json:"vcpu_cap"`
	Floor   GCPDiskFloor       `json:"reporting_floor"`
}

type GCPConnectorMachine struct {
	MinMbps int `json:"min_mbps"`
	MaxMbps int `json:"max_mbps"`
}

type GCPConnector struct {
	MinInstancesFloor   int                            `json:"min_instances_floor"`
	MaxInstancesCeiling int                            `json:"max_instances_ceiling"`
	DefaultMinInstances int                            `json:"default_min_instances"`
	DefaultMaxInstances int                            `json:"default_max_instances"`
	Source              string                         `json:"source"`
	VerifiedAt          string                         `json:"verified_at"`
	Confidence          string                         `json:"confidence"`
	Notes               string                         `json:"notes"`
	MachineTypes        map[string]GCPConnectorMachine `json:"machine_types"`
	SizingNotes         string                         `json:"sizing_notes"`
}

type GCPCloudRun struct {
	DefaultMaxInstanceCount int    `json:"default_max_instance_count"`
	Source                  string `json:"source"`
	VerifiedAt              string `json:"verified_at"`
	Confidence              string `json:"confidence"`
	Notes                   string `json:"notes"`
}

type gcpServerlessCatalog struct {
	Connector GCPConnector `json:"vpc_access_connector"`
	CloudRun  GCPCloudRun  `json:"cloud_run"`
}

type gcpCatalog struct {
	sql        gcpSQLCatalog
	gke        gcpGKECatalog
	nat        gcpNATCatalog
	disk       gcpDiskCatalog
	serverless gcpServerlessCatalog
	err        error
}

// The tables are immutable once parsed, so they are decoded once for the process
// rather than per Catalog. A malformed table is recorded rather than discarded:
// a dropped parse error turns every Google Cloud rule into a silent no-op, which
// looks exactly like a clean plan. That is the one failure mode this product
// cannot have, so the error is carried out to GCPCatalogError for a caller to
// report. The AWS and Azure loaders already propagate; this one used to not.
var (
	gcpOnce sync.Once
	gcpData gcpCatalog
)

func gcpTables() *gcpCatalog {
	gcpOnce.Do(func() {
		for _, step := range []struct {
			raw  []byte
			into any
			name string
		}{
			{gcpSQLJSON, &gcpData.sql, "gcp-cloudsql.json"},
			{gcpGKEJSON, &gcpData.gke, "gcp-gke.json"},
			{gcpNATJSON, &gcpData.nat, "gcp-nat.json"},
			{gcpDiskJSON, &gcpData.disk, "gcp-disk.json"},
			{gcpServerlessJSON, &gcpData.serverless, "gcp-serverless.json"},
		} {
			if err := json.Unmarshal(step.raw, step.into); err != nil {
				gcpData.err = &gcpCatalogError{file: step.name, err: err}
				return
			}
		}
	})
	return &gcpData
}

type gcpCatalogError struct {
	file string
	err  error
}

func (e *gcpCatalogError) Error() string {
	return "catalog " + e.file + " is malformed: " + e.err.Error()
}
func (e *gcpCatalogError) Unwrap() error { return e.err }

// GCPCatalogError reports a broken Google Cloud table, so a rule can say what it
// skipped instead of silently reporting nothing.
func (c *Catalog) GCPCatalogError() error { return gcpTables().err }

// GCPSQLEngineOf maps a Cloud SQL database_version ("POSTGRES_16") onto the
// catalog's engine key.
func GCPSQLEngineOf(databaseVersion string) string {
	v := strings.ToLower(strings.TrimSpace(databaseVersion))
	if i := strings.Index(v, "_"); i > 0 {
		v = v[:i]
	}
	return v
}

// GCPSQLTierMemoryMB resolves the memory behind a Cloud SQL tier. The custom
// family states it outright, which is the one place GCP is kinder than AWS; the
// fixed families need the table. An unknown tier yields ok=false so the caller
// stays silent rather than interpolating.
func (c *Catalog) GCPSQLTierMemoryMB(tier string) (int, bool) {
	t := strings.ToLower(strings.TrimSpace(tier))
	if t == "" {
		return 0, false
	}
	tiers := gcpTables().sql.Tiers

	if mb, ok := tiers.ExplicitMB[t]; ok {
		return mb, true
	}
	if mb, ok := tiers.SharedCoreMB[t]; ok {
		return mb, true
	}
	if prefix := tiers.CustomPrefix; prefix != "" && strings.HasPrefix(t, prefix) {
		// db-custom-{vCPUs}-{memoryMB}
		rest := t[len(prefix):]
		i := strings.LastIndex(rest, "-")
		if i <= 0 {
			return 0, false
		}
		mb, err := strconv.Atoi(rest[i+1:])
		if err != nil || mb <= 0 {
			return 0, false
		}
		return mb, true
	}
	for prefix, perVCPU := range tiers.PredefinedPerVCPU {
		if !strings.HasPrefix(t, prefix) {
			continue
		}
		vcpus, err := strconv.Atoi(t[len(prefix):])
		if err != nil || vcpus <= 0 {
			return 0, false
		}
		return vcpus * perVCPU, true
	}
	return 0, false
}

// GCPSQLMaxConnections returns the default connection ceiling for a Cloud SQL
// instance. It refuses to guess: an engine with no published table, an unknown
// tier, or a memory size that falls in the documented gap all yield ok=false.
func (c *Catalog) GCPSQLMaxConnections(databaseVersion, tier string) (limit int, e GCPSQLEngine, ok bool) {
	engine := GCPSQLEngineOf(databaseVersion)
	e, known := gcpTables().sql.MaxConnections[engine]
	if !known {
		return 0, e, false
	}
	t := strings.ToLower(strings.TrimSpace(tier))
	if v, hit := e.SharedCore[t]; hit {
		return v, e, true
	}
	mb, hit := c.GCPSQLTierMemoryMB(tier)
	if !hit {
		return 0, e, false
	}
	for _, b := range e.Bands {
		if mb >= b.MinMB && (b.MaxMB == 0 || mb < b.MaxMB) {
			return b.Value, e, true
		}
	}
	return 0, e, false
}

// GCPSQLUnsupportedReason explains why an engine has no encoded ceiling, so the
// CLI can say what it skipped instead of silently reporting nothing.
func (c *Catalog) GCPSQLUnsupportedReason(databaseVersion string) (string, bool) {
	reason, ok := gcpTables().sql.UnsupportedEngines[GCPSQLEngineOf(databaseVersion)]
	return reason, ok
}

func (c *Catalog) GCPSQLTierNotEncoded() map[string]string { return gcpTables().sql.Tiers.NotEncoded }

func (c *Catalog) GCPSQLTierSource() string { return gcpTables().sql.Tiers.Source }

func (c *Catalog) GCPPodRange() GCPPodRange { return gcpTables().gke.PodRange }

func (c *Catalog) GCPNodeRange() GCPNodeRange { return gcpTables().gke.NodeRange }

func (c *Catalog) GCPServicesRange() GCPServicesRange { return gcpTables().gke.ServicesRange }

// GCPPodBlockNetmask returns the alias range netmask GKE allocates per node for
// a given max_pods_per_node. This is the number that turns a roomy-looking
// secondary range into a small node count.
func (c *Catalog) GCPPodBlockNetmask(maxPodsPerNode int) (int, bool) {
	r := gcpTables().gke.PodRange
	if maxPodsPerNode <= 0 || maxPodsPerNode > r.MaxPodsPerNodeCap {
		return 0, false
	}
	for _, b := range r.Blocks {
		if maxPodsPerNode <= b.MaxPods {
			return b.Netmask, true
		}
	}
	return 0, false
}

func (c *Catalog) GCPPublicNAT() GCPNAT { return gcpTables().nat.Public }

func (c *Catalog) GCPPrivateNAT() GCPNAT { return gcpTables().nat.Private }

func (c *Catalog) GCPNATAutoAllocation() GCPNATAuto { return gcpTables().nat.Auto }

func (c *Catalog) GCPDisk(diskType string) (GCPDisk, bool) {
	d, ok := gcpTables().disk.Types[strings.ToLower(strings.TrimSpace(diskType))]
	return d, ok
}

func (c *Catalog) GCPDiskVCPUCap() GCPDiskVCPUCap { return gcpTables().disk.VCPUCap }

// GCPDiskReportingFloor is headroom's own threshold, not a Google number. It is
// in the catalog so that the one place a judgement call enters the numbers is
// visible next to the numbers it sits beside.
func (c *Catalog) GCPDiskReportingFloor() GCPDiskFloor { return gcpTables().disk.Floor }

func (c *Catalog) GCPConnector() GCPConnector { return gcpTables().serverless.Connector }

func (c *Catalog) GCPConnectorMachine(machineType string) (GCPConnectorMachine, bool) {
	m, ok := gcpTables().serverless.Connector.MachineTypes[strings.ToLower(strings.TrimSpace(machineType))]
	return m, ok
}

func (c *Catalog) GCPCloudRun() GCPCloudRun { return gcpTables().serverless.CloudRun }
