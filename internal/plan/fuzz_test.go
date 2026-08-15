package plan_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/headroom-project/headroom/internal/plan"
)

// A terraform plan JSON is the one input this tool does not produce itself, and
// it arrives from a customer's repository or from a CI job. Parse has to survive
// anything: a truncated file, a BOM in the middle, deeply nested modules, a type
// where an object was expected. Every accessor below is exercised too, because a
// parse that succeeds and then panics on the first read is the same outage.
func FuzzParse(f *testing.F) {
	// The corpus is the real fixtures, so the fuzzer starts from valid input and
	// mutates outward instead of spending its budget rediscovering JSON.
	matches, err := filepath.Glob("../../fixtures/*/plan.json")
	if err != nil {
		f.Fatalf("glob fixtures: %v", err)
	}
	if len(matches) == 0 {
		f.Fatal("no fixtures found to seed the corpus")
	}
	for _, path := range matches {
		raw, err := os.ReadFile(path)
		if err != nil {
			f.Fatalf("read %s: %v", path, err)
		}
		f.Add(raw)
	}

	// Shapes that are valid JSON and invalid plans, which is the interesting
	// half: malformed JSON is rejected by the decoder before any of this code
	// runs.
	f.Add([]byte(`{}`))
	f.Add([]byte(`{"format_version":"1.0"}`))
	f.Add([]byte(`{"format_version":"1.0","planned_values":null}`))
	f.Add([]byte(`{"format_version":"1.0","planned_values":{"root_module":{"child_modules":[{"child_modules":[{}]}]}}}`))
	f.Add([]byte(`{"format_version":"1.0","planned_values":{"root_module":{"resources":[{"address":"","type":"","values":null}]}}}`))
	f.Add([]byte(`{"format_version":"1.0","configuration":{"root_module":{"resources":[{"address":"a.b","expressions":{"x":[1,2]}}]}}}`))
	f.Add([]byte("\xEF\xBB\xBF{\"format_version\":\"1.0\"}"))

	f.Fuzz(func(t *testing.T, raw []byte) {
		file, err := plan.Parse(raw)
		if err != nil {
			if file != nil {
				t.Fatal("Parse returned both a file and an error")
			}
			return
		}
		if file == nil {
			t.Fatal("Parse returned neither a file nor an error")
		}

		// Everything a rule would reach for, on input nobody vetted.
		for _, r := range file.Resources() {
			plan.Base(r.Address)
			plan.Str(r.Values, "instance_class")
			plan.Num(r.Values, "allocated_storage")
		}
		for _, r := range file.PriorResources() {
			plan.Base(r.Address)
		}
		for _, c := range file.ConfigResources() {
			plan.Base(c.Address)
		}
		file.DataSources()
		file.ByType("aws_db_instance")
	})
}

// Base is pure string handling over an address the tool did not write, so it
// gets its own target: it is called on every resource of every plan, and its
// output is what the redaction path hashes.
func FuzzBase(f *testing.F) {
	for _, seed := range []string{
		"aws_db_instance.main",
		`module.net.aws_subnet.private["a"]`,
		"aws_subnet.private[0]",
		"", ".", "[", `["`, "a.b.c.d.e", "module.",
		"aws_subnet.private[[0]]", "]", "]][[",
		// Found by this target on its first run in CI. Base ranges over runes,
		// so an invalid UTF-8 byte decodes to U+FFFD and comes back three bytes
		// long: the output can be longer than the input in bytes, which is why
		// the invariants below count runes and brackets instead.
		"\xa6",
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, address string) {
		got := plan.Base(address)

		// Brackets are the entire job. If one survives, an address from
		// planned_values will not match the same address from configuration and
		// the edge is silently lost.
		if strings.ContainsAny(got, "[]") {
			t.Fatalf("Base(%q) = %q, which still carries an index", address, got)
		}

		// Runes only ever come out or stay, never appear. Bytes can grow, on
		// invalid UTF-8, and that is fine: replacement is deterministic, so the
		// same address still hashes to the same id.
		if in, out := utf8.RuneCountInString(address), utf8.RuneCountInString(got); out > in {
			t.Fatalf("Base(%q) = %q: %d runes out of %d in", address, got, out, in)
		}

		// The graph joins on this value from two directions, so it has to be a
		// fixed point. If a second pass moved it, which side of the join is
		// right would depend on how many times each caller happened to apply it.
		if again := plan.Base(got); again != got {
			t.Fatalf("Base is not idempotent: Base(%q) = %q, then %q", address, got, again)
		}
	})
}
