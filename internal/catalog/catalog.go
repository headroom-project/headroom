// Package catalog holds the ceiling tables: given a resource in a given
// configuration, what is its real limit.
//
// This is the defensible asset of the product. Parsing a plan file is a
// weekend; knowing that a db.t3.medium running postgres tops out around 450
// connections is years of operating the thing. The tables are plain JSON on
// purpose, so the knowledge survives any rewrite of the code around it.
package catalog

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"
)

//go:embed data/aws-rds.json
var rdsJSON []byte

//go:embed data/aws-ebs.json
var ebsJSON []byte

// GP2 and GP3 are the volume performance envelopes. gp2 earns burst credits and
// falls back to a baseline derived from size; gp3 has no burst bucket at all,
// and its ratios are hard limits AWS rejects at create time.
type GP2 struct {
	BaselineIOPSPerGiB   int    `json:"baseline_iops_per_gib"`
	MinBaselineIOPS      int    `json:"min_baseline_iops"`
	BurstIOPS            int    `json:"burst_iops"`
	BurstIrrelevantAtGiB int    `json:"burst_irrelevant_at_gib"`
	MaxIOPS              int    `json:"max_iops"`
	Source               string `json:"source"`
	Confidence           string `json:"confidence"`
	Notes                string `json:"notes"`
}

type GP3 struct {
	IncludedIOPS          int     `json:"included_iops"`
	IncludedThroughputMiB int     `json:"included_throughput_mibps"`
	MaxIOPS               int     `json:"max_iops"`
	MaxThroughputMiB      int     `json:"max_throughput_mibps"`
	MaxIOPSPerGiB         int     `json:"max_iops_per_gib"`
	MaxMiBPerIOPS         float64 `json:"max_mibps_per_iops"`
	Source                string  `json:"source"`
	Confidence            string  `json:"confidence"`
	Notes                 string  `json:"notes"`
}

type ebsCatalog struct {
	GP2 GP2 `json:"gp2"`
	GP3 GP3 `json:"gp3"`
}

//go:embed data/aws-burstable.json
var burstableJSON []byte

//go:embed data/aws-vpn.json
var vpnJSON []byte

type Burstable struct {
	VCPUs             int `json:"vcpus"`
	BaselinePctPerCPU int `json:"baseline_pct_per_vcpu"`
	CreditsPerHour    int `json:"credits_per_hour"`
}

type burstableCatalog struct {
	Source            string               `json:"source"`
	Confidence        string               `json:"confidence"`
	DefaultCreditMode map[string]string    `json:"default_credit_mode"`
	Consequence       map[string]string    `json:"credit_mode_consequence"`
	Instances         map[string]Burstable `json:"instances"`
}

type SiteToSite struct {
	MaxGbpsPerTunnel     float64 `json:"max_gbps_per_tunnel"`
	TunnelsPerConnection int     `json:"tunnels_per_connection"`
	Source               string  `json:"source"`
	Confidence           string  `json:"confidence"`
	Notes                string  `json:"notes"`
}

type ECMP struct {
	VirtualPrivateGateway bool   `json:"virtual_private_gateway"`
	TransitGateway        bool   `json:"transit_gateway"`
	Source                string `json:"source"`
	Confidence            string `json:"confidence"`
	Notes                 string `json:"notes"`
}

type vpnCatalog struct {
	SiteToSite SiteToSite `json:"site_to_site"`
	ECMP       ECMP       `json:"ecmp"`
}

//go:embed data/aws-lambda.json
var lambdaJSON []byte

type SQSEventSource struct {
	DefaultBatchSize              int    `json:"default_batch_size"`
	MinReservedConcurrency        int    `json:"min_reserved_concurrency"`
	RecommendedVisibilityMultiple int    `json:"recommended_visibility_multiple"`
	Source                        string `json:"source"`
	Confidence                    string `json:"confidence"`
	VisibilityNotes               string `json:"visibility_notes"`
	ConcurrencyNotes              string `json:"concurrency_notes"`
}

type LambdaAccount struct {
	DefaultConcurrentExecutions int    `json:"default_concurrent_executions"`
	Source                      string `json:"source"`
	Notes                       string `json:"notes"`
}

type lambdaCatalog struct {
	SQS     SQSEventSource `json:"sqs_event_source"`
	Account LambdaAccount  `json:"account"`
}

type ConnectionFormula struct {
	Formula    string `json:"formula"`
	Divisor    int64  `json:"divisor"`
	Cap        int    `json:"cap"`
	Source     string `json:"source"`
	VerifiedAt string `json:"verified_at"`
	Confidence string `json:"confidence"`
	Notes      string `json:"notes"`
}

type rdsCatalog struct {
	UpdatedAt          string                       `json:"updated_at"`
	MaxConnections     map[string]ConnectionFormula `json:"max_connections"`
	UnsupportedEngines map[string]string            `json:"unsupported_engines"`
	InstanceClasses    map[string]json.RawMessage   `json:"instance_classes"`
}

type Catalog struct {
	rds        rdsCatalog
	ebs        ebsCatalog
	burstable  burstableCatalog
	vpn        vpnCatalog
	lambda     lambdaCatalog
	classGiB   map[string]int
	classSrc   string
	classCheck string
}

func Load() (*Catalog, error) {
	var c Catalog
	if err := json.Unmarshal(rdsJSON, &c.rds); err != nil {
		return nil, fmt.Errorf("catalog aws-rds.json is malformed: %w", err)
	}
	if err := json.Unmarshal(ebsJSON, &c.ebs); err != nil {
		return nil, fmt.Errorf("catalog aws-ebs.json is malformed: %w", err)
	}
	if err := json.Unmarshal(burstableJSON, &c.burstable); err != nil {
		return nil, fmt.Errorf("catalog aws-burstable.json is malformed: %w", err)
	}
	if err := json.Unmarshal(vpnJSON, &c.vpn); err != nil {
		return nil, fmt.Errorf("catalog aws-vpn.json is malformed: %w", err)
	}
	if err := json.Unmarshal(lambdaJSON, &c.lambda); err != nil {
		return nil, fmt.Errorf("catalog aws-lambda.json is malformed: %w", err)
	}
	c.classGiB = map[string]int{}
	for k, raw := range c.rds.InstanceClasses {
		if strings.HasPrefix(k, "_") {
			var s string
			if json.Unmarshal(raw, &s) == nil {
				switch k {
				case "_source":
					c.classSrc = s
				case "_verified_at":
					c.classCheck = s
				}
			}
			continue
		}
		var gib int
		if json.Unmarshal(raw, &gib) == nil {
			c.classGiB[k] = gib
		}
	}
	return &c, nil
}

// AddInstanceClass teaches the catalog a class it shipped without knowing, so
// an operator can unblock a rule that would otherwise stay silent rather than
// wait for a release.
func (c *Catalog) AddInstanceClass(class string, memoryGiB int) {
	if class == "" || memoryGiB <= 0 {
		return
	}
	c.classGiB[class] = memoryGiB
}

// ClassMemoryBytes is the DBInstanceClassMemory value the RDS parameter group
// formulas are written against.
func (c *Catalog) ClassMemoryBytes(class string) (int64, bool) {
	gib, ok := c.classGiB[class]
	if !ok {
		return 0, false
	}
	return int64(gib) * 1024 * 1024 * 1024, true
}

// MaxConnections returns the default connection ceiling for an engine on an
// instance class. It refuses to guess: an unknown engine or an unknown class
// yields ok=false, and the caller must stay silent rather than report a number
// it cannot ground.
func (c *Catalog) MaxConnections(engine, class string) (limit int, f ConnectionFormula, ok bool) {
	engine = strings.ToLower(strings.TrimSpace(engine))
	if reason, unsupported := c.rds.UnsupportedEngines[engine]; unsupported {
		f.Notes = reason
		return 0, f, false
	}
	f, known := c.rds.MaxConnections[engine]
	if !known || f.Divisor == 0 {
		return 0, f, false
	}
	mem, known := c.ClassMemoryBytes(class)
	if !known {
		return 0, f, false
	}
	limit = int(mem / f.Divisor)
	if f.Cap > 0 && limit > f.Cap {
		limit = f.Cap
	}
	return limit, f, true
}

// UnsupportedReason explains why an engine has no encoded ceiling, so the CLI
// can tell the user what it skipped instead of silently reporting nothing.
func (c *Catalog) UnsupportedReason(engine string) (string, bool) {
	reason, ok := c.rds.UnsupportedEngines[strings.ToLower(strings.TrimSpace(engine))]
	return reason, ok
}

func (c *Catalog) UpdatedAt() string { return c.rds.UpdatedAt }

// Burstable returns the credit profile of a T family instance type, and the
// credit mode it launches with when the plan does not say. That default is the
// whole point: T3 keeps performance and bills for it, T2 throttles instead, and
// almost nobody sets credit_specification explicitly.
func (c *Catalog) Burstable(instanceType string) (b Burstable, defaultMode string, ok bool) {
	b, ok = c.burstable.Instances[strings.ToLower(strings.TrimSpace(instanceType))]
	if !ok {
		return b, "", false
	}
	family := instanceType
	if i := strings.Index(instanceType, "."); i > 0 {
		family = instanceType[:i]
	}
	return b, c.burstable.DefaultCreditMode[family], true
}

func (c *Catalog) CreditModeConsequence(mode string) string {
	return c.burstable.Consequence[mode]
}

func (c *Catalog) BurstableSource() string { return c.burstable.Source }

func (c *Catalog) SQSEventSource() SQSEventSource { return c.lambda.SQS }

func (c *Catalog) LambdaAccount() LambdaAccount { return c.lambda.Account }

func (c *Catalog) SiteToSiteVPN() SiteToSite { return c.vpn.SiteToSite }

func (c *Catalog) ECMP() ECMP { return c.vpn.ECMP }

func (c *Catalog) GP2() GP2 { return c.ebs.GP2 }

func (c *Catalog) GP3() GP3 { return c.ebs.GP3 }

// GP2Baseline is the sustained IOPS a gp2 volume delivers once its burst credits
// are gone. The gap between this and the 3000 IOPS burst is the cliff.
func (c *Catalog) GP2Baseline(sizeGiB int) int {
	baseline := c.ebs.GP2.BaselineIOPSPerGiB * sizeGiB
	if baseline < c.ebs.GP2.MinBaselineIOPS {
		baseline = c.ebs.GP2.MinBaselineIOPS
	}
	if baseline > c.ebs.GP2.MaxIOPS {
		baseline = c.ebs.GP2.MaxIOPS
	}
	return baseline
}
