// Package tty answers one question: is a person looking at this stream.
//
// Two features ask it, colour and the update check, and they have to get the
// same answer. Two copies of this would drift, and the way they would drift is
// that somebody fixes the detection in one place and a pipeline starts getting
// a notice it should never have seen.
package tty

import (
	"io"
	"os"
)

// Is reports whether w is a character device.
//
// The check is os.File.Stat rather than golang.org/x/term on purpose. This
// module has one dependency and the README makes an argument out of that, so
// pulling in a package to ask one question would cost more than it returns.
//
// Anything that is not an *os.File is not a terminal. That is the right answer
// for a bytes.Buffer in a test, for an io.MultiWriter in a wrapper, and for a
// pipe, and it means a caller cannot accidentally opt into terminal behaviour
// by wrapping its output.
func Is(w io.Writer) bool {
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
