package migrate

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// writer writes pkl source, a line at a time, indented by how many blocks are open.
type writer struct {
	b     strings.Builder
	depth int
}

func (w *writer) line(format string, args ...any) {
	w.b.WriteString(strings.Repeat("  ", w.depth))
	fmt.Fprintf(&w.b, format, args...)
	w.b.WriteString("\n")
}

// open starts a block, such as functions {, and close ends it.
func (w *writer) open(head string) {
	w.line("%s {", head)
	w.depth++
}

func (w *writer) close() {
	w.depth--
	w.line("}")
}

// amend writes expr, a call of a kdst.pkl helper, after lead, such as ["orders"] = , and amended
// with what body writes: (kdst.get("/x")) { ... }, or expr alone when body writes nothing.
func (w *writer) amend(lead, expr string, body func(w *writer)) {
	inner := writer{depth: w.depth + 1}
	body(&inner)
	if inner.b.Len() == 0 {
		w.line("%s%s", lead, expr)
		return
	}
	w.line("%s(%s) {", lead, expr)
	w.b.WriteString(inner.String())
	w.line("}")
}

func (w *writer) blank() {
	w.b.WriteString("\n")
}

// comment writes a comment line, a line of its own above what it is about.
func (w *writer) comment(text string) {
	for l := range strings.SplitSeq(text, "\n") {
		w.line("// %s", l)
	}
}

func (w *writer) String() string {
	return w.b.String()
}

var identifier = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// keywords are pkl's reserved words, which cannot name a property.
var keywords = []string{
	"abstract", "amends", "as", "case", "class", "const", "delete", "else", "extends", "external", "false",
	"fixed", "for", "function", "hidden", "if", "import", "in", "is", "let", "local", "module", "new",
	"nothing", "null", "open", "out", "outer", "override", "protected", "read", "record", "super",
	"switch", "this", "throw", "trace", "true", "typealias", "unknown", "vararg", "when",
}

// member is how a key is written in a dynamic object: as a property when it is a name pkl
// allows, else as an entry, ["aws:SourceArn"]. Both render as the same json.
func member(key string) string {
	if identifier.MatchString(key) && !slices.Contains(keywords, key) {
		return key
	}
	return "[" + quote(key) + "]"
}

// quote writes s as a pkl string.
func quote(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`, "\t", `\t`, "\r", `\r`)
	return `"` + r.Replace(s) + `"`
}
