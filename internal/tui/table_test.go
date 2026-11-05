package tui

import (
	"strconv"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/kontrolplane/koldstart/internal/tui/keys"
	"github.com/kontrolplane/koldstart/internal/tui/styles"
)

// shown is the titles of the columns a table shows.
func shown(t dataTable) []string {
	var out []string
	for _, c := range t.columns {
		if !c.hide {
			out = append(out, c.title)
		}
	}
	return out
}

// numbered is n rows of a single cell, the row's number.
func numbered(n int) []tableRow {
	rows := make([]tableRow, n)
	for i := range rows {
		rows[i] = tableRow{text(strconv.Itoa(i), styles.ToneBody)}
	}
	return rows
}

// TestTableFitsWidth sizes every table of the pages to widths from far too narrow to very wide,
// and checks the rule and the selected row are as wide as the table, which is as wide as it was
// given while its columns fit at one column each, and no line is wider.
func TestTableFitsWidth(t *testing.T) {
	tests := []struct {
		name    string
		columns []column
	}{
		{name: "overview", columns: overviewColumns},
		{name: "functions", columns: functionColumns},
		{name: "resources", columns: resourceColumns},
		{name: "outputs", columns: outputColumns},
		{name: "history", columns: historyColumns},
		{name: "invocations", columns: invocationColumns},
		{name: "events", columns: eventColumns},
		{name: "no growing column", columns: []column{{title: "a", width: 4}, {title: "b", width: 6, right: true}}},
	}
	long := strings.Repeat("日本語🚀 very-long-value ", 20)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for width := 4; width <= 320; width++ {
				tbl := newTable(tt.columns, "")
				row := make(tableRow, len(tt.columns))
				for i := range row {
					row[i] = cell{styles.S(long, styles.ToneMuted), styles.S(long, styles.ToneFaint)}
				}
				tbl.setRows([]tableRow{row, row})
				tbl.setSize(width, 4)
				floor := tableGutter + len(shown(tbl))*(1+tableGap) - tableGap
				if want := max(width, floor); tbl.width() != want {
					t.Fatalf("width %d: table is %d wide, want %d", width, tbl.width(), want)
				}
				lines := strings.Split(tbl.View(), "\n")
				for i, line := range lines[:4] {
					w := ansi.StringWidth(line)
					if w > tbl.width() || ((i == 1 || i == 2) && w != tbl.width()) {
						t.Fatalf("width %d: line %d is %d wide, want %d:\n%s", width, i, w, tbl.width(), ansi.Strip(line))
					}
				}
			}
		})
	}
}

// TestTableDropsColumns checks the columns with the highest drop are hidden first as the table
// narrows, and the ones with drop 0 always show.
func TestTableDropsColumns(t *testing.T) {
	columns := []column{
		{title: "name", width: 10, grow: 1},
		{title: "a", width: 10, drop: 1},
		{title: "b", width: 10, drop: 2},
		{title: "c", width: 10},
	}
	tests := []struct {
		name  string
		width int
		want  []string
	}{
		{name: "roomy", width: 60, want: []string{"name", "a", "b", "c"}},
		{name: "exact", width: 48, want: []string{"name", "a", "b", "c"}},
		{name: "one short", width: 47, want: []string{"name", "a", "c"}},
		{name: "narrower", width: 35, want: []string{"name", "c"}},
		{name: "too narrow", width: 10, want: []string{"name", "c"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tbl := newTable(columns, "")
			tbl.setSize(tt.width, 3)
			if got := shown(tbl); strings.Join(got, ",") != strings.Join(tt.want, ",") {
				t.Errorf("shows %q, want %q", got, tt.want)
			}
		})
	}
}

// TestTableFitsContent checks the room a table has goes to the columns its rows are cut in, as
// with the long ids of a real stage, rather than to columns their rows leave empty: a column a
// little short shows whole beside one that cannot, a fixed column gives up what no row fills,
// and short rows let a column show that would otherwise be hidden.
func TestTableFitsContent(t *testing.T) {
	row := func(cells ...string) tableRow {
		r := make(tableRow, len(cells))
		for i, c := range cells {
			r[i] = text(c, styles.ToneBody)
		}
		return r
	}
	tests := []struct {
		name    string
		columns []column
		rows    []tableRow
		width   int
		whole   []string
		shows   []string
	}{
		{
			name: "resources", columns: resourceColumns, width: 116,
			rows: []tableRow{
				row("├─ permission/api/httpapi", "ApiGatewayV2::Integration", "r2ui-shop-dev-api/koldstart-aba93bd69165933a", ""),
				row("resource/jobs", "AWS::SQS::Queue", "http://localhost:4566/000000000000/cloudcontrol-resource-14d4ee40e893", "0 queued"),
			},
			whole: []string{"├─ permission/api/httpapi", "ApiGatewayV2::Integration", "r2ui-shop-dev-api/koldstart-aba93bd69165933a", "0 queued"},
		},
		{
			name: "functions on 80 columns", columns: functionColumns, width: 76,
			rows:  []tableRow{row("api", "nodejs22.x", "384 mb", "14s", "0", "0", "-", "d5e474f8c12a", "r2ui-shop-dev-api")},
			whole: []string{"api", "nodejs22.x"},
			shows: []string{"function", "runtime", "memory", "timeout", "calls 1h", "errors", "p99"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tbl := newTable(tt.columns, "")
			tbl.setSize(tt.width, 4)
			tbl.setRows(tt.rows)
			v := ansi.Strip(tbl.View())
			for _, want := range tt.whole {
				if !strings.Contains(v, want) {
					t.Errorf("%q is cut:\n%s", want, v)
				}
			}
			if tt.shows != nil && strings.Join(shown(tbl), ",") != strings.Join(tt.shows, ",") {
				t.Errorf("shows %q, want %q", shown(tbl), tt.shows)
			}
			if tbl.width() != tt.width {
				t.Errorf("table is %d wide, want %d", tbl.width(), tt.width)
			}
		})
	}
}

// TestSelectedRowSpansTable checks the ground of the selected row reaches the end of the table,
// also when no column grows to fill it.
func TestSelectedRowSpansTable(t *testing.T) {
	tests := []struct {
		name    string
		columns []column
	}{
		{name: "growing", columns: []column{{title: "a", width: 4, grow: 1}}},
		{name: "fixed", columns: []column{{title: "a", width: 4}, {title: "n", width: 4, right: true}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tbl := newTable(tt.columns, "")
			tbl.setRows(numbered(2))
			tbl.setSize(40, 3)
			if w := ansi.StringWidth(tbl.renderRow(0)); w != 40 {
				t.Errorf("selected row is %d wide, want 40", w)
			}
		})
	}
}

// TestRenderCell checks cells are cut to the column with an ellipsis, wide runes and emoji are
// measured as the terminal draws them, and right-aligned cells are padded on the left.
func TestRenderCell(t *testing.T) {
	tests := []struct {
		name  string
		cell  cell
		width int
		right bool
		want  string
	}{
		{name: "fits", cell: text("abc", styles.ToneBody), width: 5, want: "abc  "},
		{name: "exact", cell: text("abcde", styles.ToneBody), width: 5, want: "abcde"},
		{name: "cut", cell: text("abcdef", styles.ToneBody), width: 5, want: "abcd…"},
		{name: "right", cell: text("42", styles.ToneBody), width: 5, right: true, want: "   42"},
		{name: "right cut", cell: text("123456", styles.ToneBody), width: 5, right: true, want: "1234…"},
		{name: "between spans", cell: cell{styles.S("abcd", styles.ToneBody), styles.S("ef", styles.ToneFaint)}, width: 5, want: "abcd…"},
		{name: "wide fits", cell: text("日本", styles.ToneBody), width: 5, want: "日本 "},
		{name: "wide cut", cell: text("日本語", styles.ToneBody), width: 5, want: "日本…"},
		{name: "wide not whole", cell: text("a日本語", styles.ToneBody), width: 5, want: "a日… "},
		{name: "wide not whole before a span", cell: cell{styles.S("ab日", styles.ToneBody), styles.S("c", styles.ToneBody)}, width: 4, want: "ab… "},
		{name: "emoji", cell: text("🚀 go", styles.ToneBody), width: 6, want: "🚀 go "},
		{name: "emoji cut", cell: text("🚀🚀🚀", styles.ToneBody), width: 4, want: "🚀… "},
		{name: "one wide", cell: text("abc", styles.ToneBody), width: 1, want: "…"},
		{name: "no width", cell: text("abc", styles.ToneBody), width: 0, want: ""},
		{name: "empty", cell: nil, width: 3, want: "   "},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := renderCell(tt.cell, column{width: tt.width, right: tt.right}, nil)
			if plain := ansi.Strip(got); plain != tt.want {
				t.Errorf("got %q, want %q", plain, tt.want)
			}
			if w := ansi.StringWidth(got); w != tt.width {
				t.Errorf("%d wide, want %d", w, tt.width)
			}
		})
	}
}

// TestTableMove checks the keys that move the cursor stop at the first and the last row, and a
// page moves the rows in sight with the cursor.
func TestTableMove(t *testing.T) {
	tests := []struct {
		name       string
		rows       int
		from       int
		key        tea.KeyPressMsg
		cursor     int
		offset     int
		wantHandle bool
	}{
		{name: "down", rows: 20, key: tea.KeyPressMsg{Code: 'j', Text: "j"}, cursor: 1, offset: 0, wantHandle: true},
		{name: "up at the top", rows: 20, key: tea.KeyPressMsg{Code: tea.KeyUp}, cursor: 0, offset: 0, wantHandle: true},
		{name: "down at the bottom", rows: 20, from: 19, key: tea.KeyPressMsg{Code: tea.KeyDown}, cursor: 19, offset: 15, wantHandle: true},
		{name: "page down", rows: 20, key: tea.KeyPressMsg{Code: tea.KeyPgDown}, cursor: 5, offset: 5, wantHandle: true},
		{name: "page down keeps the place", rows: 20, from: 2, key: tea.KeyPressMsg{Code: tea.KeyPgDown}, cursor: 7, offset: 5, wantHandle: true},
		{name: "page down at the end", rows: 20, from: 17, key: tea.KeyPressMsg{Code: tea.KeyPgDown}, cursor: 19, offset: 15, wantHandle: true},
		{name: "page up", rows: 20, from: 12, key: tea.KeyPressMsg{Code: tea.KeyPgUp}, cursor: 7, offset: 3, wantHandle: true},
		{name: "page up at the start", rows: 20, from: 3, key: tea.KeyPressMsg{Code: tea.KeyPgUp}, cursor: 0, offset: 0, wantHandle: true},
		{name: "top", rows: 20, from: 12, key: tea.KeyPressMsg{Code: 'g', Text: "g"}, cursor: 0, offset: 0, wantHandle: true},
		{name: "bottom", rows: 20, key: tea.KeyPressMsg{Code: tea.KeyEnd}, cursor: 19, offset: 15, wantHandle: true},
		{name: "fewer rows than fit", rows: 3, key: tea.KeyPressMsg{Code: tea.KeyPgDown}, cursor: 2, offset: 0, wantHandle: true},
		{name: "no rows", rows: 0, key: tea.KeyPressMsg{Code: tea.KeyDown}, cursor: 0, offset: 0, wantHandle: true},
		{name: "other key", rows: 20, key: tea.KeyPressMsg{Code: 'x', Text: "x"}, cursor: 0, offset: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tbl := newTable([]column{{title: "n", width: 4, grow: 1}}, "")
			tbl.setSize(20, 5)
			tbl.setRows(numbered(tt.rows))
			tbl.setCursor(tt.from)
			if handled := tbl.move(tt.key, keys.Keys); handled != tt.wantHandle {
				t.Errorf("handled %t, want %t", handled, tt.wantHandle)
			}
			if tbl.cursor != tt.cursor || tbl.offset != tt.offset {
				t.Errorf("cursor %d offset %d, want %d and %d", tbl.cursor, tbl.offset, tt.cursor, tt.offset)
			}
			if tbl.cursor < tbl.offset || (tt.rows > 0 && tbl.cursor >= tbl.offset+tbl.height) {
				t.Errorf("cursor %d is out of sight at offset %d", tbl.cursor, tbl.offset)
			}
		})
	}
}

// TestTableKeepsCursorWhenRowsShrink checks the cursor and the scroll stay on the rows when a
// filter leaves fewer of them.
func TestTableKeepsCursorWhenRowsShrink(t *testing.T) {
	tests := []struct {
		name   string
		rows   int
		cursor int
		offset int
	}{
		{name: "fewer than fit", rows: 2, cursor: 1, offset: 0},
		{name: "some", rows: 8, cursor: 7, offset: 3},
		{name: "none", rows: 0, cursor: 0, offset: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tbl := newTable([]column{{title: "n", width: 4, grow: 1}}, "")
			tbl.setSize(20, 5)
			tbl.setRows(numbered(40))
			tbl.setCursor(30)
			tbl.setRows(numbered(tt.rows))
			if tbl.cursor != tt.cursor || tbl.offset != tt.offset {
				t.Errorf("cursor %d offset %d, want %d and %d", tbl.cursor, tbl.offset, tt.cursor, tt.offset)
			}
		})
	}
}

// TestPosition checks the scroll position names the row under the cursor.
func TestPosition(t *testing.T) {
	tests := []struct {
		name   string
		rows   int
		cursor int
		want   string
	}{
		{name: "empty", rows: 0, want: ""},
		{name: "first", rows: 3, want: "1 of 3"},
		{name: "last", rows: 1200, cursor: 1199, want: "1,200 of 1,200"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tbl := newTable([]column{{title: "n", width: 4, grow: 1}}, "")
			tbl.setRows(numbered(tt.rows))
			tbl.setCursor(tt.cursor)
			if got := tbl.position(); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

// TestLift checks a selected row raises faint text, and muted text as well on the light
// selection, where it would read at under 4:1.
func TestLift(t *testing.T) {
	tests := []struct {
		name string
		dark bool
		in   styles.Tone
		want styles.Tone
	}{
		{name: "dark faint", dark: true, in: styles.ToneFaint, want: styles.ToneMuted},
		{name: "dark muted", dark: true, in: styles.ToneMuted, want: styles.ToneMuted},
		{name: "dark danger", dark: true, in: styles.ToneDanger, want: styles.ToneDanger},
		{name: "light faint", in: styles.ToneFaint, want: styles.ToneBody},
		{name: "light muted", in: styles.ToneMuted, want: styles.ToneBody},
		{name: "light accent", in: styles.ToneAccent, want: styles.ToneAccent},
	}
	t.Cleanup(func() { styles.Use(true) })
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			styles.Use(tt.dark)
			if got := lift(cell{styles.S("x", tt.in)})[0].Tone; got != tt.want {
				t.Errorf("got tone %d, want %d", got, tt.want)
			}
		})
	}
}
