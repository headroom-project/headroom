//go:build js && wasm

// Command headroom-wasm is the analyzer compiled for a browser.
//
// It exists so somebody can try the tool on their own plan without installing
// anything, and so the thing they try is the tool rather than a demonstration
// of it: same parser, same graph, same rules, same catalog, same renderer.
//
// What it is not is a service. There is no fetch here, no upload, no
// identifier and no storage. The plan is a string that arrives from a text box
// in the same tab, is decoded inside this module, and is gone when the page
// is. Nothing in this binary can open a socket, which is a property of the
// file and not a promise: the update check, the uploader and os/exec are all
// absent from the import graph, and the build tag above is the only way in.
//
// The whole file is a binding. Every decision lives in internal/webrun, which
// is ordinary Go and is covered by the suite that runs on this machine,
// because a package behind a js/wasm build tag cannot be.
package main

import (
	"encoding/json"
	"syscall/js"

	"github.com/headroom-project/headroom/internal/webrun"
)

// version is stamped at build time with -ldflags "-X main.version=...", the
// same way the binary is. The page prints it beside the report, so a finding
// somebody screenshots can be traced to a release.
var version = "dev"

func main() {
	js.Global().Set("headroomEngine", js.ValueOf(map[string]any{
		"version":      version,
		"maxPlanBytes": webrun.MaxPlanBytes,
		"analyze":      js.FuncOf(analyze),
	}))

	// A wasm module whose main returns is a wasm module whose exports stop
	// working. Blocking forever is how the Go runtime stays alive for the
	// callbacks above.
	select {}
}

// analyze takes (planText, optionsObject) and returns a JSON string.
//
// A string rather than a JavaScript object on purpose. Building a nested
// js.Value tree costs one crossing of the boundary per field, and the report
// alone is tens of kilobytes; one crossing with one string is faster and it
// means the page parses the result with JSON.parse, which is the same decoder
// it would use against the API. It also keeps this function's contract small
// enough to read in one sitting.
//
// Every failure comes back inside that document. Throwing into JavaScript from
// here would make the page's error path depend on the shape of a Go panic.
func analyze(this js.Value, args []js.Value) any {
	if len(args) == 0 || args[0].Type() != js.TypeString {
		return encode(webrun.Result{
			Version: version,
			Error:   "analyze expects the plan document as a string",
		})
	}

	opt := webrun.DefaultOptions()
	if len(args) > 1 && args[1].Type() == js.TypeObject {
		o := args[1]
		if v := o.Get("salt"); v.Type() == js.TypeString {
			opt.Salt = v.String()
		}
		if v := o.Get("planPath"); v.Type() == js.TypeString {
			opt.PlanPath = v.String()
		}
		if v := o.Get("colour"); v.Type() == js.TypeBoolean {
			opt.Colour = v.Bool()
		}
		if v := o.Get("poolSize"); v.Type() == js.TypeNumber {
			if n := v.Int(); n > 0 {
				opt.DefaultPoolSize = n
			}
		}
		if v := o.Get("warnAt"); v.Type() == js.TypeNumber {
			if f := v.Float(); f > 0 && f <= 1 {
				opt.WarnAt = f
			}
		}
	}

	return encode(webrun.Analyze([]byte(args[0].String()), version, opt))
}

// encode renders the result, and has an answer for the one case where that
// cannot work. json.Marshal over this struct fails for no input we can
// construct, but returning "" on the error branch would give the page a parse
// error instead of a sentence.
func encode(res webrun.Result) string {
	raw, err := json.Marshal(res)
	if err != nil {
		return `{"ok":false,"error":"the result could not be encoded"}`
	}
	return string(raw)
}
