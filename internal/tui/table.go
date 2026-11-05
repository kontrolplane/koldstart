package tui

import (
	"image/color"
	"slices"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/kontrolplane/koldstart/internal/tui/keys"
	"github.com/kontrolplane/koldstart/internal/tui/styles"
)

// column is one column of a table. A growing column takes a share of the width left over once
// every column has its own; when the table cannot fit, the columns with the highest drop are
// hidden first, and a column with drop 0 always stays.
type column struct {
	title string
	width int
	right bool
	grow  int
	drop  int
	hide  bool
}

// minimum is the narrowest the column gets.
func (c column) minimum() int {
	if c.grow == 0 {
		return c.width
	}
	return max(c.width, len(c.title), minColumnWidth)
}

// cell is the content of a table cell, in spans so a selected row can repaint it.
type cell []styles.Span

type tableRow []cell

// text builds a cell from a single span.
func text(s string, tone styles.Tone) cell { return cell{styles.S(s, tone)} }

const (
	tableGap = 2
	// tableGutter is the room for the cursor marker.
	tableGutter    = 2
	minColumnWidth = 8
)

// dataTable is a scrolling table whose cells keep their own tones. The bubbles table paints a
// selected row with a single style, which a coloured cell would cut through; this one paints every
// span onto the row's ground.
type dataTable struct {
	base    []column
	columns []column
	rows    []tableRow
	cursor  int
	offset  int
	height  int
	empty   string
}

func newTable(columns []column, empty string) dataTable {
	t := dataTable{base: columns, empty: empty}
	t.setSize(tableWidth(), contentHeight-tableHeaderRows)
	return t
}

func (t dataTable) width() int { return columnsWidth(t.columns, func(c column) int { return c.width }) }

// columnsWidth is the width of the visible columns, at the width the function gives them, with
// the gutter and the gaps between them.
func columnsWidth(columns []column, width func(column) int) int {
	w := tableGutter - tableGap
	for _, c := range columns {
		if !c.hide {
			w += tableGap + width(c)
		}
	}
	return w
}

// setSize fits the columns to width and shows height body rows. While the columns do not fit at
// their minimum, the ones with the highest drop are hidden. What is left over is shared by the
// growing columns.
func (t *dataTable) setSize(width, height int) {
	t.height = max(height, minTableHeight)
	t.columns = slices.Clone(t.base)
	for columnsWidth(t.columns, column.minimum) > width {
		worst := -1
		for i, c := range t.columns {
			if !c.hide && c.drop > 0 && (worst < 0 || c.drop > t.columns[worst].drop) {
				worst = i
			}
		}
		if worst < 0 {
			break
		}
		t.columns[worst].hide = true
	}
	shares := 0
	for i, c := range t.columns {
		t.columns[i].width = c.minimum()
		if !c.hide {
			shares += c.grow
		}
	}
	extra := width - t.width()
	if extra <= 0 || shares == 0 {
		t.setCursor(t.cursor)
		return
	}
	given, last := 0, -1
	for i, c := range t.columns {
		if c.grow > 0 && !c.hide {
			n := extra * c.grow / shares
			t.columns[i].width += n
			given += n
			last = i
		}
	}
	t.columns[last].width += extra - given
	t.setCursor(t.cursor)
}

func (t *dataTable) setRows(rows []tableRow) {
	t.rows = rows
	t.setCursor(t.cursor)
}

func (t *dataTable) setCursor(i int) {
	t.cursor = max(0, min(i, len(t.rows)-1))
	if t.cursor < t.offset {
		t.offset = t.cursor
	}
	if t.cursor >= t.offset+t.height {
		t.offset = t.cursor - t.height + 1
	}
	t.offset = max(0, min(t.offset, len(t.rows)-t.height))
}

// move handles the keys that move the cursor, and reports whether the key was one of them.
func (t *dataTable) move(msg tea.KeyPressMsg, k keys.KeyMap) bool {
	switch {
	case key.Matches(msg, k.Up):
		t.setCursor(t.cursor - 1)
	case key.Matches(msg, k.Down):
		t.setCursor(t.cursor + 1)
	case key.Matches(msg, k.PageUp):
		t.setCursor(t.cursor - t.height)
	case key.Matches(msg, k.PageDown):
		t.setCursor(t.cursor + t.height)
	case key.Matches(msg, k.Top):
		t.setCursor(0)
	case key.Matches(msg, k.Bottom):
		t.setCursor(len(t.rows) - 1)
	default:
		return false
	}
	return true
}

// position describes the scroll position, as 12 of 240.
func (t dataTable) position() string {
	if len(t.rows) == 0 {
		return ""
	}
	return formatInt(t.cursor+1) + " of " + formatInt(len(t.rows))
}

func (t dataTable) View() string {
	var b strings.Builder
	b.WriteString(strings.Repeat(" ", tableGutter))
	first := true
	for _, c := range t.columns {
		if c.hide {
			continue
		}
		if !first {
			b.WriteString(strings.Repeat(" ", tableGap))
		}
		first = false
		b.WriteString(renderCell(text(c.title, styles.ToneFaint), c, nil))
	}
	b.WriteString("\n")
	b.WriteString(lipgloss.NewStyle().Foreground(styles.P.Rule).Render(strings.Repeat("─", t.width())))
	for i := range t.height {
		b.WriteString("\n")
		r := t.offset + i
		switch {
		case r < len(t.rows):
			b.WriteString(t.renderRow(r))
		case i == 1 && len(t.rows) == 0 && t.empty != "":
			b.WriteString(strings.Repeat(" ", tableGutter) + styles.Muted(truncate(styles.Clean(t.empty), t.width()-tableGutter)))
		}
	}
	return b.String()
}

func (t dataTable) renderRow(r int) string {
	selected := r == t.cursor
	var bg color.Color
	marker := strings.Repeat(" ", tableGutter)
	if selected {
		bg = styles.P.Selection
		marker = styles.Fg(styles.ToneAccent).Background(bg).Render("▌") + pad(1, bg)
	}
	var b strings.Builder
	b.WriteString(marker)
	first := true
	for i, c := range t.columns {
		if c.hide {
			continue
		}
		if !first {
			b.WriteString(pad(tableGap, bg))
		}
		first = false
		var content cell
		if i < len(t.rows[r]) {
			content = t.rows[r][i]
		}
		if selected {
			content = lift(content)
		}
		b.WriteString(renderCell(content, c, bg))
	}
	return b.String()
}

// lift raises faint spans on a selected row, where the ground has less contrast to spare.
func lift(c cell) cell {
	out := make(cell, len(c))
	for i, s := range c {
		if s.Tone == styles.ToneFaint {
			s.Tone = styles.ToneMuted
		}
		out[i] = s
	}
	return out
}

func pad(n int, bg color.Color) string {
	if n <= 0 {
		return ""
	}
	if bg == nil {
		return strings.Repeat(" ", n)
	}
	return lipgloss.NewStyle().Background(bg).Render(strings.Repeat(" ", n))
}

// renderCell cuts the spans to the column width and aligns them. Content that does not fit ends
// in an ellipsis, also when the cut falls between two spans.
func renderCell(c cell, col column, bg color.Color) string {
	total := 0
	for _, s := range c {
		total += ansi.StringWidth(s.Text)
	}
	budget := col.width
	cut := total > col.width
	if cut {
		budget = col.width - 1
	}
	var b strings.Builder
	used := 0
	last := styles.Span{Tone: styles.ToneBody}
	for _, s := range c {
		if used >= budget {
			break
		}
		txt := s.Text
		if w := ansi.StringWidth(txt); used+w > budget {
			txt = ansi.Truncate(txt, budget-used, "")
		}
		used += ansi.StringWidth(txt)
		b.WriteString(paint(s, bg, txt))
		last = s
	}
	if cut && col.width > 0 {
		b.WriteString(paint(last, bg, "…"))
		used++
	}
	gap := pad(col.width-used, bg)
	if col.right {
		return gap + b.String()
	}
	return b.String() + gap
}

func paint(s styles.Span, bg color.Color, txt string) string {
	style := s.Style()
	if bg != nil {
		style = style.Background(bg)
	}
	return style.Render(txt)
}
