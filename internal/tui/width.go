package tui

import (
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/kontrolplane/koldstart/internal/tui/keys"
	"github.com/kontrolplane/koldstart/internal/tui/styles"
)

// truncate cuts plain text to width columns, ending it in an ellipsis when it was cut.
func truncate(s string, width int) string {
	if width <= 0 {
		return ""
	}
	return ansi.Truncate(s, width, "…")
}

// indent moves every line of s one column to the right, off the frame's edge.
func indent(s string) string {
	return " " + strings.ReplaceAll(s, "\n", "\n ")
}

// tableWidth is the width of a table that fills the content area, a column in from each side.
func tableWidth() int { return contentWidth - 2 }

// spread places left and right on one line of the given width.
func spread(left, right string, width int) string {
	gap := max(1, width-ansi.StringWidth(left)-ansi.StringWidth(right))
	return left + strings.Repeat(" ", gap) + right
}

// clip cuts s to height lines, ending lines wider than width in an ellipsis.
func clip(s string, width, height int) string {
	lines := strings.Split(s, "\n")
	if len(lines) > height {
		lines = lines[:max(height, 0)]
	}
	for i, line := range lines {
		if ansi.StringWidth(line) > width {
			lines[i] = ansi.Truncate(line, width, "…")
		}
	}
	return strings.Join(lines, "\n")
}

// fill pads every line of s to width and s to height lines, after cutting what does not fit, so
// a block lays out the same whatever it holds.
func fill(s string, width, height int) string {
	lines := strings.Split(clip(s, width, height), "\n")
	for len(lines) < height {
		lines = append(lines, "")
	}
	for i, line := range lines {
		lines[i] = line + strings.Repeat(" ", max(0, width-ansi.StringWidth(line)))
	}
	return strings.Join(lines, "\n")
}

// place centres s in an area of width and height, and cuts what does not fit.
func place(width, height int, s string) string {
	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, clip(s, width, height))
}

// frame draws the rounded border around the content area, with a title set into the top edge,
// meta to its right, and foot into the bottom edge. The body fills the content area, with a
// blank line above and below it.
func frame(title, meta, foot, body string) string {
	border := lipgloss.NewStyle().Foreground(styles.P.Rule)
	edge := func(n int) string { return border.Render(strings.Repeat("─", max(1, n))) }

	metaWidth := 0
	if meta != "" {
		meta = " " + ansi.Truncate(meta, frameWidth/3, "…") + " "
		metaWidth = ansi.StringWidth(meta)
	}
	title = ansi.Truncate(title, frameWidth-7-metaWidth, "…")
	top := border.Render("╭─ ") + title + " " + edge(frameWidth-6-ansi.StringWidth(title)-metaWidth) + meta + border.Render("─╮")

	footWidth := 0
	if foot != "" {
		foot = " " + ansi.Truncate(foot, frameWidth-6, "…") + " "
		footWidth = ansi.StringWidth(foot)
	}
	bottom := border.Render("╰") + edge(frameWidth-3-footWidth) + foot + border.Render("─╯")

	side := border.Render("│")
	blank := side + strings.Repeat(" ", contentWidth) + side
	lines := strings.Split(fill(body, contentWidth, contentHeight), "\n")
	var b strings.Builder
	b.WriteString(top + "\n" + blank)
	for _, line := range lines {
		b.WriteString("\n" + side + line + side)
	}
	b.WriteString("\n" + blank + "\n" + bottom)
	return b.String()
}

// moveCursor moves a cursor among n rows by the key: a row, a page, or to the first or last. It
// reports whether the key was one that moves it.
func moveCursor(msg tea.KeyPressMsg, k keys.KeyMap, cursor, n, page int) (int, bool) {
	switch {
	case key.Matches(msg, k.Up):
		cursor--
	case key.Matches(msg, k.Down):
		cursor++
	case key.Matches(msg, k.PageUp):
		cursor -= page
	case key.Matches(msg, k.PageDown):
		cursor += page
	case key.Matches(msg, k.Top):
		cursor = 0
	case key.Matches(msg, k.Bottom):
		cursor = n - 1
	default:
		return cursor, false
	}
	return max(0, min(cursor, n-1)), true
}

// selectable draws height lines of a page with rows that can be selected, rows holding the index
// of the line of each, the selected one marked. It scrolls so the selected row is in sight, and
// returns the scroll it drew at.
func selectable(lines []string, rows []int, cursor, scroll, height int) (string, int) {
	selected := -1
	if cursor >= 0 && cursor < len(rows) {
		selected = rows[cursor]
	}
	if selected >= 0 {
		scroll = min(scroll, selected)
		scroll = max(scroll, selected-height+1)
	}
	scroll = max(0, min(scroll, len(lines)-height))
	out := make([]string, 0, height)
	for i := scroll; i < min(len(lines), scroll+height); i++ {
		mark := "  "
		if i == selected {
			mark = styles.Accent("▌") + " "
		}
		out = append(out, mark+lines[i])
	}
	return strings.Join(out, "\n"), scroll
}
