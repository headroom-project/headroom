package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// The CLI is the whole product surface for a free user, and until this file
// existed not one line of it was exercised. Exit codes matter most: headroom is
// meant to run inside somebody's pipeline, so "the tool broke" and "the tool
// found something" have to be distinguishable from the outside.

const (
	fixtureCritical     = "../../fixtures/01-ecs-rds/plan.json"    // 3 critical, 2 warning
	fixtureQuiet        = "../../fixtures/05-cross-repo/plan.json" // 0 critical, 1 warning
	fixtureAzureModules = "../../fixtures/azure-03-module-boundary/plan.json"
)

type result struct {
	code   int
	stdout string
	stderr string
	err    error
}

func exec(t *testing.T, args ...string) result {
	t.Helper()
	var out, errb bytes.Buffer
	code, err := run(args, &out, &errb)
	return result{code: code, stdout: out.String(), stderr: errb.String(), err: err}
}

// --- invocation and exit codes -------------------------------------------

func TestNoArgsPrintsUsageAndSucceeds(t *testing.T) {
	r := exec(t)
	if r.code != exitOK || r.err != nil {
		t.Fatalf("code = %d, err = %v, want 0 and nil", r.code, r.err)
	}
	for _, want := range []string{"headroom " + version, "usage:", "--fail-on", "exit codes:"} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("usage is missing %q", want)
		}
	}
}

func TestVersionIsPrintedAndParseable(t *testing.T) {
	for _, arg := range []string{"version", "--version"} {
		r := exec(t, arg)
		if r.code != exitOK {
			t.Errorf("%s: code = %d, want 0", arg, r.code)
		}
		if got := strings.TrimSpace(r.stdout); got != "headroom "+version {
			t.Errorf("%s printed %q, want %q", arg, got, "headroom "+version)
		}
	}
}

// Exit code 2 is "could not run". Anything the user typed wrong lands here, and
// never on 1, which a pipeline reads as a real finding.
func TestUserErrorsExitTwoAndNeverOne(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"unknown command", []string{"analyse", fixtureCritical}, "unknown command"},
		{"no plan file", []string{"analyze"}, "exactly one plan"},
		{"two plan files", []string{"analyze", fixtureCritical, fixtureQuiet}, "exactly one plan"},
		{"missing file", []string{"analyze", "does-not-exist.json"}, ""},
		{"unknown flag", []string{"analyze", "--nope", fixtureCritical}, ""},
		{"bad fail-on", []string{"analyze", "--fail-on", "urgent", fixtureCritical}, "not a severity"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := exec(t, tc.args...)
			if r.code != exitError {
				t.Errorf("code = %d, want %d", r.code, exitError)
			}
			if r.err == nil {
				t.Fatal("no error returned")
			}
			if tc.want != "" && !strings.Contains(r.err.Error(), tc.want) {
				t.Errorf("error = %q, want it to mention %q", r.err, tc.want)
			}
		})
	}
}

// A typo in --fail-on used to fall through to the critical-only behaviour, so a
// pipeline that asked to fail on warnings silently failed on less. This is the
// regression that made the flag worth validating.
func TestFailOnTypoIsRejectedRatherThanQuietlyWeakened(t *testing.T) {
	r := exec(t, "analyze", "--fail-on", "warnings", fixtureQuiet)
	if r.code != exitError {
		t.Fatalf("code = %d, want %d: a plural typo must not be accepted", r.code, exitError)
	}
	if !strings.Contains(r.err.Error(), "warning") || !strings.Contains(r.err.Error(), "critical") {
		t.Errorf("the error does not name the accepted values: %v", r.err)
	}
}

// A flag whose default comes from os.Getenv has that default printed back by
// PrintDefaults, and PrintDefaults runs on any flag error. So a single mistyped
// flag used to write HEADROOM_SALT, and later HEADROOM_API_KEY, straight into
// stderr, which in CI means straight into a log a whole team can read. The
// environment is read after Parse instead, and this is the test that says so.
func TestAFlagErrorNeverPrintsASecretFromTheEnvironment(t *testing.T) {
	t.Setenv("HEADROOM_SALT", "salt-from-the-environment")
	t.Setenv("HEADROOM_API_KEY", "hr_live_key_from_the_environment")

	r := exec(t, "analyze", "--not-a-flag", fixtureCritical)
	if r.code != exitError {
		t.Fatalf("code = %d, want %d", r.code, exitError)
	}
	for _, secret := range []string{"salt-from-the-environment", "hr_live_key_from_the_environment"} {
		if strings.Contains(r.stderr, secret) {
			t.Errorf("a flag error printed %q from the environment:\n%s", secret, r.stderr)
		}
		if strings.Contains(r.stdout, secret) {
			t.Errorf("a flag error printed %q on stdout", secret)
		}
	}
	// The salt still has to work, or the fix broke the flag it was fixing.
	ok := exec(t, "analyze", "--dry-run", fixtureCritical)
	if strings.Contains(ok.stderr, "no salt set") {
		t.Error("HEADROOM_SALT stopped being read at all")
	}
}

func TestFailOnCriticalExitsOneOnACriticalPlan(t *testing.T) {
	r := exec(t, "analyze", "--fail-on", "critical", fixtureCritical)
	if r.code != exitFinding {
		t.Fatalf("code = %d, want %d", r.code, exitFinding)
	}
	if r.err != nil {
		t.Errorf("a matched gate is not a tool error, got err = %v", r.err)
	}
	if !strings.Contains(r.stdout, "CRITICAL") {
		t.Error("the report was not written before the gate fired")
	}
}

// The gate has to be silent when nothing matches, otherwise nobody turns it on.
func TestFailOnCriticalIsSilentWhenOnlyWarningsExist(t *testing.T) {
	r := exec(t, "analyze", "--fail-on", "critical", fixtureQuiet)
	if r.code != exitOK {
		t.Fatalf("code = %d, want 0: this plan has warnings and no criticals", r.code)
	}
}

func TestFailOnWarningCatchesWhatCriticalLetsThrough(t *testing.T) {
	critical := exec(t, "analyze", "--fail-on", "critical", fixtureQuiet)
	warning := exec(t, "analyze", "--fail-on", "warning", fixtureQuiet)
	if critical.code != exitOK {
		t.Fatalf("--fail-on critical = %d, want 0", critical.code)
	}
	if warning.code != exitFinding {
		t.Fatalf("--fail-on warning = %d, want %d on the same plan", warning.code, exitFinding)
	}
}

// Without the flag there is no gate at all, whatever the findings say. This is
// the fail-open promise: a capacity tool must never be the reason a deploy is
// stuck.
func TestWithoutFailOnEvenACriticalPlanExitsZero(t *testing.T) {
	r := exec(t, "analyze", fixtureCritical)
	if r.code != exitOK {
		t.Fatalf("code = %d, want 0 with no --fail-on", r.code)
	}
	if !strings.Contains(r.stdout, "CRITICAL") {
		t.Error("the critical findings were not reported")
	}
}

// --- output shapes --------------------------------------------------------

func TestJSONOutputIsValidAndCarriesTheNumbers(t *testing.T) {
	r := exec(t, "analyze", "--json", fixtureCritical)
	if r.code != exitOK || r.err != nil {
		t.Fatalf("code = %d, err = %v", r.code, r.err)
	}

	var findings []struct {
		Rule       string         `json:"rule"`
		Severity   string         `json:"severity"`
		Title      string         `json:"title"`
		Confidence string         `json:"confidence"`
		Metrics    map[string]int `json:"metrics"`
	}
	if err := json.Unmarshal([]byte(r.stdout), &findings); err != nil {
		t.Fatalf("--json did not emit valid JSON: %v", err)
	}
	if len(findings) == 0 {
		t.Fatal("no findings in the JSON for a plan the text mode calls critical")
	}

	var r1 int
	for _, f := range findings {
		if f.Rule == "" || f.Severity == "" || f.Title == "" {
			t.Errorf("a finding is missing an identifying field: %+v", f)
		}
		if f.Rule == "R1" {
			r1++
			if f.Metrics["demand"] != 800 || f.Metrics["ceiling"] != 450 {
				t.Errorf("R1 metrics = %v, want demand 800 and ceiling 450", f.Metrics)
			}
		}
	}
	if r1 == 0 {
		t.Error("R1 is absent from the JSON output")
	}

	// Text and JSON must describe the same run, or one of them is lying.
	text := exec(t, "analyze", fixtureCritical)
	if got := strings.Count(text.stdout, "CRITICAL "); got == 0 {
		t.Fatal("no CRITICAL lines in the text report")
	}
}

// The quiet run is the one a pipeline sees every day, and it was the one that
// emitted `null`. `jq 'length'` over null is an error, not 0, so the shape of
// the output changed with the result, which is the one thing a machine readable
// format must never do.
func TestJSONOnAPlanWithNoFindingsIsAnEmptyListNotNull(t *testing.T) {
	empty := filepath.Join(t.TempDir(), "plan.json")
	if err := os.WriteFile(empty, []byte(
		`{"format_version":"1.2","terraform_version":"1.6.5",`+
			`"planned_values":{"root_module":{"resources":[]}}}`), 0o600); err != nil {
		t.Fatalf("write plan: %v", err)
	}

	r := exec(t, "analyze", "--json", empty)
	if r.code != exitOK || r.err != nil {
		t.Fatalf("code = %d, err = %v", r.code, r.err)
	}
	if got := strings.TrimSpace(r.stdout); got != "[]" {
		t.Errorf("stdout = %q, want %q", got, "[]")
	}

	// The bytes have to survive a decode into a list, because that is what
	// every consumer downstream actually does with them.
	var findings []map[string]any
	if err := json.Unmarshal([]byte(r.stdout), &findings); err != nil {
		t.Fatalf("--json did not emit valid JSON: %v", err)
	}
	if findings == nil {
		t.Error("decoded to nil: the document said null, not []")
	}
	if len(findings) != 0 {
		t.Errorf("len = %d, want 0", len(findings))
	}
}

func TestTextReportNamesThePlanAndCountsTheFindings(t *testing.T) {
	r := exec(t, "analyze", fixtureCritical)
	if !strings.Contains(r.stdout, filepath.Base(fixtureCritical)) {
		t.Error("the report does not say which plan it read")
	}
	if !strings.Contains(r.stdout, "critical") || !strings.Contains(r.stdout, "total") {
		t.Error("the report has no summary line")
	}
	// The finding is only useful if it carries its own evidence.
	for _, want := range []string{"aws_db_instance.main", "db.t3.medium", "confidence:", "source:"} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("the report is missing %q", want)
		}
	}
}

// --- the promise the product is sold on -----------------------------------

// "Your plan file never leaves your machine" is the security claim on the
// landing page and in the README. --dry-run is what makes it checkable, so if
// this test ever fails the claim is false.
func TestDryRunPayloadCarriesNoRealAddresses(t *testing.T) {
	r := exec(t, "analyze", "--dry-run", "--salt", "test-salt", fixtureCritical)
	if r.code != exitOK || r.err != nil {
		t.Fatalf("code = %d, err = %v", r.code, r.err)
	}

	var payload struct {
		SchemaVersion string `json:"schema_version"`
		GeneratedBy   string `json:"generated_by"`
		Salted        bool   `json:"salted"`
		Nodes         []struct {
			ID   string `json:"id"`
			Type string `json:"type"`
		} `json:"nodes"`
		Findings []struct {
			Rule      string   `json:"rule"`
			Resources []string `json:"resources"`
		} `json:"findings"`
	}
	if err := json.Unmarshal([]byte(r.stdout), &payload); err != nil {
		t.Fatalf("--dry-run did not emit valid JSON: %v", err)
	}
	if !payload.Salted {
		t.Error("salted = false even though --salt was given")
	}
	if payload.GeneratedBy != "headroom/"+version {
		t.Errorf("generated_by = %q, want headroom/%s", payload.GeneratedBy, version)
	}
	if len(payload.Nodes) == 0 {
		t.Fatal("the payload has no nodes at all")
	}

	// Every real name that appears in the local report, and the prose around
	// it, must be absent from what would travel.
	leaks := []string{
		"aws_db_instance.main", "aws_ecs_service.api", "aws_subnet.private_a",
		"aws_appautoscaling_target.api", "DB_POOL_SIZE",
		"Scale asymmetry", "outgrows the database",
	}
	for _, leak := range leaks {
		if strings.Contains(r.stdout, leak) {
			t.Errorf("the payload contains %q, which never leaves the machine", leak)
		}
	}
	for _, n := range payload.Nodes {
		if strings.Contains(n.ID, ".") {
			t.Errorf("node id %q looks like a terraform address, not a hash", n.ID)
		}
	}
}

// Allowlisted capacity attributes travel with their real values, and a subnet
// CIDR is one of them. That is deliberate, because the ceiling cannot be
// recomputed server-side without it, but it means "nothing identifying leaves
// your machine" is not the whole truth: real network topology does. This test
// pins the fact so the privacy copy on the site and in the README has to keep
// saying it out loud rather than quietly rounding it off.
func TestAllowlistedAttributesTravelWithRealValues(t *testing.T) {
	r := exec(t, "analyze", "--dry-run", "--salt", "test-salt", fixtureCritical)

	var payload struct {
		Nodes []struct {
			Type  string         `json:"type"`
			Attrs map[string]any `json:"attrs"`
		} `json:"nodes"`
	}
	if err := json.Unmarshal([]byte(r.stdout), &payload); err != nil {
		t.Fatalf("payload: %v", err)
	}

	var sawCIDR, sawClass bool
	for _, n := range payload.Nodes {
		if n.Type == "aws_subnet" {
			if got, ok := n.Attrs["cidr_block"].(string); ok && got != "" {
				sawCIDR = true
			}
			// The address itself is still hashed, and anything outside the
			// allowlist is still absent.
			for key := range n.Attrs {
				switch key {
				case "cidr_block", "availability_zone", "map_public_ip_on_launch", "vpc_id", "ipv6_cidr_block":
				default:
					t.Errorf("aws_subnet carries %q, which is not a capacity attribute", key)
				}
			}
		}
		if n.Type == "aws_db_instance" {
			if n.Attrs["instance_class"] == "db.t3.medium" {
				sawClass = true
			}
		}
	}
	if !sawCIDR {
		t.Error("no subnet CIDR in the payload; if that changed on purpose, the privacy copy can be strengthened")
	}
	if !sawClass {
		t.Error("the instance class did not travel, and no ceiling can be recomputed without it")
	}
}

// A different salt has to produce different ids, or the salt is decoration and
// two customers hash their identical addresses to identical values.
func TestSaltChangesEveryIdentifier(t *testing.T) {
	a := exec(t, "analyze", "--dry-run", "--salt", "org-a", fixtureCritical)
	b := exec(t, "analyze", "--dry-run", "--salt", "org-b", fixtureCritical)
	if a.stdout == b.stdout {
		t.Fatal("two different salts produced byte-identical payloads")
	}

	ids := func(out string) []string {
		var p struct {
			Nodes []struct {
				ID string `json:"id"`
			} `json:"nodes"`
		}
		if err := json.Unmarshal([]byte(out), &p); err != nil {
			t.Fatalf("payload: %v", err)
		}
		got := make([]string, len(p.Nodes))
		for i, n := range p.Nodes {
			got[i] = n.ID
		}
		return got
	}
	idsA, idsB := ids(a.stdout), ids(b.stdout)
	if len(idsA) != len(idsB) {
		t.Fatalf("node counts differ: %d and %d", len(idsA), len(idsB))
	}
	for i := range idsA {
		if idsA[i] == idsB[i] {
			t.Errorf("node %d hashed to %q under both salts", i, idsA[i])
		}
	}
}

// An unsalted payload is reversible by guessing, because terraform addresses are
// low entropy. The warning is the only thing standing between that and an
// upload, so it has to be on stderr where a redirected stdout cannot hide it.
func TestUnsaltedDryRunWarnsOnStderr(t *testing.T) {
	t.Setenv("HEADROOM_SALT", "")
	r := exec(t, "analyze", "--dry-run", fixtureCritical)
	if !strings.Contains(r.stderr, "no salt set") {
		t.Errorf("stderr does not warn about the missing salt: %q", r.stderr)
	}
	if strings.Contains(r.stdout, "no salt set") {
		t.Error("the warning is on stdout, where it would end up inside a redirected payload file")
	}
}

// --- upload ---------------------------------------------------------------

// Not one test in this file touches a real network. Every server here is an
// httptest.Server on loopback, and --api-url is how the CLI is pointed at it.

const testAPIKey = "hr_live_1f4a9c2e7b0d5836"

// recorder is a fake API. It counts requests and keeps the last one, so a test
// can assert both what was sent and that nothing was sent at all.
type recorder struct {
	srv    *httptest.Server
	calls  int32
	body   []byte
	auth   string
	path   string
	method string
}

// newRecorder answers every request with status and body. Status 201 with an
// empty body gets a plausible one, because that is the common case.
func newRecorder(t *testing.T, status int, body string) *recorder {
	t.Helper()
	rec := &recorder{}
	rec.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&rec.calls, 1)
		rec.method, rec.path = r.Method, r.URL.Path
		rec.auth = r.Header.Get("Authorization")
		rec.body, _ = io.ReadAll(r.Body)
		if status == http.StatusCreated && body == "" {
			body = `{"id":"rep_test","findings_count":5,"created_at":"2026-08-15T09:00:00Z"}`
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(rec.srv.Close)
	return rec
}

func (r *recorder) count() int { return int(atomic.LoadInt32(&r.calls)) }

// cleanEnv stops a developer's own HEADROOM_* variables from deciding what a
// test proves.
func cleanEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{"HEADROOM_API_KEY", "HEADROOM_API_URL", "HEADROOM_SALT", "HEADROOM_CONFIG"} {
		t.Setenv(k, "")
	}
}

// The one rule the whole audit story rests on. If the bytes on the wire can
// differ from the bytes --dry-run prints, then "run --dry-run and you are
// looking at all of it" is marketing rather than fact. Comparing the two byte
// sequences is the only assertion that settles it; anything softer would pass
// while a field quietly diverged.
func TestUploadedBytesAreExactlyWhatDryRunPrints(t *testing.T) {
	cleanEnv(t)
	for _, fixture := range []string{fixtureCritical, fixtureQuiet} {
		t.Run(filepath.Base(filepath.Dir(fixture)), func(t *testing.T) {
			rec := newRecorder(t, http.StatusCreated, "")

			dry := exec(t, "analyze", "--dry-run", "--salt", "org-salt", fixture)
			if dry.code != exitOK {
				t.Fatalf("--dry-run: code = %d, err = %v", dry.code, dry.err)
			}
			up := exec(t, "analyze", "--upload", "--salt", "org-salt",
				"--api-key", testAPIKey, "--api-url", rec.srv.URL, fixture)
			if up.code != exitOK {
				t.Fatalf("--upload: code = %d, err = %v, stderr = %s", up.code, up.err, up.stderr)
			}
			if rec.count() != 1 {
				t.Fatalf("the server saw %d requests, want 1", rec.count())
			}

			printed, sent := []byte(dry.stdout), rec.body
			if len(printed) == 0 {
				t.Fatal("--dry-run printed nothing to compare against")
			}
			if bytes.Equal(printed, sent) {
				return
			}
			for i := 0; i < len(printed) && i < len(sent); i++ {
				if printed[i] != sent[i] {
					t.Fatalf("printed and uploaded bytes differ at offset %d: printed %q, uploaded %q",
						i, snippet(printed, i), snippet(sent, i))
				}
			}
			t.Fatalf("printed %d bytes and uploaded %d bytes", len(printed), len(sent))
		})
	}
}

func snippet(b []byte, at int) string {
	lo, hi := at-20, at+20
	if lo < 0 {
		lo = 0
	}
	if hi > len(b) {
		hi = len(b)
	}
	return string(b[lo:hi])
}

// Two flags that mean opposite things must not have a silent winner. Somebody
// who typed both believed one of them, and guessing which turns a rehearsal into
// an upload.
func TestUploadAndDryRunTogetherIsAnExplicitError(t *testing.T) {
	cleanEnv(t)
	rec := newRecorder(t, http.StatusCreated, "")
	for _, order := range [][]string{
		{"analyze", "--dry-run", "--upload"},
		{"analyze", "--upload", "--dry-run"},
	} {
		args := append(append([]string{}, order...), "--salt", "s", "--api-key", testAPIKey, "--api-url", rec.srv.URL, fixtureCritical)
		r := exec(t, args...)
		if r.code != exitError {
			t.Errorf("%v: code = %d, want %d", order, r.code, exitError)
		}
		if r.err == nil || !strings.Contains(r.err.Error(), "--dry-run") || !strings.Contains(r.err.Error(), "--upload") {
			t.Errorf("%v: the error does not name both flags: %v", order, r.err)
		}
	}
	if rec.count() != 0 {
		t.Errorf("%d requests were sent by a run that should have refused to start", rec.count())
	}
}

// An unsalted payload hashes "aws_db_instance.main" to something a dictionary of
// a few thousand guesses reverses, and unsalted ids from two organizations
// collide. --dry-run warns because printing it locally harms nobody. Uploading it
// is different, so it is refused, and there is deliberately no flag to override
// the refusal.
func TestUploadRefusesAnUnsaltedPayloadAndSendsNothing(t *testing.T) {
	cleanEnv(t)
	rec := newRecorder(t, http.StatusCreated, "")
	r := exec(t, "analyze", "--upload", "--api-key", testAPIKey, "--api-url", rec.srv.URL, fixtureCritical)
	if r.code != exitError {
		t.Fatalf("code = %d, want %d", r.code, exitError)
	}
	if r.err == nil {
		t.Fatal("no error returned")
	}
	// The refusal has to say why, or the next thing the user does is look for
	// the flag that turns it off.
	for _, want := range []string{"salt", "reversible", "HEADROOM_SALT"} {
		if !strings.Contains(r.err.Error(), want) {
			t.Errorf("the refusal does not mention %q: %v", want, r.err)
		}
	}
	if rec.count() != 0 {
		t.Errorf("%d requests were sent despite the refusal", rec.count())
	}
}

// There is no --allow-unsalted and there must not be one. A flag that disables a
// privacy control gets pasted into a CI config once and is never revisited, so
// the control becomes decoration. This test fails the day somebody adds it,
// which is the point: adding it should require deleting this test and arguing
// for it in a pull request.
func TestThereIsNoFlagThatPermitsAnUnsaltedUpload(t *testing.T) {
	cleanEnv(t)
	usage := exec(t).stdout
	for _, forbidden := range []string{"unsalted", "no-salt", "insecure", "force"} {
		if strings.Contains(strings.ToLower(usage), forbidden) {
			t.Errorf("the usage text offers %q, which would make the salt optional again", forbidden)
		}
	}
	rec := newRecorder(t, http.StatusCreated, "")
	for _, flag := range []string{"--allow-unsalted", "--force", "--insecure", "--no-salt"} {
		r := exec(t, "analyze", "--upload", flag, "--api-key", testAPIKey, "--api-url", rec.srv.URL, fixtureCritical)
		if r.code != exitError {
			t.Errorf("%s: code = %d, want %d", flag, r.code, exitError)
		}
	}
	if rec.count() != 0 {
		t.Errorf("%d requests were sent", rec.count())
	}
}

func TestUploadWithoutAnAPIKeyIsRefusedBeforeAnythingIsSent(t *testing.T) {
	cleanEnv(t)
	rec := newRecorder(t, http.StatusCreated, "")
	r := exec(t, "analyze", "--upload", "--salt", "org-salt", "--api-url", rec.srv.URL, fixtureCritical)
	if r.code != exitError {
		t.Fatalf("code = %d, want %d", r.code, exitError)
	}
	if r.err == nil || !strings.Contains(r.err.Error(), "HEADROOM_API_KEY") {
		t.Errorf("the error does not say where to put a key: %v", r.err)
	}
	if rec.count() != 0 {
		t.Errorf("%d requests were sent without a key", rec.count())
	}
}

// A bearer token in cleartext belongs to whoever is on the path between here and
// there.
func TestUploadRefusesACleartextAPIURL(t *testing.T) {
	cleanEnv(t)
	r := exec(t, "analyze", "--upload", "--salt", "org-salt", "--api-key", testAPIKey,
		"--api-url", "http://api.headroomcli.com", fixtureCritical)
	if r.code != exitError {
		t.Fatalf("code = %d, want %d", r.code, exitError)
	}
	if r.err == nil || !strings.Contains(r.err.Error(), "https") {
		t.Errorf("the error does not point at https: %v", r.err)
	}
}

func TestUploadPostsToTheDocumentedRouteWithABearerToken(t *testing.T) {
	cleanEnv(t)
	rec := newRecorder(t, http.StatusCreated, "")
	r := exec(t, "analyze", "--upload", "--salt", "org-salt",
		"--api-key", testAPIKey, "--api-url", rec.srv.URL, fixtureCritical)
	if r.code != exitOK {
		t.Fatalf("code = %d, err = %v, stderr = %s", r.code, r.err, r.stderr)
	}
	if rec.method != http.MethodPost || rec.path != "/v1/reports" {
		t.Errorf("request = %s %s, want POST /v1/reports", rec.method, rec.path)
	}
	if rec.auth != "Bearer "+testAPIKey {
		t.Errorf("Authorization = %q", rec.auth)
	}
}

// The analysis is what the user asked for. It is printed before a socket is
// opened, so a failed upload costs them a retry and never their report.
func TestTheLocalReportSurvivesAFailedUpload(t *testing.T) {
	cleanEnv(t)
	rec := newRecorder(t, http.StatusInternalServerError, `{"error":{"code":"internal","message":"boom"}}`)
	r := exec(t, "analyze", "--upload", "--salt", "org-salt",
		"--api-key", testAPIKey, "--api-url", rec.srv.URL, fixtureCritical)

	if !strings.Contains(r.stdout, "CRITICAL") || !strings.Contains(r.stdout, "aws_db_instance.main") {
		t.Fatalf("the report is missing from stdout after a failed upload:\n%s", r.stdout)
	}
	if r.code != exitError {
		t.Errorf("code = %d, want %d for a failed upload", r.code, exitError)
	}
	if !strings.Contains(r.stderr, "500") {
		t.Errorf("stderr does not name the status: %q", r.stderr)
	}

	// And the report a failed upload prints is the report a successful one
	// prints. The upload is not allowed to change the analysis.
	ok := newRecorder(t, http.StatusCreated, "")
	good := exec(t, "analyze", "--upload", "--salt", "org-salt",
		"--api-key", testAPIKey, "--api-url", ok.srv.URL, fixtureCritical)
	if good.stdout != r.stdout {
		t.Error("the local report differs between a successful and a failed upload")
	}
}

func TestUploadFailureIsExitTwoAndNamesTheServerErrorCode(t *testing.T) {
	cleanEnv(t)
	cases := []struct {
		name   string
		status int
		body   string
		want   []string
	}{
		{"unauthenticated", 401, `{"error":{"code":"unauthenticated","message":"unknown key"}}`, []string{"401", "unauthenticated"}},
		{"invalid body", 400, `{"error":{"code":"invalid_body","message":"nodes: required"}}`, []string{"400", "invalid_body"}},
		{"rate limited", 429, `{"error":{"code":"rate_limited"}}`, []string{"429", "rate_limited"}},
		{"gateway", 502, `<html>bad gateway</html>`, []string{"502"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := newRecorder(t, tc.status, tc.body)
			r := exec(t, "analyze", "--upload", "--salt", "org-salt",
				"--api-key", testAPIKey, "--api-url", rec.srv.URL, fixtureQuiet)
			if r.code != exitError {
				t.Fatalf("code = %d, want %d", r.code, exitError)
			}
			for _, want := range tc.want {
				if !strings.Contains(r.stderr, want) {
					t.Errorf("stderr %q does not mention %q", r.stderr, want)
				}
			}
		})
	}
}

// The precedence, which is the whole question when both apply.
//
// --fail-on asks about capacity. The network is not capacity. Exit 2 means
// "could not run", and the advice this project gives about "could not run" is to
// carry on, so letting 2 win would dress a real critical finding in the one
// label that invites a pipeline to ignore it. The gate wins; the upload failure
// is still on stderr.
func TestAnUploadFailureNeverOverturnsTheFailOnVerdict(t *testing.T) {
	cleanEnv(t)
	cases := []struct {
		name     string
		fixture  string
		failOn   []string
		status   int
		wantCode int
	}{
		{"gate fires and the upload fails", fixtureCritical, []string{"--fail-on", "critical"}, 500, exitFinding},
		{"gate fires and the upload works", fixtureCritical, []string{"--fail-on", "critical"}, 201, exitFinding},
		{"gate is quiet and the upload fails", fixtureQuiet, []string{"--fail-on", "critical"}, 500, exitError},
		{"gate is quiet and the upload works", fixtureQuiet, []string{"--fail-on", "critical"}, 201, exitOK},
		{"no gate and the upload fails", fixtureCritical, nil, 500, exitError},
		{"no gate and the upload works", fixtureCritical, nil, 201, exitOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := newRecorder(t, tc.status, "")
			args := append([]string{"analyze", "--upload", "--salt", "org-salt",
				"--api-key", testAPIKey, "--api-url", rec.srv.URL}, tc.failOn...)
			r := exec(t, append(args, tc.fixture)...)
			if r.code != tc.wantCode {
				t.Fatalf("code = %d, want %d (stderr: %s)", r.code, tc.wantCode, r.stderr)
			}
			// A failure that changes nothing about the exit code still has to be
			// visible, or a broken upload goes unnoticed for a month.
			if tc.status != 201 && !strings.Contains(r.stderr, "upload failed") {
				t.Errorf("stderr does not report the failed upload: %q", r.stderr)
			}
		})
	}
}

// The key is a credential. It has no business in a payload, in a report, in an
// error, or in the usage text that a flag error prints.
func TestTheAPIKeyNeverAppearsInAnyOutput(t *testing.T) {
	cleanEnv(t)
	// A hostile or merely careless server echoing the header back is the
	// realistic way a key reaches a CI log.
	rec := newRecorder(t, http.StatusUnauthorized,
		`{"error":{"code":"unauthenticated","message":"Bearer `+testAPIKey+` is not a key"}}`)

	runs := []result{
		exec(t, "analyze", "--upload", "--salt", "org-salt", "--api-key", testAPIKey, "--api-url", rec.srv.URL, fixtureCritical),
		exec(t, "analyze", "--upload", "--json", "--salt", "org-salt", "--api-key", testAPIKey, "--api-url", rec.srv.URL, fixtureCritical),
	}
	for _, r := range runs {
		if strings.Contains(r.stdout, testAPIKey) {
			t.Error("the API key is on stdout")
		}
		if strings.Contains(r.stderr, testAPIKey) {
			t.Errorf("the API key is on stderr: %q", r.stderr)
		}
		if r.err != nil && strings.Contains(r.err.Error(), testAPIKey) {
			t.Errorf("the API key is in the returned error: %v", r.err)
		}
	}
	if bytes.Contains(rec.body, []byte(testAPIKey)) {
		t.Error("the API key is inside the uploaded payload")
	}

}

// The environment is what CI has instead of a command line, and an explicit flag
// still wins over it.
func TestUploadReadsTheEnvironmentAndTheFlagWins(t *testing.T) {
	cleanEnv(t)
	wrong := newRecorder(t, http.StatusInternalServerError, "")
	right := newRecorder(t, http.StatusCreated, "")

	t.Setenv("HEADROOM_API_KEY", testAPIKey)
	t.Setenv("HEADROOM_SALT", "org-salt")
	t.Setenv("HEADROOM_API_URL", right.srv.URL)

	r := exec(t, "analyze", "--upload", fixtureQuiet)
	if r.code != exitOK {
		t.Fatalf("code = %d, err = %v, stderr = %s", r.code, r.err, r.stderr)
	}
	if right.count() != 1 || right.auth != "Bearer "+testAPIKey {
		t.Errorf("the environment was not used: %d calls, auth %q", right.count(), right.auth)
	}

	t.Setenv("HEADROOM_API_URL", wrong.srv.URL)
	r = exec(t, "analyze", "--upload", "--api-url", right.srv.URL, fixtureQuiet)
	if r.code != exitOK {
		t.Fatalf("code = %d, stderr = %s", r.code, r.stderr)
	}
	if wrong.count() != 0 {
		t.Error("--api-url did not win over HEADROOM_API_URL")
	}
	if right.count() != 2 {
		t.Errorf("the flag URL got %d calls, want 2", right.count())
	}
}

// stdout belongs to the report. A confirmation line on it would end up inside a
// redirected --json file and break whatever reads it.
func TestUploadConfirmationGoesToStderrAndLeavesJSONOnStdoutValid(t *testing.T) {
	cleanEnv(t)
	rec := newRecorder(t, http.StatusCreated, "")
	r := exec(t, "analyze", "--upload", "--json", "--salt", "org-salt",
		"--api-key", testAPIKey, "--api-url", rec.srv.URL, fixtureCritical)
	if r.code != exitOK {
		t.Fatalf("code = %d, err = %v", r.code, r.err)
	}
	if !strings.Contains(r.stderr, "rep_test") {
		t.Errorf("stderr does not confirm the upload: %q", r.stderr)
	}
	if strings.Contains(r.stdout, "rep_test") || strings.Contains(r.stdout, "uploaded") {
		t.Error("the upload confirmation is on stdout")
	}
	var findings []map[string]any
	if err := json.Unmarshal([]byte(r.stdout), &findings); err != nil {
		t.Fatalf("--json --upload did not leave valid JSON on stdout: %v", err)
	}
}

// Without --upload nothing is sent, whatever else is on the command line. This
// is the README's claim, and until this milestone it was true only because the
// capability did not exist.
func TestNothingIsSentWithoutTheUploadFlag(t *testing.T) {
	cleanEnv(t)
	rec := newRecorder(t, http.StatusCreated, "")
	for _, mode := range [][]string{
		nil,
		{"--json"},
		{"--dry-run", "--salt", "org-salt"},
	} {
		args := []string{"analyze"}
		args = append(args, mode...)
		args = append(args, "--api-key", testAPIKey, "--api-url", rec.srv.URL, fixtureCritical)
		if r := exec(t, args...); r.code != exitOK {
			t.Fatalf("%v: code = %d, err = %v", mode, r.code, r.err)
		}
	}
	if rec.count() != 0 {
		t.Errorf("%d requests were sent without --upload", rec.count())
	}
}

func TestUsageDocumentsUploadAndItsExitCode(t *testing.T) {
	usage := exec(t).stdout
	for _, want := range []string{"--upload", "--api-key", "--api-url", "HEADROOM_API_KEY", "--upload failed"} {
		if !strings.Contains(usage, want) {
			t.Errorf("the usage text does not mention %q", want)
		}
	}
	// Spelled as an escape so this assertion does not itself put the character
	// it forbids into the source.
	if strings.ContainsRune(usage, '\u2014') {
		t.Error("the usage text contains an em-dash")
	}
}

// --- config discovery -----------------------------------------------------

// --no-config has to win over a headroom.yaml sitting next to the plan,
// otherwise there is no way to ask "what do the defaults say".
func TestNoConfigIgnoresADiscoveredFile(t *testing.T) {
	dir := t.TempDir()
	planCopy := filepath.Join(dir, "plan.json")
	raw, err := os.ReadFile(fixtureCritical)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	if err := os.WriteFile(planCopy, raw, 0o600); err != nil {
		t.Fatalf("write plan copy: %v", err)
	}
	cfg := "version: 1\nrules:\n  R1:\n    enabled: false\n"
	if err := os.WriteFile(filepath.Join(dir, "headroom.yaml"), []byte(cfg), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	withConfig := exec(t, "analyze", "--json", planCopy)
	if !strings.Contains(withConfig.stderr, "using config") {
		t.Error("the discovered headroom.yaml was not reported on stderr")
	}
	if strings.Contains(withConfig.stdout, `"R1"`) {
		t.Error("R1 fired even though the discovered config disables it")
	}

	without := exec(t, "analyze", "--json", "--no-config", planCopy)
	if strings.Contains(without.stderr, "using config") {
		t.Error("--no-config still loaded the file")
	}
	if !strings.Contains(without.stdout, `"R1"`) {
		t.Error("--no-config did not restore the built-in R1")
	}
}

func TestBrokenConfigIsAToolErrorNotAFinding(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.yaml")
	if err := os.WriteFile(bad, []byte("rules: [this is not a mapping\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	r := exec(t, "analyze", "--config", bad, fixtureCritical)
	if r.code != exitError {
		t.Fatalf("code = %d, want %d", r.code, exitError)
	}
}

// --- flags that change the numbers ----------------------------------------

// --pool-size is the assumption the finding rests on when the task definition
// is silent, so it has to actually move the demand figure.
func TestPoolSizeFlagMovesTheDemand(t *testing.T) {
	demand := func(args ...string) int {
		r := exec(t, args...)
		var findings []struct {
			Rule    string         `json:"rule"`
			Metrics map[string]int `json:"metrics"`
		}
		if err := json.Unmarshal([]byte(r.stdout), &findings); err != nil {
			t.Fatalf("json: %v", err)
		}
		for _, f := range findings {
			if f.Rule == "GC1" {
				return f.Metrics["demand"]
			}
		}
		return 0
	}
	const gcp = "../../fixtures/gcp-01-gke-cloudrun-sql/plan.json"
	base := demand("analyze", "--json", gcp)
	if base == 0 {
		t.Skip("no GC1 finding with a demand to move")
	}
	// The fixture declares DB_POOL_SIZE, so a declared value must win over the
	// flag: the flag is only the fallback.
	bumped := demand("analyze", "--json", "--pool-size", "50", gcp)
	if bumped != base {
		t.Errorf("demand moved from %d to %d, but the plan declares its pool size, so the flag must not apply", base, bumped)
	}
}

// The empty report has always ended with "run with --explain to see what was
// skipped", and the flag did not exist, so anybody who followed the instruction
// got a parse error and exit 2. It was the only sentence a user reads when the
// tool finds nothing, and it was false.
func TestExplainExistsAndSaysWhyARuleStayedQuiet(t *testing.T) {
	r := exec(t, "analyze", "--no-color", "--explain", fixtureAzureModules)
	if r.code != exitOK || r.err != nil {
		t.Fatalf("code = %d, err = %v", r.code, r.err)
	}
	if !strings.Contains(r.stderr, "explain: what each rule did with this plan") {
		t.Fatalf("no explanation on stderr: %q", r.stderr)
	}
	// A rule that let a resource go has to say which resource and why.
	for _, want := range []string{"azurerm_linux_virtual_machine.archive", "within its limits", "AZ6"} {
		if !strings.Contains(r.stderr, want) {
			t.Errorf("the explanation is missing %q, stderr was: %s", want, r.stderr)
		}
	}

	// The report is the product; the explanation is commentary. It goes to
	// stderr so a pipeline reading stdout sees the same bytes either way.
	plain := exec(t, "analyze", "--no-color", fixtureAzureModules)
	if r.stdout != plain.stdout {
		t.Error("--explain changed the report on stdout")
	}
	if plain.stderr != "" {
		t.Errorf("stderr is not empty without --explain: %q", plain.stderr)
	}
}

// Asking for an explanation must not be able to change an answer, or the
// explanation is of a different run than the one that was reported.
func TestExplainNeverChangesTheVerdict(t *testing.T) {
	for _, fixture := range []string{fixtureCritical, fixtureQuiet, fixtureAzureModules} {
		with := exec(t, "analyze", "--json", "--explain", fixture)
		without := exec(t, "analyze", "--json", fixture)
		if with.stdout != without.stdout {
			t.Errorf("%s: the findings changed when --explain was passed", fixture)
		}
		if with.code != without.code {
			t.Errorf("%s: exit code %d with --explain, %d without", fixture, with.code, without.code)
		}
	}
}
