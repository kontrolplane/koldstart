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

// TestTruncate checks text is cut to its width in columns, ending in an ellipsis only when cut.
func TestTruncate(t *testing.T) {
	tests := []struct {
		name  string
		in    string
		width int
		want  string
	}{
		{name: "fits", in: "orders", width: 10, want: "orders"},
		{name: "exact", in: "orders", width: 6, want: "orders"},
		{name: "cut", in: "orders-dev", width: 6, want: "order…"},
		{name: "wide", in: "日本語", width: 5, want: "日本…"},
		{name: "wide not whole", in: "日本語", width: 4, want: "日…"},
		{name: "emoji", in: "🚀🚀🚀", width: 3, want: "🚀…"},
		{name: "one", in: "orders", width: 1, want: "…"},
		{name: "none", in: "orders", width: 0, want: ""},
		{name: "negative", in: "orders", width: -3, want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := truncate(tt.in, tt.width); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

// TestSpread checks left and right sit at the two ends of the line, and left gives way when they
// do not fit.
func TestSpread(t *testing.T) {
	tests := []struct {
		name  string
		left  string
		right string
		width int
		want  string
	}{
		{name: "roomy", left: "a", right: "b", width: 5, want: "a   b"},
		{name: "exact", left: "abc", right: "de", width: 6, want: "abc de"},
		{name: "left cut", left: "abcdef", right: "xy", width: 6, want: "ab… xy"},
		{name: "wide left cut", left: "日本語", right: "ok", width: 7, want: "日…  ok"},
		{name: "styled left cut", left: styles.Muted("abcdef"), right: "xy", width: 6, want: "ab… xy"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := spread(tt.left, tt.right, tt.width)
			if plain := ansi.Strip(got); plain != tt.want {
				t.Errorf("got %q, want %q", plain, tt.want)
			}
			if w := ansi.StringWidth(got); w != tt.width {
				t.Errorf("%d wide, want %d", w, tt.width)
			}
		})
	}
}

// TestFill checks a block is cut and padded to exactly its width and height.
func TestFill(t *testing.T) {
	tests := []struct {
		name   string
		in     string
		width  int
		height int
		want   string
	}{
		{name: "pads", in: "a\nbc", width: 3, height: 3, want: "a  \nbc \n   "},
		{name: "cuts lines", in: "abcdef", width: 4, height: 1, want: "abc…"},
		{name: "cuts rows", in: "a\nb\nc", width: 1, height: 2, want: "a\nb"},
		{name: "wide", in: "日本語", width: 5, height: 1, want: "日本…"},
		{name: "wide padded", in: "日", width: 3, height: 1, want: "日 "},
		{name: "empty", in: "", width: 2, height: 2, want: "  \n  "},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := fill(tt.in, tt.width, tt.height)
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
			for i, line := range strings.Split(got, "\n") {
				if w := ansi.StringWidth(line); w != tt.width {
					t.Errorf("line %d is %d wide, want %d", i, w, tt.width)
				}
			}
		})
	}
}

// TestSelectable checks the page scrolls so the selected line is in sight, and only it is marked.
func TestSelectable(t *testing.T) {
	lines := make([]string, 20)
	for i := range lines {
		lines[i] = strconv.Itoa(i)
	}
	rows := []int{0, 2, 4, 10, 19}
	tests := []struct {
		name   string
		cursor int
		scroll int
		height int
		want   int
		first  string
	}{
		{name: "top", cursor: 0, scroll: 0, height: 5, want: 0, first: "0"},
		{name: "in sight", cursor: 2, scroll: 0, height: 5, want: 0, first: "0"},
		{name: "below", cursor: 3, scroll: 0, height: 5, want: 6, first: "6"},
		{name: "above", cursor: 1, scroll: 10, height: 5, want: 2, first: "2"},
		{name: "last", cursor: 4, scroll: 0, height: 5, want: 15, first: "15"},
		{name: "past the end", cursor: -1, scroll: 30, height: 5, want: 15, first: "15"},
		{name: "taller than the page", cursor: 4, scroll: 3, height: 40, want: 0, first: "0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			view, scroll := selectable(lines, rows, tt.cursor, tt.scroll, tt.height)
			if scroll != tt.want {
				t.Errorf("scroll %d, want %d", scroll, tt.want)
			}
			out := strings.Split(ansi.Strip(view), "\n")
			if got := strings.TrimSpace(strings.TrimPrefix(out[0], "▌")); got != tt.first {
				t.Errorf("first line %q, want %q", got, tt.first)
			}
			marked := 0
			for _, line := range out {
				if strings.HasPrefix(line, "▌") {
					marked++
				}
			}
			if want := min(1, tt.cursor+1); marked != want {
				t.Errorf("%d lines marked, want %d", marked, want)
			}
		})
	}
}

// TestMoveCursor checks the cursor moves by a row, a page, or to the ends, and stays on the rows.
func TestMoveCursor(t *testing.T) {
	tests := []struct {
		name    string
		key     tea.KeyPressMsg
		cursor  int
		n       int
		want    int
		handled bool
	}{
		{name: "down", key: tea.KeyPressMsg{Code: tea.KeyDown}, cursor: 3, n: 10, want: 4, handled: true},
		{name: "up at the top", key: tea.KeyPressMsg{Code: 'k', Text: "k"}, cursor: 0, n: 10, want: 0, handled: true},
		{name: "page down past the end", key: tea.KeyPressMsg{Code: tea.KeyPgDown}, cursor: 8, n: 10, want: 9, handled: true},
		{name: "page up past the start", key: tea.KeyPressMsg{Code: tea.KeyPgUp}, cursor: 2, n: 10, want: 0, handled: true},
		{name: "bottom", key: tea.KeyPressMsg{Code: 'G', Text: "G"}, cursor: 0, n: 10, want: 9, handled: true},
		{name: "home", key: tea.KeyPressMsg{Code: tea.KeyHome}, cursor: 7, n: 10, want: 0, handled: true},
		{name: "no rows", key: tea.KeyPressMsg{Code: tea.KeyEnd}, cursor: 0, n: 0, want: 0, handled: true},
		{name: "other key", key: tea.KeyPressMsg{Code: 'x', Text: "x"}, cursor: 4, n: 10, want: 4},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, handled := moveCursor(tt.key, keys.Keys, tt.cursor, tt.n, 5)
			if got != tt.want || handled != tt.handled {
				t.Errorf("got %d %t, want %d %t", got, handled, tt.want, tt.handled)
			}
		})
	}
}
