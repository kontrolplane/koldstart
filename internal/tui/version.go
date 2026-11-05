package tui

import (
	"bytes"
	"encoding/json"
	"maps"
	"slices"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

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

// versionUpdate handles a key on the version page: ↑ and ↓ choose a change and enter opens its
// diff, or they scroll when the deploy changed no resource.
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
		return m.openDiff(c.id, c.verb+" in this deploy", inputsOf(v.before, c.id), inputsOf(v.current, c.id))
	}
	l := m.versionLines()
	if len(l.rows) == 0 {
		v.scroll = scrollBy(msg, m.keys, v.scroll, contentHeight, maxOffset(len(l.lines), contentHeight))
		return m, nil
	}
	if cursor, ok := moveCursor(msg, m.keys, v.cursor, len(l.rows), contentHeight); ok {
		v.cursor = cursor
		_, v.scroll = selectable(l, v.cursor, v.scroll, contentHeight)
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

// stateChange is a resource that changed between two versions of a state, what happened to it,
// and the line that says how.
type stateChange struct {
	id   string
	verb string
	line cell
}

// stateChanges lists what changed between two versions of a state, a line for each resource:
// added, removed, or the inputs that changed.
func stateChanges(before, after *state.State) []stateChange {
	if after == nil {
		return nil
	}
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
	var lines []stateChange
	line := func(symbol, verb string, tone styles.Tone, id, detail string) {
		c := cell{styles.S(symbol+" ", tone), styles.S(id, styles.ToneText)}
		if detail != "" {
			c = append(c, styles.S("  "+detail, styles.ToneMuted))
		}
		lines = append(lines, stateChange{id: id, verb: verb, line: c})
	}
	for _, id := range slices.Sorted(maps.Keys(ids)) {
		b, a := before.Resources[id], after.Resources[id]
		switch {
		case b == nil:
			line("+", "added", styles.ToneSuccess, id, a.PhysicalID)
		case a == nil:
			line("-", "removed", styles.ToneDanger, id, b.PhysicalID)
		case !bytes.Equal(b.Inputs, a.Inputs) || b.SecretsHash != a.SecretsHash:
			var bi, ai map[string]any
			_ = json.Unmarshal(b.Inputs, &bi)
			_ = json.Unmarshal(a.Inputs, &ai)
			changed := engine.ChangedKeys(bi, ai)
			if b.SecretsHash != a.SecretsHash {
				changed = append(changed, "a secret")
			}
			line("~", "changed", styles.ToneWarning, id, strings.Join(changed, ", "))
		}
	}
	return lines
}

// outputChanges are the lines of the outputs that changed between two versions of a state:
// added with their value, removed, or changed from one value to another.
func outputChanges(before, after *state.State) []cell {
	var was map[string]string
	if before != nil {
		was = before.Outputs
	}
	names := map[string]bool{}
	for k := range was {
		names[k] = true
	}
	for k := range after.Outputs {
		names[k] = true
	}
	var out []cell
	for _, k := range slices.Sorted(maps.Keys(names)) {
		b, inBefore := was[k]
		a, inAfter := after.Outputs[k]
		switch {
		case !inBefore:
			out = append(out, cell{styles.S("+ ", styles.ToneSuccess), styles.S(k, styles.ToneText), styles.S("  "+a, styles.ToneMuted)})
		case !inAfter:
			out = append(out, cell{styles.S("- ", styles.ToneDanger), styles.S(k, styles.ToneText), styles.S("  "+b, styles.ToneMuted)})
		case a != b:
			out = append(out, cell{styles.S("~ ", styles.ToneWarning), styles.S(k, styles.ToneText), styles.S("  "+b, styles.ToneMuted), styles.S("  →  ", styles.ToneFaint), styles.S(a, styles.ToneBody)})
		}
	}
	return out
}

// versionLines are the lines of the version, each change one that can be chosen.
func (m model) versionLines() pickList {
	v := m.state.version
	width := contentWidth - 5
	if !v.loaded || v.current == nil {
		return pickList{}
	}
	cur := v.current
	lines := []string{
		styles.SectionHeader("deploy of "+formatTime(v.version.LastModified), width),
		row("saved", text(formatAgo(v.version.LastModified), styles.ToneBody), width),
		row("version", text(v.version.ID, styles.ToneFaint), width),
		row("deployed by", text(orDash(cur.DeployedBy), styles.ToneBody), width),
		row("serial", text(formatInt(int(cur.Serial)), styles.ToneBody), width),
		row("resources", text(formatInt(len(cur.Resources)), styles.ToneBody), width),
		row("writes", text(plural(v.writes, "write")+" of the state, the last shown", styles.ToneMuted), width),
		"",
	}
	changes := stateChanges(v.before, cur)
	outputs := outputChanges(v.before, cur)
	title := "changed from the deploy before"
	if v.before == nil {
		title = "the first deploy"
	}
	if len(changes) > 0 {
		title += " · " + plural(len(changes), "resource")
	}
	lines = append(lines, styles.SectionHeader(title, width))
	l := pickList{lines: lines}
	cols := []column{{width: width}}
	cells := make([]cell, len(changes))
	for i, c := range changes {
		cells[i] = c.line
	}
	for _, c := range alignNames(cells, width/2) {
		l.add(renderCell(c, cols[0], nil), litRow(cols, c))
	}
	lines = l.lines
	if len(changes) == 0 {
		empty := "no resource or output changed: the deploy found everything as configured"
		if len(outputs) > 0 {
			empty = "no resource changed"
		}
		lines = append(lines, styles.Faint(empty))
	}
	if len(outputs) > 0 {
		lines = append(lines, "", styles.SectionHeader("outputs · "+plural(len(outputs), "change"), width))
		for _, c := range alignNames(outputs, width/2) {
			lines = append(lines, renderCell(c, column{width: width}, nil))
		}
	}
	l.lines = lines
	return l
}

// alignNames pads the name of each change, its second span after the mark, to the longest of
// them up to limit, so what follows the names lines up: the ids in aws a deploy created, or what
// changed in each resource.
func alignNames(cells []cell, limit int) []cell {
	longest := 0
	for _, c := range cells {
		if len(c) > 2 {
			longest = max(longest, ansi.StringWidth(c[1].Text))
		}
	}
	longest = min(longest, limit)
	out := make([]cell, len(cells))
	for i, c := range cells {
		out[i] = slices.Clone(c)
		if w := ansi.StringWidth(c[1].Text); len(c) > 2 && w < longest {
			out[i][1].Text += strings.Repeat(" ", longest-w)
		}
	}
	return out
}

func (m model) versionView() string {
	view, _ := selectable(m.versionLines(), m.state.version.cursor, m.state.version.scroll, contentHeight)
	return view
}
