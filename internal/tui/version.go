package tui

import (
	"bytes"
	"encoding/json"
	"maps"
	"slices"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/kontrolplane/koldstart/internal/engine"
	"github.com/kontrolplane/koldstart/internal/state"
	"github.com/kontrolplane/koldstart/internal/tui/styles"
)

// versionState is one earlier version of the stage's state, and what it changed from the one
// before it.
type versionState struct {
	gen     int
	version state.ObjectVersion
	writes  int
	loaded  bool
	current *state.State
	before  *state.State
	scroll  int
	cursor  int
}

// openVersion reads the deploy at i of the stage's history, newest first: the last version it
// wrote, and the last one before it, to tell what it changed.
func (m model) openVersion(i int) (model, tea.Cmd) {
	d := deploys(m.state.stage.history)
	m.state.version = versionState{version: d[i][0], writes: len(d[i])}
	previous := ""
	if i+1 < len(d) {
		previous = d[i+1][0].ID
	}
	m = m.switchPage(versionPage)
	m.loading, m.loadingMsg = true, "reading the deploy…"
	return m.loadVersion(d[i][0].ID, previous)
}

func (m model) applyVersion(msg versionLoadedMsg) (model, tea.Cmd) {
	v := &m.state.version
	if msg.gen != v.gen {
		return m, nil
	}
	if m.page == versionPage {
		m.loading = false
	}
	if msg.err != nil {
		m.error = "could not read the version: " + msg.err.Error()
		return m.switchPage(stagePage), nil
	}
	v.current, v.before, v.loaded = msg.current, msg.previous, true
	return m, nil
}

func (m model) versionUpdate(msg tea.KeyPressMsg) (model, tea.Cmd) {
	v := &m.state.version
	switch {
	case key.Matches(msg, m.keys.Back, m.keys.Quit):
		return m.switchPage(stagePage), nil
	case key.Matches(msg, m.keys.Copy):
		return m.copy(v.version.ID)
	}
	changes := stateChanges(v.before, v.current)
	if key.Matches(msg, m.keys.View) && v.cursor < len(changes) {
		c := changes[v.cursor]
		return m.openDiff(c.id, "changed in this deploy", inputsOf(v.before, c.id), inputsOf(v.current, c.id))
	}
	lines, rows := m.versionLines()
	if cursor, ok := moveCursor(msg, m.keys, v.cursor, len(rows), contentHeight); ok {
		v.cursor = cursor
		_, v.scroll = selectable(lines, rows, v.cursor, v.scroll, contentHeight)
	}
	return m, nil
}

// inputsOf are the inputs a resource had in a version of the state, or nil when it had none.
func inputsOf(st *state.State, id string) any {
	if st == nil || st.Resources[id] == nil {
		return nil
	}
	var v any
	_ = json.Unmarshal(st.Resources[id].Inputs, &v)
	return v
}

// stateChange is a resource that changed between two versions of a state, and the line that says
// how.
type stateChange struct {
	id   string
	line cell
}

// stateChanges lists what changed between two versions of a state, a line for each resource:
// added, removed, or the inputs that changed.
func stateChanges(before, after *state.State) []stateChange {
	if before == nil {
		before = &state.State{}
	}
	ids := map[string]bool{}
	for id := range before.Resources {
		ids[id] = true
	}
	for id := range after.Resources {
		ids[id] = true
	}
	if after == nil {
		return nil
	}
	var lines []stateChange
	line := func(symbol string, tone styles.Tone, id, detail string) {
		c := cell{styles.S(symbol+" ", tone), styles.S(id, styles.ToneText)}
		if detail != "" {
			c = append(c, styles.S("  "+detail, styles.ToneMuted))
		}
		lines = append(lines, stateChange{id: id, line: c})
	}
	for _, id := range slices.Sorted(maps.Keys(ids)) {
		b, a := before.Resources[id], after.Resources[id]
		switch {
		case b == nil:
			line("+", styles.ToneSuccess, id, a.PhysicalID)
		case a == nil:
			line("-", styles.ToneDanger, id, b.PhysicalID)
		case !bytes.Equal(b.Inputs, a.Inputs) || b.SecretsHash != a.SecretsHash:
			var bi, ai map[string]any
			_ = json.Unmarshal(b.Inputs, &bi)
			_ = json.Unmarshal(a.Inputs, &ai)
			changed := engine.ChangedKeys(bi, ai)
			if b.SecretsHash != a.SecretsHash {
				changed = append(changed, "a secret")
			}
			line("~", styles.ToneWarning, id, strings.Join(changed, ", "))
		}
	}
	return lines
}

// versionLines are the lines of the version, with the index of the line of each change.
func (m model) versionLines() ([]string, []int) {
	v := m.state.version
	width := contentWidth - 6
	if !v.loaded || v.current == nil {
		return nil, nil
	}
	cur := v.current
	lines := []string{
		styles.SectionHeader("deploy of "+formatTime(v.version.LastModified), width),
		row("saved", text(formatTime(v.version.LastModified)+", "+formatAgo(v.version.LastModified), styles.ToneBody), width),
		row("version", text(v.version.ID, styles.ToneFaint), width),
		row("deployed by", text(orDash(cur.DeployedBy), styles.ToneBody), width),
		row("serial", text(formatInt(int(cur.Serial)), styles.ToneBody), width),
		row("resources", text(formatInt(len(cur.Resources)), styles.ToneBody), width),
		row("writes", text(plural(v.writes, "write")+" of the state, the last shown", styles.ToneMuted), width),
		"",
	}
	title := "changed from the deploy before"
	if v.before == nil {
		title = "the first deploy"
	}
	lines = append(lines, styles.SectionHeader(title, width))
	changes := stateChanges(v.before, cur)
	var rows []int
	for _, c := range changes {
		rows = append(rows, len(lines))
		lines = append(lines, renderCell(c.line, column{width: width}, nil))
	}
	if len(changes) == 0 {
		lines = append(lines, styles.Faint("no resource changed: only the outputs or the record of the deploy"))
	}
	return lines, rows
}

func (m model) versionView() string {
	lines, rows := m.versionLines()
	view, _ := selectable(lines, rows, m.state.version.cursor, m.state.version.scroll, contentHeight)
	return view
}
