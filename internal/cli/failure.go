package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// Failure is the error a function failed with, as a person reads it: its type, its message, and
// its stack trace a line at a time.
type Failure struct {
	Type    string
	Message string
	// Trace is the stack trace, its common indentation taken off, without the first line when it
	// only says the type and message again, as node's does.
	Trace []TraceLine
}

// TraceLine is a line of a stack trace. Internal is set on the lines of a frame of the runtime
// or of a dependency, which a person reads past to the frames of their own code.
type TraceLine struct {
	Text     string
	Internal bool
}

// Head is the type and message of the error, the type left out when there is none.
func (f Failure) Head() string {
	if f.Type == "" {
		return f.Message
	}
	return f.Type + ": " + f.Message
}

// TimedOut reports whether lambda, or invoke --local, ended the function at its timeout.
func (f Failure) TimedOut() bool {
	return f.Type == "Sandbox.Timedout" || strings.Contains(f.Message, "Task timed out after")
}

// failure is the error a function returned, as its runtime writes it: its type, its message and
// its stack trace, as lines in node and python and as frames in go.
type failure struct {
	Type    string            `json:"errorType"`
	Message string            `json:"errorMessage"`
	Trace   []json.RawMessage `json:"stackTrace"`
}

// ReadFailure reads the error a function returned from what it returned, as node, python and go
// write one. It is false for a payload without an errorMessage.
func ReadFailure(payload []byte) (Failure, bool) {
	var f failure
	if json.Unmarshal(payload, &f) != nil || f.Message == "" {
		return Failure{}, false
	}
	return newFailure(f.Type, f.Message, f.Trace), true
}

// newFailure splits the frames of a trace into its lines, marks the frames that are not the
// function's own, leaves out a first line that says the head again, and dedents the rest.
func newFailure(typ, message string, frames []json.RawMessage) Failure {
	f := Failure{Type: typ, Message: message}
	for _, frame := range frames {
		text, internal := traceLine(frame)
		for line := range strings.SplitSeq(strings.TrimRight(text, "\n"), "\n") {
			if strings.TrimSpace(line) != "" {
				f.Trace = append(f.Trace, TraceLine{Text: strings.TrimRight(line, " \t\r"), Internal: internal})
			}
		}
	}
	if len(f.Trace) > 0 && strings.TrimSpace(f.Trace[0].Text) == f.Head() {
		f.Trace = f.Trace[1:]
	}
	common := -1
	for _, line := range f.Trace {
		n := len(line.Text) - len(strings.TrimLeft(line.Text, " \t"))
		if common < 0 || n < common {
			common = n
		}
	}
	for i := range f.Trace {
		f.Trace[i].Text = f.Trace[i].Text[common:]
	}
	return f
}

// traceLine is one frame of a stack trace, and whether it is the runtime's or a dependency's: a
// line of node or python as it is, and a frame of go as its function and where it is.
func traceLine(frame json.RawMessage) (string, bool) {
	var line string
	if json.Unmarshal(frame, &line) == nil {
		return line, internalLine(line)
	}
	var g struct {
		Path  string `json:"path"`
		Line  int    `json:"line"`
		Label string `json:"label"`
	}
	if json.Unmarshal(frame, &g) == nil && g.Path != "" {
		return fmt.Sprintf("%s (%s:%d)", g.Label, g.Path, g.Line), internalGoFrame(g.Path, g.Label)
	}
	return string(frame), false
}

// internalLines are what a line of a node or python trace holds when the frame is the runtime's
// or a dependency's: node_modules and node's own modules, lambda's runtime in /var/runtime, and
// python's library, its site-packages and its frozen modules.
var internalLines = []string{"node_modules/", "(node:", "at node:", "(internal/", "/var/runtime/", "/var/lang/", "site-packages/", "<frozen "}

// internalLine reports whether a line of a node or python trace is the runtime's or a
// dependency's.
func internalLine(line string) bool {
	for _, s := range internalLines {
		if strings.Contains(line, s) {
			return true
		}
	}
	return false
}

// internalGoFrame reports whether a frame of go is the runtime's or a dependency's: a module with
// a version, as -trimpath writes one from the module cache, the module cache itself, and the
// runtime and reflect of the standard library.
func internalGoFrame(path, label string) bool {
	switch {
	case strings.Contains(path, "@v"), strings.Contains(path, "/pkg/mod/"), strings.Contains(path, "/go/src/"):
		return true
	case strings.HasPrefix(path, "runtime/"), strings.HasPrefix(path, "reflect/"):
		return true
	}
	return strings.HasPrefix(label, "runtime.") || strings.HasPrefix(label, "reflect.")
}

// functionError says what a function that failed in aws returned: the type and message of its
// error, as invoke --local says them, with its stack trace indented under them; or what lambda
// calls the failure, such as Unhandled, with what the function returned when it was not empty.
func functionError(payload []byte, kind string) string {
	f, ok := ReadFailure(payload)
	if !ok {
		var b bytes.Buffer
		if len(bytes.TrimSpace(payload)) == 0 || !prettyJSON(&b, payload, palette{}) {
			return kind
		}
		return kind + ", returning\n" + indentLines(strings.TrimRight(b.String(), "\n"))
	}
	return f.String()
}

// describeFailure says what a function that failed on this machine failed with, as functionError
// says one that failed in aws.
func describeFailure(typ, message string, trace []json.RawMessage) string {
	return newFailure(typ, message, trace).String()
}

// String is the head of a failure with its stack trace under it, indented, as the cli writes it.
func (f Failure) String() string {
	if len(f.Trace) == 0 {
		return f.Head()
	}
	lines := make([]string, len(f.Trace))
	for i, line := range f.Trace {
		lines[i] = line.Text
	}
	return f.Head() + "\n" + indentLines(strings.Join(lines, "\n"))
}

// indentLines indents every line of s by four spaces, under the line of an error.
func indentLines(s string) string {
	return "    " + strings.ReplaceAll(s, "\n", "\n    ")
}
