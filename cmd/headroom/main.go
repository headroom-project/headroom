// Command headroom reports what a terraform plan will hit before it breaks.
//
//	terraform plan -out=tfplan
//	terraform show -json tfplan > plan.json
//	headroom analyze plan.json
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/headroom-project/headroom/internal/catalog"
	"github.com/headroom-project/headroom/internal/config"
	"github.com/headroom-project/headroom/internal/extract"
	"github.com/headroom-project/headroom/internal/graph"
	"github.com/headroom-project/headroom/internal/plan"
	"github.com/headroom-project/headroom/internal/report"
	"github.com/headroom-project/headroom/internal/rules"
	"github.com/headroom-project/headroom/internal/upload"
)

// version is stamped at release time with -ldflags "-X main.version=...". A
// build from source says "dev", which is honest: it is not a release and it has
// no checksum anybody published.
var version = "dev"

// Exit codes are part of the contract, because this runs in CI and a pipeline
// has to tell "the tool broke" apart from "the tool found something".
const (
	exitOK      = 0
	exitFinding = 1 // --fail-on matched; the analysis itself succeeded
	exitError   = 2 // the tool could not run
)

func main() {
	code, err := run(os.Args[1:], os.Stdout, os.Stderr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "headroom: %v\n", err)
	}
	os.Exit(code)
}

// run holds the whole command so it can be exercised in a test. Nothing here
// calls os.Exit or writes to os.Stdout directly: the exit code comes back as a
// value and both streams are injected.
func run(args []string, stdout, stderr io.Writer) (int, error) {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		usage(stdout)
		return exitOK, nil
	}
	if args[0] == "version" || args[0] == "--version" {
		fmt.Fprintln(stdout, "headroom "+version)
		return exitOK, nil
	}
	if args[0] != "analyze" {
		return exitError, fmt.Errorf("unknown command %q (try: headroom analyze plan.json)", args[0])
	}

	fs := flag.NewFlagSet("analyze", flag.ContinueOnError)
	fs.SetOutput(stderr)
	asJSON := fs.Bool("json", false, "emit findings as JSON instead of text")
	dryRun := fs.Bool("dry-run", false, "print the exact redacted payload that would be uploaded, and upload nothing")
	poolSize := fs.Int("pool-size", 10, "connections per task to assume when the task definition does not declare one")
	warnAt := fs.Float64("warn-at", 0.8, "utilization ratio above which a headroom warning fires")
	// The environment is read after Parse, never as a flag default. A default is
	// printed verbatim by PrintDefaults, and PrintDefaults runs on any flag
	// error, so a default taken from the environment would write HEADROOM_SALT
	// and HEADROOM_API_KEY to stderr the first time somebody mistyped a flag.
	salt := fs.String("salt", "", "per-organization salt for hashing resource addresses (env: HEADROOM_SALT)")
	failOn := fs.String("fail-on", "", "exit non-zero when a finding of this severity or worse exists (critical|warning)")
	configPath := fs.String("config", os.Getenv("HEADROOM_CONFIG"), "path to headroom.yaml (default: discovered next to the plan, then in the working directory)")
	noConfig := fs.Bool("no-config", false, "ignore any headroom.yaml and run the built-in rules at their defaults")
	noColor := fs.Bool("no-color", false, "never colour the report (also honoured: NO_COLOR)")
	doUpload := fs.Bool("upload", false, "send the redacted payload to the headroom API after printing the local report")
	apiKey := fs.String("api-key", "", "API key for --upload, format hr_live_... (env: HEADROOM_API_KEY)")
	apiURL := fs.String("api-url", upload.DefaultBaseURL, "base URL of the headroom API (env: HEADROOM_API_URL)")
	if err := fs.Parse(args[1:]); err != nil {
		return exitError, err
	}
	*salt = orEnv(*salt, "HEADROOM_SALT")
	*apiKey = orEnv(*apiKey, "HEADROOM_API_KEY")
	if !flagGiven(fs, "api-url") {
		if v := os.Getenv("HEADROOM_API_URL"); v != "" {
			*apiURL = v
		}
	}
	if fs.NArg() != 1 {
		return exitError, fmt.Errorf("expected exactly one plan JSON file (generate it with: terraform show -json tfplan > plan.json)")
	}
	// An unrecognised severity used to fall through and behave like "critical",
	// so a pipeline asking to fail on a typo silently failed on less than it
	// asked for. A gate that quietly weakens itself is worse than no gate.
	switch *failOn {
	case "", rules.SeverityCritical, rules.SeverityWarning:
	default:
		return exitError, fmt.Errorf("--fail-on %q is not a severity (use %q or %q)",
			*failOn, rules.SeverityCritical, rules.SeverityWarning)
	}

	// Everything --upload needs is knowable before a plan is read, so it is
	// checked before a plan is read. A refusal that arrives after the analysis
	// has already been printed reads like a failure of the analysis, and this
	// class of problem is a mistyped invocation, not a broken run.
	var client *upload.Client
	if *doUpload {
		var err error
		if client, err = uploadClient(*dryRun, *salt, *apiKey, *apiURL); err != nil {
			return exitError, err
		}
	}

	planPath := fs.Arg(0)

	f, err := plan.Load(planPath)
	if err != nil {
		return exitError, err
	}
	cat, err := catalog.Load()
	if err != nil {
		return exitError, err
	}

	opt := rules.DefaultOptions()
	opt.DefaultPoolSize = *poolSize
	opt.WarnAt = *warnAt

	if !*noConfig {
		path := *configPath
		if path == "" {
			path = config.Discover(planPath)
		}
		if path != "" {
			cfg, err := config.Load(path)
			if err != nil {
				return exitError, err
			}
			opt.Config = cfg
			applyDefaults(cfg, &opt, fs)
			for class, gib := range cfg.InstanceClassMemory() {
				cat.AddInstanceClass(class, gib)
			}
			fmt.Fprintf(stderr, "using config %s\n", path)
		}
	}

	g := graph.Build(f)
	findings := rules.Run(f, g, cat, opt)

	// One payload, built once and encoded once, for both the printing and the
	// sending of it. See payloadJSON.
	var payload extract.Payload
	var body []byte
	if *dryRun || client != nil {
		payload = extract.NewRedactor(*salt).Build(f, g, findings, version)
		if body, err = payloadJSON(payload); err != nil {
			return exitError, err
		}
	}

	switch {
	case *dryRun:
		if _, err := stdout.Write(body); err != nil {
			return exitError, err
		}
		if !payload.Salted {
			fmt.Fprintln(stderr, "\nwarning: no salt set, so resource ids are a plain hash of a low-entropy address. Set HEADROOM_SALT before uploading anything.")
		}
	case *asJSON:
		// A run that finds nothing emits an empty list, never null. This is the
		// most common invocation there is, the green pipeline, and `jq length`
		// over null is an error rather than 0, so the quiet case was the one
		// that broke. extract.Build already initialises its slices for exactly
		// this reason; this path did not.
		if findings == nil {
			findings = []rules.Finding{}
		}
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(findings); err != nil {
			return exitError, err
		}
	default:
		report.Text(stdout, findings, planPath, *noColor)
	}

	// The gate is decided here, on the findings alone, and nothing that happens
	// on a socket afterwards is allowed to move it.
	gate := *failOn != "" && exceeds(findings, *failOn)

	// The report is already on stdout. Whatever the network does next, the user
	// keeps their analysis.
	uploadFailed := false
	if client != nil {
		res, err := client.Post(context.Background(), body)
		if err != nil {
			uploadFailed = true
			fmt.Fprintf(stderr, "headroom: upload failed: %v\n", err)
		} else {
			fmt.Fprintf(stderr, "uploaded: report %s, %d findings\n", res.ID, res.FindingsCount)
		}
	}

	// Precedence, stated once and tested: the --fail-on verdict outranks an
	// upload failure.
	//
	// Both cannot be reported, because a process has one exit code. Exit 2 means
	// "could not run", and the sane thing to do with "could not run" is to
	// fail open and carry on, which is what this project tells people to do.
	// So letting 2 win here would take a critical capacity finding that the user
	// explicitly asked to gate on and hand it to CI wearing the one label that
	// invites CI to ignore it. A network failure must never be able to launder a
	// finding into a shrug. It is reported on stderr, loudly, and the exit code
	// keeps saying what the capacity analysis found.
	if gate {
		if uploadFailed {
			fmt.Fprintln(stderr, "headroom: exiting 1 for the finding that matched --fail-on, not 2 for the upload; an upload failure does not overturn the gate")
		}
		return exitFinding, nil
	}
	if uploadFailed {
		return exitError, nil
	}
	return exitOK, nil
}

// payloadJSON renders the redacted payload.
//
// This is the only encoder for it. --dry-run writes what it returns to stdout
// and --upload sends what it returns as the request body, so the two cannot
// disagree by construction rather than by discipline. If this ever grows a
// second call path, the claim that a customer can audit an upload by running
// --dry-run stops being true.
func payloadJSON(p extract.Payload) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	if err := enc.Encode(p); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// uploadClient checks everything about an upload that can be checked without a
// plan, and refuses rather than guessing.
func uploadClient(dryRun bool, salt, apiKey, apiURL string) (*upload.Client, error) {
	// Silent precedence between two flags that mean opposite things is how a
	// pipeline ends up uploading when its author believed it was rehearsing.
	if dryRun {
		return nil, errors.New("--dry-run and --upload contradict each other (--dry-run sends nothing; drop one)")
	}
	// No salt, no upload, and no flag to say otherwise.
	//
	// A terraform address is low entropy: "aws_db_instance.main" is guessable in
	// a dictionary of a few thousand, so an unsalted hash is not redaction, it is
	// obfuscation that survives one afternoon. Worse for the product, the salt is
	// per organization by design, so unsalted ids from two customers collide, and
	// the cross-repo view is built on exactly that identity. An override flag
	// would be pasted into a CI config once, by somebody in a hurry, and would
	// then quietly be the setting forever. The escape hatch already exists, costs
	// nothing and leaves the guarantee intact: set a salt.
	if salt == "" {
		return nil, errors.New("--upload refuses to send an unsalted payload: without a salt the resource ids are a plain sha256 of a low-entropy terraform address, which is reversible by guessing, and ids from different organizations collide. Set HEADROOM_SALT (or --salt) to a per-organization secret and try again")
	}
	if apiKey == "" {
		return nil, errors.New("--upload needs an API key (set HEADROOM_API_KEY or pass --api-key)")
	}
	client, err := upload.NewClient(apiURL, apiKey, upload.DefaultTimeout)
	if err != nil {
		return nil, err
	}
	return client, nil
}

// orEnv prefers what was typed on the command line and falls back to the
// environment, which is what CI has instead of a command line.
func orEnv(v, key string) string {
	if v != "" {
		return v
	}
	return os.Getenv(key)
}

// flagGiven reports whether a flag was actually typed, as opposed to sitting at
// its default. It is what lets an explicit --api-url beat HEADROOM_API_URL.
func flagGiven(fs *flag.FlagSet, name string) bool {
	found := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == name {
			found = true
		}
	})
	return found
}

// applyDefaults lets the config set thresholds, while an explicitly typed flag
// still wins. Someone debugging on the command line should not have to find and
// edit a file to try a number.
func applyDefaults(cfg *config.Config, opt *rules.Options, fs *flag.FlagSet) {
	given := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { given[f.Name] = true })

	if cfg.Defaults.PoolSize != nil && !given["pool-size"] {
		opt.DefaultPoolSize = *cfg.Defaults.PoolSize
	}
	if cfg.Defaults.WarnAt != nil && !given["warn-at"] {
		opt.WarnAt = *cfg.Defaults.WarnAt
	}
	if cfg.Defaults.ScaleRatioWarn != nil {
		opt.ScaleRatioWarn = *cfg.Defaults.ScaleRatioWarn
	}
}

func exceeds(findings []rules.Finding, threshold string) bool {
	for _, f := range findings {
		if f.Severity == rules.SeverityCritical {
			return true
		}
		if threshold == rules.SeverityWarning && f.Severity == rules.SeverityWarning {
			return true
		}
	}
	return false
}

func usage(w io.Writer) {
	fmt.Fprint(w, `headroom `+version+` - what this terraform plan hits before it breaks

usage:
  terraform plan -out=tfplan
  terraform show -json tfplan > plan.json
  headroom analyze plan.json

flags:
  --json          emit findings as JSON
  --dry-run       print the exact redacted payload that would be uploaded
  --upload        print the local report, then send that same payload to the
                  headroom API. Refuses to run with --dry-run, and refuses to
                  send anything without a salt
  --api-key K     API key for --upload, hr_live_... (env HEADROOM_API_KEY)
  --api-url U     base URL of the API (env HEADROOM_API_URL, default
                  `+upload.DefaultBaseURL+`)
  --pool-size N   connections per task to assume when not declared (default 10)
  --warn-at R     utilization ratio that triggers a warning (default 0.8)
  --salt S        per-organization salt for hashing addresses (env HEADROOM_SALT)
  --fail-on SEV   exit 1 on critical, or on warning and worse
  --config F      path to headroom.yaml (default: found next to the plan, then
                  in the working directory; env HEADROOM_CONFIG)
  --no-config     ignore any headroom.yaml and run the built-in defaults
  --no-color      never colour the report. NO_COLOR in the environment does
                  the same, and colour is off automatically when stdout is
                  not a terminal

exit codes:
  0  ran, and nothing matched --fail-on
  1  ran, and a finding matched --fail-on
  2  could not run, or --upload failed to reach the API

When both apply, 1 wins: the local analysis is what --fail-on asked about, and
an upload failure never overturns its verdict. The failure is still reported on
stderr. Uploading is one attempt with a 30s timeout and no retry.

The plan file never leaves this machine. Only an allowlisted set of capacity
attributes is ever read; run --dry-run to see exactly what that is, and
--upload sends those same bytes and no others.
`)
}
