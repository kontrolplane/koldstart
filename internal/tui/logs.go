package tui

import (
	"context"
	"errors"
	"fmt"
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

// maxLogLines bounds the lines kept; the oldest go first when new ones come.
const maxLogLines = 5000

// logState is the logs of the function on screen: the lines read, whether the newest are
// followed, as a live tail brings them or by reading again, and how far back they go.
type logState struct {
	gen    int
	loaded bool
	lines  []cli.LogEvent
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
		return m.loadError("could not read the logs of "+f.name, msg.err, l.loaded)
	}
	switch msg.how {
	case logsAppend:
		l.lines = appendNew(l.lines, msg.events)
	case logsPrepend:
		l.from = msg.from
		if len(msg.events) == 0 {
			l.emptyOlder++
		} else {
			l.emptyOlder = 0
		}
		l.lines = append(slices.Clone(msg.events), l.lines...)
	default:
		l.lines, l.from = msg.events, msg.from
	}
	if len(l.lines) > maxLogLines {
		l.lines = slices.Delete(l.lines, 0, len(l.lines)-maxLogLines)
	}
	l.loaded = true
	if m.page == functionPage {
		m.refreshedAt = time.Now()
	}
	return m.updateRequests(), nil
}

// appendNew adds the lines that are not on screen yet, as a tail and a read can bring the same.
func appendNew(have, lines []cli.LogEvent) []cli.LogEvent {
	if len(have) == 0 {
		return slices.Clone(lines)
	}
	seen := map[cli.LogEvent]bool{}
	for _, e := range have[max(0, len(have)-500):] {
		seen[e] = true
	}
	out := have
	for _, e := range lines {
		if !seen[e] {
			out = append(out, e)
		}
	}
	slices.SortStableFunc(out, func(a, b cli.LogEvent) int { return a.Time.Compare(b.Time) })
	return out
}

// applyTail adds what the live tail sent. Where no live tail can run, as on an emulator, the logs
// are read again on every refresh instead.
func (m model) applyTail(msg tailMsg) (model, tea.Cmd) {
	l := &m.state.function.logs
	if msg.gen != l.tailGen {
		return m, nil
	}
	if msg.ended {
		l.tailing, l.stopTail = false, nil
		if errors.Is(msg.err, cli.ErrNoLiveTail) {
			l.noTail = true
		}
		return m, nil
	}
	if l.follow {
		l.lines = appendNew(l.lines, msg.lines)
		if len(l.lines) > maxLogLines {
			l.lines = slices.Delete(l.lines, 0, len(l.lines)-maxLogLines)
		}
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
		l.follow = !l.follow
		if !l.follow {
			return m.stopTail(), nil
		}
		l.scroll = 0
		m, cmd := m.refreshLogs()
		m, tail := m.startTail()
		return m, tea.Batch(cmd, tail)
	}
	lines := len(m.logLines())
	page := functionBodyHeight()
	top := max(0, lines-page)
	before := l.scroll
	switch {
	case key.Matches(msg, m.keys.Up):
		l.scroll++
	case key.Matches(msg, m.keys.Down):
		l.scroll--
	case key.Matches(msg, m.keys.PageUp):
		l.scroll += page
	case key.Matches(msg, m.keys.PageDown):
		l.scroll -= page
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

// logLines are the lines of the logs as they are drawn: tinted, wrapped to the width, and only
// the ones the filter matches.
func (m model) logLines() []string {
	l := m.state.function.logs
	width := contentWidth - 4
	var out []string
	for _, e := range l.lines {
		if !l.filter.matches(e.Message) {
			continue
		}
		out = append(out, logEntry(e, width)...)
	}
	return out
}

// logEntry draws one line of the logs: its time, and its message tinted and wrapped under it.
func logEntry(e cli.LogEvent, width int) []string {
	stamp := styles.Faint(e.Time.Local().Format("15:04:05.000")) + "  "
	cells := logCells(e.Message, width-14)
	out := make([]string, 0, len(cells))
	for i, c := range cells {
		if i > 0 {
			stamp = strings.Repeat(" ", 14)
		}
		out = append(out, stamp+renderCell(c, column{width: width - 14}, nil))
	}
	return out
}

// logsHead says how far back the logs go, and what reads the lines before.
func (m model) logsHead() string {
	l := m.state.function.logs
	switch {
	case l.loadingOlder:
		return styles.Accent(m.spinner.View()) + " " + styles.Muted("reading the hour before "+formatTime(l.from)+"…")
	case l.emptyOlder > 0:
		return styles.Faint(fmt.Sprintf("nothing logged in the %s before %s · ↑ reads further back", time.Duration(l.emptyOlder)*olderStep, formatTime(l.from.Add(time.Duration(l.emptyOlder)*olderStep))))
	}
	return styles.Faint("from " + formatTime(l.from) + " · ↑ at the top reads the hour before")
}

func (m model) logsView() string {
	f := m.state.function
	l := f.logs
	height := functionBodyHeight()
	switch {
	case f.logGroup == "":
		return " " + styles.Muted("no log group: deploy the function first")
	case !l.loaded:
		return " " + styles.Muted("reading the logs…")
	}
	lines := m.logLines()
	if len(lines) == 0 {
		empty := fmt.Sprintf("nothing logged since %s", formatTime(l.from))
		if l.filter.text != "" {
			empty = "no line has " + l.filter.text
		}
		return lipgloss.NewStyle().PaddingLeft(1).Render(m.logsHead() + "\n\n" + styles.Muted(empty))
	}
	end := max(0, len(lines)-l.scroll)
	start := max(0, end-height)
	if start == 0 {
		lines = append([]string{m.logsHead()}, lines...)
		end++
		start = max(0, end-height)
	}
	return lipgloss.NewStyle().PaddingLeft(1).Render(strings.Join(lines[start:end], "\n"))
}
