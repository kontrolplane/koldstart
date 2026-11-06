package tui

import (
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"

	"github.com/kontrolplane/koldstart/internal/tui/styles"
)

// tint splits a line of the logs into spans so the structure of json and logfmt recedes behind
// the values: keys and punctuation are faint, values keep the body ink, and the message, what a
// line is read for, the text ink. Lambda's own lines, START, END and REPORT, are faint, and a
// line a node function wrote, with its time, request and level in front, keeps them faint and
// its level in a tone. Anything else reads as it is, with a leading timestamp faint.
func tint(line string) cell {
	if platformLine(line) {
		tone := styles.ToneFaint
		if strings.Contains(line, "Status: timeout") || strings.Contains(line, "Status: error") {
			tone = styles.ToneDanger
		}
		return cell{{Text: line, Tone: tone}}
	}
	if strings.Contains(line, "Task timed out after") {
		return cell{{Text: line, Tone: styles.ToneDanger}}
	}
	if c, ok := tintNode(line); ok {
		return c
	}
	if t := strings.TrimLeft(line, " "); strings.HasPrefix(t, "{") {
		return tintJSON(line)
	}
	if c, ok := tintLogfmt(line); ok {
		return c
	}
	if n := leadingTimestamp(line); n > 0 {
		return cell{{Text: line[:n], Tone: styles.ToneFaint}, {Text: line[n:], Tone: styles.ToneBody}}
	}
	return cell{{Text: line, Tone: styles.ToneBody}}
}

// messageKeys are the fields loggers write the message in.
var messageKeys = []string{"msg", "message", "error", "err"}

func isMessageKey(key string) bool {
	for _, k := range messageKeys {
		if strings.EqualFold(key, k) {
			return true
		}
	}
	return false
}

// isLevelKey reports whether a field holds the level a line was logged at.
func isLevelKey(key string) bool {
	switch strings.ToLower(key) {
	case "level", "lvl", "severity":
		return true
	}
	return false
}

// valueLevelTone is the tone of a level written as a value of json or logfmt, a word or pino's
// number: error and worse in danger, a warning in warning, the rest in the body ink.
func valueLevelTone(level string) styles.Tone {
	switch levelName(level) {
	case "error", "fatal", "crit", "panic":
		return styles.ToneDanger
	case "warn":
		return styles.ToneWarning
	}
	return styles.ToneBody
}

// levelName is a level as the invocation page writes it, lowercase and at most five wide: pino's
// numbers by the names it gives them, 50 as error, and the names loggers spell differently as one,
// warning as warn and critical as crit.
func levelName(level string) string {
	if n, err := strconv.Atoi(level); err == nil {
		switch {
		case n >= 60:
			return "fatal"
		case n >= 50:
			return "error"
		case n >= 40:
			return "warn"
		case n >= 30:
			return "info"
		case n >= 20:
			return "debug"
		}
		return "trace"
	}
	switch l := strings.ToLower(level); l {
	case "warning":
		return "warn"
	case "err":
		return "error"
	case "critical":
		return "crit"
	case "dpanic":
		return "panic"
	case "information":
		return "info"
	default:
		return truncateBytes(l, 5)
	}
}

// truncateBytes is s cut to n bytes at most, at a rune's start.
func truncateBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// tintJSON tints a JSON line. It does not check the line is valid, a broken one is tinted as far
// as it reads. The parts come in the order of the line, so a part in the tone of the one before
// grows that span over the line rather than copying it again.
func tintJSON(line string) cell {
	var c cell
	from := 0
	at := 0
	add := func(text string, tone styles.Tone) {
		if text == "" {
			return
		}
		end := at + len(text)
		if n := len(c); n > 0 && c[n-1].Tone == tone {
			c[n-1].Text = line[from:end]
		} else {
			from = at
			c = append(c, styles.Span{Text: line[at:end], Tone: tone})
		}
		at = end
	}
	depth := 0
	key := ""
	for i := 0; i < len(line); {
		switch ch := line[i]; {
		case ch == '"':
			j := i + 1
			for j < len(line) && line[j] != '"' {
				if line[j] == '\\' {
					j++
				}
				j++
			}
			j = min(j+1, len(line))
			k := j
			for k < len(line) && line[k] == ' ' {
				k++
			}
			switch {
			case k < len(line) && line[k] == ':':
				key = line[i+1 : max(i+1, j-1)]
				add(line[i:j], styles.ToneFaint)
			case depth == 1 && isMessageKey(key):
				add(line[i:j], styles.ToneText)
			case depth == 1 && isLevelKey(key):
				add(line[i:j], valueLevelTone(strings.Trim(line[i:j], `"`)))
			default:
				add(line[i:j], styles.ToneBody)
			}
			i = j
		case strings.IndexByte("{}[],:", ch) >= 0:
			switch ch {
			case '{', '[':
				depth++
			case '}', ']':
				depth--
			}
			add(string(ch), styles.ToneFaint)
			i++
		default:
			j := i + 1
			for j < len(line) && strings.IndexByte(`"{}[],:`, line[j]) < 0 {
				j++
			}
			tone := styles.ToneBody
			if depth == 1 && isLevelKey(key) {
				tone = valueLevelTone(strings.TrimSpace(line[i:j]))
			}
			add(line[i:j], tone)
			i = j
		}
	}
	return c
}

// tintLogfmt tints a line of key=value pairs. Like loki.Fields, it takes a line for logfmt when at
// least two words are pairs and they make up most of it.
func tintLogfmt(line string) (cell, bool) {
	var c cell
	words, pairs := 0, 0
	last := 0
	for i := 0; i < len(line); {
		for i < len(line) && line[i] == ' ' {
			i++
		}
		if i >= len(line) {
			break
		}
		words++
		start := i
		for i < len(line) && line[i] != '=' && line[i] != ' ' && line[i] != '"' {
			i++
		}
		if i >= len(line) || line[i] != '=' || i == start {
			for i < len(line) && line[i] != ' ' {
				if line[i] == '"' {
					i = skipQuoted(line, i)
					continue
				}
				i++
			}
			continue
		}
		pairs++
		key := line[start:i]
		if start > last {
			c = append(c, styles.Span{Text: line[last:start], Tone: styles.ToneBody})
		}
		c = append(c, styles.Span{Text: line[start : i+1], Tone: styles.ToneFaint})
		i++
		vstart := i
		if i < len(line) && line[i] == '"' {
			i = skipQuoted(line, i)
		} else {
			for i < len(line) && line[i] != ' ' {
				i++
			}
		}
		tone := styles.ToneBody
		switch {
		case isMessageKey(key):
			tone = styles.ToneText
		case isLevelKey(key):
			tone = valueLevelTone(strings.Trim(line[vstart:i], `"`))
		}
		if i > vstart {
			c = append(c, styles.Span{Text: line[vstart:i], Tone: tone})
		}
		last = i
	}
	if pairs < 2 || pairs*3 < words*2 {
		return nil, false
	}
	if last < len(line) {
		c = append(c, styles.Span{Text: line[last:], Tone: styles.ToneBody})
	}
	return c, true
}

// skipQuoted returns where the quoted string starting at i ends.
func skipQuoted(s string, i int) int {
	for j := i + 1; j < len(s); j++ {
		switch s[j] {
		case '\\':
			j++
		case '"':
			return j + 1
		}
	}
	return len(s)
}

// leadingTimestamp is the length of a date and time a line starts with, like
// "2026-10-01 12:00:00.123", "2026-10-01T12:00:00Z" or go's log "2026/10/01 12:00:00", with the
// space after it, or 0.
func leadingTimestamp(line string) int {
	const layout = "0000-00-00?00:00:00"
	if len(line) < len(layout) {
		return 0
	}
	for i := range len(layout) {
		c := line[i]
		switch layout[i] {
		case '0':
			if c < '0' || c > '9' {
				return 0
			}
		case '?':
			if c != ' ' && c != 'T' {
				return 0
			}
		case '-':
			if c != line[4] || (c != '-' && c != '/') {
				return 0
			}
		default:
			if c != layout[i] {
				return 0
			}
		}
	}
	n := len(layout)
	for n < len(line) && strings.IndexByte("0123456789.,:+-Z", line[n]) >= 0 {
		n++
	}
	for n < len(line) && line[n] == ' ' {
		n++
	}
	return n
}

// platformLines are the starts of the lines lambda writes itself: those of a request, of the start
// of an instance, and of its extensions.
var platformLines = []string{
	"START RequestId:", "END RequestId:", "REPORT RequestId:",
	"INIT_START ", "INIT_REPORT ", "RESTORE_START ", "RESTORE_REPORT ", "EXTENSION ", "TELEMETRY ",
}

// platformLine reports whether lambda wrote the line. Its tabs are spaces by now, as Clean makes them.
func platformLine(line string) bool {
	for _, p := range platformLines {
		if strings.HasPrefix(line, p) {
			return true
		}
	}
	return false
}

// levelTones are the tones of the levels a node function logs at.
var levelTones = map[string]styles.Tone{
	"ERROR": styles.ToneDanger, "WARN": styles.ToneWarning, "INFO": styles.ToneMuted, "DEBUG": styles.ToneFaint, "TRACE": styles.ToneFaint,
}

// tintNode tints a line of node's console in lambda: its time, request id and level, tab
// separated, before the message, which is tinted as json or logfmt when it is either.
func tintNode(line string) (cell, bool) {
	parts := strings.SplitN(line, "\t", 4)
	if len(parts) != 4 || leadingTimestamp(parts[0]+" ") == 0 {
		return nil, false
	}
	tone, ok := levelTones[parts[2]]
	if !ok {
		return nil, false
	}
	return append(cell{
		{Text: parts[0] + " ", Tone: styles.ToneFaint},
		{Text: parts[1] + " ", Tone: styles.ToneFaint},
		{Text: parts[2] + " ", Tone: tone, Bold: tone == styles.ToneDanger},
	}, tint(parts[3])...), true
}

// levelTone is the tone of a level levelName wrote: error and worse in danger, a warning in
// warning, info muted and debug and trace faint.
func levelTone(name string) styles.Tone {
	switch name {
	case "error", "fatal", "crit", "panic":
		return styles.ToneDanger
	case "warn":
		return styles.ToneWarning
	case "info":
		return styles.ToneMuted
	case "debug", "trace":
		return styles.ToneFaint
	}
	return styles.ToneBody
}

// pythonLevel is the level of a line python's logging writes in lambda, [INFO], or "".
func pythonLevel(field string) string {
	if len(field) < 3 || field[0] != '[' || field[len(field)-1] != ']' {
		return ""
	}
	switch name := field[1 : len(field)-1]; name {
	case "DEBUG", "INFO", "WARNING", "ERROR", "CRITICAL":
		return levelName(name)
	}
	return ""
}

// tintPython tints a line of python's logging in lambda: its level, time and request id, tab
// separated, before the message, which is tinted as tint does.
func tintPython(parts []string) (cell, bool) {
	level := pythonLevel(parts[0])
	if level == "" || leadingTimestamp(parts[1]+" ") == 0 {
		return nil, false
	}
	tone := levelTone(level)
	return append(cell{
		{Text: parts[0] + " ", Tone: tone, Bold: tone == styles.ToneDanger},
		{Text: parts[1] + " ", Tone: styles.ToneFaint},
		{Text: parts[2] + " ", Tone: styles.ToneFaint},
	}, tint(parts[3])...), true
}

// colours are the sgr sequences a logger colours its lines with. They are dropped; any other
// escape sequence is shown escaped, by Clean.
var colours = regexp.MustCompile(`\x1b\[[0-9;:]*m`)

// tintLine tints a raw line of a log for drawing: a node or a python line by its tab separated
// fields, anything else as tint does, its colours dropped and every span cleaned of control
// characters.
func tintLine(raw string) cell {
	raw = colours.ReplaceAllString(raw, "")
	if parts := strings.SplitN(raw, "\t", 4); len(parts) == 4 {
		for i := range parts {
			parts[i] = styles.Clean(parts[i])
		}
		if node, ok := tintNode(strings.Join(parts, "\t")); ok {
			return node
		}
		if py, ok := tintPython(parts); ok {
			return py
		}
	}
	return tint(styles.Clean(raw))
}

// messageLines are the lines of a message. The runtimes of node and python end each line of one
// with a carriage return rather than a newline, so cloudwatch keeps it one event; either breaks a
// line. The newlines a message ends in make no lines.
func messageLines(message string) []string {
	if strings.IndexByte(message, '\r') >= 0 {
		message = strings.ReplaceAll(strings.ReplaceAll(message, "\r\n", "\n"), "\r", "\n")
	}
	return strings.Split(strings.TrimRight(message, "\n"), "\n")
}

// frameKind is what a line of a stack trace is.
type frameKind int

const (
	// notFrame is a line that is no part of a stack trace, or its message, as Error: boom.
	notFrame frameKind = iota
	// frameHead opens a trace: Traceback (most recent call last): or goroutine 1 [running]:.
	frameHead
	// frameCall is a call: at handler (/var/task/index.js:4:2) in node, File "/var/task/app.py",
	// line 8, in handler in python, main.handler(...) in go.
	frameCall
	// frameSource is the line under a call: the code of a python frame, the file of a go one.
	frameSource
)

// stackLine is what a line of a message is in a stack trace, and whether it is in code the
// function's own does not hold: a dependency or the runtime.
type stackLine struct {
	kind     frameKind
	internal bool
}

// goCall is the call of a frame of a go stack trace: main.handler(...), net/http.(*conn).serve(...).
var goCall = regexp.MustCompile(`^[\w./-]*\w\.[\w.*()\[\]$-]*\(.*\)( \+0x[0-9a-f]+)?$`)

// isGoCall reports whether a line is the call of a go frame. The expression runs only on a line
// that ends as one does.
func isGoCall(line string) bool {
	return (strings.HasSuffix(line, ")") || strings.Contains(line, ") +0x")) && goCall.MatchString(line)
}

// stackLines finds the stack traces in the lines of a message, as node, python and go write them.
// block says the lines are one, as a field of json holds, where a go call is its name alone.
func stackLines(lines []string, block bool) []stackLine {
	out := make([]stackLine, len(lines))
	for i, line := range lines {
		t := strings.TrimLeft(line, " \t")
		indented := len(t) < len(line)
		switch {
		case indented && strings.HasPrefix(t, "at "):
			out[i] = stackLine{frameCall, nodeInternal(t)}
		case indented && strings.HasPrefix(t, `File "`):
			out[i] = stackLine{frameCall, pythonInternal(t)}
		case t == "Traceback (most recent call last):", strings.HasPrefix(t, "goroutine ") && strings.HasSuffix(t, "]:"):
			out[i] = stackLine{kind: frameHead}
		case indented && goSource(t):
			out[i] = stackLine{frameSource, goInternalPath(t)}
		case indented && i > 0 && out[i-1].kind == frameCall && strings.HasPrefix(strings.TrimLeft(lines[i-1], " \t"), `File "`):
			out[i] = stackLine{frameSource, out[i-1].internal}
		case !indented && (isGoCall(t) || block && i+1 < len(lines) && goSource(strings.TrimLeft(lines[i+1], " \t"))):
			out[i] = stackLine{frameCall, goInternalCall(t)}
			if i+1 < len(lines) {
				if next := strings.TrimLeft(lines[i+1], " \t"); goSource(next) {
					out[i].internal = goInternalPath(next)
				}
			}
		}
	}
	return out
}

// tone is the tone of a line of a stack trace: the function's own frames in the body ink, so they
// stand out, the rest faint.
func (s stackLine) tone() styles.Tone {
	if s.kind == frameHead || s.internal {
		return styles.ToneFaint
	}
	return styles.ToneBody
}

// nodeInternal reports whether a frame of node is in a dependency or node and lambda's runtime.
func nodeInternal(frame string) bool {
	return strings.Contains(frame, "node_modules/") || strings.Contains(frame, "node:") || strings.Contains(frame, "/var/runtime/")
}

// pythonInternal reports whether a frame of python is in a package or python and lambda's runtime.
func pythonInternal(frame string) bool {
	for _, in := range []string{"/var/runtime/", "/var/lang/", "site-packages/", "dist-packages/", "<frozen "} {
		if strings.Contains(frame, in) {
			return true
		}
	}
	return false
}

// goSource reports whether a line is the file and line of a go frame: /build/main.go:23 +0x1d.
func goSource(line string) bool {
	return (strings.HasPrefix(line, "/") || strings.HasPrefix(line, "<autogenerated>")) && strings.Contains(line, ":")
}

// goInternalPath reports whether the file of a go frame is in the module cache or go's own source.
func goInternalPath(path string) bool {
	return strings.Contains(path, "/pkg/mod/") || strings.Contains(path, "/go/src/") || strings.HasPrefix(path, "<autogenerated>")
}

// goStd are the packages of go's own a stack trace runs through most.
var goStd = []string{"runtime", "net", "sync", "reflect", "context", "syscall", "internal", "encoding", "fmt", "io", "os", "testing"}

// goInternalCall reports whether a go call is in go's runtime or library, or in aws-lambda-go, by
// its name, for a frame without its file.
func goInternalCall(call string) bool {
	if strings.Contains(call, "aws-lambda-go") {
		return true
	}
	first := call
	if i := strings.IndexAny(first, "/.("); i >= 0 {
		first = first[:i]
	}
	for _, std := range goStd {
		if first == std {
			return true
		}
	}
	return false
}

// wrapCell breaks a cell into lines of at most width columns, keeping each span's tone. A line
// breaks at its last space or after its last comma, when that fills at least half of it, and
// otherwise where the width runs out. The space it breaks at, or after the comma, is dropped.
func wrapCell(c cell, width int) []cell {
	text := plainText(c)
	if width <= 0 || ansi.StringWidth(text) <= width {
		return []cell{c}
	}
	var lines []cell
	parts := cellSlicer{c: c}
	start, used := 0, 0
	cut, next, cutUsed := -1, -1, 0
	for i := 0; i < len(text); {
		g, w := ansi.FirstGraphemeCluster(text[i:], ansi.GraphemeWidth)
		if g == "" {
			_, n := utf8.DecodeRuneInString(text[i:])
			g, w = text[i:i+n], 1
		}
		size := len(g)
		if used+w > width && i > start {
			if cut <= start || cutUsed*2 < width {
				cut, next = i, i
				if g == " " {
					next = i + size
				}
			}
			lines = append(lines, parts.slice(start, cut))
			start, i, used, cut = next, next, 0, -1
			continue
		}
		switch g {
		case " ":
			cut, next, cutUsed = i, i+size, used
		case ",":
			cut, next, cutUsed = i+size, i+size, used+w
			if next < len(text) && text[next] == ' ' {
				next++
			}
		}
		used += w
		i += size
	}
	if start < len(text) {
		lines = append(lines, parts.slice(start, len(text)))
	}
	return lines
}

// plainText is the text of a cell, without its tones.
func plainText(c cell) string {
	var b strings.Builder
	for _, s := range c {
		b.WriteString(s.Text)
	}
	return b.String()
}

// cellSlicer cuts a cell into parts by byte offsets of its text, each part after the one before,
// so cutting a long line into many walks its spans once rather than once a part.
type cellSlicer struct {
	c cell
	// span is the first span the next part can take from, and at where it starts in the text.
	span int
	at   int
}

// slice is the part of the cell from byte from to byte to of its text. from is no less than the
// from of the part before.
func (k *cellSlicer) slice(from, to int) cell {
	for k.span < len(k.c) && k.at+len(k.c[k.span].Text) <= from {
		k.at += len(k.c[k.span].Text)
		k.span++
	}
	var out cell
	at := k.at
	for _, s := range k.c[k.span:] {
		if at >= to {
			break
		}
		end := at + len(s.Text)
		if lo, hi := max(from, at), min(to, end); lo < hi {
			out = append(out, styles.Span{Text: s.Text[lo-at : hi-at], Tone: s.Tone, Bold: s.Bold})
		}
		at = end
	}
	return out
}

// highlight marks where a cell holds want, ignoring case, in bold accent, so the lines a filter
// keeps show why.
func highlight(c cell, want string) cell {
	text := plainText(c)
	lower, want := strings.ToLower(text), strings.ToLower(want)
	if want == "" || len(lower) != len(text) || !strings.Contains(lower, want) {
		return c
	}
	var out cell
	parts := cellSlicer{c: c}
	at := 0
	for {
		i := strings.Index(lower[at:], want)
		if i < 0 {
			break
		}
		out = append(out, parts.slice(at, at+i)...)
		out = append(out, styles.Span{Text: text[at+i : at+i+len(want)], Tone: styles.ToneAccent, Bold: true})
		at += i + len(want)
	}
	return append(out, parts.slice(at, len(text))...)
}

// logCells are the drawn lines of one log message: each of its lines tinted and wrapped to width.
func logCells(message string, width int) []cell { return markedLogCells(message, "", width) }

// markedLogCells are logCells with where the message holds mark marked. A line of a stack trace
// is in the body ink when it is in the function's own code, faint when it is not.
func markedLogCells(message, mark string, width int) []cell {
	lines := messageLines(message)
	var stack []stackLine
	if len(lines) > 1 || strings.IndexByte(message, '\t') == 0 || isGoCall(message) {
		stack = stackLines(lines, false)
	}
	out := make([]cell, 0, len(lines))
	for i, raw := range lines {
		var c cell
		if i < len(stack) && stack[i].kind != notFrame {
			c = cell{{Text: styles.Clean(colours.ReplaceAllString(raw, "")), Tone: stack[i].tone()}}
		} else {
			c = tintLine(raw)
		}
		out = append(out, wrapCell(highlight(c, mark), width)...)
	}
	return out
}
