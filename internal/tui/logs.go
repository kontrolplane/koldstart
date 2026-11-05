package tui

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/kontrolplane/koldstart/internal/cli"
	"github.com/kontrolplane/koldstart/internal/tui/styles"
)

// logsSince is how far back the logs start when a function is opened, and olderStep how much
// further back each read of older lines goes.
const (
	logsSince = 30 * time.Minute
	olderStep = time.Hour
)

// maxLogLines bounds the lines kept. Reading newer lines drops the oldest, and reading older
// ones the newest.
const maxLogLines = 5000

// stampWidth is the width of the time in front of a line of the logs, with the gap after it.
const stampWidth = 14

// logState is the logs of the function on screen: the lines read, whether the newest are
// followed, as a live tail brings them or by reading again, and how far back they go.
type logState struct {
	gen    int
	loaded bool
	// err is why the first read of the logs failed.
	err   string
	lines []cli.LogEvent
	// from is the start of the oldest window read; lines before it are not read yet.
	from time.Time
	// follow keeps the newest line in sight; scroll is how many lines the view is above it.
	follow bool
	scroll int
	filter filterBox

	tailGen  int
	tailing  bool
	noTail   bool
	stopTail context.CancelFunc

	olderGen     int
	loadingOlder bool
	// emptyOlder counts the reads of older lines in a row that found none.
	emptyOlder int
}

func newLogState() logState {
	return logState{follow: true, filter: newFilterBox("filter the lines…")}
}

func (m model) applyLogs(msg logsLoadedMsg) (model, tea.Cmd) {
	f := &m.state.function
	l := &f.logs
	switch {
	case msg.how == logsPrepend && msg.gen != l.olderGen, msg.how != logsPrepend && msg.gen != l.gen:
		return m, nil
	}
	if msg.how == logsPrepend {
		l.loadingOlder = false
	}
	if msg.err != nil {
		if !l.loaded && msg.how != logsPrepend {
			l.err = msg.err.Error()
		}
		return m.loadError("could not read the logs of "+f.name, msg.err, l.loaded)
	}
	events, from := readWindow(msg.events, msg.from)
	switch msg.how {
	case logsAppend:
		l.lines = appendNew(l.lines, events)
		l.trimOldest()
	case logsPrepend:
		l.from = from
		if len(events) == 0 {
			l.emptyOlder++
		} else {
			l.emptyOlder = 0
		}
		l.lines = append(slices.Clone(events), l.lines...)
		dropped := l.trimNewest()
		l.scroll = max(0, l.scroll-len(m.logRows(dropped)))
	default:
		l.lines, l.from = events, from
		l.trimOldest()
	}
	l.loaded, l.err = true, ""
	if m.page == functionPage {
		m = m.markRead()
	}
	return m.updateRequests(), nil
}

// readWindow is what a read of the logs from from brings, and where the window it covers starts.
// A read that hit cli.MaxLogEvents kept its newest lines, so the window starts after the
// millisecond of its first line, whose other lines may be cut, and a read of the lines before
// takes them.
func readWindow(events []cli.LogEvent, from time.Time) ([]cli.LogEvent, time.Time) {
	if len(events) < cli.MaxLogEvents {
		return events, from
	}
	first := events[0].Time
	i := 0
	for i < len(events) && !events[i].Time.After(first) {
		i++
	}
	return events[i:], first.Add(time.Millisecond)
}

// trimOldest keeps the newest maxLogLines lines, and the rest of the millisecond the oldest kept
// is in, so a read of the lines before from takes what was dropped.
func (l *logState) trimOldest() {
	if len(l.lines) <= maxLogLines {
		return
	}
	cut := l.lines[len(l.lines)-maxLogLines].Time
	i := 0
	for i < len(l.lines) && l.lines[i].Time.Before(cut) {
		i++
	}
	l.lines, l.from = slices.Delete(l.lines, 0, i), cut
}

// trimNewest keeps the oldest maxLogLines lines, and the rest of the millisecond the newest kept
// is in, so following again reads what was dropped. It returns the lines it dropped.
func (l *logState) trimNewest() []cli.LogEvent {
	if len(l.lines) <= maxLogLines {
		return nil
	}
	cut := l.lines[maxLogLines-1].Time
	i := maxLogLines
	for i < len(l.lines) && !l.lines[i].Time.After(cut) {
		i++
	}
	dropped := slices.Clone(l.lines[i:])
	l.lines = l.lines[:i]
	return dropped
}

// appendNew adds the lines that are not on screen yet, as a tail and a read can bring the same.
// It compares them with every line on screen as new as the oldest of them.
func appendNew(have, lines []cli.LogEvent) []cli.LogEvent {
	if len(have) == 0 {
		return slices.Clone(lines)
	}
	if len(lines) == 0 {
		return have
	}
	oldest := slices.MinFunc(lines, func(a, b cli.LogEvent) int { return a.Time.Compare(b.Time) }).Time
	seen := map[cli.LogEvent]bool{}
	for i := len(have) - 1; i >= 0 && !have[i].Time.Before(oldest); i-- {
		seen[have[i]] = true
	}
	out := slices.Clip(have)
	for _, e := range lines {
		if !seen[e] {
			out = append(out, e)
		}
	}
	slices.SortStableFunc(out, func(a, b cli.LogEvent) int { return a.Time.Compare(b.Time) })
	return out
}

// applyTail adds what the live tail sent. Where no live tail can run, as on an emulator, or it
// stopped on a bug, the logs are read again on every refresh instead.
func (m model) applyTail(msg tailMsg) (model, tea.Cmd) {
	l := &m.state.function.logs
	if msg.gen != l.tailGen {
		return m, nil
	}
	if msg.ended {
		l.tailing, l.stopTail = false, nil
		if _, bug := errors.AsType[*panicError](msg.err); bug {
			l.noTail = true
			return m.setStatus("the live tail of the logs failed: "+msg.err.Error(), styles.ToneDanger)
		}
		if errors.Is(msg.err, cli.ErrNoLiveTail) {
			l.noTail = true
		}
		return m, nil
	}
	if l.follow {
		l.lines = appendNew(l.lines, msg.lines)
		l.trimOldest()
		m = m.updateRequests()
	}
	return m, waitFor(msg.ch)
}

// refreshLogs reads the lines that came since the newest on screen.
func (m model) refreshLogs() (model, tea.Cmd) {
	f := m.state.function
	if f.logGroup == "" || !f.logs.follow {
		return m, nil
	}
	if len(f.logs.lines) == 0 {
		return m.loadLogs(time.Now().Add(-logsSince), time.Time{}, logsReplace)
	}
	return m.loadLogs(f.logs.lines[len(f.logs.lines)-1].Time.Add(time.Millisecond), time.Time{}, logsAppend)
}

// followLogs keeps the newest line in sight again, reading what came while it was not.
func (m model) followLogs() (model, tea.Cmd) {
	l := &m.state.function.logs
	l.follow, l.scroll = true, 0
	m, cmd := m.refreshLogs()
	m, tail := m.startTail()
	return m, tea.Batch(cmd, tail)
}

// loadOlder reads the hour of logs before the oldest window read.
func (m model) loadOlder() (model, tea.Cmd) {
	l := m.state.function.logs
	if l.loadingOlder || m.state.function.logGroup == "" || l.from.IsZero() {
		return m, nil
	}
	return m.loadLogs(l.from.Add(-olderStep), l.from.Add(-time.Millisecond), logsPrepend)
}

func (m model) logsUpdate(msg tea.KeyPressMsg) (model, tea.Cmd) {
	l := &m.state.function.logs
	switch {
	case key.Matches(msg, m.keys.Filter):
		return m, l.filter.open()
	case key.Matches(msg, m.keys.Follow):
		if l.follow {
			l.follow = false
			return m.stopTail(), nil
		}
		return m.followLogs()
	case key.Matches(msg, m.keys.Bottom) && !l.follow:
		return m.followLogs()
	}
	page := functionBodyHeight()
	want := l.scroll + 2*page + 2
	if key.Matches(msg, m.keys.Top) {
		want = math.MaxInt
	}
	rows, _ := m.lastRows(want)
	top := max(0, len(rows)+1-page)
	before := min(l.scroll, top)
	switch {
	case key.Matches(msg, m.keys.Up):
		l.scroll = before + 1
	case key.Matches(msg, m.keys.Down):
		l.scroll = before - 1
	case key.Matches(msg, m.keys.PageUp):
		l.scroll = before + page
	case key.Matches(msg, m.keys.PageDown):
		l.scroll = before - page
	case key.Matches(msg, m.keys.Top):
		l.scroll = top
	case key.Matches(msg, m.keys.Bottom):
		l.scroll = 0
	default:
		return m, nil
	}
	l.scroll = max(0, l.scroll)
	if l.scroll > 0 {
		l.follow = false
	}
	if l.scroll > top {
		l.scroll = top
		if before == top && key.Matches(msg, m.keys.Up, m.keys.PageUp) {
			return m.loadOlder()
		}
	}
	return m, nil
}

// logRow is one drawn line of the logs, before it is painted: a line of the message of an event,
// the first with the event's time in front of it.
type logRow struct {
	at    time.Time
	first bool
	c     cell
}

// logWidth is the width the lines of the logs are drawn in.
func logWidth() int { return contentWidth - 4 }

// logRows are the drawn lines of the events the filter keeps, tinted, with what it looks for
// marked, and wrapped to the width.
func (m model) logRows(events []cli.LogEvent) []logRow {
	l := m.state.function.logs
	var out []logRow
	for _, e := range events {
		if l.filter.matches(e.Message) {
			out = append(out, entryRows(e, l.filter.text, logWidth())...)
		}
	}
	return out
}

// lastRows are the last n drawn lines of the logs, or more to finish the line of an event, and
// whether they are all of them. Only what is in sight is tinted, so a long log draws quickly.
func (m model) lastRows(n int) ([]logRow, bool) {
	l := m.state.function.logs
	var chunks [][]logRow
	count, i := 0, len(l.lines)-1
	for ; i >= 0 && count < n; i-- {
		if e := l.lines[i]; l.filter.matches(e.Message) {
			rows := entryRows(e, l.filter.text, logWidth())
			chunks = append(chunks, rows)
			count += len(rows)
		}
	}
	out := make([]logRow, 0, count)
	for _, rows := range slices.Backward(chunks) {
		out = append(out, rows...)
	}
	return out, i < 0
}

// entryRows are the drawn lines of one event, where it holds mark marked.
func entryRows(e cli.LogEvent, mark string, width int) []logRow {
	cells := markedLogCells(e.Message, mark, width-stampWidth)
	out := make([]logRow, len(cells))
	for i, c := range cells {
		out[i] = logRow{at: e.Time, first: i == 0, c: c}
	}
	return out
}

// paint draws a row in width columns: the time, or the indent under it, and the row's spans.
func (r logRow) paint(width int) string {
	stamp := strings.Repeat(" ", stampWidth)
	if r.first {
		stamp = styles.Faint(r.at.Local().Format("15:04:05.000")) + "  "
	}
	return stamp + renderCell(r.c, column{width: width - stampWidth}, nil)
}

// logEntry draws one line of the logs: its time, and its message tinted and wrapped beside it.
func logEntry(e cli.LogEvent, width int) []string {
	rows := entryRows(e, "", width)
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.paint(width)
	}
	return out
}

// logsHead says how far back the logs go, how many lines the filter keeps, and what reads the
// lines before.
func (m model) logsHead() string {
	l := m.state.function.logs
	var parts []string
	if l.filter.text != "" {
		kept := 0
		for _, e := range l.lines {
			if l.filter.matches(e.Message) {
				kept++
			}
		}
		parts = append(parts, fmt.Sprintf("%s of %s lines", formatInt(kept), formatInt(len(l.lines))))
	}
	switch {
	case l.loadingOlder:
		parts = append(parts, "reading the hour before "+formatTime(l.from)+"…")
	case l.emptyOlder > 0:
		parts = append(parts, "nothing logged in "+hoursBefore(l.emptyOlder)+" "+formatTime(l.from.Add(time.Duration(l.emptyOlder)*olderStep)), "↑ reads further back")
	default:
		parts = append(parts, "from "+formatTime(l.from), "↑ at the top reads the hour before")
	}
	head := styles.Faint(strings.Join(parts, " · "))
	if l.loadingOlder {
		head = styles.Accent(m.spinner.View()) + " " + head
	}
	return head
}

// hoursBefore names n steps of older lines: the hour before, the 3 hours before.
func hoursBefore(n int) string {
	if n == 1 {
		return "the hour before"
	}
	return fmt.Sprintf("the %d hours before", n)
}

// logsError says why the logs could not be read, and how to try again.
func (m model) logsError() string {
	return strings.Join(cellLines(m.couldNotRead("the logs", m.state.function.logs.err, styles.ToneDanger), logWidth()), "\n ")
}

func (m model) logsView() string {
	f := m.state.function
	l := f.logs
	switch {
	case f.logGroup == "":
		return " " + styles.Muted("no log group: deploy the function first")
	case !l.loaded && l.err != "":
		return " " + m.logsError()
	case !l.loaded:
		return " " + styles.Muted(readingNote("the logs"))
	}
	w := m.logsWindow()
	if w.all && len(w.rows) == 0 {
		empty := fmt.Sprintf("nothing logged since %s", formatTime(l.from))
		if l.filter.text != "" {
			empty = noMatch("line", styles.Clean(l.filter.text))
		}
		return lipgloss.NewStyle().PaddingLeft(1).Render(m.logsHead() + "\n\n" + styles.Muted(empty))
	}
	lines := make([]string, 0, w.end-w.start)
	for i := w.start; i < w.end; i++ {
		if i < w.head {
			lines = append(lines, m.logsHead())
			continue
		}
		lines = append(lines, w.rows[i-w.head].paint(logWidth()))
	}
	return lipgloss.NewStyle().PaddingLeft(1).Render(strings.Join(lines, "\n"))
}

// logsWindow is what the logs tab has in sight: the last drawn rows, a head line above them once
// they are all of them, and the lines from start to end of the head and the rows.
type logsWindow struct {
	rows       []logRow
	all        bool
	head       int
	start, end int
}

// logsWindow works out the lines of the logs in sight at the scroll, the newest at the bottom.
func (m model) logsWindow() logsWindow {
	l := m.state.function.logs
	height := functionBodyHeight()
	w := logsWindow{}
	w.rows, w.all = m.lastRows(l.scroll + height)
	if w.all {
		w.head = 1
	}
	total := len(w.rows) + w.head
	w.end = total - min(l.scroll, max(0, total-height))
	w.start = max(0, w.end-height)
	return w
}

// logsPosition is the lines of the logs in sight, counted as the tab counts them, a line for each
// event the filter keeps, however many rows it wraps to.
func (m model) logsPosition() string {
	f := m.state.function
	l := f.logs
	if f.logGroup == "" || !l.loaded {
		return ""
	}
	total := len(l.lines)
	if l.filter.text != "" {
		total = 0
		for _, e := range l.lines {
			if l.filter.matches(e.Message) {
				total++
			}
		}
	}
	w := m.logsWindow()
	from, to := max(0, w.start-w.head), max(0, w.end-w.head)
	below, shown := 0, 0
	for _, r := range w.rows[to:] {
		if r.first {
			below++
		}
	}
	for i, r := range w.rows[from:to] {
		if r.first || i == 0 {
			shown++
		}
	}
	return scrollCue(total-below-shown, shown, total)
}
