package report

import (
	"io"
	"os"
	"regexp"
	"strings"
)

// Colour in the terminal report.
//
// Three rules shape everything here.
//
// **Severity is information, so it gets colour. Nothing else does.** A report
// that paints every token is a report nobody can skim, and the whole value of
// this output is that a person finds the critical line in under a second.
//
// **Eight-colour ANSI only.** No 256-colour, no truecolour. This output ends up
// in CI logs, in a chat paste, in tmux over ssh, and in a Windows console, and
// the basic set is the one that survives all of them. It also means the user's
// own terminal theme decides the exact shade, which is the correct place for
// that decision to live.
//
// **Colour is never load bearing.** Every distinction made with colour is also
// made with a word: CRITICAL says CRITICAL. Piping through `sed` to strip
// escapes must never remove information, and it does not.

const (
	ansiReset = "\x1b[0m"

	styBold = "\x1b[1m"
	styDim  = "\x1b[2m"

	styRed    = "\x1b[31;1m" // critical
	styYellow = "\x1b[33;1m" // warning
	styCyan   = "\x1b[36;1m" // info
	styIdent  = "\x1b[36m"   // a terraform address, the same family as terraform's own
)

// palette wraps text in escapes, or does not. The `on` variant is the only
// place an escape sequence is written, so an off palette is provably plain.
type palette struct{ on bool }

func (p palette) s(style, text string) string {
	if !p.on || text == "" {
		return text
	}
	return style + text + ansiReset
}

func (p palette) severity(sev, text string) string {
	switch sev {
	case "critical":
		return p.s(styRed, text)
	case "warning":
		return p.s(styYellow, text)
	default:
		return p.s(styCyan, text)
	}
}

// One alternation, matched in one pass, and the order of the branches is the
// whole design.
//
// It has to be a single pass. An earlier version ran two: addresses, then
// measurements. The second pass then matched the digits inside the escape
// sequences the first had just written, turning \x1b[36m into \x1b[ plus a bold
// 36, and the terminal rendered the wreckage. Anything that rewrites text
// cannot afterwards be pointed at its own output.
//
//  1. a URL, consumed whole so its host is not mistaken for an address
//  2. a terraform address: a lowercase type, a dot, a name, maybe an index
//  3. a measurement
//
// Go's regexp is leftmost-first across an alternation, so an address swallows
// the "3" in db.t3.medium before the measurement branch is ever offered it.
// The \b in the measurement branch is the second guard on the same problem.
//
// Whitespace never appears inside any of the three, and wrap() only ever breaks
// at a space, so colouring per line after wrapping cannot split an escape
// across a newline.
var reToken = regexp.MustCompile(
	`https?://\S+` +
		`|[a-z][a-z0-9_]*\.[a-z0-9_][a-z0-9_.\-]*(?:\[[^\]\s]*\])?` +
		`|~?\b\d+(?:,\d{3})*(?:\.\d+)?%?`)

// tokens colours one already-wrapped line.
func (p palette) tokens(line string) string {
	if !p.on {
		return line
	}
	return reToken.ReplaceAllStringFunc(line, func(m string) string {
		switch {
		case strings.HasPrefix(m, "http"):
			return styDim + m + ansiReset
		case m[0] >= 'a' && m[0] <= 'z':
			return styIdent + m + ansiReset
		default:
			return styBold + m + ansiReset
		}
	})
}

// wantColour decides once, at the edge, in this order:
//
//  1. --no-color, because an explicit flag beats an inherited environment
//  2. NO_COLOR, the no-color.org convention: set to anything at all means off
//  3. FORCE_COLOR, for a pipeline that renders escapes itself
//  4. stdout is a character device
//
// Detection uses os.File.Stat rather than golang.org/x/term on purpose. This
// module has one dependency and the README makes an argument out of that, so
// pulling in a package to ask one question would cost more than it returns.
func wantColour(w io.Writer, noColorFlag bool) bool {
	if noColorFlag {
		return false
	}
	if _, set := os.LookupEnv("NO_COLOR"); set {
		return false
	}
	if v, set := os.LookupEnv("FORCE_COLOR"); set && v != "0" && !strings.EqualFold(v, "false") {
		return true
	}
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}
