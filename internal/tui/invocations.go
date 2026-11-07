package tui

import (
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/kontrolplane/koldstart/internal/cli"
	"github.com/kontrolplane/koldstart/internal/tui/styles"
)

// errorLabel names what an invocation failed with, in the table and the head.
const errorLabel = "error"

// invocationColumns are the columns of the invocations: the status first, so a failure is seen
// at a glance, and what it failed with last, the first to go when the table is narrow.
var invocationColumns = []column{
	{title: "", width: 11},
	{title: "time", width: 12},
	{title: "request", width: 8, drop: 2},
	{title: "duration", width: 8, right: true},
	{title: "billed", width: 8, right: true, drop: 4},
	{title: "memory", width: 12, right: true, drop: 1},
	{title: "cold start", width: 10, right: true, drop: 3},
	{title: errorLabel, width: 10, grow: 1, drop: 5},
}

// invocationsTableHeight is how many rows the invocations table shows, below the line that sums
// them up.
func invocationsTableHeight() int { return functionBodyHeight() - tableHeaderRows - 2 }

// updateRequests reads the invocations out of the lines of the logs. The cursor stays on the
// invocation it was on, or on the newest when it was there.
func (m model) updateRequests() model {
	f := &m.state.function
	var on string
	if i := f.invocations.cursor; i > 0 && i < len(f.requests) {
		on = f.requests[i].id
	}
	f.requests = requests(f.logs.lines)
	rows := make([]tableRow, 0, len(f.requests))
	timeout := time.Duration(f.inputs.Timeout) * time.Second
	for _, r := range f.requests {
		rows = append(rows, requestRow(r, timeout))
	}
	f.invocations.setRows(rows)
	if on != "" {
		if i := slices.IndexFunc(f.requests, func(r request) bool { return r.id == on }); i >= 0 {
			f.invocations.setCursor(i)
		}
	}
	return m
}

// requestStatus is how an invocation ended, its symbol first. One without a report is running
// while it is within the function's timeout, and has no report after it, as on an emulator that
// writes none.
func requestStatus(r request, timeout time.Duration) cell {
	switch {
	case r.timedOut:
		return text("⏱ timed out", styles.ToneDanger)
	case r.failed:
		return text("✗ failed", styles.ToneDanger)
	case !r.reported && time.Since(r.last) < timeout+5*time.Second:
		return text("● running", styles.ToneAccent)
	case !r.reported:
		return text("no report", styles.ToneFaint)
	}
	return text("✓ ok", styles.ToneSuccess)
}

// requestRow draws an invocation.
func requestRow(r request, timeout time.Duration) tableRow {
	memory := text("-", styles.ToneFaint)
	if r.memory > 0 {
		memory = cell{styles.S(formatInt(r.maxMemory), styles.ToneBody), styles.S("/"+formatInt(r.memory)+" mb", styles.ToneFaint)}
	}
	cold := text("", styles.ToneFaint)
	if r.init > 0 {
		cold = text(formatMillis(r.init), styles.ToneInfo)
	}
	duration := text("-", styles.ToneFaint)
	if r.reported {
		duration = millisCell(r.duration, styles.ToneBody)
	}
	billed := text("-", styles.ToneFaint)
	if r.billed > 0 {
		billed = millisCell(r.billed, styles.ToneMuted)
	}
	return tableRow{
		requestStatus(r, timeout), text(r.start.Local().Format("15:04:05.000"), styles.ToneBody), text(shortRequest(r.id), styles.ToneFaint),
		duration, billed, memory, cold, causeCell(r.cause, styles.ToneMuted),
	}
}

// millisCell is a duration in a column of them, its number in tone and its unit faint, and the
// unit two wide, so the numbers line up whether they are in ms or s.
func millisCell(ms float64, tone styles.Tone) cell {
	s := formatMillis(ms)
	n := len(s)
	for n > 0 && (s[n-1] < '0' || s[n-1] > '9') {
		n--
	}
	unit := s[n:]
	if len(unit) == 1 {
		unit += " "
	}
	return cell{styles.S(s[:n], tone), styles.S(unit, styles.ToneFaint)}
}

// causeCell is what an invocation failed with on one line: the type of its error in the body ink
// and its message in tone.
func causeCell(c cause, tone styles.Tone) cell {
	message, _, _ := strings.Cut(c.message, "\n")
	switch {
	case c.kind == "" && message == "":
		return nil
	case c.kind == "":
		return cell{styles.S(message, tone)}
	case message == "":
		return cell{styles.S(c.kind, styles.ToneBody)}
	}
	return cell{styles.S(c.kind, styles.ToneBody), styles.S(": "+message, tone)}
}

// shortRequest is a request id as a table or a breadcrumb names it: the first group of a uuid,
// which tells the requests of an hour apart.
func shortRequest(id string) string {
	if i := strings.IndexByte(id, '-'); i >= 8 {
		return id[:i]
	}
	return shortSHA(id)
}

// invocationsSummary sums up the invocations read: how many, how many failed, the median and
// p99 of their durations, and how many were cold starts.
func invocationsSummary(rs []request) cell {
	failed, cold := 0, 0
	var durations []float64
	for _, r := range rs {
		if r.failed {
			failed++
		}
		if r.init > 0 {
			cold++
		}
		if r.reported {
			durations = append(durations, r.duration)
		}
	}
	failedTone := styles.ToneMuted
	if failed > 0 {
		failedTone = styles.ToneDanger
	}
	c := cell{styles.S(plural(len(rs), "invocation"), styles.ToneBody), sep(), styles.S(formatInt(failed)+" failed", failedTone)}
	if len(durations) > 0 {
		slices.Sort(durations)
		c = append(c,
			sep(), styles.S("p50 ", styles.ToneFaint), styles.S(formatMillis(percentile(durations, 50)), styles.ToneBody),
			sep(), styles.S("p99 ", styles.ToneFaint), styles.S(formatMillis(percentile(durations, 99)), styles.ToneBody),
		)
	}
	coldTone := styles.ToneMuted
	if cold > 0 {
		coldTone = styles.ToneInfo
	}
	return append(c, sep(), styles.S(plural(cold, "cold start"), coldTone))
}

// percentile is the nearest-rank p-th percentile of sorted values.
func percentile(sorted []float64, p float64) float64 {
	i := int(math.Ceil(p/100*float64(len(sorted)))) - 1
	return sorted[max(0, min(i, len(sorted)-1))]
}

func (m model) invocationsUpdate(msg tea.KeyPressMsg) (model, tea.Cmd) {
	f := &m.state.function
	if f.invocations.move(msg, m.keys) {
		return m, nil
	}
	if key.Matches(msg, m.keys.View) && f.invocations.cursor < len(f.requests) {
		return m.openInvocation(f.requests[f.invocations.cursor])
	}
	return m, nil
}

func (m model) invocationsView() string {
	f := m.state.function
	switch {
	case f.logGroup == "":
		return " " + styles.Muted("no log group: deploy the function first")
	case !f.logs.loaded && f.logs.err != "":
		return " " + m.logsError()
	case !f.logs.loaded:
		return " " + styles.Muted(readingNote("the logs"))
	}
	if len(f.requests) == 0 {
		return indent(f.invocations.View())
	}
	return indent(renderCell(invocationsSummary(f.requests), column{width: tableWidth()}, nil) + "\n\n" + f.invocations.View())
}

// invocationState is one invocation of the function, with the lines of the log it wrote, and the
// page it was opened from, the function's or a line of its logs, which going back returns to.
type invocationState struct {
	req    request
	scroll int
	from   page
	// cache keeps the lines of the log read and drawn.
	cache *invocationCache
}

func (m model) openInvocation(r request) (model, tea.Cmd) {
	m.state.invocation = invocationState{req: r, from: m.page, cache: &invocationCache{}}
	return m.switchPage(invocationPage), nil
}

// stepInvocation shows the invocation by rows below the one on screen in the table, above it
// when by is negative, and moves the table's cursor to it, so going back finds it there.
func (m model) stepInvocation(by int) (model, tea.Cmd) {
	f := &m.state.function
	id := m.state.invocation.req.id
	i := slices.IndexFunc(f.requests, func(r request) bool { return r.id == id })
	switch j := i + by; {
	case i < 0:
		return m.setStatus("this invocation is no longer in the logs read", styles.ToneMuted)
	case j < 0:
		return m.setStatus("this is the newest invocation", styles.ToneMuted)
	case j >= len(f.requests):
		return m.setStatus("this is the oldest invocation in the logs read: ↑ on the logs tab reads further back", styles.ToneMuted)
	default:
		f.invocations.setCursor(j)
		m.state.invocation = invocationState{req: f.requests[j], from: m.state.invocation.from, cache: m.state.invocation.cache}
		return m, nil
	}
}

func (m model) invocationUpdate(msg tea.KeyPressMsg) (model, tea.Cmd) {
	v := &m.state.invocation
	switch {
	case key.Matches(msg, m.keys.Back, m.keys.Quit):
		return m.switchPage(v.from), nil
	case key.Matches(msg, m.keys.Copy):
		return m.copy(v.req.id)
	case key.Matches(msg, m.keys.Next):
		return m.stepInvocation(1)
	case key.Matches(msg, m.keys.Previous):
		return m.stepInvocation(-1)
	}
	v.scroll = scrollBy(msg, m.keys, v.scroll, contentHeight, maxOffset(m.invocationLines().len(), contentHeight))
	return m, nil
}

// shownRequest is the invocation on screen as the logs read now tell it, so one that was running
// shows its report once it comes.
func (m model) shownRequest() request {
	r := m.state.invocation.req
	if i := slices.IndexFunc(m.state.function.requests, func(n request) bool { return n.id == r.id }); i >= 0 {
		return m.state.function.requests[i]
	}
	return r
}

// invocationWidth is the width of the invocation page, as of every page of detail.
func invocationWidth() int { return detailWidth() }

// invocationLines are the lines of the invocation page: the head, what lambda reports of it, and
// the lines of its log, each drawn only where it is in sight.
func (m model) invocationLines() lazyLines {
	r := m.shownRequest()
	width := invocationWidth()
	out := lazyOf(m.invocationHead(r, width))
	out.add("", styles.SectionHeader("log", width))
	cache := m.state.invocation.cache
	if cache == nil {
		cache = &invocationCache{}
	}
	shown := shownEvents(r)
	if len(shown) == 0 {
		out.add(row("", text("nothing logged but lambda's own lines", styles.ToneFaint), width))
		return out
	}
	g := cache.gutter(r, shown)
	blank, above := offsetCell("", g), ""
	for _, e := range shown {
		rows := cache.rows(e, r, g, width-rowInset)
		first := blank
		if offset := formatOffset(e.Time.Sub(r.start)); offset != above {
			first, above = offsetCell(offset, g), offset
		}
		out.addDrawn(len(rows), func(i int) string {
			gutter := blank
			if i == 0 {
				gutter = first
			}
			return pad(rowInset, nil) + renderCell(append(slices.Clip(gutter), rows[i]...), column{width: width - rowInset}, nil)
		})
	}
	return out
}

func (m model) invocationView() string {
	return indent(scrolledLines(m.invocationLines(), m.state.invocation.scroll, contentHeight))
}

// invocationHead sums up an invocation: its request id, how it ended and what it failed with,
// when it started, how long it ran and was billed for, the memory it used of what it has, and the
// init of a cold start.
func (m model) invocationHead(r request, width int) []string {
	timeout := time.Duration(m.state.function.inputs.Timeout) * time.Second
	field := func(label string, value cell) string { return row(label, value, width) }
	lines := []string{
		styles.SectionHeader("invocation", width),
		field("request", text(r.id, styles.ToneBody)),
		field("status", requestStatus(r, timeout)),
	}
	if c := causeCell(r.cause, styles.ToneBody); c != nil {
		parts := wrapCell(c, max(1, width-labelWidth-rowInset))
		if len(parts) > 3 {
			parts = append(parts[:2], append(parts[2], styles.S("…", styles.ToneFaint)))
		}
		for i, p := range parts {
			label := ""
			if i == 0 {
				label = errorLabel
			}
			lines = append(lines, field(label, p))
		}
	}
	lines = append(lines, field("started", cell{styles.S(formatStamp(r.start), styles.ToneBody), sep(), styles.S(formatAgo(r.start), styles.ToneMuted)}))
	if r.reported {
		lines = append(lines, field("duration", cell{styles.S(formatMillis(r.duration), styles.ToneBody), sep(), styles.S("billed ", styles.ToneFaint), styles.S(formatMillis(r.billed), styles.ToneMuted)}))
	}
	if r.memory > 0 {
		lines = append(lines, field("memory", append(cell{
			styles.S(formatInt(r.maxMemory), styles.ToneBody), styles.S(" of "+formatInt(r.memory)+" mb  ", styles.ToneMuted),
		}, memoryBar(r.maxMemory, r.memory)...)))
	}
	if r.init > 0 || r.runtime != "" || r.initStatus != "" {
		var cold cell
		if r.init > 0 {
			cold = append(cold, styles.S(formatMillis(r.init), styles.ToneInfo))
		}
		if r.initStatus != "" {
			cold = appendSep(cold, styles.S("init "+r.initStatus, styles.ToneWarning))
		}
		if r.runtime != "" {
			cold = appendSep(cold, styles.S(r.runtime, styles.ToneMuted))
		}
		lines = append(lines, field("cold start", cold))
	}
	return lines
}

// appendSep appends s to c, after a separator when c holds something already.
func appendSep(c cell, s styles.Span) cell {
	if len(c) > 0 {
		c = append(c, sep())
	}
	return append(c, s)
}

// formatStamp writes t to the millisecond, in local time, without the year when it is this year.
func formatStamp(t time.Time) string {
	t = t.Local()
	if t.Year() == time.Now().Year() {
		return t.Format("Jan 02 15:04:05.000")
	}
	return t.Format("2006-01-02 15:04:05.000")
}

// barWidth is the width of the bar of the memory an invocation used.
const barWidth = 10

// memoryBar is how much of its memory an invocation used, as a bar and a percentage: warning from
// 90%, danger once it used all of it, as one that ran out does.
func memoryBar(used, size int) cell {
	share := float64(used) / float64(max(size, 1))
	filled := min(barWidth, int(math.Round(share*barWidth)))
	if used > 0 {
		filled = max(filled, 1)
	}
	tone := styles.ToneBody
	switch {
	case used >= size:
		tone = styles.ToneDanger
	case share >= 0.9:
		tone = styles.ToneWarning
	}
	return cell{
		styles.S(strings.Repeat("█", filled), tone), styles.S(strings.Repeat("░", barWidth-filled), styles.ToneFaint),
		styles.S(fmt.Sprintf(" %d%%", int(math.Round(share*100))), styles.ToneFaint),
	}
}

// folded are the lines lambda writes of a request that the head of the invocation page says
// instead.
var folded = []string{"START RequestId:", "END RequestId:", "REPORT RequestId:", "INIT_START ", "INIT_REPORT ", "RESTORE_START ", "RESTORE_REPORT "}

// shownEvents are the lines of an invocation the page shows: all but the ones the head says and
// the blank ones.
func shownEvents(r request) []cli.LogEvent {
	out := make([]cli.LogEvent, 0, len(r.lines))
	for _, e := range r.lines {
		if strings.TrimSpace(e.Message) == "" || slices.ContainsFunc(folded, func(p string) bool { return strings.HasPrefix(e.Message, p) }) {
			continue
		}
		out = append(out, e)
	}
	return out
}

// gutter is how the lines of an invocation line up: the width of the widest offset from its
// start, and whether a column of levels is drawn, as when one of them names its level.
type gutter struct {
	offset int
	levels bool
}

// lead is the width before the message of a line.
func (g gutter) lead() int {
	if g.levels {
		return g.offset + 2 + levelWidth + 2
	}
	return g.offset + 2
}

// levelWidth is the width of the column of levels.
const levelWidth = 5

// maxMessageRows is the most rows a line of the log takes on the invocation page; the logs show
// a longer one whole.
const maxMessageRows = 12

// maxStackRows is the most rows the stack trace of a line takes.
const maxStackRows = 80

// formatOffset writes how long after the start of its request a line was logged: +12ms, or -150ms
// for a line of the init before it.
func formatOffset(d time.Duration) string {
	if d < 0 {
		return "-" + cli.Duration(-d)
	}
	return "+" + cli.Duration(d)
}

// invocationCache keeps what the invocation page read of the lines of a log, at any width, and
// the rows it drew of them, at one width, gutter and palette.
type invocationCache struct {
	read    map[cli.LogEvent]shownLine
	id      string
	width   int
	palette int
	g       gutter
	drawn   map[cli.LogEvent][]cell
}

// line is a line of the log as the page shows it, read once.
func (c *invocationCache) line(e cli.LogEvent, id string) shownLine {
	if c.read == nil || c.id != id || len(c.read) > 2*maxLogLines {
		c.read, c.id, c.drawn = map[cli.LogEvent]shownLine{}, id, nil
	}
	if l, ok := c.read[e]; ok {
		return l
	}
	l := readLogLine(e.Message, id)
	c.read[e] = l
	return l
}

// gutter is the gutter of the lines of r.
func (c *invocationCache) gutter(r request, shown []cli.LogEvent) gutter {
	var g gutter
	for _, e := range shown {
		g.offset = max(g.offset, len(formatOffset(e.Time.Sub(r.start))))
		g.levels = g.levels || c.line(e, r.id).level != ""
	}
	return g
}

// rows are the drawn rows of a line of r, from the cache while the width, the gutter and the
// palette are the ones it drew at.
func (c *invocationCache) rows(e cli.LogEvent, r request, g gutter, width int) []cell {
	l := c.line(e, r.id)
	if c.drawn == nil || c.width != width || c.g != g || c.palette != styles.Gen() {
		c.drawn, c.width, c.g, c.palette = map[cli.LogEvent][]cell{}, width, g, styles.Gen()
	}
	if rows, ok := c.drawn[e]; ok {
		return rows
	}
	rows := lineRows(l, g, width)
	c.drawn[e] = rows
	return rows
}

// offsetCell is the offset of a line in the gutter, right aligned, or as many spaces, for a line
// at the offset of the one above or a row under a line's first.
func offsetCell(offset string, g gutter) cell {
	return cell{styles.S(fmt.Sprintf("%*s  ", g.offset, offset), styles.ToneFaint)}
}

// frameDepth is how far a line of a stack trace is indented under its message: a call by two, the
// source under it by four.
func frameDepth(kind frameKind) int {
	switch kind {
	case frameCall:
		return 2
	case frameSource:
		return 4
	}
	return 0
}

// lineRows draws a line of the log of an invocation, right of the gutter of offsets: its level,
// what it says wrapped under itself, and the stack trace it holds below, the frames of the
// function's own code in the body ink and the rest faint, calls indented under the message, their
// source under them, and what wraps of a frame under it.
func lineRows(l shownLine, g gutter, width int) []cell {
	lead := g.lead() - g.offset - 2
	room := max(1, width-g.lead())
	first := make(cell, 0, 3)
	if g.levels {
		tone := levelTone(l.level)
		level := truncate(styles.Clean(l.level), levelWidth)
		level += strings.Repeat(" ", max(0, levelWidth-ansi.StringWidth(level)))
		first = cell{styles.Span{Text: level, Tone: tone, Bold: tone == styles.ToneDanger}, styles.S("  ", styles.ToneFaint)}
	}
	pad := func(n int) styles.Span { return styles.S(strings.Repeat(" ", n), styles.ToneFaint) }
	indents := func(kind frameKind) (depth, hang int) {
		if kind == notFrame || room < frameDepth(kind)+2+minColumnWidth {
			return 0, 0
		}
		return frameDepth(kind), 2
	}
	depth, hung := indents(l.frame.kind)
	parts := wrapCell(l.body, room-depth-hung)
	out := []cell{append(append(first, pad(depth)), parts[0]...)}
	shown := min(len(parts), maxMessageRows)
	for _, p := range parts[1:shown] {
		out = append(out, append(cell{pad(lead + depth + hung)}, p...))
	}
	if hidden := len(parts) - shown; hidden > 0 {
		out = append(out, cell{pad(lead), styles.S(truncate("… "+plural(hidden, "more line")+" of this one: the logs tab shows it whole", room), styles.ToneFaint)})
	}
	stack := stackLines(l.more, l.block)
	rows := 0
	for i, line := range l.more {
		if rows >= maxStackRows {
			out = append(out, cell{pad(lead), styles.S(truncate("… "+plural(len(l.more)-i, "more line")+": the logs tab shows them all", room), styles.ToneFaint)})
			break
		}
		c := tint(styles.Clean(line))
		if stack[i].kind != notFrame {
			c = text(strings.TrimLeft(line, " \t"), stack[i].tone())
		}
		depth, hung := indents(stack[i].kind)
		for j, p := range wrapCell(c, room-depth-hung) {
			at := lead + depth
			if j > 0 {
				at += hung
			}
			out = append(out, append(cell{pad(at)}, p...))
			rows++
		}
	}
	return out
}
