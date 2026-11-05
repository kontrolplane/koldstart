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

// minimum is the narrowest the column gets while the table fits.
func (c column) minimum() int {
	if c.grow == 0 {
		return c.width
	}
	return max(c.width, ansi.StringWidth(c.title), minColumnWidth)
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
	// spare is the width no column takes, painted at the end of a row so a selected row spans the
	// table.
	spare int
}

func newTable(columns []column, empty string) dataTable {
	t := dataTable{base: columns, empty: empty}
	t.setSize(tableWidth(), contentHeight-tableHeaderRows)
	return t
}

// width is the width the table draws at: its columns, the gaps between them and the spare.
func (t dataTable) width() int {
	return columnsWidth(t.columns, func(c column) int { return c.width }) + t.spare
}

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
// their minimum, the ones with the highest drop are hidden; if they still do not fit, the widest
// give way. What is left over is shared by the growing columns, or kept as spare when none shows.
func (t *dataTable) setSize(width, height int) {
	t.height = max(height, minTableHeight)
	t.columns = slices.Clone(t.base)
	t.spare = 0
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
	t.shrink(width)
	extra := width - t.width()
	switch {
	case extra <= 0:
	case shares == 0:
		t.spare = extra
	default:
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
	}
	t.setCursor(t.cursor)
}

// shrink narrows the widest shown column, the rightmost of equals, a column at a time until the
// table fits width or every column is one wide.
func (t *dataTable) shrink(width int) {
	for over := t.width() - width; over > 0; over-- {
		widest := -1
		for i, c := range t.columns {
			if !c.hide && c.width > 1 && (widest < 0 || c.width >= t.columns[widest].width) {
				widest = i
			}
		}
		if widest < 0 {
			return
		}
		t.columns[widest].width--
	}
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
		t.page(-t.height)
	case key.Matches(msg, k.PageDown):
		t.page(t.height)
	case key.Matches(msg, k.Top):
		t.setCursor(0)
	case key.Matches(msg, k.Bottom):
		t.setCursor(len(t.rows) - 1)
	default:
		return false
	}
	return true
}

// page moves the rows in sight and the cursor by n rows, so the cursor keeps its place on screen
// until the first or the last row is in sight.
func (t *dataTable) page(n int) {
	t.offset = max(0, min(t.offset+n, len(t.rows)-t.height))
	t.setCursor(t.cursor + n)
}

// position describes the scroll position, as 12 of 240.
func (t dataTable) position() string {
	return ofCount(t.cursor, len(t.rows))
}

// ofCount names the row at cursor among n, as 12 of 240, or nothing without rows.
func ofCount(cursor, n int) string {
	if n == 0 {
		return ""
	}
	return formatInt(cursor+1) + " of " + formatInt(n)
}

// cursorMark is the marker of the row under the cursor, on the ground of the selection.
func cursorMark(bg color.Color) string {
	return styles.Fg(styles.ToneAccent).Background(bg).Render("▌") + pad(tableGutter-1, bg)
}

// litRow draws cells side by side in their columns, lifted onto the ground of the selection, for a
// chosen line of a page that is not a table, as a change of a plan.
func litRow(cols []column, cells ...cell) string {
	bg := styles.P.Selection
	var b strings.Builder
	for i, c := range cells {
		if i < len(cols) {
			b.WriteString(renderCell(lift(c), cols[i], bg))
		}
	}
	return b.String()
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

// renderRow draws a row, the selected one on the selection with its marker. Where the selection
// has no ground to sit on (see styles.Reverse), the selected row is its text in reverse video.
func (t dataTable) renderRow(r int) string {
	selected := r == t.cursor
	if selected && styles.Reverse() {
		plain := t
		plain.cursor = -1
		row := ansi.Strip(plain.renderRow(r))[1:] + strings.Repeat(" ", t.spare)
		return styles.Accent("▌") + lipgloss.NewStyle().Reverse(true).Render(row)
	}
	var bg color.Color
	marker := strings.Repeat(" ", tableGutter)
	if selected {
		bg = styles.P.Selection
		marker = cursorMark(bg)
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
	if bg != nil {
		b.WriteString(pad(t.spare, bg))
	}
	return b.String()
}

// lift raises faint spans on a selected row, where the ground has less contrast to spare: faint
// text reads at under 4.5:1 on either selection. The light selection takes more of it than the
// dark one, muted text on it reading at under 4.5:1 as well, so there faint and muted spans are
// lifted to body.
func lift(c cell) cell {
	out := make(cell, len(c))
	for i, s := range c {
		switch {
		case s.Tone == styles.ToneFaint && styles.P.Dark:
			s.Tone = styles.ToneMuted
		case (s.Tone == styles.ToneFaint || s.Tone == styles.ToneMuted) && !styles.P.Dark:
			s.Tone = styles.ToneBody
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
// in an ellipsis, also when the cut falls between two spans or a wide rune does not fit whole.
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
		full := used+ansi.StringWidth(txt) <= budget
		if !full {
			txt = ansi.Truncate(txt, budget-used, "")
		}
		used += ansi.StringWidth(txt)
		b.WriteString(paint(s, bg, txt))
		last = s
		if !full {
			break
		}
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
