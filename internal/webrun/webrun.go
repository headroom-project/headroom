// Package webrun runs one analysis against a plan held in memory and returns
// everything a caller outside a terminal needs from it.
//
// It exists for the WebAssembly build, which is the same engine as the CLI
// compiled for a browser so somebody can try the tool without installing it.
// The rule that shapes this package is that the browser must never have to
// look inside the plan: it hands over bytes and gets back a report, a findings
// document, the redacted payload and a set of counts. A JSON parser on the
// page would be a second reader of customer data, written in a language with
// no allowlist, and there would be nothing left to promise about it.
//
// Three properties are load bearing and each one has a test.
//
// The report is byte identical to what the CLI prints. Same rules, same
// catalog, same renderer, same escapes. A playground that reimplements the
// output is a demo of a different product.
//
// Nothing here can panic out. In a process a panic is an exit code and a stack
// trace; in a browser it tears down the runtime and the page goes dead with no
// way back except a reload. Input arrives from a paste box, which is the least
// trusted input this project has, so the whole run is wrapped.
//
// Stats carry vocabulary, never identity. A terraform type ("aws_db_instance")
// is a public word from a provider schema. A terraform address
// ("aws_db_instance.customer_billing") is the customer's. Only the first kind
// is counted here, and there is a test that reads every field of Stats and
// fails if a resource name from a fixture appears anywhere in it.
package webrun

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/headroom-project/headroom/internal/catalog"
	"github.com/headroom-project/headroom/internal/extract"
	"github.com/headroom-project/headroom/internal/graph"
	"github.com/headroom-project/headroom/internal/plan"
	"github.com/headroom-project/headroom/internal/report"
	"github.com/headroom-project/headroom/internal/rules"
)

// MaxPlanBytes is the largest plan this build accepts.
//
// The number is a browser budget, not a property of the analysis: the whole
// document is decoded into memory on the main thread of a tab that also has to
// stay responsive. A plan larger than this is exactly the case where somebody
// should be running the binary in their pipeline, and the refusal says so.
const MaxPlanBytes = 8 << 20

// Options are the knobs the page is allowed to turn. Deliberately a fraction
// of what the CLI exposes: no config file, no upload, no fail-on. A flag that
// cannot change an answer is a flag that does not need to exist here.
type Options struct {
	// Salt hashes resource addresses in the redacted payload. The page
	// generates a random one per session, which is the honest analogue of a
	// per-organization secret: it makes the payload demonstrate redaction
	// without pretending the id would be stable across runs.
	Salt string

	// DefaultPoolSize and WarnAt mirror --pool-size and --warn-at.
	DefaultPoolSize int
	WarnAt          float64

	// Colour asks for the ANSI escapes the CLI writes to a terminal. The page
	// renders them; nothing else does.
	Colour bool

	// PlanPath is the name printed in the report header. The browser has no
	// file path, so the caller supplies the label it wants a reader to see.
	PlanPath string
}

// DefaultOptions matches the CLI's defaults, so a report produced here and a
// report produced by `headroom analyze` differ in nothing but the file name.
func DefaultOptions() Options {
	r := rules.DefaultOptions()
	return Options{
		DefaultPoolSize: r.DefaultPoolSize,
		WarnAt:          r.WarnAt,
		Colour:          true,
		PlanPath:        "plan.json",
	}
}

// Timings are measured, not decorative.
//
// The page prints them as the run unfolds, and they are the reason it can show
// progress without inventing any: every line it draws names work that already
// happened and states how long it took.
type Timings struct {
	ParseMS int `json:"parse_ms"`
	GraphMS int `json:"graph_ms"`
	RulesMS int `json:"rules_ms"`
	TotalMS int `json:"total_ms"`
}

// Count is one name and how many times it occurred.
type Count struct {
	Key string `json:"key"`
	N   int    `json:"n"`
}

// RuleCount is one rule, its severity, and how many findings it produced.
type RuleCount struct {
	Rule     string `json:"rule"`
	Severity string `json:"severity"`
	N        int    `json:"n"`
}

// Stats is the shape of what may be counted about a run.
//
// This is the payload the page reports back, so its field list is a privacy
// boundary and not a convenience. Adding a field here is adding a field to
// what leaves a stranger's browser, and the API refuses anything it was not
// told to expect.
type Stats struct {
	// FormatVersion and TerraformVersion come from the plan header. Both are
	// version strings of public software, and both already travel in the CLI's
	// upload payload.
	FormatVersion    string `json:"format_version"`
	TerraformVersion string `json:"terraform_version"`

	// ResourceCount is every managed resource in planned_values. NodeCount is
	// how many of those the extractor recognised, so the gap between them is
	// coverage, which is the most useful number in this struct.
	ResourceCount int `json:"resource_count"`
	NodeCount     int `json:"node_count"`
	EdgeCount     int `json:"edge_count"`

	// Clouds counts resources per cloud, so a plan touching two providers is
	// counted in both rather than filed under a winner.
	Clouds []Count `json:"clouds"`

	// Resources counts terraform types. Bounded, because an unbounded list
	// keyed by anything the input chooses is a way to push text through a
	// counter.
	Resources []Count `json:"resources"`

	// Rules counts findings per rule.
	Rules []RuleCount `json:"rules"`

	// Findings totals, split by severity.
	FindingCount  int `json:"finding_count"`
	CriticalCount int `json:"critical_count"`
	WarningCount  int `json:"warning_count"`
}

// MaxResourceTypes bounds the type histogram. Real plans sit far below it: the
// widest fixture in this repository reaches 21 distinct types.
const MaxResourceTypes = 200

// MaxRuleCounts bounds the rule histogram. There are 20 rules today.
const MaxRuleCounts = 100

// Result is one run.
//
// A failed run still returns a Result: OK is false and Error carries a message
// meant for a person reading a screen, because every error this package can
// produce is a message about the input somebody just pasted.
type Result struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`

	// Version is the CLI version this engine was built from, stamped the same
	// way the binary is. The page prints it, so a report from the browser can
	// be traced to a release the same way a report from a terminal can.
	Version string `json:"version"`

	// Report is the human report, with escapes when Colour asked for them.
	Report string `json:"report"`

	// Findings is what `--json` writes: the findings array, indented, never
	// null.
	Findings string `json:"findings"`

	// Payload is what `--dry-run` writes: the exact bytes an upload would
	// send, redacted with Options.Salt.
	Payload string `json:"payload"`

	Stats   Stats   `json:"stats"`
	Timings Timings `json:"timings"`
}

// Analyze runs the whole pipeline over raw plan bytes.
//
// It never panics and it never returns an error value: a browser has one place
// to put a failure, which is the screen, so every outcome comes back inside
// Result.
func Analyze(raw []byte, version string, opt Options) (res Result) {
	res.Version = version

	// The recover is the point. plan.Parse is fuzzed and the rules are pure,
	// so this is not expected to fire, but "not expected" is the wrong
	// standard for the one entry point that eats arbitrary paste and cannot
	// afford to take the page down with it.
	defer func() {
		if r := recover(); r != nil {
			res = Result{
				OK:      false,
				Version: version,
				Error:   fmt.Sprintf("the analysis failed on this input: %v. Please open an issue with the plan, or the shape of it, at github.com/headroom-project/headroom", r),
			}
		}
	}()

	if len(raw) == 0 {
		res.Error = "there is nothing to analyze yet. Paste the output of: terraform show -json tfplan"
		return res
	}
	if len(raw) > MaxPlanBytes {
		res.Error = fmt.Sprintf("this plan is %s and the browser build stops at %s. Run the binary instead: it has no such limit, and a plan this size is the case the CLI was written for",
			mib(len(raw)), mib(MaxPlanBytes))
		return res
	}

	started := time.Now()

	f, err := plan.Parse(raw)
	if err != nil {
		res.Error = "this is not a terraform plan document: " + err.Error() + ". Produce one with: terraform plan -out=tfplan && terraform show -json tfplan"
		return res
	}
	parsed := time.Now()

	cat, err := catalog.Load()
	if err != nil {
		res.Error = "the built-in catalog failed to load: " + err.Error()
		return res
	}

	g := graph.Build(f)
	built := time.Now()

	ropt := rules.DefaultOptions()
	ropt.DefaultPoolSize = opt.DefaultPoolSize
	ropt.WarnAt = opt.WarnAt
	findings := rules.Run(f, g, cat, ropt)
	ran := time.Now()

	// The same encoder the CLI uses for --dry-run, so the payload the page
	// shows is the payload an upload would send.
	payload := extract.NewRedactor(opt.Salt).Build(f, g, findings, version)
	payloadJSON, err := indented(payload)
	if err != nil {
		res.Error = "the redacted payload could not be encoded: " + err.Error()
		return res
	}

	// --json emits an empty array and never null, for the same reason it does
	// in the CLI: `jq length` over null is an error rather than zero, and the
	// quiet plan is the common one.
	if findings == nil {
		findings = []rules.Finding{}
	}
	findingsJSON, err := indented(findings)
	if err != nil {
		res.Error = "the findings could not be encoded: " + err.Error()
		return res
	}

	var buf bytes.Buffer
	report.Coloured(&buf, findings, opt.PlanPath, opt.Colour)

	res.OK = true
	res.Report = buf.String()
	res.Findings = findingsJSON
	res.Payload = payloadJSON
	res.Stats = statsOf(f, payload, findings)
	res.Timings = Timings{
		ParseMS: ms(started, parsed),
		GraphMS: ms(parsed, built),
		RulesMS: ms(built, ran),
		TotalMS: ms(started, time.Now()),
	}
	return res
}

// statsOf counts a run.
//
// Every value it writes is either a number, a rule id, a severity, a cloud
// name, a terraform type or a version string. Nothing that came out of a
// customer's naming scheme is reachable from here, which is asserted by a test
// that greps the encoded struct for the addresses in each fixture.
func statsOf(f *plan.File, payload extract.Payload, findings []rules.Finding) Stats {
	s := Stats{
		FormatVersion:    f.FormatVersion,
		TerraformVersion: f.TerraformVersion,
		NodeCount:        len(payload.Nodes),
		EdgeCount:        len(payload.Edges),
		Clouds:           []Count{},
		Resources:        []Count{},
		Rules:            []RuleCount{},
	}

	types := map[string]int{}
	clouds := map[string]int{}
	for _, r := range f.Resources() {
		s.ResourceCount++
		t := typeName(r.Type)
		if t == "" {
			continue
		}
		types[t]++
		clouds[CloudOf(t)]++
	}

	s.Resources = topCounts(types, MaxResourceTypes)
	s.Clouds = topCounts(clouds, len(clouds))

	byRule := map[string]*RuleCount{}
	order := []string{}
	for _, fd := range findings {
		s.FindingCount++
		switch fd.Severity {
		case rules.SeverityCritical:
			s.CriticalCount++
		case rules.SeverityWarning:
			s.WarningCount++
		}
		key := fd.Rule + "\x00" + fd.Severity
		if _, ok := byRule[key]; !ok {
			byRule[key] = &RuleCount{Rule: fd.Rule, Severity: fd.Severity}
			order = append(order, key)
		}
		byRule[key].N++
	}
	sort.Strings(order)
	for _, key := range order {
		if len(s.Rules) == MaxRuleCounts {
			break
		}
		s.Rules = append(s.Rules, *byRule[key])
	}

	return s
}

// CloudOf names the cloud a terraform type belongs to.
//
// By provider prefix and nothing else. The alternative is a list of known
// types, which would file every type added by a provider release under the
// wrong answer until somebody updated the list, and the counts exist to show
// what people are actually bringing.
//
// "other" is a real answer, not a failure: a plan is routinely half cloud and
// half kubernetes, random, tls or local, and pretending those belong to a
// cloud would inflate whichever one is listed first.
func CloudOf(terraformType string) string {
	switch {
	case strings.HasPrefix(terraformType, "aws_"):
		return "aws"
	case strings.HasPrefix(terraformType, "azurerm_"),
		strings.HasPrefix(terraformType, "azuread_"),
		strings.HasPrefix(terraformType, "azapi_"),
		strings.HasPrefix(terraformType, "azurestack_"):
		return "azure"
	case strings.HasPrefix(terraformType, "google_"):
		return "gcp"
	default:
		return "other"
	}
}

// typeName keeps a terraform type only if it looks like one.
//
// The counter is keyed by this string and the key travels off the machine, so
// a type is admitted only when it matches the shape a provider schema can
// produce: lower case, digits and underscores, starting with a letter, and
// short. Anything else is dropped rather than truncated, because a truncated
// unknown is still an unknown with a shorter name.
func typeName(t string) string {
	const maxLen = 64
	if t == "" || len(t) > maxLen {
		return ""
	}
	for i := 0; i < len(t); i++ {
		c := t[i]
		switch {
		case c >= 'a' && c <= 'z':
		case c >= '0' && c <= '9', c == '_':
			if i == 0 {
				return ""
			}
		default:
			return ""
		}
	}
	return t
}

// topCounts turns a histogram into a stable, bounded list: highest count
// first, ties broken by name so two runs over the same plan encode the same
// bytes.
func topCounts(m map[string]int, limit int) []Count {
	out := make([]Count, 0, len(m))
	for k, n := range m {
		out = append(out, Count{Key: k, N: n})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].N != out[j].N {
			return out[i].N > out[j].N
		}
		return out[i].Key < out[j].Key
	})
	if limit >= 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

func indented(v any) (string, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return "", err
	}
	return buf.String(), nil
}

func ms(from, to time.Time) int {
	d := to.Sub(from)
	if d < 0 {
		return 0
	}
	return int(d.Milliseconds())
}

func mib(n int) string {
	return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
}
