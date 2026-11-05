package tui

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/kontrolplane/koldstart/internal/tui/styles"
)

var invocationColumns = []column{
	{title: "time", width: 12},
	{title: "request", width: 10, grow: 1, drop: 2},
	{title: "duration", width: 10, right: true},
	{title: "billed", width: 9, right: true, drop: 3},
	{title: "memory", width: 13, right: true, drop: 1},
	{title: "cold start", width: 10, right: true, drop: 4},
	{title: "", width: 12},
}

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

// requestRow draws an invocation. One without a report is running while it is within the
// function's timeout, and has no report after it, as on an emulator that writes none.
func requestRow(r request, timeout time.Duration) tableRow {
	status := text("✓ ok", styles.ToneSuccess)
	switch {
	case r.timedOut:
		status = text("✗ timed out", styles.ToneDanger)
	case r.failed:
		status = text("✗ failed", styles.ToneDanger)
	case !r.reported && time.Since(r.last) < timeout+5*time.Second:
		status = text("● running", styles.ToneWarning)
	case !r.reported:
		status = text("no report", styles.ToneFaint)
	}
	memory := text("-", styles.ToneFaint)
	if r.memory > 0 {
		memory = cell{styles.S(formatInt(r.maxMemory), styles.ToneBody), styles.S("/"+formatInt(r.memory)+" mb", styles.ToneFaint)}
	}
	cold := text("", styles.ToneFaint)
	if r.init > 0 {
		cold = text(formatMillis(r.init), styles.ToneWarning)
	}
	duration := text("-", styles.ToneFaint)
	if r.reported {
		duration = text(formatMillis(r.duration), styles.ToneBody)
	}
	billed := text("-", styles.ToneFaint)
	if r.billed > 0 {
		billed = text(formatMillis(r.billed), styles.ToneMuted)
	}
	return tableRow{text(r.start.Local().Format("15:04:05.000"), styles.ToneMuted), text(r.id, styles.ToneFaint), duration, billed, memory, cold, status}
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
	return indent(f.invocations.View())
}

// invocationState is one invocation of the function, with the lines of the log it wrote.
type invocationState struct {
	req    request
	scroll int
}

func (m model) openInvocation(r request) (model, tea.Cmd) {
	m.state.invocation = invocationState{req: r}
	return m.switchPage(invocationPage), nil
}

func (m model) invocationUpdate(msg tea.KeyPressMsg) (model, tea.Cmd) {
	v := &m.state.invocation
	switch {
	case key.Matches(msg, m.keys.Back, m.keys.Quit):
		return m.switchPage(functionPage), nil
	case key.Matches(msg, m.keys.Copy):
		return m.copy(v.req.id)
	}
	v.scroll = min(scrollBy(msg, m.keys, v.scroll, contentHeight), max(0, len(m.invocationLines())-contentHeight))
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

func (m model) invocationLines() []string {
	r := m.shownRequest()
	width := contentWidth - 4
	row := requestRow(r, time.Duration(m.state.function.inputs.Timeout)*time.Second)
	lines := []string{
		styles.SectionHeader("request "+r.id, width),
		renderCell(append(row[6], styles.S("  at "+formatTime(r.start), styles.ToneFaint)), column{width: width}, nil),
	}
	facts := []string{}
	if r.reported {
		facts = append(facts, "duration "+formatMillis(r.duration), "billed "+formatMillis(r.billed), fmt.Sprintf("memory %d of %d mb", r.maxMemory, r.memory))
	}
	if r.init > 0 {
		facts = append(facts, "cold start "+formatMillis(r.init))
	}
	if len(facts) > 0 {
		lines = append(lines, styles.Muted(strings.Join(facts, " · ")))
	}
	lines = append(lines, "", styles.SectionHeader("log", width))
	for _, e := range r.lines {
		lines = append(lines, logEntry(e, width)...)
	}
	return lines
}

func (m model) invocationView() string {
	return indent(scrolled(m.invocationLines(), m.state.invocation.scroll, contentHeight))
}
