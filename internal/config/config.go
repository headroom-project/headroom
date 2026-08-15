// Package config is the organization's say over the analysis.
//
// Three layers, deliberately separated, because they carry very different
// risks:
//
//  1. Tuning of the built-in capacity rules. Enable, disable, reseverity,
//     change a threshold. Cheap, safe, and what almost everyone actually wants.
//
//  2. Facts about this account that no plan can state: the parameter group that
//     really does allow 2000 connections, the instance class the catalog has
//     never heard of. Filling these in moves a finding from medium confidence
//     to high without granting anybody AWS access.
//
//  3. Custom assertion rules. These are policy, not capacity: "gp2 is banned",
//     "every database must declare storage autoscaling". They are declarative
//     on purpose. The capacity rules stay in code because their value is the
//     curated ceiling behind them, and a ceiling nobody verified is worse than
//     no ceiling at all.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const CurrentVersion = 1

type Config struct {
	Version  int              `yaml:"version"`
	Defaults Defaults         `yaml:"defaults"`
	Rules    map[string]Rule  `yaml:"rules"`
	Catalog  CatalogOverrides `yaml:"catalog"`
	Except   []Exception      `yaml:"exceptions"`
	Custom   []CustomRule     `yaml:"custom"`

	path string
}

type Defaults struct {
	PoolSize       *int     `yaml:"pool_size"`
	WarnAt         *float64 `yaml:"warn_at"`
	ScaleRatioWarn *float64 `yaml:"scale_ratio_warn"`
}

type Rule struct {
	Enabled  *bool  `yaml:"enabled"`
	Severity string `yaml:"severity"`
}

type CatalogOverrides struct {
	// RDSMaxConnections keys on the terraform address, because the number
	// depends on a parameter group this plan cannot read.
	RDSMaxConnections map[string]int `yaml:"rds_max_connections"`
	// InstanceClassMemoryGiB teaches the catalog a class it does not know yet,
	// so a rule that would have stayed silent can speak.
	InstanceClassMemoryGiB map[string]int `yaml:"instance_class_memory_gib"`
}

// Exception silences a finding. It requires a reason and an expiry, because a
// suppression that never rots is how a policy tool stops being read.
type Exception struct {
	Rule     string `yaml:"rule"`
	Resource string `yaml:"resource"`
	Reason   string `yaml:"reason"`
	Expires  string `yaml:"expires"`
}

type CustomRule struct {
	ID       string   `yaml:"id"`
	Title    string   `yaml:"title"`
	Severity string   `yaml:"severity"`
	Match    Match    `yaml:"match"`
	Summary  string   `yaml:"summary"`
	Detail   []string `yaml:"detail"`
	Source   string   `yaml:"source"`
}

type Match struct {
	Type  string      `yaml:"type"`
	Where []Condition `yaml:"where"`
}

// Condition is deliberately a fixed set of operators rather than an expression
// language. A config file that can compute anything is a program, and a program
// in a config file is a debugging problem shipped to the customer.
type Condition struct {
	Attr string `yaml:"attr"`

	Equals     *string  `yaml:"equals"`
	NotEquals  *string  `yaml:"not_equals"`
	In         []string `yaml:"in"`
	NotIn      []string `yaml:"not_in"`
	Exists     *bool    `yaml:"exists"`
	Matches    string   `yaml:"matches"`
	NotMatches string   `yaml:"not_matches"`

	GT  *float64 `yaml:"gt"`
	GTE *float64 `yaml:"gte"`
	LT  *float64 `yaml:"lt"`
	LTE *float64 `yaml:"lte"`

	// CIDR prefix comparisons, so "/28 is too small" is expressible without
	// asking anyone to write netmask arithmetic in YAML.
	PrefixGT  *int `yaml:"cidr_prefix_gt"`
	PrefixGTE *int `yaml:"cidr_prefix_gte"`
	PrefixLT  *int `yaml:"cidr_prefix_lt"`
	PrefixLTE *int `yaml:"cidr_prefix_lte"`
}

var validSeverity = map[string]bool{"critical": true, "warning": true, "info": true}

// Discover looks for a config next to the plan, then in the working directory.
// An org keeps one file at the root of each repository and never passes a flag.
func Discover(planPath string) string {
	var dirs []string
	if planPath != "" {
		dirs = append(dirs, filepath.Dir(planPath))
	}
	if cwd, err := os.Getwd(); err == nil {
		dirs = append(dirs, cwd)
	}
	for _, dir := range dirs {
		for _, name := range []string{"headroom.yaml", "headroom.yml", ".headroom.yaml"} {
			candidate := filepath.Join(dir, name)
			if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
				return candidate
			}
		}
	}
	return ""
}

func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c Config
	dec := yaml.NewDecoder(strings.NewReader(string(raw)))
	dec.KnownFields(true) // a typo in a config is a silent policy change
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	c.path = path
	if err := c.validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &c, nil
}

func (c *Config) Path() string { return c.path }

func (c *Config) validate() error {
	if c.Version == 0 {
		return fmt.Errorf("missing version; this file must start with `version: %d`", CurrentVersion)
	}
	if c.Version != CurrentVersion {
		return fmt.Errorf("version %d is not supported by this build (expected %d)", c.Version, CurrentVersion)
	}

	for id, r := range c.Rules {
		if r.Severity != "" && !validSeverity[r.Severity] {
			return fmt.Errorf("rules.%s.severity %q must be critical, warning or info", id, r.Severity)
		}
	}

	for i, e := range c.Except {
		if e.Rule == "" || e.Resource == "" {
			return fmt.Errorf("exceptions[%d] needs both rule and resource", i)
		}
		if strings.TrimSpace(e.Reason) == "" {
			return fmt.Errorf("exceptions[%d] needs a reason: a silent suppression is indistinguishable from a bug", i)
		}
		if e.Expires == "" {
			return fmt.Errorf("exceptions[%d] needs an expires date (YYYY-MM-DD): a suppression with no end date outlives the reason for it", i)
		}
		if _, err := time.Parse("2006-01-02", e.Expires); err != nil {
			return fmt.Errorf("exceptions[%d].expires %q is not a YYYY-MM-DD date", i, e.Expires)
		}
	}

	seen := map[string]bool{}
	for i, r := range c.Custom {
		switch {
		case r.ID == "":
			return fmt.Errorf("custom[%d] needs an id", i)
		case strings.HasPrefix(strings.ToUpper(r.ID), "R") && len(r.ID) <= 3:
			return fmt.Errorf("custom[%d].id %q collides with the built-in rule namespace; use a prefix of your own", i, r.ID)
		case seen[r.ID]:
			return fmt.Errorf("custom[%d].id %q is used twice", i, r.ID)
		case r.Title == "":
			return fmt.Errorf("custom rule %s needs a title", r.ID)
		case r.Match.Type == "":
			return fmt.Errorf("custom rule %s needs match.type", r.ID)
		case r.Severity != "" && !validSeverity[r.Severity]:
			return fmt.Errorf("custom rule %s has severity %q; want critical, warning or info", r.ID, r.Severity)
		}
		seen[r.ID] = true

		for j, cond := range r.Match.Where {
			if cond.Attr == "" {
				return fmt.Errorf("custom rule %s where[%d] needs an attr", r.ID, j)
			}
			if cond.operatorCount() == 0 {
				return fmt.Errorf("custom rule %s where[%d] on %q has no operator, so it matches everything", r.ID, j, cond.Attr)
			}
		}
	}
	return nil
}

func (c Condition) operatorCount() int {
	n := 0
	for _, set := range []bool{
		c.Equals != nil, c.NotEquals != nil, len(c.In) > 0, len(c.NotIn) > 0,
		c.Exists != nil, c.Matches != "", c.NotMatches != "",
		c.GT != nil, c.GTE != nil, c.LT != nil, c.LTE != nil,
		c.PrefixGT != nil, c.PrefixGTE != nil, c.PrefixLT != nil, c.PrefixLTE != nil,
	} {
		if set {
			n++
		}
	}
	return n
}

// RuleEnabled reports whether a built-in rule should run. Absent config means
// enabled: turning a capacity rule off has to be a decision someone wrote down.
func (c *Config) RuleEnabled(id string) bool {
	if c == nil {
		return true
	}
	if r, ok := c.Rules[id]; ok && r.Enabled != nil {
		return *r.Enabled
	}
	return true
}

// SeverityFor lets an organization say that a warning is a blocker here, or the
// reverse, without forking the rule.
func (c *Config) SeverityFor(id, fallback string) string {
	if c == nil {
		return fallback
	}
	if r, ok := c.Rules[id]; ok && r.Severity != "" {
		return r.Severity
	}
	return fallback
}

// RDSMaxConnections is the ceiling an operator declares for a database whose
// parameter group the plan cannot read. It is the cheapest way to turn a
// medium-confidence finding into a high-confidence one: a human states the fact
// instead of the tool being granted an AWS role to go and look.
func (c *Config) RDSMaxConnections(address string) (int, bool) {
	if c == nil {
		return 0, false
	}
	n, ok := c.Catalog.RDSMaxConnections[address]
	return n, ok && n > 0
}

// InstanceClassMemory teaches the catalog classes released after this build.
func (c *Config) InstanceClassMemory() map[string]int {
	if c == nil {
		return nil
	}
	return c.Catalog.InstanceClassMemoryGiB
}

// Suppressed reports whether a finding is excepted, and separately whether the
// exception has expired. An expired exception is not silently ignored: it
// becomes a finding of its own, so the debt surfaces instead of rotting.
func (c *Config) Suppressed(rule string, resources []string, now time.Time) (hit *Exception, expired bool) {
	if c == nil {
		return nil, false
	}
	for i := range c.Except {
		e := &c.Except[i]
		if e.Rule != rule {
			continue
		}
		if !matchesResource(e.Resource, resources) {
			continue
		}
		until, err := time.Parse("2006-01-02", e.Expires)
		if err != nil {
			continue
		}
		if now.After(until.AddDate(0, 0, 1)) {
			return e, true
		}
		return e, false
	}
	return nil, false
}

// matchesResource supports a trailing wildcard so a team can except a module
// without listing every resource inside it.
func matchesResource(pattern string, resources []string) bool {
	for _, r := range resources {
		if pattern == r {
			return true
		}
		if strings.HasSuffix(pattern, "*") && strings.HasPrefix(r, strings.TrimSuffix(pattern, "*")) {
			return true
		}
	}
	return false
}
