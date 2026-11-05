package tui

import (
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/kontrolplane/koldstart/internal/tui/styles"
)

type confirmResult int

const (
	confirmPending confirmResult = iota
	confirmYes
	confirmNo
)

// confirmKey handles the no/yes choice of a dialog, moving selected between the buttons, 0 for
// no and 1 for yes. Once answered, selected is back on no for the next time the dialog opens.
func (m model) confirmKey(msg tea.Msg, selected *int) confirmResult {
	k, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return confirmPending
	}
	result := confirmPending
	switch {
	case key.Matches(k, m.keys.Left, m.keys.Right, m.keys.NextTab, m.keys.PrevTab):
		*selected = 1 - *selected
	case key.Matches(k, m.keys.Yes):
		result = confirmYes
	case key.Matches(k, m.keys.No, m.keys.Quit, m.keys.Back):
		result = confirmNo
	case key.Matches(k, m.keys.View):
		result = confirmNo
		if *selected == 1 {
			result = confirmYes
		}
	}
	if result != confirmPending {
		*selected = 0
	}
	return result
}

// confirmButtons renders a no/yes pair, selected 0 highlighting no and 1 yes.
func confirmButtons(selected int, tone styles.Tone) string {
	return lipgloss.JoinHorizontal(lipgloss.Center,
		styles.Button("no", selected == 0, styles.ToneText),
		"    ",
		styles.Button("yes", selected == 1, tone),
	)
}

// dialogPadX is the room a dialog card leaves between its edge and its text.
const dialogPadX = 4

// dialogTextWidth is the widest a line of dialog text gets before it wraps.
func dialogTextWidth() int {
	return max(10, min(contentWidth-4-2*dialogPadX, 90))
}

// dialog renders a card with a toned edge, for confirmations and errors, to be set over the page
// with overlay. Lines wider than the card wrap, and the card gives up its inner margin when it
// would not fit the height.
func dialog(title string, tone styles.Tone, body ...string) string {
	heading := styles.Render(styles.B("▲ ", tone), styles.B(title, tone))
	width := dialogTextWidth()
	lines := []string{heading, ""}
	for _, block := range body {
		for line := range strings.SplitSeq(block, "\n") {
			if ansi.StringWidth(line) > width {
				line = lipgloss.Wrap(line, width, "")
			}
			lines = append(lines, line)
		}
	}
	content := lipgloss.JoinVertical(lipgloss.Center, lines...)
	padY := 1
	if lipgloss.Height(content)+2+2*padY > contentHeight {
		padY = 0
	}
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(styles.P.Color(tone)).
		Padding(padY, dialogPadX).
		Render(clip(content, width, contentHeight-2-2*padY))
}

// overlay sets card in the middle of the content area, over the page, which recedes behind it so
// it stays clear what the card is about.
func overlay(page, card string) string {
	lines := strings.Split(ansi.Strip(fill(page, contentWidth, contentHeight)), "\n")
	dim := lipgloss.NewStyle().Foreground(styles.P.Rule)
	for i, l := range lines {
		lines[i] = dim.Render(l)
	}
	card = lipgloss.NewStyle().Padding(0, 1).Render(card)
	w, h := lipgloss.Width(card), lipgloss.Height(card)
	c := lipgloss.NewCanvas(contentWidth, contentHeight)
	c.Compose(lipgloss.NewCompositor(
		lipgloss.NewLayer(strings.Join(lines, "\n")),
		lipgloss.NewLayer(card).X(max(0, (contentWidth-w)/2)).Y(max(0, (contentHeight-h)/2)).Z(1),
	))
	return fill(c.Render(), contentWidth, contentHeight)
}

// typedConfirm guards an action that cannot be undone behind typing a phrase, the name of the
// stage it removes. action is what enter then does, as remove orders/dev.
type typedConfirm struct {
	want   string
	action string
	input  textinput.Model
	// tried is enter pressed on what does not match, until it is typed again.
	tried bool
}

func newTypedConfirm(want, action string) (typedConfirm, tea.Cmd) {
	input := textinput.New()
	input.CharLimit = 255
	input.SetWidth(min(max(24, ansi.StringWidth(want)+2), dialogTextWidth()-6))
	input.Prompt = "› "
	input.SetStyles(styles.TextInput())
	cmd := input.Focus()
	return typedConfirm{want: want, action: action, input: input}, cmd
}

func (c typedConfirm) ok() bool {
	return strings.TrimSpace(c.input.Value()) == c.want
}

// wrong reports whether what was typed cannot become the phrase, or enter was pressed on it.
func (c typedConfirm) wrong() bool {
	typed := strings.TrimSpace(c.input.Value())
	if typed == "" || c.ok() {
		return false
	}
	return c.tried || !strings.HasPrefix(c.want, typed)
}

func (c typedConfirm) update(msg tea.Msg) (typedConfirm, tea.Cmd) {
	before := c.input.Value()
	var cmd tea.Cmd
	c.input, cmd = c.input.Update(msg)
	if c.input.Value() != before {
		c.tried = false
	}
	return c, cmd
}

// view asks for the phrase, marks whether what was typed matches it, and says below what enter
// does or that the phrase is not what was typed. The lines stay the same in number as it is
// typed, so the dialog does not move. The input takes the palette in use now.
func (c typedConfirm) view() string {
	c.input.SetStyles(styles.TextInput())
	mark := "  "
	say := styles.Render(styles.S("esc cancels", styles.ToneFaint))
	switch {
	case c.ok():
		mark = styles.Render(styles.S("✓ ", styles.ToneSuccess))
		say = styles.Render(styles.S("enter to "+c.action, styles.ToneSuccess))
	case c.wrong():
		mark = styles.Render(styles.S("✗ ", styles.ToneDanger))
		say = styles.Render(styles.S("that is not ", styles.ToneDanger), styles.B(c.want, styles.ToneDanger))
	}
	ask := styles.Render(styles.S("type ", styles.ToneFaint), styles.B(c.want, styles.ToneText), styles.S(" to confirm", styles.ToneFaint))
	return lipgloss.JoinVertical(lipgloss.Center, ask, "", mark+c.input.View()+"  ", "", say)
}
