package tui

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
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
	"github.com/kontrolplane/koldstart/internal/tui/styles"
)

// defaultEvent is the event a function is invoked with when its events directory has none.
const defaultEvent = "{}"

// defaultEventFile is the event e creates when the function has none.
const defaultEventFile = "event.json"

// invocationResult is the last invocation with an event. While running is set, one more is on
// its way, and the rest is the one before it.
type invocationResult struct {
	inv     cli.Invocation
	local   bool
	running bool
	at      time.Time
	err     error
	// failure is the error the function failed with, read from what it returned; failed is set
	// when what it returned was one.
	failure cli.Failure
	failed  bool
	// report is what lambda's REPORT line in the log's tail says of the invocation, when it has
	// one.
	report request
	// view is what it returned, as json or as the lines it is.
	view jsonView
	// logs are the cells of its log as the last frame wrapped them, so the next does not again.
	logs *wrappedLog
}

// wrappedLog is an invocation's log wrapped to width in the palette numbered palette. It holds
// no colour, but is wrapped again in a new palette all the same, so nothing kept outlives one.
type wrappedLog struct {
	width   int
	palette int
	cells   []cell
}

// newResult reads what an invocation returned: the error it failed with, the report in its log
// and the json of its payload. It runs off the interface's goroutine, as a payload can be
// megabytes.
func newResult(inv cli.Invocation, local bool, err error) invocationResult {
	r := invocationResult{inv: inv, local: local, err: err, logs: &wrappedLog{}}
	if err != nil {
		return r
	}
	if inv.Error != "" {
		r.failure, r.failed = cli.ReadFailure(inv.Payload)
	}
	r.report = tailReport(inv)
	if !r.failed {
		r.view = newJSONViewBytes(inv.Payload)
	}
	return r
}

// tailReport is what the REPORT line of an invocation's log tail says of it: of the request lambda
// gave it, or else of the last one reported.
func tailReport(inv cli.Invocation) request {
	if !strings.Contains(inv.Logs, "REPORT RequestId:") {
		return request{}
	}
	var events []cli.LogEvent
	for line := range strings.SplitSeq(inv.Logs, "\n") {
		events = append(events, cli.LogEvent{Stream: "tail", Message: line})
	}
	var out request
	for _, r := range requests(events) {
		switch {
		case !r.reported:
		case r.id == inv.RequestID:
			return r
		case out.id == "":
			out = r
		}
	}
	return out
}

// timedOut reports whether lambda ended the invocation at the function's timeout.
func (r invocationResult) timedOut() bool {
	return r.inv.Error != "" && (r.failed && r.failure.TimedOut() || r.report.timedOut)
}

// took is how long the invocation ran: what lambda's report says, or else how long the call took.
func (r invocationResult) took() time.Duration {
	if r.report.reported {
		return time.Duration(r.report.duration * float64(time.Millisecond))
	}
	return r.inv.Duration
}

// requestID is the id lambda gave the invocation, from its answer or its report.
func (r invocationResult) requestID() string {
	return cmpOr(r.inv.RequestID, r.report.id)
}

// logCells are the cells of the log at width, or as the last frame wrapped them.
func (r invocationResult) logCells(width int) []cell {
	if w := r.logs; w != nil && w.width == width && w.palette == styles.Gen() && w.cells != nil {
		return w.cells
	}
	cells := logCells(r.inv.Logs, width)
	if r.logs != nil {
		*r.logs = wrappedLog{width: width, palette: styles.Gen(), cells: cells}
	}
	return cells
}

// errNotJSON is an event file that does not hold json.
var errNotJSON = errors.New("not json")

// eventFile is an event kept for the function, as it was when the events were listed.
type eventFile struct {
	size     int64
	modified time.Time
	// data is what it holds, and err why it cannot be sent, nil for one that can.
	data []byte
	err  error
	// view is what it holds as json, or as the lines it is, read once so choosing it costs
	// nothing.
	view jsonView
}

var eventColumns = []column{
	{title: "event", width: 12, grow: 1},
	{title: "size", width: 7, right: true, drop: 2},
	{title: "modified", width: 8, right: true, drop: 1},
}

// invokeWideWidth is the content width from which the events and the pane are side by side;
// below it the pane is under the events.
const invokeWideWidth = 96

// invokeLayout is where the invoke tab draws: the events, their width and the rows they show,
// and the pane of the payload or the result, beside them when the terminal is wide and under them
// when it is narrow, where the pane takes all of the tab while its json has the keys.
type invokeLayout struct {
	wide       bool
	showEvents bool
	eventsW    int
	eventsH    int
	paneW      int
	paneH      int
}

// paneChrome is the lines above the pane's body: its tabs and a rule.
const paneChrome = 2

func (m model) invokeLayout() invokeLayout {
	f := m.state.function
	height := functionBodyHeight()
	if contentWidth >= invokeWideWidth {
		w := min(48, contentWidth*2/5)
		return invokeLayout{wide: true, showEvents: true, eventsW: w, eventsH: height - tableHeaderRows, paneW: contentWidth - 2 - w - 2, paneH: height}
	}
	rows := max(1, min(len(f.events.rows), (height-1)/3))
	l := invokeLayout{showEvents: true, eventsW: contentWidth - 2, eventsH: rows, paneW: contentWidth - 2, paneH: height - rows - tableHeaderRows - 1}
	if f.pane.focused {
		l.showEvents, l.paneH = false, height
	}
	return l
}

// paneBodyHeight is the lines the pane's body shows.
func (l invokeLayout) paneBodyHeight() int { return max(1, l.paneH-paneChrome) }

// fitEvents sizes the events' table to the layout. Over the pane it shows no more rows than
// there are events, fewer than a table's least, so the pane has the room.
func (m model) fitEvents() model {
	l := m.invokeLayout()
	t := &m.state.function.events
	t.setSize(l.eventsW, l.eventsH)
	if !l.wide {
		t.height = l.eventsH
		t.setCursor(t.cursor)
	}
	return m
}

// readEventFiles lists the events kept for the function, with their size and when they were
// changed, marking the ones that are not json, and keeps the cursor on the one it was on.
//
//nolint:gosec // reads the event files of the project
func (m model) readEventFiles() model {
	f := &m.state.function
	dir := m.b.EventsDir(f.name)
	var entries []os.DirEntry
	if dir != "" {
		entries, _ = os.ReadDir(dir)
	}
	var files []string
	var infos []eventFile
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		var ev eventFile
		if info, err := e.Info(); err == nil {
			ev.size, ev.modified = info.Size(), info.ModTime()
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			ev.err = fmt.Errorf("reading the event %s: %w", e.Name(), err)
		} else {
			ev.data, ev.size, ev.err = data, int64(len(data)), checkEvent(e.Name(), data)
			ev.view = newJSONViewBytes(data)
		}
		files, infos = append(files, e.Name()), append(infos, ev)
	}
	current := m.selectedEvent()
	f.eventFiles, f.eventInfo = files, infos
	rows := make([]tableRow, 0, len(files))
	for i, name := range files {
		ev := infos[i]
		c := cell{styles.S(strings.TrimSuffix(name, ".json"), styles.ToneText)}
		if errors.Is(ev.err, errNotJSON) {
			c = append(c, styles.S("  not json", styles.ToneWarning))
		}
		modified := text(formatAgo(ev.modified), styles.ToneBody)
		if ev.modified.IsZero() {
			modified = text("-", styles.ToneFaint)
		}
		rows = append(rows, tableRow{c, text(cli.Size(ev.size), styles.ToneBody), modified})
	}
	switch {
	case len(files) == 0 && dir == "":
		rows = []tableRow{{text(defaultEvent, styles.ToneText), text("-", styles.ToneFaint), text("-", styles.ToneFaint)}}
	case len(files) == 0:
		rows = []tableRow{{cell{styles.S(defaultEvent, styles.ToneText), styles.S("  e creates "+defaultEventFile, styles.ToneFaint)}, text("-", styles.ToneFaint), text("-", styles.ToneFaint)}}
	}
	f.events.setRows(rows)
	for i, name := range files {
		if name == current {
			f.events.setCursor(i)
		}
	}
	return m.fitEvents().syncPane(true)
}

// selectedEvent is the file of the event under the cursor, or "" for the default event.
func (m model) selectedEvent() string {
	f := m.state.function
	if f.events.cursor < len(f.eventFiles) {
		return f.eventFiles[f.events.cursor]
	}
	return ""
}

// selectedEventFile is the event under the cursor as it was listed, false for the default event.
func (m model) selectedEventFile() (eventFile, bool) {
	f := m.state.function
	if i := f.events.cursor; i < len(f.eventFiles) && i < len(f.eventInfo) {
		return f.eventInfo[i], true
	}
	return eventFile{}, false
}

// eventPayload reads the selected event, and checks it is json, as lambda takes nothing else.
// The error of one that is not says the line it breaks on.
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
	if err := checkEvent(name, data); err != nil {
		return nil, err
	}
	return data, nil
}

// checkEvent checks an event is json. The error of one that is not says the line it breaks on.
func checkEvent(name string, data []byte) error {
	if json.Valid(data) {
		return nil
	}
	var v any
	err := json.Unmarshal(data, &v)
	if syntax, ok := errors.AsType[*json.SyntaxError](err); ok {
		line := 1 + bytes.Count(data[:min(int(syntax.Offset), len(data))], []byte("\n"))
		return fmt.Errorf("%s is %w, on line %d: %s", name, errNotJSON, line, syntax.Error())
	}
	return fmt.Errorf("%s is %w: %w", name, errNotJSON, err)
}

// eventErr is why the selected event cannot be sent, as it was when the events were listed, so a
// frame does not read the file again.
func (m model) eventErr() error {
	ev, _ := m.selectedEventFile()
	return ev.err
}

// editor is the command of $VISUAL, or $EDITOR, or vi, split into its words.
func editor() []string {
	return strings.Fields(cmpOr(strings.TrimSpace(os.Getenv("VISUAL")), strings.TrimSpace(os.Getenv("EDITOR")), "vi"))
}

// editEvent opens the selected event in $VISUAL or $EDITOR, creating event.json with {} when the
// function has no events yet. Without a project here there is nowhere to keep one, and an editor
// that cannot be found is said before anything is created.
//
//nolint:gosec // runs the editor the user set, on an event of the project
func (m model) editEvent() (model, tea.Cmd) {
	dir := m.b.EventsDir(m.state.function.name)
	if dir == "" {
		return m.setStatus("events are kept in a project: run "+binary+" ui in one to edit them", styles.ToneWarning)
	}
	args := editor()
	if _, err := exec.LookPath(args[0]); err != nil {
		m.error = "could not find the editor " + args[0] + ": set $EDITOR, or $VISUAL, to the command of the one to use"
		return m, nil
	}
	name := m.selectedEvent()
	if name == "" {
		name = defaultEventFile
		if err := os.MkdirAll(dir, 0o750); err != nil {
			m.error = "could not create " + dir + ": " + err.Error()
			return m, nil
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(defaultEvent+"\n"), 0o600); err != nil {
			m.error = "could not create the event: " + err.Error()
			return m, nil
		}
	}
	cmd := exec.Command(args[0], append(args[1:], filepath.Join(dir, name))...)
	return m, tea.ExecProcess(cmd, func(err error) tea.Msg { return eventEditedMsg{err: err} })
}

// applyEventEdited lists the events again once the editor closes, and warns when the one edited
// is no longer json.
func (m model) applyEventEdited(msg eventEditedMsg) (model, tea.Cmd) {
	m = m.readEventFiles()
	m.state.function.scroll = 0
	if msg.err != nil {
		m.error = "the editor exited with an error: " + msg.err.Error()
		return m, nil
	}
	if _, err := m.eventPayload(); err != nil {
		return m.setStatus(m.selectedEvent()+" is not json: e edits it", styles.ToneWarning)
	}
	return m, nil
}

// startInvoke invokes the function with the selected event, in aws or on this machine, and shows
// the result. Against prod or production, running here asks first. An event that is not json is not sent.
func (m model) startInvoke(local bool) (model, tea.Cmd) {
	f := &m.state.function
	ref := m.state.stage.ref
	switch {
	case f.invoking:
		return m.setStatus("an invocation is running already", styles.ToneWarning)
	case local && !m.b.Local(ref):
		return m.setStatus("only the project here runs on this machine: run "+binary+" ui in its directory", styles.ToneWarning)
	case !local && f.lambdaName == "":
		return m.setStatus(f.name+" is not deployed yet", styles.ToneWarning)
	}
	from := f.tab
	f.tab = invokeTab
	payload, err := m.eventPayload()
	switch {
	case errors.Is(err, errNotJSON):
		return m.setStatus(m.selectedEvent()+" is not json: e edits it", styles.ToneWarning)
	case err != nil:
		m.error = err.Error()
		return m, nil
	}
	if local && cli.Production(ref.Stage) {
		m.state.runLocal = runLocalState{event: m.selectedEvent(), payload: payload, tab: from}
		return m.switchPage(runLocalPage), nil
	}
	m = m.showPane(true)
	return m.invoke(m.selectedEvent(), payload, local)
}

func (m model) applyInvoked(msg invokedMsg) (model, tea.Cmd) {
	f := &m.state.function
	if msg.gen != f.invokeGen {
		return m, nil
	}
	f.invoking = false
	r := msg.result
	r.at = time.Now()
	prev := f.results[msg.event].view
	if f.pane.result && f.pane.event == msg.event {
		prev = f.pane.view
	}
	r.view = r.view.carry(prev)
	f.results[msg.event] = r
	f.scroll = 0
	m = m.syncPane(true)
	switch {
	case r.err != nil:
		return m.setStatus("could not invoke "+f.name, styles.ToneDanger)
	case r.timedOut():
		return m.setStatus(timedOutStatus(f.name, r), styles.ToneDanger)
	case r.inv.Error != "":
		return m.setStatus(f.name+" failed: "+functionError(r), styles.ToneDanger)
	}
	return m.setStatus(f.name+" returned in "+cli.Duration(r.took()), styles.ToneSuccess)
}

// timedOutStatus says a function timed out and after how long, short enough for the footer to
// keep its keys; the pane says what lambda said of it.
func timedOutStatus(name string, r invocationResult) string {
	if took := r.took(); took > 0 {
		return name + " timed out after " + cli.Duration(took)
	}
	return name + " timed out"
}

// invokePane is what the right of the invoke tab shows, or below the events on a narrow terminal:
// the selected event's payload, or what the last invocation with it returned, as json. While
// focused, its json has the keys.
type invokePane struct {
	event   string
	result  bool
	focused bool
	// view is of the result when built is set, otherwise of the payload, of event, once synced.
	view   jsonView
	built  bool
	synced bool
}

// showPane shows the result, or the payload, of the selected event.
func (m model) showPane(result bool) model {
	f := &m.state.function
	if f.pane.result != result {
		f.pane.result, f.scroll = result, 0
	}
	if result && f.results[m.selectedEvent()].running {
		f.pane.focused = false
	}
	return m.syncPane(false)
}

// syncPane points the pane at the selected event. The view it leaves is kept with its result or
// its event, so its folds and cursor are there when it comes back. Again set, it reads the view
// anew, the payload of an event read again keeping what was done to it.
func (m model) syncPane(again bool) model {
	f := &m.state.function
	p := &f.pane
	event := m.selectedEvent()
	same := p.synced && p.event == event && p.built == p.result
	if same && !again {
		p.view.blurred = !p.focused
		return m
	}
	if p.synced && !same {
		m = m.keepView(again)
	}
	old := p.view
	if p.result {
		p.view = f.results[event].view
	} else {
		p.view = m.payloadView()
		if same {
			p.view = p.view.carry(old)
		}
	}
	p.event, p.built, p.synced = event, p.result, true
	if p.view.empty() {
		p.focused = false
	}
	p.view.blurred = !p.focused
	return m
}

// keepView keeps the pane's view with what it was of: the result of its event, or the event,
// unless the events were read again and the one it was of is not what it was.
func (m model) keepView(again bool) model {
	f := &m.state.function
	p := f.pane
	if p.built {
		if r, ok := f.results[p.event]; ok {
			r.view = p.view
			f.results[p.event] = r
		}
		return m
	}
	if i := slices.Index(f.eventFiles, p.event); i >= 0 && i < len(f.eventInfo) && !again {
		f.eventInfo = slices.Clone(f.eventInfo)
		f.eventInfo[i].view = p.view
	}
	return m
}

// payloadView is the selected event as json, or its lines when it is not json, as it was read
// when the events were listed.
func (m model) payloadView() jsonView {
	if ev, ok := m.selectedEventFile(); ok {
		return ev.view
	}
	return newJSONViewBytes([]byte(defaultEvent))
}

// paneKey gives a key to the json of the pane while it has the keys, before the function's own:
// esc or q, once its search is clear, gives them back to the events. It reports whether the key
// was taken.
func (m model) paneKey(msg tea.KeyPressMsg) (model, tea.Cmd, bool) {
	f := &m.state.function
	l := m.invokeLayout()
	v := &f.pane.view
	before := v.at()
	if res, ok := v.update(msg, m.keys, l.paneW, l.paneBodyHeight()); ok {
		m = m.followPane(msg, before)
		m, cmd := m.jsonDone(res)
		return m, cmd, true
	}
	if key.Matches(msg, m.keys.Back, m.keys.Quit) {
		f.pane.focused, v.blurred = false, true
		return m.fitEvents(), nil, true
	}
	return m, nil, false
}

// invokeUpdate handles a key on the invoke tab that the pane's json did not take: ↑ and ↓ choose
// the event, enter goes into the json shown, v switches between the payload and the result,
// pgup and pgdn scroll the pane, and shift+c copies the request id of the result.
func (m model) invokeUpdate(msg tea.KeyPressMsg) (model, tea.Cmd) {
	f := &m.state.function
	l := m.invokeLayout()
	switch {
	case key.Matches(msg, m.keys.Pane):
		return m.showPane(!f.pane.result), nil
	case key.Matches(msg, m.keys.View) && !f.pane.focused:
		if f.pane.view.empty() || f.pane.result && f.results[m.selectedEvent()].running {
			return m, nil
		}
		f.pane.focused, f.pane.view.blurred = true, false
		return m.fitEvents().followPane(tea.KeyPressMsg{}, -1), nil
	case key.Matches(msg, m.keys.CopyPath) && f.pane.result:
		return m.copy(f.results[m.selectedEvent()].requestID())
	case key.Matches(msg, m.keys.PageUp, m.keys.PageDown):
		height := l.paneBodyHeight()
		body, _ := m.paneBody(l.paneW)
		f.scroll = scrollBy(msg, m.keys, f.scroll, height, maxResultScroll(body.len(), height))
	case !f.pane.focused && f.events.move(msg, m.keys):
		f.scroll = 0
		return m.syncPane(false), nil
	}
	return m, nil
}

// followPane scrolls the pane after msg moved the cursor of its json from row before: as little
// as keeps the cursor in sight, but for up on the json's first row, which scrolls up to what is
// above it, and g, which goes to the top.
func (m model) followPane(msg tea.KeyPressMsg, before int) model {
	f := &m.state.function
	l := m.invokeLayout()
	height := l.paneBodyHeight()
	lines, at := m.paneBody(l.paneW)
	most := maxResultScroll(lines.len(), height)
	v := &f.pane.view
	switch {
	case at < 0:
	case key.Matches(msg, m.keys.Top):
		f.scroll = 0
	case before == 0 && v.at() == 0 && key.Matches(msg, m.keys.Up, m.keys.PageUp):
		f.scroll = scrollBy(msg, m.keys, f.scroll, height, most)
	default:
		top, bottom := v.cursorLines(l.paneW)
		f.scroll = keepInSight(f.scroll, at+top, at+bottom, height)
	}
	f.scroll = max(0, min(f.scroll, most))
	return m
}

// paneBody is what the pane shows under its tabs, and the line its json starts on, -1 without
// one in it: the payload of the selected event, or the invocation running, what the last one
// returned, or how to invoke it.
func (m model) paneBody(width int) (lazyLines, int) {
	f := m.state.function
	if !f.pane.result {
		return m.payloadBody(width)
	}
	name := styles.Clean(f.name)
	r, ok := f.results[m.selectedEvent()]
	switch {
	case ok && r.running:
		what := "invoking " + name + " in aws…"
		if r.local {
			what = "running " + name + " on this machine…"
		}
		return lazyOf([]string{styles.Accent(m.spinner.View()) + " " + styles.Muted(what)}), -1
	case !ok:
		if err := m.eventErr(); err != nil {
			lines := warningLines(err.Error(), width)
			return lazyOf(append(lines, "", styles.Faint("e edits it, and i sends it once it is json"))), -1
		}
		return lazyOf(m.invokeHints(name)), -1
	case r.err != nil:
		lines := cellLines(cell{styles.S("✗ could not invoke", styles.ToneDanger), sep(), styles.S(r.where(), styles.ToneMuted), sep(), styles.S(formatAgo(r.at), styles.ToneFaint)}, width)
		lines = append(lines, "", styles.SectionHeader("error", width))
		lines = append(lines, insetLines(cellLines(text(r.err.Error(), styles.ToneDanger), width-rowInset))...)
		return lazyOf(append(lines, "", styles.Faint("i tries again"))), -1
	}
	return resultLines(r, f.pane.view, width)
}

// warningLines are a warning wrapped to width after a ▲, its lines under its first word.
func warningLines(s string, width int) []string {
	lines := cellLines(text(s, styles.ToneWarning), max(1, width-2))
	for i, line := range lines {
		mark := "  "
		if i == 0 {
			mark = styles.Fg(styles.ToneWarning).Render("▲ ")
		}
		lines[i] = mark + line
	}
	return lines
}

// where is where the invocation ran: in aws, or on this machine.
func (r invocationResult) where() string {
	if r.local {
		return "on this machine"
	}
	return "in aws"
}

// payloadBody is the selected event: why it cannot be sent, when it cannot, and what it holds,
// as json or as the lines it is.
func (m model) payloadBody(width int) (lazyLines, int) {
	var lines lazyLines
	if err := m.eventErr(); err != nil {
		lines.add(warningLines(err.Error(), width)...)
		lines.add("")
	}
	v := m.state.function.pane.view
	if v.empty() {
		lines.add(row("", text("empty", styles.ToneFaint), width))
		return lines, -1
	}
	at := lines.len()
	lines.join(v.lines(width))
	return lines, at
}

// invokeHints say how the selected event can be sent: i where the function is deployed, shift+i
// where it is the project here.
func (m model) invokeHints(name string) []string {
	f := m.state.function
	local := m.b.Local(m.state.stage.ref)
	var lines []string
	switch {
	case f.lambdaName != "":
		lines = append(lines, styles.Muted("i invokes "+name+" in aws with this event"))
		if local {
			lines = append(lines, styles.Faint("shift+i runs it on this machine"))
		}
	case local:
		lines = append(lines, styles.Muted(name+" is not deployed yet: shift+i runs it on this machine"))
	default:
		lines = append(lines, styles.Muted(name+" is not deployed yet"))
	}
	if m.b.EventsDir(f.name) == "" {
		return lines
	}
	return append(lines, styles.Faint("e edits the event"))
}

// paneCopyValue is what c copies on the invoke tab from the events: the payload shown, as its
// file holds it, or the result, the error a function failed with as the cli writes it.
func (m model) paneCopyValue() string {
	f := m.state.function
	if !f.pane.result {
		if ev, ok := m.selectedEventFile(); ok {
			return string(ev.data)
		}
		return defaultEvent
	}
	r, ok := f.results[m.selectedEvent()]
	switch {
	case !ok || r.running || r.err != nil:
		return ""
	case r.failed:
		return r.failure.String()
	}
	return string(r.inv.Payload)
}

// invokeKeys are the footer's keys on the invoke tab: those of the json while it has them, with
// esc back to the events last, where q is on every other page, and otherwise i first, as what
// the tab is for, then enter into the json, v, e, scrolling and copying. The footer drops the
// third from last first, so copying a path goes before v and i.
func (m model) invokeKeys() [][2]string {
	k := m.keys
	f := m.state.function
	if f.pane.focused {
		keys := m.jsonHints(f.pane.view)
		last := len(keys) - 1
		h := append(slices.Clone(keys[:last]), hint(k.Pane), hint(k.Invoke), keys[last])
		return append(h, hint(k.Help), hint(k.Back, "events"))
	}
	r, ok := f.results[m.selectedEvent()]
	h := [][2]string{hint(k.Invoke)}
	if m.paneScrolls() {
		h = append(h, [2]string{k.PageUp.Help().Key + "/" + k.PageDown.Help().Key, "scroll"})
	}
	if !f.pane.view.empty() && (!f.pane.result || !r.running) {
		h = append(h, hint(k.View, "open json"))
	}
	h = append(h, hint(k.Pane))
	if m.b.EventsDir(f.name) != "" {
		h = append(h, hint(k.EditEvent))
	}
	h = append(h, hint(k.NextTab))
	if m.b.Local(m.state.stage.ref) {
		h = append(h, hint(k.RunLocal), hint(k.Deploy, "plan its code"))
	}
	h = append(h, hint(k.Copy))
	if ok && f.pane.result && !r.running && r.requestID() != "" {
		h = append(h, hint(k.CopyPath, "copy request id"))
	}
	return append(h, m.pauseHint(), hint(k.Help), hint(k.Quit))
}

// maxResultScroll is how far n lines of a result scroll in height.
func maxResultScroll(n, height int) int {
	return max(0, n-height)
}

// paneScrolls reports whether the pane's body is longer than it shows.
func (m model) paneScrolls() bool {
	l := m.invokeLayout()
	body, _ := m.paneBody(l.paneW)
	return body.len() > l.paneBodyHeight()
}

// paneTabs are the tabs over the pane: the payload with its size, and the result, after the
// event's name where the events are not in sight.
func (m model) paneTabs() string {
	f := m.state.function
	size := cli.Size(int64(len(defaultEvent)))
	if ev, ok := m.selectedEventFile(); ok {
		size = cli.Size(ev.size)
	}
	open := 0
	if f.pane.result {
		open = 1
	}
	tabs := tabBar([]string{"payload", "result"}, []string{size, ""}, open)
	if m.invokeLayout().showEvents {
		return tabs
	}
	name := cmpOr(strings.TrimSuffix(m.selectedEvent(), ".json"), defaultEvent)
	return styles.Render(styles.S(name, styles.ToneText), sep()) + tabs
}

// paneView draws the pane at width and height: its tabs, a rule, and its body scrolled.
func (m model) paneView(width, height int) string {
	f := m.state.function
	body, _ := m.paneBody(width)
	bodyHeight := max(1, height-paneChrome)
	rule := lipgloss.NewStyle().Foreground(styles.P.Rule).Render(strings.Repeat("─", max(0, width)))
	return fill(m.paneTabs()+"\n"+rule+"\n"+scrolledLines(body, f.scroll, bodyHeight), width, height)
}

func (m model) invokeView() string {
	f := m.state.function
	l := m.invokeLayout()
	height := functionBodyHeight()
	pane := m.paneView(l.paneW, l.paneH)
	var body string
	switch {
	case !l.showEvents:
		body = pane
	case l.wide:
		body = lipgloss.JoinHorizontal(lipgloss.Top, fill(f.events.View(), l.eventsW, height), "  ", pane)
	default:
		body = fill(f.events.View(), l.eventsW, l.eventsH+tableHeaderRows) + "\n\n" + pane
	}
	return lipgloss.NewStyle().PaddingLeft(1).Render(body)
}

// functionError is what a function failed with: the type and message of the error it returned,
// or the kind of error lambda says when its payload has neither.
func functionError(r invocationResult) string {
	if r.failed {
		return r.failure.Head()
	}
	return r.inv.Error
}

// resultFacts are what lambda measured of an invocation, a cell each, and the length of its log
// and its request id: how long it took, what was billed, the memory it used of what it has, its
// cold start, its log's lines and the id.
func resultFacts(r invocationResult) []cell {
	var facts []cell
	fact := func(label string, value ...styles.Span) {
		facts = append(facts, append(cell{styles.S(label+" ", styles.ToneFaint)}, value...))
	}
	if took := r.took(); took > 0 {
		fact("duration", styles.S(cli.Duration(took), styles.ToneBody))
	}
	if r.report.billed > 0 {
		fact("billed", styles.S(formatMillis(r.report.billed), styles.ToneMuted))
	}
	if r.report.memory > 0 {
		fact("memory", styles.S(formatInt(r.report.maxMemory), styles.ToneBody), styles.S(" of "+formatInt(r.report.memory)+" mb", styles.ToneFaint))
	}
	if r.report.init > 0 {
		fact("cold start", styles.S(formatMillis(r.report.init), styles.ToneInfo))
	}
	if n := logLineCount(r.inv.Logs); n > 0 {
		unit := " lines"
		if n == 1 {
			unit = " line"
		}
		fact("log", styles.S(formatInt(n), styles.ToneBody), styles.S(unit, styles.ToneFaint))
	}
	if id := r.requestID(); id != "" {
		fact("request", styles.S(id, styles.ToneFaint))
	}
	return facts
}

// packFacts sets facts on as few lines of width as they fit, between dots, breaking a line only
// between two of them, and a fact wider than a line on lines of its own.
func packFacts(facts []cell, width int) []string {
	var lines []string
	var line cell
	used := 0
	for _, f := range facts {
		w := ansi.StringWidth(plainText(f))
		switch {
		case len(line) > 0 && used+3+w <= width:
			line, used = append(append(line, sep()), f...), used+3+w
			continue
		case len(line) > 0:
			lines = append(lines, cellLines(line, width)...)
		}
		line, used = append(cell{}, f...), w
	}
	if len(line) > 0 {
		lines = append(lines, cellLines(line, width)...)
	}
	return lines
}

// logLineCount is the lines of a log, without the newlines it ends in.
func logLineCount(logs string) int {
	logs = strings.TrimRight(logs, "\n")
	if strings.TrimSpace(logs) == "" {
		return 0
	}
	return strings.Count(logs, "\n") + 1
}

// resultHead are the lines over an invocation's result: how it ended, where it ran and when,
// and what lambda measured of it, with its request id.
func resultHead(r invocationResult, width int) []string {
	status := styles.S("✓ ok", styles.ToneSuccess)
	switch {
	case r.timedOut():
		status = styles.S("⏱ timed out", styles.ToneDanger)
	case r.inv.Error != "":
		status = styles.S("✗ failed", styles.ToneDanger)
	}
	lines := cellLines(cell{status, sep(), styles.S(r.where(), styles.ToneMuted), sep(), styles.S(formatAgo(r.at), styles.ToneFaint)}, width)
	return append(lines, packFacts(resultFacts(r), width)...)
}

// hanging wraps s to width in tone, its first line indented by first and the rest by more, as
// a frame of a stack trace too long for a line reads under its start.
func hanging(s string, tone styles.Tone, first, more, width int) []string {
	lines := cellLines(text(s, tone), max(2, width-more))
	for i, line := range lines {
		pad := more
		if i == 0 {
			pad = first
		}
		lines[i] = strings.Repeat(" ", pad) + line
	}
	return lines
}

// failureLines are the error a function failed with: its type and message, and its stack trace,
// the frames of the runtime and of dependencies faint, so the function's own stand out.
func failureLines(r invocationResult, width int) []string {
	if !r.failed {
		return cellLines(text(r.inv.Error, styles.ToneDanger), width)
	}
	return traceLines(r.failure, width)
}

// traceLines are a failure's type and message, and its stack trace under them, the frames of the
// runtime and of dependencies faint, as the invoke tab and the page of a line of the logs draw
// one.
func traceLines(f cli.Failure, width int) []string {
	var lines []string
	for line := range strings.SplitSeq(styles.CleanBlock(f.Head()), "\n") {
		lines = append(lines, cellLines(text(line, styles.ToneDanger), width)...)
	}
	for _, line := range f.Trace {
		tone := styles.ToneBody
		if line.Internal {
			tone = styles.ToneFaint
		}
		lines = append(lines, hanging(line.Text, tone, 2, 4, width)...)
	}
	return lines
}

// resultLines show how an invocation ended, the error it failed with or what it returned in its
// json view v, and its log, and the line the json starts on, -1 without it. Under their headers
// the error, the json and the log start rowInset in, as rows do, and a note in place of the json
// where the values of a row do. Only the lines in sight are drawn, so a payload of megabytes
// draws as quickly as a short one.
func resultLines(r invocationResult, v jsonView, width int) (lazyLines, int) {
	var lines lazyLines
	lines.add(resultHead(r, width)...)
	at := -1
	if r.inv.Error != "" {
		lines.add("", styles.SectionHeader("error", width))
		lines.add(insetLines(failureLines(r, width-rowInset))...)
	}
	if !r.failed {
		lines.add("", styles.SectionHeader("returned", width))
		if v.empty() {
			lines.add(row("", text("nothing", styles.ToneFaint), width))
		} else {
			at = lines.len()
			lines.join(v.lines(width))
		}
	}
	if strings.TrimSpace(r.inv.Logs) != "" {
		room := width - rowInset
		cells := r.logCells(room)
		lines.add("", styles.SectionHeader("log", width))
		lines.addDrawn(len(cells), func(i int) string { return pad(rowInset, nil) + renderCell(cells[i], column{width: room}, nil) })
	}
	return lines, at
}
