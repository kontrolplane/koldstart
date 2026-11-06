package tui

import (
	"encoding/json"
	"maps"
	"os/exec"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/kontrolplane/koldstart/internal/cli"
	"github.com/kontrolplane/koldstart/internal/desired"
	"github.com/kontrolplane/koldstart/internal/engine"
	"github.com/kontrolplane/koldstart/internal/provider/awsp"
	"github.com/kontrolplane/koldstart/internal/state"
	"github.com/kontrolplane/koldstart/internal/tui/keys"
	"github.com/kontrolplane/koldstart/internal/tui/styles"
)

type functionTab int

const (
	overviewTab functionTab = iota
	logsTab
	invocationsTab
	invokeTab
)

var functionTabs = []string{"overview", "logs", "invocations", "invoke"}

// functionState is one function of the stage: what it is, what lambda says of it now and how it
// did in the last hour, its logs and the invocations in them, and invoking it.
type functionState struct {
	name        string
	lambdaName  string
	logGroup    string
	logGroupARN string
	arn         string
	url         string
	inputs      awsp.FunctionInputs
	env         map[string]any
	invokers    []invoker
	// roleID is the id of the function's role in the stage; role is what koldstart made it with,
	// nil when the function's role is not koldstart's.
	roleID string
	role   *awsp.RoleInputs
	// gone is set once the stage's state no longer has the function.
	gone   bool
	tab    functionTab
	scroll int
	// cursor is the row of the overview under the cursor.
	cursor int

	liveGen      int
	liveAnswered int
	live         *cli.FunctionState
	// liveAt is when live was read; liveErr is why the last read failed.
	liveAt  time.Time
	liveErr string

	logs logState

	invocations dataTable
	requests    []request

	events     dataTable
	eventFiles []string
	// eventErrs are why each event cannot be sent, nil for one that can, as they were listed.
	eventErrs []error
	invokeGen int
	invoking  bool
	results   map[string]invocationResult
}

func functionBodyHeight() int { return contentHeight - 2 }

// openFunction shows a function of the stage, its overview first, and reads what lambda says of
// it and its logs.
func (m model) openFunction(name string) (model, tea.Cmd) {
	f := &m.state.function
	if f.name != name {
		*f = functionState{
			name: name, results: map[string]invocationResult{},
			events:      newTable(eventColumns, ""),
			invocations: newTable(invocationColumns, "no invocations in the logs read: ↑ on the logs tab reads further back"),
			logs:        newLogState(),
		}
		f.events.setSize(eventsWidth(), functionBodyHeight()-tableHeaderRows)
		f.invocations.setSize(tableWidth(), functionBodyHeight()-tableHeaderRows)
	}
	m = m.switchPage(functionPage).syncFunction()
	m = m.readEventFiles()
	var cmds []tea.Cmd
	var cmd tea.Cmd
	if f.lambdaName != "" {
		m, cmd = m.loadFunctionLive()
		cmds = append(cmds, cmd)
	}
	if f.logGroup != "" {
		m, cmd = m.loadLogs(time.Now().Add(-logsSince), time.Time{}, logsReplace)
		cmds = append(cmds, cmd)
		m, cmd = m.startTail()
		cmds = append(cmds, cmd)
	}
	return m, tea.Batch(cmds...)
}

// leaveFunction goes back to the stage, ending the live tail of the logs.
func (m model) leaveFunction() (model, tea.Cmd) {
	return m.stopTail().openStage(m.state.stage.ref)
}

// syncFunction takes what the stage's state says of the function.
func (m model) syncFunction() model {
	f := &m.state.function
	st := m.state.stage.st
	if st == nil {
		return m
	}
	fn := st.Resources[desired.FunctionID(f.name)]
	f.gone = fn == nil
	if fn == nil {
		return m
	}
	f.lambdaName, f.arn = fn.PhysicalID, fn.Attributes["Arn"]
	f.inputs = functionInputs(fn)
	var in struct {
		Environment map[string]any `json:"environment"`
	}
	_ = json.Unmarshal(fn.Inputs, &in)
	f.env = in.Environment
	if g := st.Resources[desired.LogGroupID(f.name)]; g != nil {
		f.logGroup, f.logGroupARN = g.PhysicalID, g.Attributes["Arn"]
	}
	f.url = ""
	if u := st.Resources[desired.URLID(f.name)]; u != nil {
		f.url = u.Attributes["FunctionUrl"]
	}
	f.invokers = invokersOf(st, f.name)
	f.roleID, f.role = desired.RoleID(f.name), nil
	if r := st.Resources[f.roleID]; r != nil {
		var in awsp.RoleInputs
		_ = json.Unmarshal(r.Inputs, &in)
		f.role = &in
	}
	return m
}

func (m model) applyFunctionLive(msg functionLiveMsg) (model, tea.Cmd) {
	f := &m.state.function
	if msg.gen != f.liveGen {
		return m, nil
	}
	f.liveAnswered = msg.gen
	if msg.err != nil {
		f.liveErr = msg.err.Error()
		return m, nil
	}
	live := msg.live
	f.live, f.liveAt, f.liveErr = &live, time.Now(), ""
	m.state.stage.markDrift(desired.FunctionID(f.name), codeChanged(f.inputs, live) || len(configDrift(f.inputs, live)) > 0)
	return m.updateStageTables(), nil
}

// refreshFunction reads the function again, its state, what lambda says, and the new lines of
// its logs when no live tail brings them.
//
// On a tick, what is still being read since the last refresh is not asked for again: a read
// slower than the refresh would otherwise be dropped as stale every time, and never land.
func (m model) refreshFunction(tick bool) (model, tea.Cmd) {
	var cmds []tea.Cmd
	var cmd tea.Cmd
	s, f := m.state.stage, m.state.function
	if !tick || s.gen == s.answered {
		m, cmd = m.loadStage()
		cmds = append(cmds, cmd)
	}
	if f.lambdaName != "" && (!tick || f.liveGen == f.liveAnswered) {
		m, cmd = m.loadFunctionLive()
		cmds = append(cmds, cmd)
	}
	m, cmd = m.loadMetrics()
	cmds = append(cmds, cmd)
	if !f.logs.tailing && (!tick || f.logs.gen == f.logs.answered) {
		m, cmd = m.refreshLogs()
		cmds = append(cmds, cmd)
	}
	return m, tea.Batch(cmds...)
}

func (m model) functionUpdate(msg tea.KeyPressMsg) (model, tea.Cmd) {
	f := &m.state.function
	if f.tab == logsTab && f.logs.filter.active {
		changed, cmd := f.logs.filter.update(msg, m.keys)
		if changed {
			f.logs.scroll = 0
		}
		return m, cmd
	}
	switch {
	case key.Matches(msg, m.keys.Back):
		if f.tab == logsTab && f.logs.filter.clear() {
			return m, nil
		}
		return m.leaveFunction()
	case key.Matches(msg, m.keys.Quit):
		return m.leaveFunction()
	case key.Matches(msg, m.keys.NextTab, m.keys.Right):
		f.tab, f.scroll = (f.tab+1)%functionTab(len(functionTabs)), 0
		return m, nil
	case key.Matches(msg, m.keys.PrevTab, m.keys.Left):
		f.tab, f.scroll = (f.tab+functionTab(len(functionTabs))-1)%functionTab(len(functionTabs)), 0
		return m, nil
	case key.Matches(msg, m.keys.Invoke):
		return m.startInvoke(false)
	case key.Matches(msg, m.keys.RunLocal):
		return m.startInvoke(true)
	case key.Matches(msg, m.keys.EditEvent):
		return m.editEvent()
	case key.Matches(msg, m.keys.Deploy):
		return m.planDeploy(m.state.stage.ref, f.name)
	case key.Matches(msg, m.keys.Refresh):
		return m.refreshFunction(false)
	case key.Matches(msg, m.keys.Copy):
		return m.copy(m.functionCopyValue())
	}
	switch f.tab {
	case logsTab:
		return m.logsUpdate(msg)
	case invocationsTab:
		return m.invocationsUpdate(msg)
	case invokeTab:
		return m.invokeUpdate(msg)
	}
	return m.overviewKey(msg)
}

// scrollBy moves a scroll offset by the key: a line, a page, or to the top or the bottom, which is
// most, and keeps it between 0 and most.
func scrollBy(msg tea.KeyPressMsg, k keys.KeyMap, offset, page, most int) int {
	switch {
	case key.Matches(msg, k.Up):
		offset--
	case key.Matches(msg, k.Down):
		offset++
	case key.Matches(msg, k.PageUp):
		offset -= page
	case key.Matches(msg, k.PageDown):
		offset += page
	case key.Matches(msg, k.Top):
		offset = 0
	case key.Matches(msg, k.Bottom):
		offset = most
	}
	return max(0, min(offset, most))
}

// maxOffset is the furthest n lines scroll in height: to where the last is at the bottom.
func maxOffset(n, height int) int { return max(0, n-height) }

// functionCopyValue is what c copies: the whole value of the row under the cursor on the
// overview, the request id of an invocation on the invocations tab, the payload of the last
// invocation on the invoke tab, the arn elsewhere.
func (m model) functionCopyValue() string {
	f := m.state.function
	switch f.tab {
	case overviewTab:
		if r, ok := m.functionOverview().selected(f.cursor); ok {
			return r.copy
		}
	case invocationsTab:
		if i := f.invocations.cursor; i < len(f.requests) {
			return f.requests[i].id
		}
	case invokeTab:
		if r, ok := f.results[m.selectedEvent()]; ok {
			return string(r.inv.Payload)
		}
	}
	return cmpOr(f.arn, f.lambdaName)
}

func (m model) functionView() string {
	f := m.state.function
	var body string
	switch f.tab {
	case overviewTab:
		body = m.overviewTabView()
	case logsTab:
		body = m.logsView()
	case invocationsTab:
		body = m.invocationsView()
	case invokeTab:
		body = m.invokeView()
	}
	counts := []string{"", formatInt(len(f.logs.lines)), formatInt(len(f.requests)), ""}
	if !f.logs.loaded {
		counts[logsTab], counts[invocationsTab] = "", ""
	}
	return indent(tabBar(functionTabs, counts, int(f.tab))) + "\n\n" + body
}

// functionPosition is where the function's tab is: the lines in sight of the overview, the logs
// and what an invocation returned, or the invocation under the cursor.
func (m model) functionPosition() string {
	f := m.state.function
	switch f.tab {
	case overviewTab:
		o := m.functionOverview()
		return scrollCue(o.follow(f.cursor, f.scroll), o.height(), o.lines())
	case logsTab:
		return m.logsPosition()
	case invocationsTab:
		if f.logs.loaded {
			return f.invocations.position()
		}
	case invokeTab:
		height := functionBodyHeight()
		n := m.invokeLines(resultWidth()).len()
		return scrollCue(min(f.scroll, maxResultScroll(n, height)), height, n)
	}
	return ""
}

const labelWidth = 14

// row renders a label right aligned next to its value, cut to width.
func row(label string, value cell, width int) string {
	return rowIn(labelWidth, label, value, width)
}

// rowIn is row with labels as wide as labels, for the names of a resource's attributes, which
// run longer than the labels koldstart writes.
func rowIn(labels int, label string, value cell, width int) string {
	l := styles.Fg(styles.ToneFaint).Width(labels).Align(lipgloss.Right).PaddingRight(2).Render(truncate(label, labels-2))
	return l + renderCell(value, column{width: max(1, width-labels)}, nil)
}

// labelsFor is how wide labels for names are drawn: the longest with its padding, at least
// labelWidth and at most half of width.
func labelsFor(names []string, width int) int {
	w := labelWidth
	for _, k := range names {
		w = max(w, ansi.StringWidth(k)+2)
	}
	return max(labelWidth, min(w, width/2))
}

func sep() styles.Span { return styles.S(" · ", styles.ToneFaint) }

// wrapped breaks text into lines of width in one tone, as cellLines does. With a limit above 0
// there are at most limit of them, the last ending in an ellipsis when there was more.
func wrapped(s string, tone styles.Tone, width, limit int) []string {
	width = max(2, width)
	lines := cellLines(text(s, tone), width)
	if limit > 0 && len(lines) > limit {
		lines = lines[:limit]
		last := strings.TrimRight(ansi.Strip(lines[limit-1]), " ")
		lines[limit-1] = styles.Fg(tone).Render(ansi.Truncate(last, width-1, "") + "…")
	}
	return lines
}

// cutMiddle cuts plain text to width columns in the middle, keeping its start and more of its
// end, as the end of an arn or a path names what it is.
func cutMiddle(s string, width int) string {
	w := ansi.StringWidth(s)
	if w <= width || width < 8 {
		return s
	}
	head := (width - 1) / 4
	tail := width - 1 - head
	return ansi.Truncate(s, head, "") + "…" + ansi.TruncateLeft(s, w-tail, "")
}

// overviewTwoColumns is the narrowest content area the overview is drawn in two columns in; a
// narrower one has a column, so no value is cut to half the width.
const overviewTwoColumns = 109

const (
	// overviewGutter is the room left of each column of the overview for the cursor's marker.
	overviewGutter = 2
	// overviewGap is the room between the overview's two columns.
	overviewGap = 3
)

// detailRow is a row of the overview the cursor rests on: the whole value c copies, and what
// enter opens, a resource of the stage by its id or a url.
type detailRow struct {
	copy     string
	resource string
	url      string
}

// overviewColumn is a column of the overview: its lines, drawn in width, and each line of a row
// drawn again on the selection's ground. rowOf is the row of each line, -1 for none; top and
// bottom are the lines to keep in sight with each row, from its section's header for the first
// of a section.
type overviewColumn struct {
	width  int
	lines  []string
	lit    []string
	rowOf  []int
	rows   []detailRow
	top    []int
	bottom []int
	// header is the line of the header of the section the next row is the first of, -1 when it
	// is not the first.
	header int
}

func newOverviewColumn(width int) *overviewColumn {
	return &overviewColumn{width: width, header: -1}
}

// add appends lines that are not a row.
func (c *overviewColumn) add(lines ...string) {
	for _, l := range lines {
		c.lines, c.lit, c.rowOf = append(c.lines, l), append(c.lit, ""), append(c.rowOf, -1)
	}
}

// section starts a section, a line apart from the one before, under a header with count beside
// its title when count is set.
func (c *overviewColumn) section(title, count string) {
	if len(c.lines) > 0 {
		c.add("")
	}
	c.header = len(c.lines)
	c.add(sectionHeader(title, count, c.width))
}

// note appends what a section says in place of its rows, or above them, wrapped to the column.
func (c *overviewColumn) note(n cell) {
	c.add(cellLines(n, c.width)...)
}

// row appends a row whose lines are the cells of each, in cols.
func (c *overviewColumn) row(r detailRow, cols []column, lines ...[]cell) {
	i := len(c.rows)
	top := len(c.lines)
	if c.header >= 0 {
		top, c.header = c.header, -1
	}
	for _, cells := range lines {
		var b strings.Builder
		for j, cl := range cells {
			b.WriteString(renderCell(cl, cols[j], nil))
		}
		c.lines, c.lit, c.rowOf = append(c.lines, b.String()), append(c.lit, litRow(cols, cells...)), append(c.rowOf, i)
	}
	c.rows, c.top, c.bottom = append(c.rows, r), append(c.top, top), append(c.bottom, len(c.lines)-1)
}

// labelled appends a row of a label and its value, the value wrapped onto as many as limit lines
// under itself, the last ending in an ellipsis when there is more.
func (c *overviewColumn) labelled(label string, value cell, r detailRow, limit int) {
	cols := labelColumns(c.width)
	parts := []cell{value}
	if limit > 1 {
		parts = wrapCell(value, cols[2].width)
	}
	if len(parts) > limit {
		parts = parts[:limit]
		last := slices.Clone(parts[limit-1])
		parts[limit-1] = append(last, styles.S("…", last[len(last)-1].Tone))
	}
	lines := make([][]cell, len(parts))
	for i, p := range parts {
		lines[i] = []cell{nil, nil, p}
	}
	lines[0][0] = text(label, styles.ToneFaint)
	c.row(r, cols, lines...)
}

// draw draws height lines of the column from scroll, as far as it scrolls, each after the
// gutter, the lines of the row at cursor marked and on the selection's ground.
func (c *overviewColumn) draw(scroll, height, cursor int) string {
	scroll = max(0, min(scroll, maxOffset(len(c.lines), height)))
	out := make([]string, 0, height)
	for i := scroll; i < min(len(c.lines), scroll+height); i++ {
		if cursor >= 0 && c.rowOf[i] == cursor {
			out = append(out, cursorMark(styles.P.Selection)+c.lit[i])
		} else {
			out = append(out, pad(overviewGutter, nil)+c.lines[i])
		}
	}
	return fill(strings.Join(out, "\n"), c.width+overviewGutter, height)
}

// labelColumns are the columns of a labelled row in width: the label right aligned, a gap, and
// the value.
func labelColumns(width int) []column {
	return []column{{width: labelWidth - 2, right: true}, {width: 2}, {width: max(1, width-labelWidth)}}
}

// ident is a value that names something, as an arn, a name or a url, cut in the middle to width
// so its start and its end, which name it, are both in sight. Nothing is a faint dash.
func ident(s string, tone styles.Tone, width int) cell {
	if s == "" {
		return text("-", styles.ToneFaint)
	}
	return text(cutMiddle(styles.Clean(s), width), tone)
}

// sectionHeader is styles.SectionHeader with count beside the title in faint, as a tab has it.
func sectionHeader(title, count string, width int) string {
	w := ansi.StringWidth(title) + 1 + ansi.StringWidth(count)
	if count == "" || w > width-2 {
		return styles.SectionHeader(title, width)
	}
	label := styles.Render(styles.Span{Text: title, Tone: styles.ToneText, Bold: true}, styles.S(" "+count, styles.ToneFaint))
	return label + " " + lipgloss.NewStyle().Foreground(styles.P.Rule).Render(strings.Repeat("─", max(1, width-w-1)))
}

// packCells lays cells out on lines of width with a separator between two on a line, starting
// a line where the next does not fit. A cell wider than width is cut.
func packCells(cells []cell, width int) []string {
	var lines []string
	var line cell
	used := 0
	for _, c := range cells {
		w := ansi.StringWidth(plainText(c))
		if used > 0 && used+3+w > width {
			lines = append(lines, renderCell(line, column{width: width}, nil))
			line, used = nil, 0
		}
		if used > 0 {
			line, used = append(line, sep()), used+3
		}
		line, used = append(line, c...), used+w
	}
	if len(line) > 0 {
		lines = append(lines, renderCell(line, column{width: width}, nil))
	}
	return lines
}

// functionOverview is what the overview tab draws: the head, the function's name, the facts of
// it and a notice of what changed outside koldstart, above one column, or two side by side,
// that scroll together.
type functionOverview struct {
	head []string
	cols []*overviewColumn
}

// height is how many lines of the columns are in sight.
func (o functionOverview) height() int {
	return max(1, functionBodyHeight()-len(o.head))
}

// lines is how many lines the longer column has.
func (o functionOverview) lines() int {
	n := 0
	for _, c := range o.cols {
		n = max(n, len(c.lines))
	}
	return n
}

// maxScroll is the furthest the columns scroll: to the end of the longer one.
func (o functionOverview) maxScroll() int {
	return maxOffset(o.lines(), o.height())
}

// rowCount is how many rows the cursor can rest on, in both columns.
func (o functionOverview) rowCount() int {
	n := 0
	for _, c := range o.cols {
		n += len(c.rows)
	}
	return n
}

// locate is the column of the row at cursor, the left column's rows first, and its index there,
// -1 and -1 for none. A cursor past the last row is on the last.
func (o functionOverview) locate(cursor int) (int, int) {
	cursor = min(cursor, o.rowCount()-1)
	if cursor < 0 {
		return -1, -1
	}
	for i, c := range o.cols {
		if cursor < len(c.rows) {
			return i, cursor
		}
		cursor -= len(c.rows)
	}
	return -1, -1
}

// selected is the row at cursor, and whether there is one.
func (o functionOverview) selected(cursor int) (detailRow, bool) {
	col, r := o.locate(cursor)
	if col < 0 {
		return detailRow{}, false
	}
	return o.cols[col].rows[r], true
}

// follow is the scroll that keeps the row at cursor in sight with its section's header, moved as
// little from scroll as it can be; on the last row of a column the rest of the column comes
// into sight with it.
func (o functionOverview) follow(cursor, scroll int) int {
	h := o.height()
	col, r := o.locate(cursor)
	if col < 0 {
		return max(0, min(scroll, o.maxScroll()))
	}
	c := o.cols[col]
	top, bottom := c.top[r], c.bottom[r]
	if r == len(c.rows)-1 {
		bottom = len(c.lines) - 1
	}
	scroll = max(scroll, bottom-h+1)
	scroll = min(scroll, top)
	return max(0, min(scroll, maxOffset(len(c.lines), h)))
}

// functionOverview lays out the overview. In one column its sections run from what matters most
// to what is looked up: the configuration, what lambda says of the function now, the last hour,
// what invokes it, its environment and its permissions. In two, the configuration, lambda and
// the permissions are on the left; the last hour, what invokes it and the environment on the
// right.
func (m model) functionOverview() functionOverview {
	f := m.state.function
	width := contentWidth - 2 - overviewGutter
	o := functionOverview{head: m.overviewHead(width)}
	if f.lambdaName == "" {
		c := newOverviewColumn(width)
		note := cell{styles.S("not deployed yet", styles.ToneMuted)}
		if m.b.Local(m.state.stage.ref) {
			note = append(note, sep(), styles.S(m.keys.Deploy.Help().Key+" plans a deploy of its code", styles.ToneFaint))
		}
		c.note(note)
		o.cols = []*overviewColumn{c}
		return o
	}
	if contentWidth < overviewTwoColumns {
		c := newOverviewColumn(width)
		m.configSection(c)
		m.liveSection(c)
		m.metricSection(c)
		m.invokerSection(c)
		m.envSection(c)
		m.permissionSection(c)
		o.cols = []*overviewColumn{c}
		return o
	}
	inner := contentWidth - 2 - 2*overviewGutter - overviewGap
	left, right := newOverviewColumn(inner/2), newOverviewColumn(inner-inner/2)
	m.configSection(left)
	m.liveSection(left)
	m.permissionSection(left)
	m.metricSection(right)
	m.invokerSection(right)
	m.envSection(right)
	o.cols = []*overviewColumn{left, right}
	return o
}

// overviewHead is the function's name, the facts of it below as far as they fit, and a notice of
// what lambda runs that koldstart did not deploy, so it is in sight however far the columns
// scroll; each line in the gutter, and a blank line after.
func (m model) overviewHead(width int) []string {
	f := m.state.function
	name := styles.Span{Text: styles.Clean(cmpOr(f.lambdaName, f.name)), Tone: styles.ToneText, Bold: true}
	lines := append([]string{renderCell(cell{name}, column{width: width}, nil)}, packCells(m.functionFacts(), width)...)
	if notice := m.driftNotice(width); len(notice) > 0 {
		lines = append(append(lines, ""), notice...)
	}
	for i, l := range lines {
		lines[i] = pad(overviewGutter, nil) + l
	}
	return append(lines, "")
}

// functionFacts are what the head says of the function: its runtime and architecture, its memory
// and timeout, and once it is deployed, when its stage was last deployed and its state in lambda.
func (m model) functionFacts() []cell {
	f := m.state.function
	in := f.inputs
	var facts []cell
	switch {
	case in.Runtime != "":
		facts = append(facts, text(styles.Clean(in.Runtime), styles.ToneBody))
	case in.Code.ImageURI != "":
		facts = append(facts, text("container image", styles.ToneBody))
	}
	if in.Architecture != "" {
		facts = append(facts, text(styles.Clean(in.Architecture), styles.ToneMuted))
	}
	if in.Memory > 0 {
		facts = append(facts, cell{styles.S(formatInt(int(in.Memory)), styles.ToneBody), styles.S(" mb", styles.ToneFaint)})
	}
	if in.Timeout > 0 {
		facts = append(facts, cell{styles.S(seconds(in.Timeout), styles.ToneBody), styles.S(" timeout", styles.ToneFaint)})
	}
	if st := m.state.stage.st; st != nil && !st.DeployedAt.IsZero() && f.lambdaName != "" {
		facts = append(facts, cell{styles.S("deployed ", styles.ToneFaint), styles.S(formatAgo(st.DeployedAt), styles.ToneBody)})
	}
	switch {
	case f.gone:
		facts = append(facts, text("▲ no longer in the stage's state", styles.ToneWarning))
	case f.live != nil && f.lambdaName != "" && f.live.State != "":
		facts = append(facts, stateCell(f.live.State))
	}
	return facts
}

// stateCell is a state lambda reports in its tone, with the symbol of the tone, as ✓ active.
func stateCell(status string) cell {
	tone := stateTone(status)
	s := strings.ToLower(styles.Clean(status))
	if mark := toneMark(tone); mark != "" {
		return cell{styles.S(mark+" "+s, tone)}
	}
	return text(s, tone)
}

// toneMark is the symbol a status carries in a tone, so it reads without colour too.
func toneMark(t styles.Tone) string {
	switch t {
	case styles.ToneSuccess:
		return "✓"
	case styles.ToneAccent:
		return "●"
	case styles.ToneWarning:
		return "▲"
	case styles.ToneDanger:
		return "✗"
	}
	return ""
}

// megabytes writes a size koldstart configures in mb, or nothing when it is not set.
func megabytes(n int32) string {
	if n <= 0 {
		return ""
	}
	return formatInt(int(n)) + " mb"
}

// seconds writes a timeout koldstart configures in seconds, or nothing when it is not set.
func seconds(n int32) string {
	if n <= 0 {
		return ""
	}
	return cli.Duration(time.Duration(n) * time.Second)
}

// inStage reports whether the stage's state has a resource of id, which the resource page opens.
func (m model) inStage(id string) bool {
	st := m.state.stage.st
	return st != nil && st.Resources[id] != nil
}

// configSection is what koldstart deployed the function with, beyond the facts in the head; the
// log group opens on enter.
func (m model) configSection(c *overviewColumn) {
	f := m.state.function
	in := f.inputs
	vw := labelColumns(c.width)[2].width
	id := func(label, s string, r detailRow) {
		r.copy = cmpOr(r.copy, s)
		c.labelled(label, ident(s, styles.ToneBody, vw), r, 1)
	}
	c.section("configuration", "")
	id("name", f.lambdaName, detailRow{})
	id("arn", f.arn, detailRow{})
	if in.Code.ImageURI != "" {
		id("image", in.Code.ImageURI, detailRow{})
	} else {
		id("handler", in.Handler, detailRow{})
	}
	c.labelled("code", ident(shortSHA(in.Code.SHA256), styles.ToneFaint, vw), detailRow{copy: in.Code.SHA256}, 1)
	c.labelled("/tmp", sizeCell(in.EphemeralStorage), detailRow{copy: megabytes(in.EphemeralStorage)}, 1)
	c.labelled("concurrency", concurrencyCell(in.ReservedConcurrency), detailRow{}, 2)
	for i, l := range in.Layers {
		label := ""
		if i == 0 {
			label = "layers"
		}
		id(label, l, detailRow{})
	}
	logGroup := detailRow{}
	if g := desired.LogGroupID(f.name); m.inStage(g) {
		logGroup.resource = g
	}
	id("log group", f.logGroup, logGroup)
	if in.VPC != nil {
		subnets, groups := strings.Join(in.VPC.SubnetIDs, ", "), strings.Join(in.VPC.SecurityGroupIDs, ", ")
		c.labelled("subnets", text(orDash(styles.Clean(subnets)), styles.ToneBody), detailRow{copy: subnets}, 3)
		c.labelled("groups", text(orDash(styles.Clean(groups)), styles.ToneBody), detailRow{copy: groups}, 3)
	}
}

// sizeCell is a size koldstart configures, its unit in faint, or a faint dash when it is not set.
func sizeCell(n int32) cell {
	if n <= 0 {
		return text("-", styles.ToneFaint)
	}
	return cell{styles.S(formatInt(int(n)), styles.ToneBody), styles.S(" mb", styles.ToneFaint)}
}

// concurrencyCell is a function's reserved concurrency: unreserved, how many, or a warning that
// none means every invocation is throttled.
func concurrencyCell(rc *int32) cell {
	switch {
	case rc == nil:
		return text("unreserved", styles.ToneMuted)
	case *rc == 0:
		return text("0 reserved: every invocation is throttled", styles.ToneWarning)
	}
	return cell{styles.S(formatInt(int(*rc)), styles.ToneBody), styles.S(" reserved", styles.ToneFaint)}
}

// codeChanged reports whether lambda runs other code than koldstart deployed.
func codeChanged(in awsp.FunctionInputs, live cli.FunctionState) bool {
	return live.CodeSHA256 != "" && in.Code.SHA256 != "" && live.CodeSHA256 != in.Code.SHA256
}

// driftNotice says what lambda runs that koldstart did not deploy.
func (m model) driftNotice(width int) []string {
	f := m.state.function
	if f.live == nil {
		return nil
	}
	changed := configDrift(f.inputs, *f.live)
	if codeChanged(f.inputs, *f.live) {
		changed = append([]string{"code"}, changed...)
	}
	if len(changed) == 0 {
		return nil
	}
	return wrapped("▲ changed outside koldstart: "+styles.Clean(strings.Join(changed, ", ")), styles.ToneWarning, width, 2)
}

// liveSection is what lambda says of the function now, with each setting it runs with that is
// not what koldstart deployed.
func (m model) liveSection(c *overviewColumn) {
	f := m.state.function
	c.section("in lambda now", "")
	switch {
	case f.live == nil && f.liveErr != "":
		c.note(m.couldNotRead("it from lambda", styles.Clean(f.liveErr), styles.ToneMuted))
		return
	case f.live == nil:
		c.note(text(readingNote("it from lambda"), styles.ToneMuted))
		return
	case f.liveErr != "":
		c.note(staleNote("", f.liveAt, styles.Clean(f.liveErr)))
	}
	live := *f.live
	c.labelled("state", reasoned(live.State, live.StateReason), detailRow{copy: joinReason(live.State, live.StateReason)}, 3)
	c.labelled("last update", reasoned(live.LastUpdate, live.LastUpdateReason), detailRow{copy: joinReason(live.LastUpdate, live.LastUpdateReason)}, 3)
	modified, at := text("-", styles.ToneFaint), ""
	if !live.LastModified.IsZero() {
		modified = cell{styles.S(formatTime(live.LastModified), styles.ToneBody), styles.S(", "+formatAgo(live.LastModified), styles.ToneMuted)}
		at = live.LastModified.Format(time.RFC3339)
	}
	c.labelled("modified", modified, detailRow{copy: at}, 1)
	c.labelled("code", liveCode(f.inputs, live), detailRow{copy: live.CodeSHA256}, 1)
	for _, d := range driftFields(f.inputs, live) {
		c.labelled(d.name, cell{styles.S("▲ "+d.now, styles.ToneWarning), styles.S(" · koldstart deployed ", styles.ToneFaint), styles.S(d.deployed, styles.ToneBody)}, detailRow{copy: d.now}, 1)
	}
}

// reasoned is a state lambda reports in its tone with the reason for it, in muted.
func reasoned(status, reason string) cell {
	if status == "" {
		return text("-", styles.ToneFaint)
	}
	c := stateCell(status)
	if reason != "" {
		c = append(c, sep(), styles.S(styles.Clean(reason), styles.ToneMuted))
	}
	return c
}

// joinReason is a state and its reason as c copies them.
func joinReason(status, reason string) string {
	if reason == "" {
		return status
	}
	return status + ": " + reason
}

// liveCode says whether lambda runs the code koldstart deployed, and how large it is.
func liveCode(in awsp.FunctionInputs, live cli.FunctionState) cell {
	var code cell
	switch {
	case codeChanged(in, live):
		code = cell{styles.S("▲ changed outside koldstart: "+shortSHA(styles.Clean(live.CodeSHA256)), styles.ToneWarning)}
	case live.CodeSHA256 != "" && in.Code.SHA256 != "":
		code = cell{styles.S("✓ ", styles.ToneSuccess), styles.S("what koldstart deployed", styles.ToneBody)}
	}
	if live.CodeSize > 0 {
		if len(code) > 0 {
			code = append(code, sep())
		}
		code = append(code, styles.S(cli.Size(live.CodeSize), styles.ToneMuted))
	}
	if len(code) == 0 {
		return text("-", styles.ToneFaint)
	}
	return code
}

// memoryWord names a function's memory, where lambda runs it with other than koldstart deployed.
const memoryWord = "memory"

// drift is a setting lambda runs with that is not what koldstart deployed: its name, what
// koldstart deployed, what lambda has now, and how the notice above the overview writes what
// koldstart deployed, the unit left to what lambda has.
type drift struct {
	name, deployed, now, from string
}

// driftFields are the settings lambda runs with that differ from what koldstart deployed.
func driftFields(in awsp.FunctionInputs, live cli.FunctionState) []drift {
	var out []drift
	if live.Memory != 0 && in.Memory != 0 && live.Memory != in.Memory {
		out = append(out, drift{memoryWord, megabytes(in.Memory), megabytes(live.Memory), formatInt(int(in.Memory))})
	}
	if live.Timeout != 0 && in.Timeout != 0 && live.Timeout != in.Timeout {
		out = append(out, drift{"timeout", seconds(in.Timeout), seconds(live.Timeout), seconds(in.Timeout)})
	}
	if live.Runtime != "" && in.Runtime != "" && live.Runtime != in.Runtime {
		out = append(out, drift{"runtime", styles.Clean(in.Runtime), styles.Clean(live.Runtime), styles.Clean(in.Runtime)})
	}
	return out
}

// configDrift names what lambda runs with that differs from what koldstart deployed, as memory
// 1,024 → 512 mb.
func configDrift(in awsp.FunctionInputs, live cli.FunctionState) []string {
	fields := driftFields(in, live)
	out := make([]string, 0, len(fields))
	for _, d := range fields {
		out = append(out, d.name+" "+d.from+" → "+d.now)
	}
	return out
}

// stateTone is the tone of a state lambda reports. A pending function or an update in progress is
// in flight, in the accent. An inactive function is one lambda reclaimed after weeks without
// invocations: the next invocation wakes it, so it warns rather than fails.
func stateTone(status string) styles.Tone {
	switch strings.ToLower(status) {
	case "active", "successful":
		return styles.ToneSuccess
	case "pending", "inprogress":
		return styles.ToneAccent
	case "inactive":
		return styles.ToneWarning
	case "failed":
		return styles.ToneDanger
	}
	return styles.ToneBody
}

// peak is the largest point of a series, 0 for none.
func peak(series []float64) float64 {
	if len(series) == 0 {
		return 0
	}
	return slices.Max(series)
}

// metricSection draws the last hour of the function's metrics as sparklines.
func (m model) metricSection(c *overviewColumn) {
	s := m.state.stage
	f := m.state.function
	c.section("last hour", "")
	mt, ok := s.metrics[f.lambdaName]
	switch {
	case !ok && s.metricsErr != "":
		c.note(m.couldNotRead("the metrics", styles.Clean(s.metricsErr), styles.ToneMuted))
		return
	case !ok:
		c.note(text(readingNote("the metrics"), styles.ToneMuted))
		return
	case s.metricsErr != "":
		c.note(staleNote("", s.metricsAt, styles.Clean(s.metricsErr)))
	}
	if peak(mt.Invocations) == 0 && peak(mt.Errors) == 0 && peak(mt.Throttles) == 0 {
		c.note(text("not invoked in the last hour", styles.ToneMuted))
		return
	}
	sum := mt.Summary()
	errTone, throttleTone := styles.ToneBody, styles.ToneBody
	if sum.Errors > 0 {
		errTone = styles.ToneDanger
	}
	if sum.Throttles > 0 {
		throttleTone = styles.ToneWarning
	}
	count := func(v float64) string { return formatInt(int(v + 0.5)) }
	c.add(sparkLines([]metricSeries{
		{label: "invocations", points: mt.Invocations, tone: styles.ToneAccent, total: text(count(sum.Invocations), styles.ToneBody), format: count, unit: perMinute},
		{label: "errors", points: mt.Errors, tone: styles.ToneDanger, total: text(count(sum.Errors), errTone), format: count, unit: perMinute},
		{label: "throttles", points: mt.Throttles, tone: styles.ToneWarning, total: text(count(sum.Throttles), throttleTone), format: count, unit: perMinute},
		{label: "duration", points: mt.DurationP99, tone: styles.ToneInfo, total: cell{styles.S("p99 ", styles.ToneFaint), styles.S(formatMillis(sum.DurationP99), styles.ToneBody)}, format: formatMillis, gauge: true},
		{label: "concurrency", points: mt.Concurrency, tone: styles.ToneMuted, total: cell{styles.S("max ", styles.ToneFaint), styles.S(count(peak(mt.Concurrency)), styles.ToneBody)}, format: count, gauge: true},
	}, c.width)...)
}

// metricSeries is a metric of the last hour as a sparkline draws it: its points, a minute each,
// the tone of its blocks, what it comes to over the hour, and how a point is written, with its
// unit after a range. The lowest point of a gauge, a duration or a concurrency, is of the
// minutes it ran, as a minute without invocations has no duration.
type metricSeries struct {
	label  string
	points []float64
	tone   styles.Tone
	total  cell
	format func(float64) string
	unit   string
	gauge  bool
}

// figures are the range of the series, lowest to highest, and its last point, empty for a series
// of nothing but zeros.
func (s metricSeries) figures() (string, string) {
	if peak(s.points) == 0 {
		return "", ""
	}
	low := slices.Max(s.points)
	for _, v := range s.points {
		if v > 0 || !s.gauge {
			low = min(low, v)
		}
	}
	last := s.points[len(s.points)-1]
	lastText := s.format(last)
	if s.gauge && last == 0 {
		lastText = "-"
	}
	return between(s.format(low), s.format(peak(s.points))) + s.unit, lastText
}

// between writes a range from low to high, the unit once where both have the same: 40–240ms,
// 900ms–1.2s.
func between(low, high string) string {
	unit := strings.TrimLeft(high, "0123456789.,")
	if number, ok := strings.CutSuffix(low, unit); ok && unit != "" && strings.Trim(number, "0123456789.,") == "" {
		low = number
	}
	return low + "–" + high
}

// perMinute is the unit of the range of a count, whose points are a minute each.
const perMinute = "/min"

// sparkMin is the narrowest a sparkline of more points is drawn; the figures beside it go first.
const sparkMin = 12

// sparkLines draw each series as a sparkline, all the same width, what it comes to beside it,
// its range and its last point as far as they fit, and under them the hour they span. A series
// of zeros is a dotted baseline, and one without points says so.
func sparkLines(series []metricSeries, width int) []string {
	points, totalW, rangeW, lastW := 0, 0, 0, 0
	ranges, lasts := make([]string, len(series)), make([]string, len(series))
	for i, s := range series {
		points = max(points, len(s.points))
		ranges[i], lasts[i] = s.figures()
		totalW = max(totalW, ansi.StringWidth(plainText(s.total)))
		rangeW = max(rangeW, ansi.StringWidth(ranges[i]))
		if lasts[i] != "" {
			lastW = max(lastW, len("last ")+ansi.StringWidth(lasts[i]))
		}
	}
	room, need := width-labelWidth-2-totalW, min(sparkMin, points)
	switch {
	case room-2-rangeW-2-lastW >= need:
	case room-2-rangeW >= need:
		lastW = 0
	default:
		rangeW, lastW = 0, 0
	}
	if rangeW > 0 {
		room -= 2 + rangeW
	}
	if lastW > 0 {
		room -= 2 + lastW
	}
	spark := max(1, min(room, points))
	cols := []column{{width: labelWidth - 2, right: true}, {width: 2}, {width: spark}, {width: 2}, {width: totalW, right: true}, {width: 2}, {width: rangeW, right: true}, {width: 2}, {width: lastW}}
	lines := make([]string, 0, len(series)+1)
	for i, s := range series {
		if len(s.points) == 0 {
			lines = append(lines, strings.TrimRight(renderCell(text(s.label, styles.ToneFaint), cols[0], nil)+pad(2, nil)+renderCell(text("no data", styles.ToneFaint), column{width: max(1, width-labelWidth)}, nil), " "))
			continue
		}
		cells := []cell{text(s.label, styles.ToneFaint), nil, sparkCell(s.points, spark, s.tone), nil, s.total, nil, text(ranges[i], styles.ToneMuted), nil, nil}
		if peak(s.points) == 0 {
			cells[4] = text(plainText(s.total), styles.ToneFaint)
		}
		if lasts[i] != "" {
			cells[8] = cell{styles.S("last ", styles.ToneFaint), styles.S(lasts[i], styles.ToneMuted)}
		}
		var b strings.Builder
		for j, cl := range cells {
			b.WriteString(renderCell(cl, cols[j], nil))
		}
		lines = append(lines, strings.TrimRight(b.String(), " "))
	}
	if axis := sparkAxis(spark); axis != "" {
		lines = append(lines, pad(labelWidth, nil)+styles.Faint(axis))
	}
	return lines
}

// sparkAxis is the hour under a sparkline of width: an hour ago at its start and now at its end,
// shorter where that does not fit, and nothing where neither does.
func sparkAxis(width int) string {
	for _, start := range []string{"1h ago", "-1h"} {
		if len(start)+1+len("now") <= width {
			return start + strings.Repeat(" ", width-len(start)-len("now")) + "now"
		}
	}
	return ""
}

// sparklines are the blocks a sparkline is drawn with, from empty to full.
const sparklines = " ▁▂▃▄▅▆▇█"

// sparkline draws series in width columns, each the largest of the points it covers, scaled to
// the largest point.
func sparkline(series []float64, width int) string {
	if len(series) == 0 || width <= 0 {
		return ""
	}
	cols := make([]float64, min(width, len(series)))
	for i, v := range series {
		c := i * len(cols) / len(series)
		cols[c] = max(cols[c], v)
	}
	top := slices.Max(cols)
	blocks := []rune(sparklines)
	var b strings.Builder
	for _, v := range cols {
		i := 0
		if top > 0 && v > 0 {
			i = max(1, int(v/top*float64(len(blocks)-1)+0.5))
		}
		b.WriteRune(blocks[i])
	}
	return b.String()
}

// sparkCell is the sparkline of series in tone, its columns without a point dotted in faint, so
// a quiet stretch reads as a baseline and not as a gap.
func sparkCell(series []float64, width int, tone styles.Tone) cell {
	var c cell
	var run strings.Builder
	quiet := false
	flush := func() {
		if run.Len() == 0 {
			return
		}
		t := tone
		if quiet {
			t = styles.ToneFaint
		}
		c = append(c, styles.S(run.String(), t))
		run.Reset()
	}
	for _, r := range sparkline(series, width) {
		if (r == ' ') != quiet {
			flush()
			quiet = r == ' '
		}
		if r == ' ' {
			r = '·'
		}
		run.WriteRune(r)
	}
	flush()
	return c
}

// hidden stands in for a value that looks like a secret.
const hidden = "••••••••"

// secretWords mark an environment variable whose value is a secret, by its name.
var secretWords = []string{"SECRET", "PASSWORD", "PASSWD", "PASSPHRASE", "TOKEN", "CREDENTIAL", "PRIVATE", "API_KEY", "APIKEY", "ACCESS_KEY"}

// urlPassword is the password in a url's user info, as in postgres://app:password@db/orders.
var urlPassword = regexp.MustCompile(`(://[^/@:\s]*:)[^/@\s]+@`)

// looksSecret reports whether an environment variable holds a secret as it is, by its name or
// a private key in its value. A secret koldstart reads from ssm or secrets manager is shown by
// where it is instead.
func looksSecret(name, value string) bool {
	upper := strings.ToUpper(name)
	return slices.ContainsFunc(secretWords, func(w string) bool { return strings.Contains(upper, w) }) ||
		strings.HasPrefix(strings.TrimSpace(value), "-----BEGIN")
}

// envValue is how an environment variable's value is shown: a value as it is, with secrets
// hidden, and a secret, output or reference by what it stands for.
func envValue(name string, v any) styles.Span {
	switch v := v.(type) {
	case string:
		switch {
		case v == "":
			return styles.S(`""`, styles.ToneFaint)
		case looksSecret(name, v):
			return styles.S(hidden, styles.ToneFaint)
		}
		return styles.S(urlPassword.ReplaceAllString(v, "${1}"+hidden+"@"), styles.ToneBody)
	case map[string]any:
		return styles.S(engine.Describe(v), styles.ToneInfo)
	}
	return styles.S(engine.Describe(v), styles.ToneBody)
}

// envCopy is what c copies of an environment variable: its value, or its name where the value is
// hidden, so a secret reaches neither the clipboard nor the status line.
func envCopy(name string, v any) string {
	s, ok := v.(string)
	switch {
	case !ok:
		return engine.Describe(v)
	case looksSecret(name, s):
		return name
	case urlPassword.MatchString(s):
		return urlPassword.ReplaceAllString(s, "${1}"+hidden+"@")
	}
	return s
}

// envSection lists the function's environment by name, the names in a column of their own.
func (m model) envSection(c *overviewColumn) {
	f := m.state.function
	count := ""
	if len(f.env) > 0 {
		count = formatInt(len(f.env))
	}
	c.section("environment", count)
	if len(f.env) == 0 {
		c.note(text("none", styles.ToneFaint))
		return
	}
	names := slices.Sorted(maps.Keys(f.env))
	nameWidth := 0
	for _, k := range names {
		nameWidth = max(nameWidth, ansi.StringWidth(styles.Clean(k)))
	}
	nameWidth = min(nameWidth, c.width/3)
	cols := []column{{width: nameWidth}, {width: 3}, {width: max(1, c.width-nameWidth-3)}}
	for _, k := range names {
		v := envValue(k, f.env[k])
		v.Text = cutMiddle(styles.Clean(v.Text), cols[2].width)
		c.row(detailRow{copy: envCopy(k, f.env[k])}, cols, []cell{text(styles.Clean(k), styles.ToneMuted), text(" = ", styles.ToneFaint), {v}})
	}
}

// invoker is something that invokes the function: its kind as a person names it, what it is,
// what c copies of it, and the id of its resource in the stage, which enter opens.
type invoker struct {
	kind, what, copy, id string
}

// invokersOf are what invoke fn in a stage, as desired.Triggers finds them, each by what its
// inputs say it is.
func invokersOf(st *state.State, fn string) []invoker {
	ids := desired.Triggers(st.Resources, fn)
	out := make([]invoker, 0, len(ids))
	for _, id := range ids {
		kind, rest, _ := strings.Cut(id, "/")
		var in map[string]any
		_ = json.Unmarshal(st.Resources[id].Inputs, &in)
		out = append(out, invokerOf(id, kind, strings.TrimPrefix(rest, fn+"/"), in))
	}
	return out
}

// invokerOf names a trigger by what it is: a schedule by its expression, a queue, a stream or a
// topic by its name, an event rule by its pattern, a load balancer's rule by its paths, and a
// route, a websocket route or a bucket by the rest of its id. Each kind fits a label.
func invokerOf(id, kind, rest string, in map[string]any) invoker {
	str := func(k string) string {
		s, _ := in[k].(string)
		return s
	}
	t := invoker{kind: kind, what: rest, copy: rest, id: id}
	set := func(what, copy string) {
		if copy != "" {
			t.what, t.copy = what, copy
		}
	}
	switch kind {
	case "schedule":
		set(strings.TrimSpace(str("expression")+" "+str("timezone")), str("expression"))
	case "mapping":
		t.kind = sourceKind(str("source"))
		set(arnName(str("source")), str("source"))
	case "subscription":
		t.kind = "topic"
		set(arnName(str("topic")), str("topic"))
	case "rule":
		t.kind = "event rule"
		set(str("pattern"), str("pattern"))
	case "lbrule":
		t.kind = "alb"
		paths := strings.Join(strs(in["paths"]), ", ")
		set(paths, paths)
	case "wsroute":
		t.kind = "websocket"
	case "notification":
		t.kind = "bucket"
	}
	return t
}

// sourceKind names what an event source mapping reads, by its arn: a queue or a stream.
func sourceKind(arn string) string {
	switch {
	case strings.Contains(arn, ":sqs:"):
		return "queue"
	case strings.Contains(arn, ":dynamodb:"):
		return "table stream"
	}
	return "stream"
}

// arnName is the name at the end of an arn, after its last colon or slash: orders for an arn of
// a queue named orders. What is not an arn is as it is.
func arnName(arn string) string {
	if !strings.HasPrefix(arn, "arn:") {
		return arn
	}
	i := strings.LastIndexAny(arn, ":/")
	if i < 0 || i == len(arn)-1 {
		return arn
	}
	return arn[i+1:]
}

// strs is a value of a policy or of inputs that is a string or a list of them as a list.
func strs(v any) []string {
	switch v := v.(type) {
	case string:
		return []string{v}
	case []any:
		out := make([]string, 0, len(v))
		for _, s := range v {
			out = append(out, engine.Describe(s))
		}
		return out
	}
	return nil
}

// invokerSection lists what invokes the function, its url first, each by its kind and what it
// is; enter opens the url in the browser, and a trigger on the resource page.
func (m model) invokerSection(c *overviewColumn) {
	f := m.state.function
	type entry struct {
		kind, what string
		r          detailRow
	}
	var entries []entry
	if f.url != "" {
		entries = append(entries, entry{"url", f.url, detailRow{copy: f.url, url: f.url}})
	}
	for _, t := range f.invokers {
		r := detailRow{copy: t.copy}
		if m.inStage(t.id) {
			r.resource = t.id
		}
		entries = append(entries, entry{t.kind, t.what, r})
	}
	count := ""
	if len(entries) > 0 {
		count = formatInt(len(entries))
	}
	c.section("invoked by", count)
	if len(entries) == 0 {
		c.note(text("nothing: invoke it with "+m.keys.Invoke.Help().Key, styles.ToneFaint))
		return
	}
	vw := labelColumns(c.width)[2].width
	for _, e := range entries {
		c.labelled(styles.Clean(e.kind), ident(e.what, styles.ToneBody, vw), e.r, 1)
	}
}

// statement is what a statement of a role's policy allows: its actions, on its resources.
type statement struct {
	actions, resources []string
}

// policyStatements are the statements of a policy document that allow something.
func policyStatements(policy any) []statement {
	if s, ok := policy.(string); ok {
		_ = json.Unmarshal([]byte(s), &policy)
	}
	doc, _ := policy.(map[string]any)
	var list []any
	switch v := doc["Statement"].(type) {
	case []any:
		list = v
	case map[string]any:
		list = []any{v}
	}
	var out []statement
	for _, s := range list {
		st, _ := s.(map[string]any)
		if st["Effect"] != "Allow" {
			continue
		}
		out = append(out, statement{actions: strs(st["Action"]), resources: strs(st["Resource"])})
	}
	return out
}

// actionsText writes actions with the service of each named once, in the order they come:
// sqs SendMessage, GetQueueUrl; s3 GetObject.
func actionsText(actions []string) string {
	var services []string
	names := map[string][]string{}
	for _, a := range actions {
		service, name, ok := strings.Cut(a, ":")
		if !ok {
			service, name = a, ""
		}
		if _, seen := names[service]; !seen {
			services, names[service] = append(services, service), nil
		}
		if name != "" {
			names[service] = append(names[service], name)
		}
	}
	parts := make([]string, 0, len(services))
	for _, s := range services {
		parts = append(parts, strings.TrimSpace(s+" "+strings.Join(names[s], ", ")))
	}
	return strings.Join(parts, "; ")
}

// resourceText names what a statement acts on by the end of the first arn, and how many more.
func resourceText(resources []string) cell {
	if len(resources) == 0 {
		return nil
	}
	first := resources[0]
	if parts := strings.SplitN(first, ":", 6); len(parts) == 6 {
		first = parts[5]
	}
	c := cell{styles.S(" on ", styles.ToneFaint), styles.S(styles.Clean(first), styles.ToneMuted)}
	if n := len(resources) - 1; n > 0 {
		c = append(c, styles.S(" +"+formatInt(n), styles.ToneFaint))
	}
	return c
}

// permissionSection is the function's role, which enter opens when it is koldstart's, and what
// its policies allow.
func (m model) permissionSection(c *overviewColumn) {
	f := m.state.function
	if f.inputs.Role == "" {
		return
	}
	vw := labelColumns(c.width)[2].width
	c.section("permissions", "")
	role := detailRow{copy: f.inputs.Role}
	if m.inStage(f.roleID) {
		role.resource = f.roleID
	}
	c.labelled("role", ident(arnName(f.inputs.Role), styles.ToneBody, vw), role, 1)
	if f.role == nil {
		c.note(text("not koldstart's role: its policies are not shown", styles.ToneFaint))
		return
	}
	for i, p := range f.role.ManagedPolicies {
		label := ""
		if i == 0 {
			label = "managed"
		}
		c.labelled(label, ident(arnName(p), styles.ToneBody, vw), detailRow{copy: p}, 1)
	}
	for i, s := range policyStatements(f.role.Policy) {
		label := ""
		if i == 0 {
			label = "allows"
		}
		value := append(cell{styles.S(styles.Clean(actionsText(s.actions)), styles.ToneBody)}, resourceText(s.resources)...)
		copied := strings.Join(s.actions, ", ")
		if len(s.resources) > 0 {
			copied += " on " + strings.Join(s.resources, ", ")
		}
		c.labelled(label, value, detailRow{copy: copied}, 2)
	}
}

// overviewKey handles a key on the overview: the cursor moves over its rows, keeping its row in
// sight, and enter opens what the row names.
func (m model) overviewKey(msg tea.KeyPressMsg) (model, tea.Cmd) {
	f := &m.state.function
	o := m.functionOverview()
	if key.Matches(msg, m.keys.View) {
		return m.openOverviewRow(o)
	}
	cursor, ok := moveCursor(msg, m.keys, min(f.cursor, o.rowCount()-1), o.rowCount(), o.height())
	if !ok {
		return m, nil
	}
	f.scroll = o.follow(cursor, o.follow(f.cursor, f.scroll))
	f.cursor = cursor
	return m, nil
}

// openOverviewRow opens what the row under the cursor names: a url in the browser, a resource on
// the resource page, which goes back to the function.
func (m model) openOverviewRow(o functionOverview) (model, tea.Cmd) {
	r, ok := o.selected(m.state.function.cursor)
	switch {
	case !ok:
		return m, nil
	case r.url != "":
		return m.browse(r.url)
	case r.resource != "" && m.inStage(r.resource):
		m, cmd := m.openResource(stageResource{id: r.resource, res: m.state.stage.st.Resources[r.resource]})
		m.state.resource.fromFunction = true
		return m, cmd
	}
	return m, nil
}

// functionOverviewHints are the keys of the overview: the cursor, and enter where its row opens.
func (m model) functionOverviewHints() [][2]string {
	h := [][2]string{chooseHint}
	r, _ := m.functionOverview().selected(m.state.function.cursor)
	switch {
	case r.url != "":
		h = append(h, hint(m.keys.View, "open in the browser"))
	case r.resource != "":
		h = append(h, hint(m.keys.View))
	}
	return h
}

// openURL opens a url in the browser; tests replace it.
var openURL = startBrowser

// startBrowser opens url in the browser with the command the platform has for it, and does not
// wait for the browser.
//
//nolint:gosec // the command is fixed, and the url, the function's own, is one argument to it
func startBrowser(url string) error {
	name := "xdg-open"
	if runtime.GOOS == "darwin" {
		name = "open"
	}
	cmd := exec.Command(name, url)
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

// browse opens a url of http or https in the browser, and says so or why it could not.
func (m model) browse(url string) (model, tea.Cmd) {
	if !strings.HasPrefix(url, "https://") && !strings.HasPrefix(url, "http://") {
		return m, nil
	}
	if err := openURL(url); err != nil {
		return m.setStatus("could not open "+styles.Clean(url)+": "+styles.Clean(err.Error()), styles.ToneDanger)
	}
	return m.setStatus("opened "+truncate(styles.Clean(url), 48), styles.ToneSuccess)
}

// overviewTabView draws the overview: the head, and below it the columns at their scroll.
func (m model) overviewTabView() string {
	f := m.state.function
	o := m.functionOverview()
	height := o.height()
	scroll := o.follow(f.cursor, f.scroll)
	col, cursor := o.locate(f.cursor)
	blocks := make([]string, 0, 2*len(o.cols))
	for i, c := range o.cols {
		if i > 0 {
			blocks = append(blocks, fill("", overviewGap, height))
		}
		at := -1
		if i == col {
			at = cursor
		}
		blocks = append(blocks, c.draw(scroll, height, at))
	}
	return indent(strings.Join(append(slices.Clone(o.head), lipgloss.JoinHorizontal(lipgloss.Top, blocks...)), "\n"))
}
