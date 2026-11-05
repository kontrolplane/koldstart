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
	loaded  bool
	current *state.State
	before  *state.State
	scroll  int
}

// openVersion reads the version at i of the stage's history, newest first, and the one before.
func (m model) openVersion(i int) (model, tea.Cmd) {
	h := m.state.stage.history
	m.state.version = versionState{version: h[i]}
	previous := ""
	if i+1 < len(h) {
		previous = h[i+1].ID
	}
	m = m.switchPage(versionPage)
	m.loading, m.loadingMsg = true, "reading the version…"
	return m.loadVersion(h[i].ID, previous)
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
	v.scroll = scrollBy(msg, m.keys, v.scroll, contentHeight)
	return m, nil
}

// stateChanges lists what changed between two versions of a state, a line for each resource:
// added, removed, or the inputs that changed.
func stateChanges(before, after *state.State) []cell {
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
	var lines []cell
	line := func(symbol string, tone styles.Tone, id, detail string) {
		c := cell{styles.S(symbol+" ", tone), styles.S(id, styles.ToneText)}
		if detail != "" {
			c = append(c, styles.S("  "+detail, styles.ToneMuted))
		}
		lines = append(lines, c)
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

func (m model) versionLines() []string {
	v := m.state.version
	width := contentWidth - 4
	if !v.loaded || v.current == nil {
		return nil
	}
	cur := v.current
	lines := []string{
		styles.SectionHeader("version "+v.version.ID, width),
		row("saved", text(formatTime(v.version.LastModified)+", "+formatAgo(v.version.LastModified), styles.ToneBody), width),
		row("deployed by", text(orDash(cur.DeployedBy), styles.ToneBody), width),
		row("serial", text(formatInt(int(cur.Serial)), styles.ToneBody), width),
		row("resources", text(formatInt(len(cur.Resources)), styles.ToneBody), width),
		"",
	}
	title := "changed from the version before"
	if v.before == nil {
		title = "the first version"
	}
	lines = append(lines, styles.SectionHeader(title, width))
	changes := stateChanges(v.before, cur)
	for _, c := range changes {
		lines = append(lines, renderCell(c, column{width: width}, nil))
	}
	if len(changes) == 0 {
		lines = append(lines, styles.Faint("no resource changed: only the outputs or the record of the deploy"))
	}
	return lines
}

func (m model) versionView() string {
	return indent(scrolled(m.versionLines(), m.state.version.scroll, contentHeight))
}
