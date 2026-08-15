// Package rules holds the capacity checks.
//
// Every rule in the v1 set is the same statement wearing different clothes:
// scale asymmetry, where the consumer scales and the provider does not. That is
// exactly what generated terraform gets wrong, because a model has no idea what
// the traffic looks like and falls back to the default, and the default is the
// bottleneck.
package rules

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/headroom-project/headroom/internal/catalog"
	"github.com/headroom-project/headroom/internal/config"
	"github.com/headroom-project/headroom/internal/graph"
	"github.com/headroom-project/headroom/internal/plan"
)

const (
	SeverityCritical = "critical"
	SeverityWarning  = "warning"
	SeverityInfo     = "info"
)

type Finding struct {
	Rule       string   `json:"rule"`
	Severity   string   `json:"severity"`
	Title      string   `json:"title"`
	Summary    string   `json:"summary"`
	Detail     []string `json:"detail,omitempty"`
	Confidence string   `json:"confidence"`
	Resources  []string `json:"resources,omitempty"`
	Source     string   `json:"source,omitempty"`

	// Metrics carries the bare numbers behind the finding. Only these travel to
	// the backend: the rendered sentence is rebuilt server-side, so resource
	// names never leave the machine even inside prose.
	Metrics map[string]int `json:"metrics,omitempty"`

	// Instances counts identical findings collapsed into one. A resource
	// created with for_each produces the same defect once per key, and
	// printing it once per key buries the other findings.
	Instances int `json:"instances,omitempty"`
}

type Options struct {
	// DefaultPoolSize is the assumed connections-per-task when the plan gives
	// no evidence. Terraform never states it, so it is either read from the
	// task definition environment or assumed, and assuming lowers confidence.
	DefaultPoolSize int
	// WarnAt is the utilization ratio above which a headroom warning fires.
	WarnAt float64
	// ScaleRatioWarn is how far a front tier may grow relative to a fixed
	// backend before the asymmetry is worth stating.
	ScaleRatioWarn float64

	// Config is the organization's say: which rules run, at what severity,
	// which findings are excepted, and any rules of its own. Nil means every
	// built-in rule runs at its default severity.
	Config *config.Config
	// Now exists so exception expiry is testable rather than dependent on the
	// day the suite happens to run.
	Now time.Time
}

func DefaultOptions() Options {
	return Options{DefaultPoolSize: 10, WarnAt: 0.8, ScaleRatioWarn: 4}
}

// ProviderRules is a set of rules for one cloud. Each provider registers itself
// from its own file so that adding a cloud never means editing this one, and two
// people can work on two clouds without meeting in a merge conflict.
type ProviderRules func(*plan.File, *graph.Graph, *catalog.Catalog, Options) []Finding

var registered []ProviderRules

// Register is called from init() in each provider's rule file.
func Register(fn ProviderRules) { registered = append(registered, fn) }

func Run(f *plan.File, g *graph.Graph, c *catalog.Catalog, opt Options) []Finding {
	var out []Finding
	for _, provider := range registered {
		out = append(out, provider(f, g, c, opt)...)
	}
	out = append(out, ruleDBConnections(f, g, c, opt)...)
	out = append(out, ruleSubnetIPs(f, g, opt)...)
	out = append(out, ruleLambdaSQS(f, g, c)...)
	out = append(out, ruleScalingAsymmetry(f, g, opt)...)
	out = append(out, ruleNATEgress(f, g, opt)...)
	out = append(out, ruleEBS(f, c, opt)...)
	out = append(out, ruleBurstableCPU(f, c, opt)...)
	out = append(out, ruleVPNAggregate(f, g, c)...)
	out = append(out, runCustom(f, opt)...)
	return sortFindings(dedupe(applyConfig(out, opt)))
}

// workload is a scalable consumer sitting in front of a fixed-capacity provider.
type workload struct {
	addr  string
	scale int
	unit  string
	how   string
}

// scaleOf resolves the maximum number of instances a workload can reach. The
// declared desired_count is not the ceiling: an autoscaling target attached to
// the service is, and that is precisely the gap generated terraform leaves open.
func scaleOf(g *graph.Graph, addr string) (workload, bool) {
	w := workload{addr: addr}
	switch g.Type(addr) {
	case "aws_ecs_service":
		w.unit = "tasks"
		for _, target := range g.ReferrersOfType(addr, "aws_appautoscaling_target") {
			if max, ok := plan.Num(g.Values(target), "max_capacity"); ok {
				w.scale, w.how = max, "max_capacity of "+target
				return w, true
			}
		}
		if n, ok := plan.Num(g.Values(addr), "desired_count"); ok {
			w.scale, w.how = n, "desired_count, no autoscaling target found"
			return w, true
		}
	case "aws_autoscaling_group":
		w.unit = "instances"
		if n, ok := plan.Num(g.Values(addr), "max_size"); ok {
			w.scale, w.how = n, "max_size"
			return w, true
		}
	}
	return w, false
}

// poolEnvNames are the environment variables that actually carry the per-task
// connection pool size across the common runtimes. Reading them out of the task
// definition is the difference between a grounded number and a guess.
var poolEnvNames = []string{
	"DB_POOL_SIZE", "DB_POOL_MAX", "DB_MAX_CONNECTIONS", "DB_POOL",
	"DATABASE_POOL_SIZE", "DATABASE_POOL_MAX",
	"POOL_MAX", "POOL_SIZE",
	"PG_POOL_MAX", "PGPOOL_MAX",
	"SEQUELIZE_POOL_MAX", "KNEX_POOL_MAX", "TYPEORM_MAX_CONNECTIONS",
	"HIKARI_MAXIMUM_POOL_SIZE", "SPRING_DATASOURCE_HIKARI_MAXIMUMPOOLSIZE",
	"SQLALCHEMY_POOL_SIZE",
}

type containerDef struct {
	Name        string `json:"name"`
	Environment []struct {
		Name  string `json:"name"`
		Value string `json:"value"`
	} `json:"environment"`
}

// poolSizeOf looks for a declared pool size in the task definition of a service.
// Returns the value and how it was obtained; "assumed" means the caller must
// downgrade the confidence of whatever it reports.
func poolSizeOf(g *graph.Graph, svcAddr string, fallback int) (int, string) {
	for _, td := range g.RefsOfType(svcAddr, "aws_ecs_task_definition") {
		raw := plan.Str(g.Values(td), "container_definitions")
		if raw == "" {
			continue
		}
		var containers []containerDef
		if json.Unmarshal([]byte(raw), &containers) != nil {
			continue
		}
		for _, ct := range containers {
			for _, env := range ct.Environment {
				name := strings.ToUpper(strings.TrimSpace(env.Name))
				for _, candidate := range poolEnvNames {
					if name != candidate {
						continue
					}
					if n := atoi(env.Value); n > 0 {
						return n, env.Name + "=" + env.Value + " in " + td
					}
				}
			}
		}
	}
	return fallback, "assumed"
}

func atoi(s string) int {
	n := 0
	for _, r := range strings.TrimSpace(s) {
		if r < '0' || r > '9' {
			return 0
		}
		n = n*10 + int(r-'0')
	}
	return n
}

func ceilDiv(a, b int) int {
	if b <= 0 {
		return a
	}
	if a%b == 0 {
		return a / b
	}
	return a/b + 1
}

// dedupe collapses findings that say exactly the same thing. Three volumes
// created by one for_each share one defect and deserve one line, not three.
func dedupe(in []Finding) []Finding {
	type key struct{ rule, title, summary string }
	at := map[key]int{}
	var out []Finding

	for _, f := range in {
		k := key{f.Rule, f.Title, f.Summary}
		if i, seen := at[k]; seen {
			out[i].Instances++
			for _, r := range f.Resources {
				if !contains(out[i].Resources, r) {
					out[i].Resources = append(out[i].Resources, r)
				}
			}
			continue
		}
		f.Instances = 1
		at[k] = len(out)
		out = append(out, f)
	}
	return out
}

var severityRank = map[string]int{SeverityCritical: 0, SeverityWarning: 1, SeverityInfo: 2}

func sortFindings(f []Finding) []Finding {
	for i := 1; i < len(f); i++ {
		for j := i; j > 0; j-- {
			a, b := f[j-1], f[j]
			if severityRank[a.Severity] < severityRank[b.Severity] ||
				(severityRank[a.Severity] == severityRank[b.Severity] && a.Rule <= b.Rule) {
				break
			}
			f[j-1], f[j] = f[j], f[j-1]
		}
	}
	return f
}
