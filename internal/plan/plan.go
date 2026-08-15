// Package plan parses the JSON produced by `terraform show -json <planfile>`.
//
// Only the subset needed for capacity analysis is decoded. There is no
// dependency on terraform-json on purpose: the CLI runs inside customer
// environments and every transitive dependency is supply-chain surface we
// would have to defend during a security review.
package plan

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

type File struct {
	FormatVersion    string        `json:"format_version"`
	TerraformVersion string        `json:"terraform_version"`
	PlannedValues    StateValues   `json:"planned_values"`
	Configuration    Configuration `json:"configuration"`

	// PriorState carries the resources that already exist, and with them the
	// only identifiers that mean the same thing in two different repositories:
	// the real AWS ids. A resource being created has an unknown id at plan
	// time; one that already exists does not.
	PriorState PriorState `json:"prior_state"`
}

type PriorState struct {
	Values StateValues `json:"values"`
}

type StateValues struct {
	RootModule Module `json:"root_module"`
}

type Module struct {
	Address      string     `json:"address"`
	Resources    []Resource `json:"resources"`
	ChildModules []Module   `json:"child_modules"`
}

// Resource comes from planned_values: addresses are already fully qualified
// and every value is resolved (variables, locals, module defaults, count).
type Resource struct {
	Address string         `json:"address"`
	Mode    string         `json:"mode"`
	Type    string         `json:"type"`
	Name    string         `json:"name"`
	Values  map[string]any `json:"values"`
}

type Configuration struct {
	RootModule ConfigModule `json:"root_module"`
}

type ConfigModule struct {
	Resources   []ConfigResource      `json:"resources"`
	ModuleCalls map[string]ModuleCall `json:"module_calls"`
}

type ModuleCall struct {
	Module ConfigModule `json:"module"`
}

// ConfigResource comes from configuration: values are unresolved, but this is
// the only place where references between resources survive. At plan time a
// not-yet-created security group has an unknown id, so planned_values cannot
// tell us who talks to whom. configuration can.
type ConfigResource struct {
	Address     string         `json:"address"`
	Mode        string         `json:"mode"`
	Type        string         `json:"type"`
	Name        string         `json:"name"`
	Expressions map[string]any `json:"expressions"`

	// A resource iterating over another one carries that dependency in its
	// for_each rather than in any attribute: `subnet_id = each.value.id` only
	// mentions "each". Losing these expressions silently disconnects the graph
	// for anyone who writes idiomatic terraform.
	CountExpression   map[string]any `json:"count_expression"`
	ForEachExpression map[string]any `json:"for_each_expression"`
}

func Load(path string) (*File, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	f, err := Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return f, nil
}

// Parse decodes a plan JSON that arrived from anywhere. It is separate from
// Load so the parser can be fuzzed without a file on disk, which matters
// because this is the one place the tool reads input it did not produce.
func Parse(raw []byte) (*File, error) {
	// PowerShell redirection writes a UTF-8 BOM, and `terraform show -json > plan.json`
	// is exactly how a Windows user produces this file.
	raw = bytes.TrimPrefix(raw, []byte{0xEF, 0xBB, 0xBF})

	var f File
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("not a terraform plan JSON: %w", err)
	}
	if f.FormatVersion == "" {
		return nil, fmt.Errorf("no format_version; generate it with: terraform show -json tfplan > plan.json")
	}
	return &f, nil
}

// Resources flattens planned_values across every child module.
func (f *File) Resources() []Resource {
	var out []Resource
	var walk func(m Module)
	walk = func(m Module) {
		for _, r := range m.Resources {
			if r.Mode == "managed" {
				out = append(out, r)
			}
		}
		for _, c := range m.ChildModules {
			walk(c)
		}
	}
	walk(f.PlannedValues.RootModule)
	return out
}

// PriorResources flattens prior_state, which is where real cloud identifiers
// live. Everything a plan is about to create has an unknown id; everything it
// already manages has a concrete one.
func (f *File) PriorResources() []Resource {
	var out []Resource
	var walk func(m Module)
	walk = func(m Module) {
		for _, r := range m.Resources {
			if r.Mode == "managed" {
				out = append(out, r)
			}
		}
		for _, c := range m.ChildModules {
			walk(c)
		}
	}
	walk(f.PriorState.Values.RootModule)
	return out
}

// DataSources returns the configuration of every data block. A
// terraform_remote_state block is a declared dependency on another repository's
// state, which is the strongest cross-repository edge that exists.
func (f *File) DataSources() []ConfigResource {
	var out []ConfigResource
	var walk func(m ConfigModule, prefix string)
	walk = func(m ConfigModule, prefix string) {
		for _, r := range m.Resources {
			if r.Mode != "data" {
				continue
			}
			r.Address = prefix + r.Address
			out = append(out, r)
		}
		for name, call := range m.ModuleCalls {
			walk(call.Module, prefix+"module."+name+".")
		}
	}
	walk(f.Configuration.RootModule, "")
	return out
}

// ByType returns every planned resource of the given terraform type.
func (f *File) ByType(t string) []Resource {
	var out []Resource
	for _, r := range f.Resources() {
		if r.Type == t {
			out = append(out, r)
		}
	}
	return out
}

// ConfigResources flattens configuration, rewriting the relative addresses used
// inside module calls into the fully qualified form planned_values uses.
func (f *File) ConfigResources() []ConfigResource {
	var out []ConfigResource
	var walk func(m ConfigModule, prefix string)
	walk = func(m ConfigModule, prefix string) {
		for _, r := range m.Resources {
			if r.Mode != "managed" {
				continue
			}
			r.Address = prefix + r.Address
			out = append(out, r)
		}
		for name, call := range m.ModuleCalls {
			walk(call.Module, prefix+"module."+name+".")
		}
	}
	walk(f.Configuration.RootModule, "")
	return out
}

// Base strips count/for_each indexes so that an address coming from
// planned_values ("aws_ecs_service.api[0]") matches the one coming from
// configuration ("aws_ecs_service.api").
func Base(address string) string {
	var b strings.Builder
	depth := 0
	for _, r := range address {
		switch r {
		case '[':
			depth++
		case ']':
			if depth > 0 {
				depth--
			}
		default:
			if depth == 0 {
				b.WriteRune(r)
			}
		}
	}
	return b.String()
}

// Str reads a string attribute, tolerating absent or null values.
func Str(values map[string]any, key string) string {
	if v, ok := values[key].(string); ok {
		return v
	}
	return ""
}

// Num reads a numeric attribute. Terraform emits every number as a JSON float.
func Num(values map[string]any, key string) (int, bool) {
	switch v := values[key].(type) {
	case float64:
		return int(v), true
	case int:
		return v, true
	}
	return 0, false
}
