package tui

import (
	"math/rand/v2"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// fuzzStormKeys are the keys a resize storm presses between sizes: the ones that move, page, switch
// tabs, filter, open a row and the help, and go back.
var fuzzStormKeys = []string{"j", "k", "down", "pgdown", "pgup", "g", "G", "end", "tab", "shift+tab", "/", "a", "backspace", "enter", "esc", "?", "f", "h", "l"}

// fuzzStorm sends n window sizes to a page, 0 by 0 and 1 by 1 among them, each with up to two keys
// after it, and checks after every message that the view fits the terminal.
func fuzzStorm(t *testing.T, name string, m model, n int, seed uint64) {
	t.Helper()
	rng := rand.New(rand.NewPCG(seed, uint64(len(name))))
	sizes := [][2]int{{0, 0}, {1, 1}, {1, 0}, {0, 1}, {minContentWidth + chromeWidth - 1, minContentHeight + chromeHeight}, {minContentWidth + chromeWidth, minContentHeight + chromeHeight}}
	check := func(what string) {
		t.Helper()
		v := m.render()
		if m.width <= 0 || m.height <= 0 {
			return
		}
		lines := strings.Split(v, "\n")
		if len(lines) > m.height {
			t.Fatalf("%s, %s at %dx%d: %d lines", name, what, m.width, m.height, len(lines))
		}
		for i, line := range lines {
			if w := ansi.StringWidth(line); w > m.width {
				t.Fatalf("%s, %s at %dx%d: line %d is %d wide", name, what, m.width, m.height, i, w)
			}
		}
	}
	for i := range n {
		size := [2]int{rng.IntN(260), rng.IntN(90)}
		if i%10 == 0 {
			size = sizes[rng.IntN(len(sizes))]
		}
		m, _ = perfUpdate(m, tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		check("resized")
		for range rng.IntN(3) {
			k := fuzzStormKeys[rng.IntN(len(fuzzStormKeys))]
			m, _ = perfUpdate(m, perfPress(k))
			check("after " + k)
		}
	}
}

// TestFuzzResizeStorm sends every page 200 window sizes between keys, with long and wide values
// in the state, and a few pages of the big fixture 50, a tenth of that under the race detector,
// and checks nothing panics and every view fits the terminal it is drawn in.
func TestFuzzResizeStorm(t *testing.T) {
	defer setLayout(120+chromeWidth, 30+chromeHeight)
	for name, m := range layoutPages(t, 120, 40) {
		fuzzStorm(t, name, m, fuzzStormSizes, 1)
	}
	if testing.Short() {
		return
	}
	big := perfPages(t, 200, 60)
	for _, name := range []string{"stage resources", "logs", "invoke", "plan", "plan diff"} {
		fuzzStorm(t, "big "+name, big[name], fuzzBigStormSizes, 2)
	}
}
