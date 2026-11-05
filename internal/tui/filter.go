package tui

import (
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"github.com/kontrolplane/koldstart/internal/tui/keys"
	"github.com/kontrolplane/koldstart/internal/tui/styles"
)

// filterBox is the / filter of a table: while active the keys go into its input, enter applies
// what was typed and esc clears it.
type filterBox struct {
	active bool
	input  textinput.Model
	text   string
}

func newFilterBox(placeholder string) filterBox {
	input := textinput.New()
	input.Placeholder = placeholder
	input.Prompt = ""
	input.CharLimit = 200
	input.SetStyles(styles.TextInput())
	return filterBox{input: input}
}

func (f *filterBox) open() tea.Cmd {
	f.active = true
	f.input.SetValue(f.text)
	f.input.CursorEnd()
	return f.input.Focus()
}

// update takes a key while the filter is active, or another message for its input, as the blink
// of its cursor or a paste. It reports whether the filter text changed.
func (f *filterBox) update(msg tea.Msg, k keys.KeyMap) (bool, tea.Cmd) {
	press, _ := msg.(tea.KeyPressMsg)
	switch {
	case key.Matches(press, k.View):
		f.active = false
		f.input.Blur()
		return false, nil
	case key.Matches(press, k.Back):
		f.active = false
		f.input.Blur()
		f.input.SetValue("")
		changed := f.text != ""
		f.text = ""
		return changed, nil
	}
	var cmd tea.Cmd
	f.input, cmd = f.input.Update(msg)
	before := f.text
	f.text = strings.TrimSpace(f.input.Value())
	return f.text != before, cmd
}

// clear drops the filter, and reports whether there was one.
func (f *filterBox) clear() bool {
	had := f.text != ""
	f.text = ""
	f.input.SetValue("")
	return had
}

// matches reports whether any of the fields holds the filter text, ignoring case.
func (f filterBox) matches(fields ...string) bool {
	if f.text == "" {
		return true
	}
	want := strings.ToLower(f.text)
	for _, field := range fields {
		if strings.Contains(strings.ToLower(field), want) {
			return true
		}
	}
	return false
}

// view draws the filter as it is typed.
func (f filterBox) view(width int, k keys.KeyMap) string {
	f.input.SetWidth(max(10, width-30))
	return styles.Accent("/ ") + f.input.View() + "  " + hints(hint(k.View, "apply"), hint(k.Back, "clear"))
}
