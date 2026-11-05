package tui

import (
	"encoding/json"
	"fmt"
	"maps"
	"regexp"
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
	f.liveAnswered = msg.gen
	if msg.err != nil {
		f.liveErr = msg.err.Error()
		return m, nil
	}
	live := msg.live
	f.live, f.liveAt, f.liveErr = &live, time.Now(), ""
	return m, nil
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
	f.scroll = scrollBy(msg, m.keys, f.scroll, functionBodyHeight(), m.functionOverview().maxScroll())
	return m, nil
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
	return indent(tabBar(functionTabs, counts, int(f.tab))) + "\n\n" + body
}

// functionPosition is where the function's tab is: the lines in sight of the overview, the logs
// and what an invocation returned, or the invocation under the cursor.
func (m model) functionPosition() string {
	f := m.state.function
	switch f.tab {
	case overviewTab:
		o := m.functionOverview()
		return scrollCue(min(f.scroll, o.maxScroll()), o.height(), max(len(o.left), len(o.right)))
	case logsTab:
		return m.logsPosition()
	case invocationsTab:
		if f.logs.loaded {
			return f.invocations.position()
		}
	case invokeTab:
		height := functionBodyHeight()
		n := len(m.invokeLines(resultWidth()))
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

// scrolled shows height lines of lines from offset, as far as there are.
func scrolled(lines []string, offset, height int) string {
	offset = max(0, min(offset, maxOffset(len(lines), height)))
	end := min(len(lines), offset+height)
	return strings.Join(lines[offset:end], "\n")
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

// functionOverview is what the overview tab draws: a notice of what changed outside koldstart,
// and below it two columns that scroll together.
type functionOverview struct {
	notice     []string
	left       []string
	right      []string
	leftWidth  int
	rightWidth int
}

// height is how many lines of the columns are in sight.
func (o functionOverview) height() int {
	if len(o.notice) == 0 {
		return functionBodyHeight()
	}
	return max(1, functionBodyHeight()-len(o.notice)-1)
}

// maxScroll is the furthest the columns scroll: to the end of the longer one.
func (o functionOverview) maxScroll() int {
	return maxOffset(max(len(o.left), len(o.right)), o.height())
}

// functionOverview lays out the overview: the configuration and what lambda says of the
// function on the left; the last hour, the environment and what invokes it on the right.
func (m model) functionOverview() functionOverview {
	o := functionOverview{leftWidth: (contentWidth - 4) / 2}
	o.rightWidth = contentWidth - 4 - o.leftWidth
	o.notice = m.driftNotice(contentWidth - 2)
	o.left = append(m.configLines(o.leftWidth), "")
	o.left = append(o.left, m.liveLines(o.leftWidth)...)
	o.right = append(m.metricLines(o.rightWidth), "")
	o.right = append(o.right, m.envLines(o.rightWidth)...)
	o.right = append(o.right, "")
	o.right = append(o.right, m.triggerLines(o.rightWidth)...)
	return o
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

// configLines are what koldstart deployed the function with.
func (m model) configLines(width int) []string {
	f := m.state.function
	in := f.inputs
	w := width - 2
	value := func(s string) cell { return text(orDash(s), styles.ToneBody) }
	long := func(s string) cell { return value(cutMiddle(styles.Clean(s), w-labelWidth)) }
	runtime := cell{styles.S(orDash(in.Runtime), styles.ToneBody)}
	if in.Architecture != "" {
		runtime = append(runtime, sep(), styles.S(in.Architecture, styles.ToneMuted))
	}
	concurrency := text("unreserved", styles.ToneMuted)
	if rc := in.ReservedConcurrency; rc != nil {
		concurrency = text(formatInt(int(*rc))+" reserved", styles.ToneBody)
		if *rc == 0 {
			concurrency = text("0 reserved: every invocation is throttled", styles.ToneWarning)
		}
	}
	lines := []string{
		styles.SectionHeader("function", w),
		row("name", long(f.lambdaName), w),
		row("arn", long(f.arn), w),
		row("runtime", runtime, w),
		row("handler", value(in.Handler), w),
		row("memory", value(megabytes(in.Memory)), w),
		row("timeout", value(seconds(in.Timeout)), w),
		row("/tmp", value(megabytes(in.EphemeralStorage)), w),
		row("concurrency", concurrency, w),
		row("code", text(orDash(shortSHA(in.Code.SHA256)), styles.ToneFaint), w),
		row("role", long(in.Role), w),
		row("log group", long(f.logGroup), w),
	}
	if in.VPC != nil {
		lines = append(lines,
			row("subnets", value(strings.Join(in.VPC.SubnetIDs, ", ")), w),
			row("groups", value(strings.Join(in.VPC.SecurityGroupIDs, ", ")), w),
		)
	}
	return lines
}

// codeChanged reports whether lambda runs other code than koldstart deployed.
func codeChanged(in awsp.FunctionInputs, live cli.FunctionState) bool {
	return live.CodeSHA256 != "" && in.Code.SHA256 != "" && live.CodeSHA256 != in.Code.SHA256
}

// driftNotice says what lambda runs that koldstart did not deploy, above the columns, so it is
// in sight however far they scroll.
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
	return wrapped("▲ changed outside koldstart: "+strings.Join(changed, ", "), styles.ToneWarning, width, 2)
}

// liveLines say what lambda says of the function now.
func (m model) liveLines(width int) []string {
	f := m.state.function
	w := width - 2
	lines := []string{styles.SectionHeader("in lambda now", w)}
	switch {
	case f.lambdaName == "":
		return append(lines, styles.Faint("not deployed yet"))
	case f.live == nil && f.liveErr != "":
		return append(lines, cellLines(m.couldNotRead("it from lambda", f.liveErr, styles.ToneMuted), w)...)
	case f.live == nil:
		return append(lines, styles.Faint(readingNote("it from lambda")))
	case f.liveErr != "":
		lines = append(lines, cellLines(staleNote("", f.liveAt, f.liveErr), w)...)
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
	modified := text("-", styles.ToneBody)
	if !live.LastModified.IsZero() {
		modified = text(formatTime(live.LastModified)+", "+formatAgo(live.LastModified), styles.ToneBody)
	}
	var code cell
	switch {
	case codeChanged(f.inputs, *live):
		code = cell{styles.S("▲ ", styles.ToneWarning), styles.S("changed outside koldstart: "+shortSHA(live.CodeSHA256), styles.ToneWarning)}
	case live.CodeSHA256 != "" && f.inputs.Code.SHA256 != "":
		code = cell{styles.S("✓ ", styles.ToneSuccess), styles.S("what koldstart deployed", styles.ToneBody)}
	}
	if live.CodeSize > 0 {
		if len(code) > 0 {
			code = append(code, sep())
		}
		code = append(code, styles.S(cli.Size(live.CodeSize), styles.ToneMuted))
	}
	if len(code) == 0 {
		code = text("-", styles.ToneBody)
	}
	return append(lines,
		row("state", state, w),
		row("last update", update, w),
		row("modified", modified, w),
		row("code", code, w),
	)
}

// configDrift names what lambda runs with that differs from what koldstart deployed.
func configDrift(in awsp.FunctionInputs, live cli.FunctionState) []string {
	var out []string
	if live.Memory != 0 && in.Memory != 0 && live.Memory != in.Memory {
		out = append(out, fmt.Sprintf("memory %s → %s", formatInt(int(in.Memory)), megabytes(live.Memory)))
	}
	if live.Timeout != 0 && in.Timeout != 0 && live.Timeout != in.Timeout {
		out = append(out, "timeout "+seconds(in.Timeout)+" → "+seconds(live.Timeout))
	}
	if live.Runtime != "" && in.Runtime != "" && live.Runtime != in.Runtime {
		out = append(out, "runtime "+in.Runtime+" → "+live.Runtime)
	}
	return out
}

// stateTone is the tone of a state lambda reports. A pending function or an update in progress is
// in flight, in the accent. An inactive function is one lambda reclaimed after weeks without
// invocations: the next invocation wakes it, so it warns rather than fails.
func stateTone(state string) styles.Tone {
	switch strings.ToLower(state) {
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

// metricLines draw the last hour of the function's metrics as sparklines, each ending in its
// total, or its peak for duration and concurrency, which is the top of its sparkline.
func (m model) metricLines(width int) []string {
	s := m.state.stage
	f := m.state.function
	w := width - 2
	lines := []string{styles.SectionHeader("last hour", w)}
	mt, ok := s.metrics[f.lambdaName]
	switch {
	case f.lambdaName == "":
		return append(lines, styles.Faint("not deployed yet"))
	case !ok && s.metricsErr != "":
		return append(lines, cellLines(m.couldNotRead("the metrics", s.metricsErr, styles.ToneMuted), w)...)
	case !ok:
		return append(lines, styles.Faint(readingNote("the metrics")))
	case s.metricsErr != "":
		lines = append(lines, cellLines(staleNote("", s.metricsAt, s.metricsErr), w)...)
	}
	if peak(mt.Invocations) == 0 && peak(mt.Errors) == 0 && peak(mt.Throttles) == 0 {
		return append(lines, styles.Faint("not invoked in the last hour"))
	}
	sum := mt.Summary()
	spark := max(8, w-labelWidth-12)
	line := func(label string, series []float64, tone styles.Tone, total string, totalTone styles.Tone) string {
		if peak(series) == 0 {
			return row(label, text("none", styles.ToneFaint), w)
		}
		return row(label, cell{styles.S(sparkline(series, spark), tone), styles.S("  "+total, totalTone)}, w)
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
		line("duration", mt.DurationP99, styles.ToneInfo, "p99 "+formatMillis(sum.DurationP99), styles.ToneBody),
		line("concurrency", mt.Concurrency, styles.ToneMuted, "max "+formatInt(int(peak(mt.Concurrency))), styles.ToneBody),
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

// envLines list the function's environment, the names in a column of their own.
func (m model) envLines(width int) []string {
	f := m.state.function
	w := width - 2
	lines := []string{styles.SectionHeader("environment", w)}
	if len(f.env) == 0 {
		return append(lines, styles.Faint("none"))
	}
	names := slices.Sorted(maps.Keys(f.env))
	nameWidth := 0
	for _, k := range names {
		nameWidth = max(nameWidth, ansi.StringWidth(styles.Clean(k)))
	}
	nameWidth = min(nameWidth, w/3)
	for _, k := range names {
		name := renderCell(text(k, styles.ToneMuted), column{width: nameWidth}, nil)
		value := renderCell(cell{styles.S(" = ", styles.ToneFaint), envValue(k, f.env[k])}, column{width: max(1, w-nameWidth)}, nil)
		lines = append(lines, name+value)
	}
	return lines
}

// triggerLines list what invokes the function: its url, routes, schedules, queues, topics and
// buckets, each by its kind and name.
func (m model) triggerLines(width int) []string {
	f := m.state.function
	w := width - 2
	lines := []string{styles.SectionHeader("invoked by", w)}
	type trigger struct{ kind, name string }
	var triggers []trigger
	if f.url != "" {
		triggers = append(triggers, trigger{"url", f.url})
	}
	for _, id := range f.triggers {
		kind, rest, _ := strings.Cut(id, "/")
		triggers = append(triggers, trigger{kind, strings.TrimPrefix(rest, f.name+"/")})
	}
	if len(triggers) == 0 {
		return append(lines, styles.Faint("nothing: invoke it with "+m.keys.Invoke.Help().Key))
	}
	kindWidth := 0
	for _, t := range triggers {
		kindWidth = max(kindWidth, ansi.StringWidth(styles.Clean(t.kind)))
	}
	for _, t := range triggers {
		kind := renderCell(text(t.kind, styles.ToneFaint), column{width: kindWidth + 2}, nil)
		lines = append(lines, kind+renderCell(text(t.name, styles.ToneBody), column{width: max(1, w-kindWidth-2)}, nil))
	}
	return lines
}

// overviewTabView draws the overview at its scroll.
func (m model) overviewTabView() string {
	o := m.functionOverview()
	height := o.height()
	scroll := min(m.state.function.scroll, o.maxScroll())
	body := lipgloss.JoinHorizontal(lipgloss.Top,
		fill(scrolled(o.left, scroll, height), o.leftWidth, height),
		"  ",
		fill(scrolled(o.right, scroll, height), o.rightWidth, height),
	)
	if len(o.notice) > 0 {
		body = strings.Join(o.notice, "\n") + "\n\n" + body
	}
	return lipgloss.NewStyle().PaddingLeft(1).Render(body)
}
