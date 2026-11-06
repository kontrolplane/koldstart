package tui

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/kontrolplane/koldstart/internal/cli"
	"github.com/kontrolplane/koldstart/internal/tui/styles"
)

// logLineState is one line of the function's logs, whole: when it was logged, in which stream,
// for which request and at what level, what it says and its fields.
type logLineState struct {
	e      cli.LogEvent
	scroll int
	// drawn is the body of the page as the last frame drew it, so the next does not again.
	drawn *logLineDrawn
	// json is the json the line is or holds, read with a cursor; it is empty for a line of none.
	json jsonView
}

// logLineDrawn is the body of the page of a line, drawn at a width in a palette.
type logLineDrawn struct {
	e       cli.LogEvent
	width   int
	palette int
	lines   []string
}

// openLogLine shows a line of the logs on a page of its own.
func (m model) openLogLine(e cli.LogEvent) model {
	m.state.logLine = newLogLine(e, &logLineDrawn{})
	return m.switchPage(logLinePage)
}

// newLogLine is the page of line e, its json in a json view, the top of which copies the line as
// it was logged. drawn is where it keeps what it drew.
func newLogLine(e cli.LogEvent, drawn *logLineDrawn) logLineState {
	s := logLineState{e: e, drawn: drawn}
	if doc := splitLogLine(e.Message).json; doc != "" {
		s.json = newJSONViewBytes([]byte(doc))
		s.json.source = e.Message
	}
	return s
}

// shown is the json of the line, or nil when it holds none.
func (s *logLineState) shown() *jsonView {
	if s.json.empty() {
		return nil
	}
	return &s.json
}

// logLineUpdate handles a key on the page of a line. The keys of its json come first, as on an
// output: they move the cursor through it, fold it, search it and copy from it, the line as it
// was logged on its top line. Then c copies the line, o, or enter on a line without json, opens
// its invocation, n, while no search holds text, and p go to the next and the previous line the
// logs tab shows, and the rest scroll.
func (m model) logLineUpdate(msg tea.KeyPressMsg) (model, tea.Cmd) {
	v := &m.state.logLine
	if j := v.shown(); j != nil {
		before := j.at()
		if res, ok := j.update(msg, m.keys, logLineWidth(), contentHeight); ok {
			m = m.followLogLineCursor(msg, before)
			return m.jsonDone(res)
		}
	}
	switch {
	case key.Matches(msg, m.keys.Back, m.keys.Quit):
		return m.switchPage(functionPage), nil
	case key.Matches(msg, m.keys.Copy):
		return m.copy(v.e.Message)
	case key.Matches(msg, m.keys.Request, m.keys.View):
		r, ok := m.lineRequest(v.e)
		if !ok {
			return m.setStatus("this line is of no invocation in the logs read", styles.ToneMuted)
		}
		return m.openInvocation(r)
	case key.Matches(msg, m.keys.Next):
		return m.stepLogLine(1)
	case key.Matches(msg, m.keys.Previous):
		return m.stepLogLine(-1)
	}
	v.scroll = scrollBy(msg, m.keys, v.scroll, contentHeight, maxOffset(m.logLineLines().len(), contentHeight))
	return m, nil
}

// stepLogLine shows the line by lines below the one on screen among those the logs tab shows,
// above it when by is negative, and moves the tab's cursor to it, so going back finds it there.
func (m model) stepLogLine(by int) (model, tea.Cmd) {
	list := m.logList()
	l := &m.state.function.logs
	e := m.state.logLine.e
	i := -1
	for k, it := range list.items {
		if it.line >= 0 && l.lines[it.line].Time.Equal(e.Time) && l.lines[it.line] == e {
			i = k
			break
		}
	}
	j := list.step(i, by)
	switch {
	case i < 0:
		return m.setStatus("this line is no longer among those the logs tab shows", styles.ToneMuted)
	case j == i && by > 0:
		return m.setStatus("this is the newest line the logs tab shows", styles.ToneMuted)
	case j == i:
		return m.setStatus("this is the oldest line the logs tab shows: ↑ on its first line reads further back", styles.ToneMuted)
	}
	m = m.putCursor(list, j).scrollTo(list, j)
	m.state.logLine = newLogLine(m.state.function.logs.lines[list.items[j].line], m.state.logLine.drawn)
	return m, nil
}

// followLogLineCursor scrolls the page after a key on its json so the cursor is in sight.
func (m model) followLogLineCursor(msg tea.KeyPressMsg, before int) model {
	v := &m.state.logLine
	lines, at := m.logLineBody()
	v.scroll = m.followJSON(v.shown(), msg, before, v.scroll, at, lines.len(), logLineWidth())
	return m
}

// lineRequest is the invocation in the logs read that a line belongs to.
func (m model) lineRequest(e cli.LogEvent) (request, bool) {
	for _, r := range m.state.function.requests {
		for _, x := range r.lines {
			if x.Time.Equal(e.Time) && x == e {
				return r, true
			}
		}
	}
	return request{}, false
}

// logLineWidth is the width of the page of a line.
func logLineWidth() int { return contentWidth - 4 }

// logLineLines are the lines of the page of a line: its head, and what it says, drawn only where
// they are in sight.
func (m model) logLineLines() lazyLines {
	lines, _ := m.logLineBody()
	return lines
}

// logLineBody is logLineLines, and the line its json starts at, or -1 when it holds none.
func (m model) logLineBody() (lazyLines, int) {
	v := m.state.logLine
	width := logLineWidth()
	out := lazyOf(m.logLineHead(v.e, width))
	body := v.body(width)
	out.addDrawn(len(body), func(i int) string { return body[i] })
	j := v.shown()
	if j == nil {
		return out, -1
	}
	out.add("", styles.SectionHeader("json", width))
	at := out.len()
	out.join(j.lines(width))
	return out, at
}

// logLineHead says when a line was logged, to the millisecond and how long ago, in which stream,
// of which invocation and how it ended, at what level, and how long the line is.
func (m model) logLineHead(e cli.LogEvent, width int) []string {
	field := func(label string, value cell) string { return rowIn(headLabels, label, value, width) }
	request := text("none in the logs read", styles.ToneFaint)
	if r, ok := m.lineRequest(e); ok {
		request = append(cell{styles.S(r.id, styles.ToneBody), sep()}, requestStatus(r, time.Duration(m.state.function.inputs.Timeout)*time.Second)...)
		request = append(request, styles.S("  "+m.keys.Request.Help().Key+" opens it", styles.ToneFaint))
	}
	level := text("none", styles.ToneFaint)
	if name := lineLevel(e.Message); name != "" {
		level = text(name, levelTone(name))
	}
	stream := text("-", styles.ToneFaint)
	if e.Stream != "" {
		stream = text(e.Stream, styles.ToneMuted)
	}
	return []string{
		styles.SectionHeader("log line", width),
		field("logged", cell{styles.S(formatStamp(e.Time), styles.ToneBody), sep(), styles.S(formatAgo(e.Time), styles.ToneMuted)}),
		field("stream", stream),
		field("request", request),
		field("level", level),
		field("size", text(cli.Size(int64(len(e.Message))), styles.ToneBody)),
	}
}

// body is what the line says, as the last frame drew it while the line, the width and the palette
// are the same.
func (v logLineState) body(width int) []string {
	d := v.drawn
	if d != nil && d.lines != nil && d.e == v.e && d.width == width && d.palette == styles.Gen() {
		return d.lines
	}
	lines := logLineText(v.e, width)
	if d != nil {
		*d = logLineDrawn{e: v.e, width: width, palette: styles.Gen(), lines: lines}
	}
	return lines
}

// logLineText draws what a line says in width but its json, which a json view reads: its
// message whole, tinted as the logs tint it and wrapped, the error and stack trace of json that
// holds one, as node's Invoke Error does, read as kdst invoke reads one, and the pairs of logfmt
// each under its key.
func logLineText(e cli.LogEvent, width int) []string {
	parts := splitLogLine(e.Message)
	var out []string
	if strings.TrimSpace(parts.message) != "" {
		out = append(out, "", styles.SectionHeader("message", width))
		for _, c := range logCells(parts.message, width) {
			out = append(out, renderCell(c, column{width: width}, nil))
		}
	}
	if f, ok := cli.ReadFailure([]byte(parts.json)); ok && len(f.Trace) > 0 {
		out = append(out, "", styles.SectionHeader("error", width))
		out = append(out, traceLines(f, width)...)
	}
	if len(parts.pairs) > 0 {
		out = append(out, "", styles.SectionHeader("fields", width))
		keys := make([]string, len(parts.pairs))
		for i, p := range parts.pairs {
			keys[i] = p[0]
		}
		labels := labelsFor(keys, width)
		for _, p := range parts.pairs {
			tone := styles.ToneBody
			switch {
			case isMessageKey(p[0]):
				tone = styles.ToneText
			case isLevelKey(p[0]):
				tone = valueLevelTone(p[1])
			}
			for i, c := range wrapCell(text(p[1], tone), max(1, width-labels)) {
				label := ""
				if i == 0 {
					label = p[0]
				}
				out = append(out, rowIn(labels, styles.Clean(label), c, width))
			}
		}
	}
	if len(out) == 0 && parts.json == "" {
		out = append(out, "", styles.Faint("an empty line"))
	}
	return out
}

// lineParts are what a raw line of the logs says: its message, without the time, request id and
// level a runtime writes in front of it, the json it is or holds, and the pairs of logfmt it is.
type lineParts struct {
	message string
	json    string
	pairs   [][2]string
}

// splitLogLine reads a raw line of the logs into its parts. A line of json, or one that ends in
// json after a few words, as node's Invoke Error does, is its json and the message the json holds;
// a line of logfmt is its pairs and the message among them; anything else is all message.
func splitLogLine(raw string) lineParts {
	lines := messageLines(colours.ReplaceAllString(raw, ""))
	var l shownLine
	first := l.dropPrefix(lines[0], "")
	rest := lines[1:]
	whole := strings.Join(append([]string{first}, rest...), "\n")
	if doc := strings.TrimSpace(whole); (strings.HasPrefix(doc, "{") || strings.HasPrefix(doc, "[")) && json.Valid([]byte(doc)) {
		return lineParts{message: jsonMessage(doc), json: doc}
	}
	if i := strings.Index(first, `{"`); i > 0 && len(rest) == 0 && json.Valid([]byte(first[i:])) {
		return lineParts{message: joinNonEmpty(strings.TrimSpace(first[:i]), jsonMessage(first[i:])), json: first[i:]}
	}
	if pairs, ok := logfmtPairs(first); ok && len(rest) == 0 {
		var message string
		for _, p := range pairs {
			if isMessageKey(p[0]) {
				message = p[1]
				break
			}
		}
		return lineParts{message: message, pairs: pairs}
	}
	return lineParts{message: whole}
}

// jsonMessage is the message a line of json holds: its msg or message, or the type and message of
// the error lambda writes of a node function.
func jsonMessage(doc string) string {
	fields, ok := jsonFields(doc)
	if !ok {
		return ""
	}
	if f, ok := fieldOf(fields, "msg", "message"); ok {
		return f.text()
	}
	var kind, message string
	if f, ok := fieldOf(fields, "errorType"); ok {
		kind = f.text()
	}
	if f, ok := fieldOf(fields, "errorMessage"); ok {
		message = f.text()
	}
	return joinNonEmpty(kind, message)
}

// logfmtPairs are the key=value pairs of a line of logfmt, a quoted value unquoted, and whether
// the line is logfmt, as tintLogfmt takes it.
func logfmtPairs(line string) ([][2]string, bool) {
	if _, ok := tintLogfmt(line); !ok {
		return nil, false
	}
	var out [][2]string
	for i := 0; i < len(line); {
		for i < len(line) && line[i] == ' ' {
			i++
		}
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
		k := line[start:i]
		i++
		from := i
		if i < len(line) && line[i] == '"' {
			i = skipQuoted(line, i)
		} else {
			for i < len(line) && line[i] != ' ' {
				i++
			}
		}
		value := line[from:i]
		if s, err := strconv.Unquote(value); err == nil && strings.HasPrefix(value, `"`) {
			value = s
		}
		out = append(out, [2]string{k, value})
	}
	return out, true
}

// logLineHints are the keys of the page of a line for the footer: n and p, then o where the line
// is of an invocation, before the keys of its json, or c, which copies the line, where it has
// none. While a search of its json holds text n goes to its next match.
func (m model) logLineHints() [][2]string {
	k := m.keys
	v := &m.state.logLine
	h := [][2]string{scrollHint, {k.Next.Help().Key + "/" + k.Previous.Help().Key, "next/previous"}}
	j := v.shown()
	if j != nil {
		keys := m.jsonHints(*j)
		h[0] = keys[0]
		if j.query != "" {
			h[1] = hint(k.Previous)
		}
		if _, ok := m.lineRequest(v.e); ok {
			h = append(h, hint(k.Request))
		}
		return append(append(h, keys[1:]...), hint(k.Help), hint(k.Quit))
	}
	if _, ok := m.lineRequest(v.e); ok {
		h = append(h, hint(k.Request))
	}
	return append(h, hint(k.Copy, "copy the line"), hint(k.Help), hint(k.Quit))
}

func (m model) logLineView() string {
	return indent(scrolledLines(m.logLineLines(), m.state.logLine.scroll, contentHeight))
}
