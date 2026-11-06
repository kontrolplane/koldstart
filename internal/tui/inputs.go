package tui

import (
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/kontrolplane/koldstart/internal/cli"
	"github.com/kontrolplane/koldstart/internal/engine"
	"github.com/kontrolplane/koldstart/internal/tui/styles"
)

// inputsState is what a change of a plan creates a resource with, where nothing was deployed
// before to diff it against: its inputs, read in a json view.
type inputsState struct {
	id     string
	detail string
	json   jsonView
	from   page
}

// opensInputs reports whether a change of a plan opens on its inputs rather than a diff: one that
// creates what has nothing deployed before it.
func opensInputs(c engine.Change) bool {
	return c.Action == engine.Create && len(c.Before) == 0
}

// openInputs shows the inputs a change creates its resource with.
func (m model) openInputs(c engine.Change) (model, tea.Cmd) {
	m.state.inputs = inputsState{id: c.ID, detail: cli.Verb(c.Action) + " " + typeName(c.Type), json: newJSONView(c.After), from: m.page}
	return m.switchPage(inputsPage), nil
}

// inputsHeaderLines are the lines above the inputs: the change, what it does and why there is no
// diff, and a blank line.
const inputsHeaderLines = 3

// inputsWidth is the width the inputs page draws its lines in.
func inputsWidth() int { return contentWidth - 4 }

// shown is the json of the inputs, or nil when there are none.
func (s *inputsState) shown() *jsonView {
	if s.json.empty() {
		return nil
	}
	return &s.json
}

// inputsUpdate handles a key on the inputs page: the keys of the json first, then q and esc go
// back to the plan.
func (m model) inputsUpdate(msg tea.KeyPressMsg) (model, tea.Cmd) {
	s := &m.state.inputs
	if v := s.shown(); v != nil {
		if res, ok := v.update(msg, m.keys, inputsWidth(), inputsHeight()); ok {
			return m.jsonDone(res)
		}
	}
	if key.Matches(msg, m.keys.Back, m.keys.Quit) {
		return m.switchPage(s.from), nil
	}
	return m, nil
}

// inputsHeight is the room the json gets below the header.
func inputsHeight() int { return max(1, contentHeight-inputsHeaderLines) }

func (m model) inputsView() string {
	s := m.state.inputs
	width := inputsWidth()
	lines := []string{
		styles.SectionHeader(s.id, width),
		renderCell(cell{styles.S(strings.TrimSpace(s.detail), styles.ToneSuccess), styles.S(" · its inputs, with nothing deployed to diff against", styles.ToneFaint)}, column{width: width}, nil),
		"",
	}
	if v := s.shown(); v != nil {
		lines = append(lines, v.view(width, inputsHeight()))
	} else {
		lines = append(lines, styles.Faint("no inputs"))
	}
	return indent(strings.Join(lines, "\n"))
}

// inputsHints are the footer's hints on the inputs page.
func (m model) inputsHints() [][2]string {
	var h [][2]string
	if v := m.state.json(inputsPage); v != nil {
		h = m.jsonHints(*v)
	}
	return append(h, hint(m.keys.Help), hint(m.keys.Quit))
}
