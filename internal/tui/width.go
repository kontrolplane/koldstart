package tui

import (
	"slices"
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

// spread places left and right on one line of the given width. When they do not fit, left is cut
// to an ellipsis so right stays whole.
func spread(left, right string, width int) string {
	rw := ansi.StringWidth(right)
	if ansi.StringWidth(left)+1+rw > width {
		left = truncate(left, width-rw-1)
	}
	gap := max(1, width-ansi.StringWidth(left)-rw)
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
		meta = " " + ansi.Truncate(meta, max(frameWidth/3, frameWidth-9-ansi.StringWidth(title)), "…") + " "
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

// pickList is a page of lines some of which can be chosen, as the changes of a plan: rows holds
// the index of the line of each, and lit the line of each drawn chosen, on the selection's ground.
// The lines are drawn only where they are in sight, so a plan of thousands of changes draws a frame
// as quickly as one of a screenful. The first head lines are the page's head, drawn one column in
// as the heads of the other pages are, rather than beside the cursor's gutter, and so are the
// lines in headers, the headers of sections that follow rows.
type pickList struct {
	lines   lazyLines
	rows    []int
	lit     []func() string
	head    int
	headers []int
}

// addHeader appends the header of a section, drawn one column in as the head is.
func (l *pickList) addHeader(line string) {
	l.headers = append(l.headers, l.lines.len())
	l.lines.add(line)
}

// addDrawn appends a line that can be chosen, drawn by line, and by lit when it is chosen, only
// when it is in sight.
func (l *pickList) addDrawn(line, lit func() string) {
	l.rows = append(l.rows, l.lines.len())
	l.lines.addDrawn(1, func(int) string { return line() })
	l.lit = append(l.lit, lit)
}

// position names the chosen row among the rows, as the tables do.
func (l pickList) position(cursor int) string {
	return ofCount(cursor, len(l.rows))
}

// selectable draws height lines of a page with rows that can be selected, the selected one marked
// and painted as a table paints the row under its cursor, its mark one column in as a table's is,
// so a row is drawn three columns in and must fit in three less than the page. It scrolls so the
// selected row is in sight, and returns the scroll it drew at.
func selectable(l pickList, cursor, scroll, height int) (string, int) {
	selected := -1
	if cursor >= 0 && cursor < len(l.rows) {
		selected = l.rows[cursor]
	}
	if selected >= 0 {
		scroll = min(scroll, selected)
		scroll = max(scroll, selected-height+1)
	}
	scroll = max(0, min(scroll, l.lines.len()-height))
	out := make([]string, 0, height)
	for i, line := range l.lines.window(scroll, scroll+height) {
		switch {
		case scroll+i == selected && cursor < len(l.lit):
			out = append(out, " "+cursorMark(styles.P.Selection)+l.lit[cursor]())
		case scroll+i == selected:
			out = append(out, " "+styles.Accent("▌")+" "+line)
		case scroll+i < l.head || slices.Contains(l.headers, scroll+i):
			out = append(out, " "+line)
		default:
			out = append(out, "   "+line)
		}
	}
	return strings.Join(out, "\n"), scroll
}

// scrollCue says which lines of a page that scrolls are in sight, as lines 12–40 of 230: shown
// lines from the one at first, of total. It is empty when every line is in sight.
func scrollCue(first, shown, total int) string {
	first = max(0, min(first, total-1))
	shown = min(shown, total-first)
	if shown <= 0 || shown >= total {
		return ""
	}
	return "lines " + formatInt(first+1) + "–" + formatInt(first+shown) + " of " + formatInt(total)
}

// lazyLines are the lines of a page, drawn only where they are in sight, so a page of thousands
// of lines draws a frame as quickly as one of a screenful: lines drawn already, and blocks of
// lines each drawn when a frame asks for it.
type lazyLines struct {
	blocks []lineBlock
	n      int
}

// lineBlock is a run of lazyLines: lines drawn already, or n lines that draw draws one at a time.
type lineBlock struct {
	lines []string
	n     int
	draw  func(i int) string
}

// add appends lines drawn already.
func (l *lazyLines) add(lines ...string) {
	if len(lines) == 0 {
		return
	}
	if last := len(l.blocks) - 1; last >= 0 && l.blocks[last].draw == nil {
		l.blocks[last].lines = append(l.blocks[last].lines, lines...)
		l.blocks[last].n += len(lines)
	} else {
		l.blocks = append(l.blocks, lineBlock{lines: slices.Clip(lines), n: len(lines)})
	}
	l.n += len(lines)
}

// addDrawn appends n lines that draw draws when they are in sight.
func (l *lazyLines) addDrawn(n int, draw func(i int) string) {
	if n <= 0 {
		return
	}
	l.blocks = append(l.blocks, lineBlock{n: n, draw: draw})
	l.n += n
}

// join appends the lines of o.
func (l *lazyLines) join(o lazyLines) {
	for _, b := range o.blocks {
		if b.draw == nil {
			l.add(b.lines...)
		} else {
			l.addDrawn(b.n, b.draw)
		}
	}
}

func (l lazyLines) len() int { return l.n }

// window draws the lines from from up to to, as far as there are.
func (l lazyLines) window(from, to int) []string {
	from, to = max(0, from), min(to, l.n)
	if from >= to {
		return nil
	}
	out := make([]string, 0, to-from)
	at := 0
	for _, b := range l.blocks {
		if at >= to {
			break
		}
		lo, hi := max(from, at)-at, min(to, at+b.n)-at
		for i := lo; i < hi; i++ {
			if b.draw != nil {
				out = append(out, b.draw(i))
			} else {
				out = append(out, b.lines[i])
			}
		}
		at += b.n
	}
	return out
}

// lazyOf is lines drawn already, as lazyLines.
func lazyOf(lines []string) lazyLines {
	var l lazyLines
	l.add(lines...)
	return l
}

// scrolledLines draws height lines of l from offset, as far as there are.
func scrolledLines(l lazyLines, offset, height int) string {
	offset = max(0, min(offset, l.len()-height))
	return strings.Join(l.window(offset, offset+height), "\n")
}

// detailWidth is the width of the lines of a detail page, a resource, an output, a diff, a
// version, a plan, an invocation or a line of the logs: they start one column in and end one
// before the frame, as a table's rows do, so their section rules end where a table's does.
func detailWidth() int { return contentWidth - 2 }
