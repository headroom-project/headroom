// Command headroom reports what a terraform plan will hit before it breaks.
//
//	terraform plan -out=tfplan
//	terraform show -json tfplan > plan.json
//	headroom analyze plan.json
package main

import (
	"encoding/json"
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
	salt := fs.String("salt", os.Getenv("HEADROOM_SALT"), "per-organization salt for hashing resource addresses (env: HEADROOM_SALT)")
	failOn := fs.String("fail-on", "", "exit non-zero when a finding of this severity or worse exists (critical|warning)")
	configPath := fs.String("config", os.Getenv("HEADROOM_CONFIG"), "path to headroom.yaml (default: discovered next to the plan, then in the working directory)")
	noConfig := fs.Bool("no-config", false, "ignore any headroom.yaml and run the built-in rules at their defaults")
	if err := fs.Parse(args[1:]); err != nil {
		return exitError, err
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

	switch {
	case *dryRun:
		payload := extract.NewRedactor(*salt).Build(f, g, findings, version)
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(payload); err != nil {
			return exitError, err
		}
		if !payload.Salted {
			fmt.Fprintln(stderr, "\nwarning: no salt set, so resource ids are a plain hash of a low-entropy address. Set HEADROOM_SALT before uploading anything.")
		}
	case *asJSON:
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(findings); err != nil {
			return exitError, err
		}
	default:
		report.Text(stdout, findings, planPath)
	}

	if *failOn != "" && exceeds(findings, *failOn) {
		return exitFinding, nil
	}
	return exitOK, nil
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
  --pool-size N   connections per task to assume when not declared (default 10)
  --warn-at R     utilization ratio that triggers a warning (default 0.8)
  --salt S        per-organization salt for hashing addresses (env HEADROOM_SALT)
  --fail-on SEV   exit 1 on critical, or on warning and worse
  --config F      path to headroom.yaml (default: found next to the plan, then
                  in the working directory; env HEADROOM_CONFIG)
  --no-config     ignore any headroom.yaml and run the built-in defaults

exit codes:
  0  ran, and nothing matched --fail-on
  1  ran, and a finding matched --fail-on
  2  could not run

The plan file never leaves this machine. Only an allowlisted set of capacity
attributes is ever read; run --dry-run to see exactly what that is.
`)
}
