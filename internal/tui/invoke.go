package tui

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/kontrolplane/koldstart/internal/cli"
	"github.com/kontrolplane/koldstart/internal/tui/styles"
)

// defaultEvent is the event a function is invoked with when its events directory has none.
const defaultEvent = "{}"

// invocationResult is the last invocation with an event.
type invocationResult struct {
	inv   cli.Invocation
	local bool
	at    time.Time
	err   error
}

var eventColumns = []column{{title: "event", width: 12, grow: 1}}

func eventsWidth() int { return min(36, contentWidth/3) }

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

// resultLines show what an invocation returned, and its log.
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
		for _, c := range logCells(r.inv.Logs, width-2) {
			lines = append(lines, renderCell(c, column{width: width - 2}, nil))
		}
	}
	return lines
}
