package webrun_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/headroom-project/headroom/internal/catalog"
	"github.com/headroom-project/headroom/internal/extract"
	"github.com/headroom-project/headroom/internal/graph"
	"github.com/headroom-project/headroom/internal/plan"
	"github.com/headroom-project/headroom/internal/report"
	"github.com/headroom-project/headroom/internal/rules"
	"github.com/headroom-project/headroom/internal/webrun"
)

const fixtureDir = "../../fixtures"

func fixtures(t *testing.T) []string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(fixtureDir, "*", "plan.json"))
	if err != nil {
		t.Fatalf("glob fixtures: %v", err)
	}
	if len(matches) == 0 {
		t.Fatal("no fixtures found; this test proves nothing without them")
	}
	return matches
}

func read(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return raw
}

// The whole premise of the browser build is that it is the CLI and not a
// lookalike. If this drifts, the page is showing a second implementation of
// the product's output and every screenshot of it is a claim nobody checked.
func TestReportIsByteIdenticalToTheCliPath(t *testing.T) {
	for _, path := range fixtures(t) {
		name := filepath.Base(filepath.Dir(path))
		t.Run(name, func(t *testing.T) {
			raw := read(t, path)

			f, err := plan.Parse(raw)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			cat, err := catalog.Load()
			if err != nil {
				t.Fatalf("catalog: %v", err)
			}
			g := graph.Build(f)
			findings := rules.Run(f, g, cat, rules.DefaultOptions())
			var want bytes.Buffer
			report.Coloured(&want, findings, "plan.json", false)

			opt := webrun.DefaultOptions()
			opt.Colour = false
			got := webrun.Analyze(raw, "v0.2.0", opt)
			if !got.OK {
				t.Fatalf("analysis refused a real fixture: %s", got.Error)
			}
			if got.Report != want.String() {
				t.Fatalf("report differs from the CLI path\n--- webrun ---\n%s\n--- cli ---\n%s", got.Report, want.String())
			}
		})
	}
}

var ansi = regexp.MustCompile("\x1b\\[[0-9;]*m")

// Colour has to be additive and nothing else. The page strips nothing, but the
// CLI's own promise is that removing the escapes gives back the plain report
// byte for byte, and the browser build must not be the one place that stops
// being true.
func TestColourAddsEscapesAndNothingElse(t *testing.T) {
	raw := read(t, filepath.Join(fixtureDir, "01-ecs-rds", "plan.json"))

	plainOpt := webrun.DefaultOptions()
	plainOpt.Colour = false
	plain := webrun.Analyze(raw, "v0.2.0", plainOpt)

	colourOpt := webrun.DefaultOptions()
	colourOpt.Colour = true
	coloured := webrun.Analyze(raw, "v0.2.0", colourOpt)

	if !strings.Contains(coloured.Report, "\x1b[") {
		t.Fatal("Colour was requested and no escape was written")
	}
	if strings.Contains(plain.Report, "\x1b[") {
		t.Fatal("Colour was not requested and an escape was written anyway")
	}
	if stripped := ansi.ReplaceAllString(coloured.Report, ""); stripped != plain.Report {
		t.Fatal("stripping the escapes did not give back the plain report")
	}
}

// --dry-run is the audit path: a customer is told they can see the exact bytes
// an upload would send. The page shows that same document, so it has to come
// out of the same encoder over the same redactor.
func TestPayloadMatchesTheDryRunEncoder(t *testing.T) {
	const salt = "a-per-session-salt"
	for _, path := range fixtures(t) {
		name := filepath.Base(filepath.Dir(path))
		t.Run(name, func(t *testing.T) {
			raw := read(t, path)

			f, err := plan.Parse(raw)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			cat, err := catalog.Load()
			if err != nil {
				t.Fatalf("catalog: %v", err)
			}
			g := graph.Build(f)
			findings := rules.Run(f, g, cat, rules.DefaultOptions())
			payload := extract.NewRedactor(salt).Build(f, g, findings, "v0.2.0")

			var want bytes.Buffer
			enc := json.NewEncoder(&want)
			enc.SetIndent("", "  ")
			if err := enc.Encode(payload); err != nil {
				t.Fatalf("encode: %v", err)
			}

			opt := webrun.DefaultOptions()
			opt.Salt = salt
			got := webrun.Analyze(raw, "v0.2.0", opt)
			if got.Payload != want.String() {
				t.Fatal("the payload the page would show is not the payload --dry-run prints")
			}
		})
	}
}

// The counts are the only thing that leaves the visitor's browser, so this is
// the test that decides whether the feature is honest. It reads every fixture,
// encodes the stats, and asserts that no terraform address from that plan
// survives anywhere inside them.
func TestStatsCarryNoResourceAddress(t *testing.T) {
	for _, path := range fixtures(t) {
		name := filepath.Base(filepath.Dir(path))
		t.Run(name, func(t *testing.T) {
			raw := read(t, path)

			f, err := plan.Parse(raw)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}

			res := webrun.Analyze(raw, "v0.2.0", webrun.DefaultOptions())
			if !res.OK {
				t.Fatalf("analysis refused a real fixture: %s", res.Error)
			}
			encoded, err := json.Marshal(res.Stats)
			if err != nil {
				t.Fatalf("encode stats: %v", err)
			}
			text := string(encoded)

			addresses := 0
			for _, r := range f.Resources() {
				if r.Address == "" {
					continue
				}
				addresses++
				if strings.Contains(text, r.Address) {
					t.Fatalf("the address %q survived into the counts: %s", r.Address, text)
				}
			}
			if addresses == 0 {
				t.Fatal("no addresses to look for; the assertion above proved nothing")
			}

			// And the inverse, so a Stats that accidentally encoded to "{}"
			// could not pass the check above by being empty.
			if len(res.Stats.Resources) == 0 {
				t.Fatal("no resource types were counted, so the search space was empty")
			}
			if !strings.Contains(text, res.Stats.Resources[0].Key) {
				t.Fatal("the type histogram is not in the encoded stats, so this test is looking at the wrong bytes")
			}
		})
	}
}

// A finding is a rule id and a severity here. The rendered sentence names
// resources and must stay out.
func TestStatsCountRulesWithoutFindingText(t *testing.T) {
	raw := read(t, filepath.Join(fixtureDir, "01-ecs-rds", "plan.json"))
	res := webrun.Analyze(raw, "v0.2.0", webrun.DefaultOptions())
	if !res.OK {
		t.Fatalf("refused: %s", res.Error)
	}
	if len(res.Stats.Rules) == 0 {
		t.Fatal("this fixture produces findings and none were counted")
	}

	// The shape the API's ingest schema accepts. Asserted here rather than
	// there, because this is the side that produces it.
	ruleID := regexp.MustCompile(`^[A-Z]{1,3}[0-9]{1,3}$`)

	total := 0
	for _, r := range res.Stats.Rules {
		total += r.N
		if !ruleID.MatchString(r.Rule) {
			t.Fatalf("rule id %q is not the shape the API accepts", r.Rule)
		}
		switch r.Severity {
		case rules.SeverityCritical, rules.SeverityWarning, rules.SeverityInfo:
		default:
			t.Fatalf("severity %q is not one the schema knows", r.Severity)
		}
	}
	if total != res.Stats.FindingCount {
		t.Fatalf("rule counts sum to %d and the run reported %d findings", total, res.Stats.FindingCount)
	}
	if res.Stats.CriticalCount+res.Stats.WarningCount > res.Stats.FindingCount {
		t.Fatal("severity counts exceed the total")
	}
}

// Two runs over the same bytes have to encode the same counts. Without this
// the histograms are map iteration order, and a corpus generated twice would
// produce a diff that looks like a change of behaviour.
func TestStatsAreDeterministic(t *testing.T) {
	raw := read(t, filepath.Join(fixtureDir, "gcp-01-gke-cloudrun-sql", "plan.json"))
	first, err := json.Marshal(webrun.Analyze(raw, "v0.2.0", webrun.DefaultOptions()).Stats)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	for i := 0; i < 8; i++ {
		again, err := json.Marshal(webrun.Analyze(raw, "v0.2.0", webrun.DefaultOptions()).Stats)
		if err != nil {
			t.Fatalf("encode: %v", err)
		}
		if !bytes.Equal(first, again) {
			t.Fatalf("run %d encoded different counts", i)
		}
	}
}

func TestCloudOf(t *testing.T) {
	cases := map[string]string{
		"aws_db_instance":            "aws",
		"aws_subnet":                 "aws",
		"azurerm_kubernetes_cluster": "azure",
		"azuread_application":        "azure",
		"azapi_resource":             "azure",
		"google_sql_database":        "gcp",
		"kubernetes_deployment":      "other",
		"random_password":            "other",
		"":                           "other",
		// Near misses, because a prefix match is exactly the kind of rule that
		// gets loose when somebody drops the underscore.
		"awsome_thing":  "other",
		"azurermx_vnet": "other",
		"googleish_api": "other",
	}
	for in, want := range cases {
		if got := webrun.CloudOf(in); got != want {
			t.Errorf("CloudOf(%q) = %q, want %q", in, got, want)
		}
	}
}

// Every fixture is counted under the cloud its directory name claims, which is
// the only check that ties the classifier to the corpus rather than to my
// opinion of what the prefixes are.
func TestFixturesLandUnderTheExpectedCloud(t *testing.T) {
	want := map[string]string{"azure": "azure", "gcp": "gcp"}
	for _, path := range fixtures(t) {
		name := filepath.Base(filepath.Dir(path))
		expected := "aws"
		for prefix, cloud := range want {
			if strings.HasPrefix(name, prefix+"-") {
				expected = cloud
			}
		}
		t.Run(name, func(t *testing.T) {
			res := webrun.Analyze(read(t, path), "v0.2.0", webrun.DefaultOptions())
			if !res.OK {
				t.Fatalf("refused: %s", res.Error)
			}
			if len(res.Stats.Clouds) == 0 {
				t.Fatal("no cloud was counted")
			}
			if res.Stats.Clouds[0].Key != expected {
				t.Fatalf("the largest cloud is %q, expected %q", res.Stats.Clouds[0].Key, expected)
			}
		})
	}
}

// A panic in a process is an exit code. A panic in WebAssembly is a dead tab
// with no error message, so the entry point absorbs everything.
func TestHostileInputIsRefusedAndNeverPanics(t *testing.T) {
	cases := map[string][]byte{
		"empty":              {},
		"not json":           []byte("terraform plan -out=tfplan"),
		"truncated":          []byte(`{"format_version":"1.0","planned_values":`),
		"null everywhere":    []byte(`{"format_version":null,"planned_values":null,"configuration":null,"prior_state":null}`),
		"wrong types":        []byte(`{"format_version":"1.0","planned_values":{"root_module":{"resources":[{"address":123,"type":[],"values":"no"}]}}}`),
		"deep modules":       []byte(`{"format_version":"1.0","planned_values":{"root_module":{"child_modules":[{"child_modules":[{"child_modules":[{}]}]}]}}}`),
		"bom then garbage":   append([]byte{0xEF, 0xBB, 0xBF}, []byte("}{")...),
		"lone bom":           {0xEF, 0xBB, 0xBF},
		"json but not plan":  []byte(`{"hello":"world"}`),
		"array at the root":  []byte(`[1,2,3]`),
		"escape in the type": []byte(`{"format_version":"1.0","planned_values":{"root_module":{"resources":[{"address":"a.b","type":"[31mred","values":{}}]}}}`),
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			res := webrun.Analyze(raw, "v0.2.0", webrun.DefaultOptions())
			if res.OK {
				// Some of these are structurally valid and simply describe
				// nothing, which is a legitimate empty run rather than an
				// error. What is never acceptable is a panic, and reaching
				// this line means there was not one.
				if strings.Contains(res.Report, "\x1b[31mred") {
					t.Fatal("an escape from the input reached the report")
				}
				return
			}
			if res.Error == "" {
				t.Fatal("the run failed and said nothing about why")
			}
		})
	}
}

// The type histogram is keyed by a string that leaves the browser, so a type
// that is not shaped like a terraform type is dropped rather than counted.
func TestOnlyProviderShapedTypesAreCounted(t *testing.T) {
	raw := []byte(`{"format_version":"1.0","planned_values":{"root_module":{"resources":[
		{"address":"aws_subnet.ok","mode":"managed","type":"aws_subnet","values":{}},
		{"address":"x.bad1","mode":"managed","type":"Has-Capitals","values":{}},
		{"address":"x.bad2","mode":"managed","type":"has spaces","values":{}},
		{"address":"x.bad3","mode":"managed","type":"9leading","values":{}},
		{"address":"x.bad4","mode":"managed","type":"tem_acento_ç","values":{}}
	]}}}`)

	res := webrun.Analyze(raw, "v0.2.0", webrun.DefaultOptions())
	if !res.OK {
		t.Fatalf("refused a structurally valid plan: %s", res.Error)
	}
	if len(res.Stats.Resources) != 1 || res.Stats.Resources[0].Key != "aws_subnet" {
		t.Fatalf("expected only aws_subnet to be counted, got %+v", res.Stats.Resources)
	}
	if res.Stats.ResourceCount != 5 {
		t.Fatalf("every resource is still counted in the total, got %d", res.Stats.ResourceCount)
	}
}

// The ceiling exists so a browser tab does not die decoding a document that
// belongs in a pipeline, and the refusal has to say that rather than fail.
func TestOversizedPlanIsRefusedWithAnInstruction(t *testing.T) {
	raw := make([]byte, webrun.MaxPlanBytes+1)
	for i := range raw {
		raw[i] = ' '
	}
	res := webrun.Analyze(raw, "v0.2.0", webrun.DefaultOptions())
	if res.OK {
		t.Fatal("an oversized plan was accepted")
	}
	if !strings.Contains(res.Error, "binary") {
		t.Fatalf("the refusal does not point at the CLI: %s", res.Error)
	}
}

// A plan that reaches no ceiling is the common case and the one that broke the
// CLI's --json path once already.
func TestQuietPlanEmitsAnEmptyArrayAndNotNull(t *testing.T) {
	raw := []byte(`{"format_version":"1.0","planned_values":{"root_module":{"resources":[]}}}`)
	res := webrun.Analyze(raw, "v0.2.0", webrun.DefaultOptions())
	if !res.OK {
		t.Fatalf("refused: %s", res.Error)
	}
	if strings.TrimSpace(res.Findings) != "[]" {
		t.Fatalf("findings encoded as %q, want []", strings.TrimSpace(res.Findings))
	}
	if res.Stats.FindingCount != 0 {
		t.Fatalf("counted %d findings in a plan with no resources", res.Stats.FindingCount)
	}
}

// Timings are printed to a person as a description of work that happened. A
// negative or absurd number would be a lie on the screen.
func TestTimingsAreMeasuredAndSane(t *testing.T) {
	raw := read(t, filepath.Join(fixtureDir, "02-ec2-nat-ebs", "plan.json"))
	res := webrun.Analyze(raw, "v0.2.0", webrun.DefaultOptions())
	if !res.OK {
		t.Fatalf("refused: %s", res.Error)
	}
	tm := res.Timings
	if tm.ParseMS < 0 || tm.GraphMS < 0 || tm.RulesMS < 0 || tm.TotalMS < 0 {
		t.Fatalf("a negative duration: %+v", tm)
	}
	if tm.TotalMS+2 < tm.ParseMS+tm.GraphMS+tm.RulesMS {
		t.Fatalf("the phases do not fit inside the total: %+v", tm)
	}
}

// The version the page prints has to be the version it was handed, because a
// report that names the wrong release is worse than one that names none.
func TestVersionIsCarriedThroughEveryOutcome(t *testing.T) {
	for name, raw := range map[string][]byte{
		"good": read(t, filepath.Join(fixtureDir, "01-ecs-rds", "plan.json")),
		"bad":  []byte("not a plan"),
	} {
		res := webrun.Analyze(raw, "v9.9.9", webrun.DefaultOptions())
		if res.Version != "v9.9.9" {
			t.Fatalf("%s: version came back as %q", name, res.Version)
		}
	}
}
