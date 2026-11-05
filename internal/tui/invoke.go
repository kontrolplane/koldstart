package tui

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
	// body is the payload's lines, indented and cleaned, and wrapped what the last frame drew of
	// it and of the log at its width, so the next does not do either again.
	body    []string
	wrapped *resultRows
}

// resultRows are what an invocation returned and its log, wrapped to width in the palette
// numbered palette. They hold no colour, but are wrapped again in a new palette all the same, so
// nothing kept outlives one.
type resultRows struct {
	width   int
	palette int
	payload []string
	logs    []cell
}

// payloadLines are the lines of what an invocation returned, indented when it is json, and
// cleaned.
func payloadLines(payload []byte) []string {
	if len(bytes.TrimSpace(payload)) == 0 {
		return nil
	}
	return strings.Split(styles.CleanBlock(strings.TrimRight(prettyJSON(payload), "\n")), "\n")
}

// rows wraps the payload and the log to width, or takes them as the last frame wrapped them.
func (r invocationResult) rows(width int) resultRows {
	if w := r.wrapped; w != nil && w.width == width && w.palette == styles.Gen() && width > 0 {
		return *w
	}
	body := r.body
	if body == nil {
		body = payloadLines(r.inv.Payload)
	}
	out := resultRows{width: width, palette: styles.Gen(), payload: hardwrapLines(body, width), logs: logCells(r.inv.Logs, width)}
	if r.wrapped != nil {
		*r.wrapped = out
	}
	return out
}

// hardwrapLines breaks each line wider than width where the width runs out, as ansi.Hardwrap
// does, cutting a line of ascii without measuring it.
func hardwrapLines(lines []string, width int) []string {
	width = max(1, width)
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		switch {
		case len(line) <= width:
			out = append(out, line)
		case printableASCII(line):
			for len(line) > width {
				out = append(out, line[:width])
				line = line[width:]
			}
			out = append(out, line)
		default:
			out = append(out, strings.Split(ansi.Hardwrap(line, width, true), "\n")...)
		}
	}
	return out
}

// errNotJSON is an event file that does not hold json.
var errNotJSON = errors.New("not json")

var eventColumns = []column{{title: "event", width: 12, grow: 1}}

func eventsWidth() int { return min(36, contentWidth/3) }

// resultWidth is the width of what an invocation returned, right of the events.
func resultWidth() int { return contentWidth - 4 - eventsWidth() - 2 }

// readEventFiles lists the events kept for the function, marking the ones that are not json, and
// keeps the cursor on the one it was on.
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
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
			files = append(files, e.Name())
		}
	}
	current := m.selectedEvent()
	f.eventFiles, f.eventErrs = files, make([]error, len(files))
	rows := make([]tableRow, 0, len(files))
	for i, name := range files {
		c := cell{styles.S(strings.TrimSuffix(name, ".json"), styles.ToneText)}
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			err = fmt.Errorf("reading the event %s: %w", name, err)
		} else {
			err = checkEvent(name, data)
		}
		if errors.Is(err, errNotJSON) {
			c = append(c, styles.S("  not json", styles.ToneWarning))
		}
		f.eventErrs[i] = err
		rows = append(rows, tableRow{c})
	}
	switch {
	case len(files) == 0 && dir == "":
		rows = []tableRow{{text(defaultEvent, styles.ToneText)}}
	case len(files) == 0:
		rows = []tableRow{{cell{styles.S(defaultEvent, styles.ToneText), styles.S("  e creates "+defaultEventFile, styles.ToneFaint)}}}
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
	f := m.state.function
	if i := f.events.cursor; i < len(f.eventFiles) && i < len(f.eventErrs) {
		return f.eventErrs[i]
	}
	return nil
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

// startInvoke invokes the function with the selected event, in aws or on this machine. Against
// prod, running here asks first. An event that is not json is not sent.
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
	f.tab = invokeTab
	payload, err := m.eventPayload()
	switch {
	case errors.Is(err, errNotJSON):
		return m.setStatus(m.selectedEvent()+" is not json: e edits it", styles.ToneWarning)
	case err != nil:
		m.error = err.Error()
		return m, nil
	}
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
	f.results[msg.event] = invocationResult{inv: msg.inv, local: msg.local, at: time.Now(), err: msg.err, body: msg.body, wrapped: &resultRows{}}
	f.scroll = 0
	switch {
	case msg.err != nil:
		return m.setStatus("could not invoke "+f.name, styles.ToneDanger)
	case msg.inv.Error != "":
		return m.setStatus(f.name+" failed: "+functionError(msg.inv), styles.ToneDanger)
	}
	return m.setStatus(f.name+" returned in "+cli.Duration(msg.inv.Duration), styles.ToneSuccess)
}

// invokeUpdate handles a key on the invoke tab: ↑ and ↓ choose the event, pgup and pgdn scroll
// what it returned.
func (m model) invokeUpdate(msg tea.KeyPressMsg) (model, tea.Cmd) {
	f := &m.state.function
	switch {
	case key.Matches(msg, m.keys.PageUp, m.keys.PageDown):
		height := functionBodyHeight()
		f.scroll = scrollBy(msg, m.keys, f.scroll, height, maxResultScroll(m.invokeLines(resultWidth()).len(), height))
	case f.events.move(msg, m.keys):
		f.scroll = 0
	}
	return m, nil
}

// invokeLines are what the right of the invoke tab shows for the selected event: the invocation
// running, what the last one returned, or how to invoke it.
func (m model) invokeLines(width int) lazyLines {
	f := m.state.function
	name := styles.Clean(f.name)
	r, ok := f.results[m.selectedEvent()]
	switch {
	case ok && r.running:
		what := "invoking " + name + " in aws…"
		if r.local {
			what = "running " + name + " on this machine…"
		}
		return lazyOf([]string{styles.Accent(m.spinner.View()) + " " + styles.Muted(what)})
	case !ok:
		if err := m.eventErr(); err != nil {
			lines := wrapped("▲ "+err.Error(), styles.ToneWarning, width-2, 0)
			return lazyOf(append(lines, "", styles.Faint("e edits it, and i sends it once it is json")))
		}
		return lazyOf(m.invokeHints(name))
	case r.err != nil:
		lines := []string{styles.Render(styles.S("✗ could not invoke "+f.name, styles.ToneDanger)), ""}
		lines = append(lines, wrapped(r.err.Error(), styles.ToneDanger, width-2, 0)...)
		return lazyOf(append(lines, "", styles.Faint("i tries again")))
	}
	return resultLines(r, width)
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

// maxResultScroll is how far n lines of a result scroll in height.
func maxResultScroll(n, height int) int {
	return max(0, n-height)
}

func (m model) invokeView() string {
	f := m.state.function
	height := functionBodyHeight()
	left, right := eventsWidth(), resultWidth()
	body := scrolledLines(m.invokeLines(right), f.scroll, height)
	return lipgloss.NewStyle().PaddingLeft(1).Render(lipgloss.JoinHorizontal(lipgloss.Top,
		fill(f.events.View(), left, height),
		"  ",
		fill(body, right, height),
	))
}

// functionError is what a function failed with: the type and message of the error it returned,
// or the kind of error lambda says when its payload has neither.
func functionError(inv cli.Invocation) string {
	var e struct {
		Type    string `json:"errorType"`
		Message string `json:"errorMessage"`
	}
	_ = json.Unmarshal(inv.Payload, &e)
	switch {
	case e.Type != "" && e.Message != "":
		return e.Type + ": " + e.Message
	case e.Message != "":
		return e.Message
	case e.Type != "":
		return e.Type
	}
	return inv.Error
}

// resultLines show what an invocation returned, or the error it failed with, and its log. Only
// the lines in sight are painted, so a payload of megabytes draws as quickly as a short one.
func resultLines(r invocationResult, width int) lazyLines {
	took := ""
	if r.inv.Duration > 0 {
		took = " in " + cli.Duration(r.inv.Duration)
	}
	where := " in aws"
	if r.local {
		where = " on this machine"
	}
	facts := styles.S(took+where+", "+formatAgo(r.at), styles.ToneFaint)
	outcome, section := styles.S("✓ returned", styles.ToneSuccess), "returned"
	if r.inv.Error != "" {
		outcome, section = styles.S("✗ failed", styles.ToneDanger), "error"
	}
	var lines lazyLines
	lines.add(styles.Render(outcome, facts))
	if r.inv.Error != "" {
		for line := range strings.SplitSeq(styles.CleanBlock(strings.TrimRight(functionError(r.inv), "\n")), "\n") {
			lines.add(cellLines(text(line, styles.ToneDanger), width-2)...)
		}
	}
	rows := r.rows(width - 2)
	lines.add("", styles.SectionHeader(section, width-2))
	if len(rows.payload) == 0 {
		lines.add(styles.Faint("nothing"))
	}
	lines.addDrawn(len(rows.payload), func(i int) string { return styles.Body(rows.payload[i]) })
	if strings.TrimSpace(r.inv.Logs) != "" {
		lines.add("", styles.SectionHeader("log", width-2))
		lines.addDrawn(len(rows.logs), func(i int) string { return renderCell(rows.logs[i], column{width: width - 2}, nil) })
	}
	return lines
}
