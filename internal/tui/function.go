package tui

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/kontrolplane/koldstart/internal/cli"
	"github.com/kontrolplane/koldstart/internal/desired"
	"github.com/kontrolplane/koldstart/internal/engine"
	"github.com/kontrolplane/koldstart/internal/provider/awsp"
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
	triggers    []string
	tab         functionTab
	scroll      int

	liveGen int
	live    *cli.FunctionState
	liveErr string

	logs logState

	invocations dataTable
	requests    []request

	events     dataTable
	eventFiles []string
	invokeGen  int
	invoking   bool
	results    map[string]invocationResult
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
	f.triggers = desired.Triggers(st.Resources, f.name)
	return m
}

func (m model) applyFunctionLive(msg functionLiveMsg) (model, tea.Cmd) {
	f := &m.state.function
	if msg.gen != f.liveGen {
		return m, nil
	}
	if msg.err != nil {
		f.liveErr = msg.err.Error()
		return m, nil
	}
	live := msg.live
	f.live, f.liveErr = &live, ""
	return m, nil
}

// refreshFunction reads the function again, its state, what lambda says, and the new lines of
// its logs when no live tail brings them.
func (m model) refreshFunction() (model, tea.Cmd) {
	m, cmd := m.loadStage()
	cmds := []tea.Cmd{cmd}
	if m.state.function.lambdaName != "" {
		m, cmd = m.loadFunctionLive()
		cmds = append(cmds, cmd)
	}
	m, cmd = m.loadMetrics()
	cmds = append(cmds, cmd)
	if !m.state.function.logs.tailing {
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
	case key.Matches(msg, m.keys.NextTab):
		f.tab, f.scroll = (f.tab+1)%functionTab(len(functionTabs)), 0
		return m, nil
	case key.Matches(msg, m.keys.PrevTab):
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
		return m.refreshFunction()
	case key.Matches(msg, m.keys.Copy):
		return m.copy(m.functionCopyValue())
	}
	switch f.tab {
	case logsTab:
		return m.logsUpdate(msg)
	case invocationsTab:
		return m.invocationsUpdate(msg)
	case invokeTab:
		if f.events.move(msg, m.keys) {
			f.scroll = 0
			return m, nil
		}
	}
	f.scroll = scrollBy(msg, m.keys, f.scroll, functionBodyHeight())
	return m, nil
}

// scrollBy moves a scroll offset by the key: a line, a page, or to the top or the bottom.
func scrollBy(msg tea.KeyPressMsg, k keys.KeyMap, offset, page int) int {
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
		offset = 1 << 30
	}
	return max(0, offset)
}

// functionCopyValue is what c copies: the request id of an invocation on the invocations tab, the
// payload of the last invocation on the invoke tab, the arn elsewhere.
func (m model) functionCopyValue() string {
	f := m.state.function
	switch f.tab {
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
	return tabBar(functionTabs, counts, int(f.tab)) + "\n\n" + body
}

const labelWidth = 14

// row renders a label right aligned next to its value, cut to width.
func row(label string, value cell, width int) string {
	l := styles.Fg(styles.ToneFaint).Width(labelWidth).Align(lipgloss.Right).PaddingRight(2).Render(truncate(label, labelWidth-2))
	return l + renderCell(value, column{width: max(1, width-labelWidth)}, nil)
}

// scrolled shows height lines of lines from offset, as far as there are.
func scrolled(lines []string, offset, height int) string {
	offset = max(0, min(offset, len(lines)-height))
	end := min(len(lines), offset+height)
	return strings.Join(lines[offset:end], "\n")
}

func sep() styles.Span { return styles.S(" · ", styles.ToneFaint) }

// liveLines say what lambda says of the function now, and where it differs from the deploy.
func (m model) liveLines(width int) []string {
	f := m.state.function
	lines := []string{styles.SectionHeader("in lambda now", width-2)}
	switch {
	case f.lambdaName == "":
		return append(lines, styles.Faint("not deployed yet"))
	case f.live == nil && f.liveErr != "":
		return append(lines, renderCell(text(f.liveErr, styles.ToneDanger), column{width: width - 2}, nil))
	case f.live == nil:
		return append(lines, styles.Faint("reading…"))
	}
	live := f.live
	state := cell{styles.S(strings.ToLower(orDash(live.State)), stateTone(live.State))}
	if live.StateReason != "" {
		state = append(state, sep(), styles.S(live.StateReason, styles.ToneMuted))
	}
	update := cell{styles.S(strings.ToLower(orDash(live.LastUpdate)), stateTone(live.LastUpdate))}
	if live.LastUpdateReason != "" {
		update = append(update, sep(), styles.S(live.LastUpdateReason, styles.ToneMuted))
	}
	code := cell{styles.S("✓ ", styles.ToneSuccess), styles.S("what koldstart deployed", styles.ToneBody)}
	if live.CodeSHA256 != "" && f.inputs.Code.SHA256 != "" && live.CodeSHA256 != f.inputs.Code.SHA256 {
		code = cell{styles.S("▲ ", styles.ToneWarning), styles.S("changed outside koldstart: "+shortSHA(live.CodeSHA256), styles.ToneWarning)}
	}
	lines = append(lines,
		row("state", state, width),
		row("last update", update, width),
		row("modified", text(formatTime(live.LastModified)+", "+formatAgo(live.LastModified), styles.ToneBody), width),
		row("code", code, width),
		row("code size", text(formatBytes(live.CodeSize), styles.ToneBody), width),
	)
	if d := configDrift(f.inputs, *live); len(d) > 0 {
		lines = append(lines, row("drift", text(strings.Join(d, ", ")+" changed outside koldstart", styles.ToneWarning), width))
	}
	return lines
}

// configDrift names what lambda runs with that differs from what koldstart deployed.
func configDrift(in awsp.FunctionInputs, live cli.FunctionState) []string {
	var out []string
	if live.Memory != 0 && in.Memory != 0 && live.Memory != in.Memory {
		out = append(out, fmt.Sprintf("memory %d → %d", in.Memory, live.Memory))
	}
	if live.Timeout != 0 && in.Timeout != 0 && live.Timeout != in.Timeout {
		out = append(out, fmt.Sprintf("timeout %ds → %ds", in.Timeout, live.Timeout))
	}
	if live.Runtime != "" && in.Runtime != "" && live.Runtime != in.Runtime {
		out = append(out, "runtime "+in.Runtime+" → "+live.Runtime)
	}
	return out
}

func stateTone(state string) styles.Tone {
	switch strings.ToLower(state) {
	case "active", "successful":
		return styles.ToneSuccess
	case "pending", "inprogress":
		return styles.ToneWarning
	case "failed", "inactive":
		return styles.ToneDanger
	}
	return styles.ToneBody
}

// metricLines draw the last hour of the function's metrics as sparklines, with their totals.
func (m model) metricLines(width int) []string {
	s := m.state.stage
	f := m.state.function
	lines := []string{styles.SectionHeader("last hour", width-2)}
	mt, ok := s.metrics[f.lambdaName]
	switch {
	case !ok && s.metricsErr != "":
		return append(lines, renderCell(text("no metrics: "+s.metricsErr, styles.ToneFaint), column{width: width - 2}, nil))
	case !ok:
		return append(lines, styles.Faint("reading…"))
	}
	sum := mt.Summary()
	spark := max(8, width-labelWidth-14)
	line := func(label string, series []float64, tone styles.Tone, total string, totalTone styles.Tone) string {
		if slices.Max(series) == 0 {
			return row(label, text("none", styles.ToneFaint), width)
		}
		return row(label, cell{styles.S(sparkline(series, spark), tone), styles.S("  "+total, totalTone)}, width)
	}
	errTone := styles.ToneFaint
	if sum.Errors > 0 {
		errTone = styles.ToneDanger
	}
	throttleTone := styles.ToneFaint
	if sum.Throttles > 0 {
		throttleTone = styles.ToneWarning
	}
	return append(lines,
		line("invocations", mt.Invocations, styles.ToneAccent, formatInt(int(sum.Invocations)), styles.ToneBody),
		line("errors", mt.Errors, styles.ToneDanger, formatInt(int(sum.Errors)), errTone),
		line("throttles", mt.Throttles, styles.ToneWarning, formatInt(int(sum.Throttles)), throttleTone),
		line("duration", mt.Duration, styles.ToneInfo, "p99 "+formatMillis(sum.DurationP99), styles.ToneBody),
		line("concurrency", mt.Concurrency, styles.ToneMuted, "max "+formatInt(int(slices.Max(mt.Concurrency))), styles.ToneBody),
	)
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

func (m model) overviewTabView() string {
	f := m.state.function
	in := f.inputs
	left := (contentWidth - 4) / 2
	right := contentWidth - 4 - left
	concurrency := "unreserved"
	if in.ReservedConcurrency != nil {
		concurrency = formatInt(int(*in.ReservedConcurrency))
	}
	value := func(s string) cell { return text(orDash(s), styles.ToneBody) }
	lines := []string{
		styles.SectionHeader("function", left-2),
		row("name in lambda", value(f.lambdaName), left),
		row("arn", value(f.arn), left),
		row("runtime", cell{styles.S(orDash(in.Runtime), styles.ToneBody), sep(), styles.S(orDash(in.Architecture), styles.ToneMuted)}, left),
		row("handler", value(in.Handler), left),
		row("memory", value(formatInt(int(in.Memory))+" mb"), left),
		row("timeout", value(formatInt(int(in.Timeout))+"s"), left),
		row("/tmp", value(formatInt(int(in.EphemeralStorage))+" mb"), left),
		row("concurrency", value(concurrency), left),
		row("code", text(orDash(shortSHA(in.Code.SHA256)), styles.ToneMuted), left),
		row("role", value(in.Role), left),
		row("log group", value(f.logGroup), left),
		row("url", value(f.url), left),
	}
	if in.VPC != nil {
		lines = append(lines, row("subnets", value(strings.Join(in.VPC.SubnetIDs, ", ")), left))
	}
	lines = append(lines, "")
	lines = append(lines, m.liveLines(left)...)

	side := m.metricLines(right)
	side = append(side, "", styles.SectionHeader("environment", right-2))
	for _, k := range slices.Sorted(maps.Keys(f.env)) {
		side = append(side, renderCell(cell{styles.S(k, styles.ToneMuted), styles.S(" = ", styles.ToneFaint), styles.S(engine.Describe(f.env[k]), styles.ToneBody)}, column{width: right - 2}, nil))
	}
	if len(f.env) == 0 {
		side = append(side, styles.Faint("none"))
	}
	side = append(side, "", styles.SectionHeader("invoked by", right-2))
	for _, id := range f.triggers {
		side = append(side, renderCell(text(id, styles.ToneBody), column{width: right - 2}, nil))
	}
	if len(f.triggers) == 0 {
		side = append(side, styles.Faint("nothing: invoke it with i"))
	}
	height := functionBodyHeight()
	body := lipgloss.JoinHorizontal(lipgloss.Top,
		fill(scrolled(lines, f.scroll, height), left, height),
		"  ",
		fill(scrolled(side, f.scroll, height), right, height),
	)
	return lipgloss.NewStyle().PaddingLeft(1).Render(body)
}
