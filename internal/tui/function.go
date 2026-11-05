package tui

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
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
	configTab functionTab = iota
	logsTab
	invokeTab
)

var functionTabs = []string{"configuration", "logs", "invoke"}

// logsSince is how far back the logs start when the function is opened.
const logsSince = 30 * time.Minute

// defaultEvent is the event a function is invoked with when its events directory has none.
const defaultEvent = "{}"

// functionState is one function of the stage: its configuration, its logs, and invoking it.
type functionState struct {
	name       string
	lambdaName string
	logGroup   string
	arn        string
	url        string
	inputs     awsp.FunctionInputs
	env        map[string]any
	triggers   []string
	tab        functionTab
	scroll     int

	logsGen    int
	logsLoaded bool
	logs       []cli.LogEvent
	// follow keeps the newest log lines in sight and reads new ones as they come; logScroll is
	// how many lines the view is above the newest.
	follow    bool
	logScroll int

	events     dataTable
	eventFiles []string
	invokeGen  int
	invoking   bool
	results    map[string]invocationResult
}

// invocationResult is the last invocation with an event.
type invocationResult struct {
	inv   cli.Invocation
	local bool
	at    time.Time
	err   error
}

var eventColumns = []column{{title: "event", width: 12, grow: 1}}

func eventsWidth() int { return min(36, contentWidth/3) }

func functionBodyHeight() int { return contentHeight - 2 }

// openFunction shows a function of the stage, its configuration first, and reads its logs.
func (m model) openFunction(name string) (model, tea.Cmd) {
	f := &m.state.function
	if f.name != name {
		*f = functionState{name: name, follow: true, results: map[string]invocationResult{}, events: newTable(eventColumns, "")}
		f.events.setSize(eventsWidth(), functionBodyHeight()-tableHeaderRows)
	}
	m = m.switchPage(functionPage).syncFunction()
	m = m.readEventFiles()
	if f.logGroup == "" {
		return m, nil
	}
	return m.loadLogs(time.Now().Add(-logsSince), false)
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
		f.logGroup = g.PhysicalID
	}
	f.url = ""
	if u := st.Resources[desired.URLID(f.name)]; u != nil {
		f.url = u.Attributes["FunctionUrl"]
	}
	f.triggers = desired.Triggers(st.Resources, f.name)
	return m
}

// readEventFiles lists the events kept for the function, keeping the cursor on the one it was on.
func (m model) readEventFiles() model {
	f := &m.state.function
	entries, _ := os.ReadDir(m.b.EventsDir(f.name))
	var files []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
			files = append(files, e.Name())
		}
	}
	current := m.selectedEvent()
	f.eventFiles = files
	rows := make([]tableRow, 0, len(files))
	for _, name := range files {
		rows = append(rows, tableRow{text(strings.TrimSuffix(name, ".json"), styles.ToneText)})
	}
	if len(files) == 0 {
		rows = []tableRow{{cell{styles.S(defaultEvent, styles.ToneText), styles.S("  e to edit", styles.ToneFaint)}}}
	}
	f.events.setRows(rows)
	for i, name := range files {
		if name == current {
			f.events.setCursor(i)
		}
	}
	return m
}

// selectedEvent is the file of the event under the cursor, or "" for the default event.
func (m model) selectedEvent() string {
	f := m.state.function
	if f.events.cursor < len(f.eventFiles) {
		return f.eventFiles[f.events.cursor]
	}
	return ""
}

// eventPayload reads the selected event.
//
//nolint:gosec // reads an event file of the project
func (m model) eventPayload() ([]byte, error) {
	name := m.selectedEvent()
	if name == "" {
		return []byte(defaultEvent), nil
	}
	data, err := os.ReadFile(filepath.Join(m.b.EventsDir(m.state.function.name), name))
	if err != nil {
		return nil, fmt.Errorf("reading the event %s: %w", name, err)
	}
	return data, nil
}

// editEvent opens the selected event in $VISUAL or $EDITOR, creating event.json with {} when the
// function has no events yet.
//
//nolint:gosec // runs the editor the user set, on an event of the project
func (m model) editEvent() (model, tea.Cmd) {
	dir := m.b.EventsDir(m.state.function.name)
	name := m.selectedEvent()
	if name == "" {
		name = "event.json"
		if err := os.MkdirAll(dir, 0o750); err != nil {
			m.error = "could not create " + dir + ": " + err.Error()
			return m, nil
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(defaultEvent+"\n"), 0o600); err != nil {
			m.error = "could not create the event: " + err.Error()
			return m, nil
		}
	}
	editor := cmpOr(os.Getenv("VISUAL"), os.Getenv("EDITOR"), "vi")
	args := strings.Fields(editor)
	cmd := exec.Command(args[0], append(args[1:], filepath.Join(dir, name))...)
	return m, tea.ExecProcess(cmd, func(err error) tea.Msg { return eventEditedMsg{err: err} })
}

// startInvoke invokes the function with the selected event, in aws or on this machine. Against
// prod, running here asks first.
func (m model) startInvoke(local bool) (model, tea.Cmd) {
	f := &m.state.function
	ref := m.state.stage.ref
	switch {
	case f.invoking:
		return m.setStatus("an invocation is running already", styles.ToneWarning)
	case local && !m.b.Local(ref):
		return m.setStatus("only the project here runs on this machine: run kdst ui in its directory", styles.ToneWarning)
	case !local && f.lambdaName == "":
		return m.setStatus(f.name+" is not deployed yet", styles.ToneWarning)
	}
	payload, err := m.eventPayload()
	if err != nil {
		m.error = err.Error()
		return m, nil
	}
	f.tab = invokeTab
	if local && ref.Stage == "prod" {
		m.state.runLocal = runLocalState{event: m.selectedEvent(), payload: payload}
		return m.switchPage(runLocalPage), nil
	}
	return m.invoke(m.selectedEvent(), payload, local)
}

func (m model) applyInvoked(msg invokedMsg) (model, tea.Cmd) {
	f := &m.state.function
	if msg.gen != f.invokeGen {
		return m, nil
	}
	f.invoking = false
	f.results[msg.event] = invocationResult{inv: msg.inv, local: msg.local, at: time.Now(), err: msg.err}
	f.scroll = 0
	switch {
	case msg.err != nil:
		return m.setStatus("could not invoke "+f.name, styles.ToneDanger)
	case msg.inv.Error != "":
		return m.setStatus(f.name+" failed: "+msg.inv.Error, styles.ToneDanger)
	}
	return m.setStatus(f.name+" returned in "+formatDuration(msg.inv.Duration), styles.ToneSuccess)
}

func (m model) applyLogs(msg logsLoadedMsg) (model, tea.Cmd) {
	f := &m.state.function
	if msg.gen != f.logsGen {
		return m, nil
	}
	if msg.err != nil {
		return m.loadError("could not read the logs of "+f.name, msg.err, f.logsLoaded)
	}
	if msg.appended {
		f.logs = append(f.logs, msg.events...)
		if len(f.logs) > 2000 {
			f.logs = slices.Delete(f.logs, 0, len(f.logs)-2000)
		}
	} else {
		f.logs = msg.events
	}
	f.logsLoaded = true
	if m.page == functionPage {
		m.refreshedAt = time.Now()
	}
	return m, nil
}

// refreshLogs reads the lines that came since the newest on screen.
func (m model) refreshLogs() (model, tea.Cmd) {
	f := m.state.function
	if f.logGroup == "" || !f.follow {
		return m, nil
	}
	if len(f.logs) == 0 {
		return m.loadLogs(time.Now().Add(-logsSince), false)
	}
	return m.loadLogs(f.logs[len(f.logs)-1].Time.Add(time.Millisecond), true)
}

func (m model) functionUpdate(msg tea.KeyPressMsg) (model, tea.Cmd) {
	f := &m.state.function
	switch {
	case key.Matches(msg, m.keys.Back, m.keys.Quit):
		return m.openStage(m.state.stage.ref)
	case key.Matches(msg, m.keys.NextTab):
		f.tab, f.scroll = (f.tab+1)%3, 0
		return m, nil
	case key.Matches(msg, m.keys.PrevTab):
		f.tab, f.scroll = (f.tab+2)%3, 0
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
		m, cmd := m.loadStage()
		if f.logGroup != "" {
			var logs tea.Cmd
			m, logs = m.loadLogs(time.Now().Add(-logsSince), false)
			cmd = tea.Batch(cmd, logs)
		}
		return m, cmd
	case key.Matches(msg, m.keys.Copy):
		return m.copy(m.functionCopyValue())
	case f.tab == logsTab && key.Matches(msg, m.keys.Follow):
		f.follow = !f.follow
		if f.follow {
			f.logScroll = 0
			return m.refreshLogs()
		}
		return m, nil
	}
	switch f.tab {
	case logsTab:
		return m.scrollLogs(msg), nil
	case invokeTab:
		if f.events.move(msg, m.keys) {
			f.scroll = 0
			return m, nil
		}
	}
	m.state.function.scroll = scrollBy(msg, m.keys, f.scroll, functionBodyHeight())
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

// scrollLogs moves through the logs. Leaving the newest line stops following them.
func (m model) scrollLogs(msg tea.KeyPressMsg) model {
	f := &m.state.function
	page := functionBodyHeight()
	switch {
	case key.Matches(msg, m.keys.Up):
		f.logScroll++
	case key.Matches(msg, m.keys.Down):
		f.logScroll--
	case key.Matches(msg, m.keys.PageUp):
		f.logScroll += page
	case key.Matches(msg, m.keys.PageDown):
		f.logScroll -= page
	case key.Matches(msg, m.keys.Top):
		f.logScroll = 1 << 30
	case key.Matches(msg, m.keys.Bottom):
		f.logScroll = 0
	default:
		return m
	}
	f.logScroll = max(0, f.logScroll)
	if f.logScroll > 0 {
		f.follow = false
	}
	return m
}

// functionCopyValue is what c copies: the payload of the last invocation on the invoke tab, the
// arn elsewhere.
func (m model) functionCopyValue() string {
	f := m.state.function
	if r, ok := f.results[m.selectedEvent()]; ok && f.tab == invokeTab {
		return string(r.inv.Payload)
	}
	return cmpOr(f.arn, f.lambdaName)
}

func (m model) functionView() string {
	f := m.state.function
	var body string
	switch f.tab {
	case configTab:
		body = m.configView()
	case logsTab:
		body = m.logsView()
	case invokeTab:
		body = m.invokeView()
	}
	return tabBar(functionTabs, nil, int(f.tab)) + "\n\n" + body
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

func (m model) configView() string {
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
		row("runtime", value(in.Runtime), left),
		row("handler", value(in.Handler), left),
		row("memory", value(formatInt(int(in.Memory))+" mb"), left),
		row("timeout", value(formatInt(int(in.Timeout))+"s"), left),
		row("architecture", value(in.Architecture), left),
		row("/tmp", value(formatInt(int(in.EphemeralStorage))+" mb"), left),
		row("concurrency", value(concurrency), left),
		row("code", text(orDash(in.Code.SHA256), styles.ToneMuted), left),
		row("role", value(in.Role), left),
		row("log group", value(f.logGroup), left),
		row("url", value(f.url), left),
	}
	if in.VPC != nil {
		lines = append(lines, row("subnets", value(strings.Join(in.VPC.SubnetIDs, ", ")), left))
	}

	side := []string{styles.SectionHeader("environment", right-2)}
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

func (m model) logsView() string {
	f := m.state.function
	width, height := contentWidth-4, functionBodyHeight()
	switch {
	case f.logGroup == "":
		return " " + styles.Muted("no log group: deploy the function first")
	case !f.logsLoaded:
		return " " + styles.Muted("reading the logs…")
	case len(f.logs) == 0:
		return " " + styles.Muted(fmt.Sprintf("nothing logged in the last %s", logsSince))
	}
	var lines []string
	for _, e := range f.logs {
		stamp := styles.Faint(e.Time.Local().Format("15:04:05.000")) + "  "
		message := styles.CleanBlock(e.Message)
		for i, line := range strings.Split(ansi.Hardwrap(message, width-14, true), "\n") {
			if i > 0 {
				stamp = strings.Repeat(" ", 14)
			}
			lines = append(lines, stamp+styles.Body(line))
		}
	}
	end := max(0, len(lines)-f.logScroll)
	start := max(0, end-height)
	return lipgloss.NewStyle().PaddingLeft(1).Render(strings.Join(lines[start:end], "\n"))
}

func (m model) invokeView() string {
	f := m.state.function
	height := functionBodyHeight()
	left := eventsWidth()
	right := contentWidth - 4 - left - 2
	r, ok := f.results[m.selectedEvent()]
	var lines []string
	switch {
	case f.invoking:
		lines = []string{styles.Accent(m.spinner.View()) + " " + styles.Muted("invoking "+f.name+"…")}
	case !ok:
		lines = []string{styles.Muted("i invokes " + f.name + " with this event"), styles.Faint("shift+i runs it on this machine, e edits the event")}
	case r.err != nil:
		lines = []string{styles.Render(styles.S("✗ ", styles.ToneDanger), styles.S(r.err.Error(), styles.ToneDanger))}
	default:
		lines = resultLines(r, right)
	}
	return lipgloss.NewStyle().PaddingLeft(1).Render(lipgloss.JoinHorizontal(lipgloss.Top,
		fill(f.events.View(), left, height),
		"  ",
		fill(scrolled(lines, f.scroll, height), right, height),
	))
}

// resultLines shows what an invocation returned, and its log.
func resultLines(r invocationResult, width int) []string {
	where := "in aws"
	if r.local {
		where = "on this machine"
	}
	outcome := styles.S("✓ returned", styles.ToneSuccess)
	if r.inv.Error != "" {
		outcome = styles.S("✗ failed: "+r.inv.Error, styles.ToneDanger)
	}
	status := []styles.Span{outcome, styles.S(" in "+formatDuration(r.inv.Duration)+" "+where+", "+formatAgo(r.at), styles.ToneFaint)}
	lines := []string{styles.Render(status...), "", styles.SectionHeader("returned", width-2)}
	wrap := func(s string) []string {
		return strings.Split(ansi.Hardwrap(styles.CleanBlock(strings.TrimRight(s, "\n")), width-2, true), "\n")
	}
	for _, line := range wrap(prettyJSON(r.inv.Payload)) {
		lines = append(lines, styles.Body(line))
	}
	if strings.TrimSpace(r.inv.Logs) != "" {
		lines = append(lines, "", styles.SectionHeader("log", width-2))
		for _, line := range wrap(r.inv.Logs) {
			lines = append(lines, styles.Muted(line))
		}
	}
	return lines
}
