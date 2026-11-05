package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/kontrolplane/koldstart/internal/config"
	"github.com/kontrolplane/koldstart/internal/engine"
	"github.com/kontrolplane/koldstart/internal/state"
)

// TestRemoveConfirm checks the typed confirmation says nothing while the name is being typed, says
// what was typed is not the name once it cannot become it or enter was pressed on it, and says
// what enter does once it matches.
func TestRemoveConfirm(t *testing.T) {
	tests := []struct {
		name  string
		typed string
		enter bool
		want  string
		not   string
	}{
		{name: "nothing typed", want: "esc cancels", not: "that is not"},
		{name: "on the way", typed: "de", want: "esc cancels", not: "that is not"},
		{name: "on the way, enter", typed: "de", enter: true, want: "that is not dev"},
		{name: "another name", typed: "prod", want: "that is not dev"},
		{name: "the name", typed: "dev", want: "enter to remove orders/dev", not: "that is not"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFake(t)
			m := pressKeys(t, started(t, f), "ctrl+d")
			m = typeText(t, m, tt.typed)
			if tt.enter {
				m = pressKeys(t, m, "enter")
			}
			v := screen(m)
			if m.page != removePage || f.removes != 0 {
				t.Fatalf("page %v, %d removes; want the confirmation still open", m.page, f.removes)
			}
			if !strings.Contains(v, tt.want) || (tt.not != "" && strings.Contains(v, tt.not)) {
				t.Errorf("typed %q: want %q, not %q:\n%s", tt.typed, tt.want, tt.not, v)
			}
		})
	}
}

// TestRemoveConfirmTypedAgain checks a wrong name marked on enter is no longer marked once it is
// typed on, and esc cancels the remove without quitting.
func TestRemoveConfirmTypedAgain(t *testing.T) {
	f := newFake(t)
	m := pressKeys(t, started(t, f), "ctrl+d")
	m = typeText(t, m, "d")
	m = pressKeys(t, m, "enter")
	if !m.state.remove.confirm.wrong() {
		t.Fatal("enter on a part of the name did not mark it")
	}
	m = typeText(t, m, "e")
	if m.state.remove.confirm.wrong() {
		t.Error("typing on did not clear the mark")
	}
	m, cmd := update(t, m, press("esc"))
	m = settle(t, m, cmd)
	if quits(cmd) || m.page != stagePage || f.removes != 0 {
		t.Errorf("esc: quits %v, page %v, %d removes", quits(cmd), m.page, f.removes)
	}
}

// TestUnlockWithoutDetails checks a lock that does not say what took it, or where, reads without
// gaps.
func TestUnlockWithoutDetails(t *testing.T) {
	f := newFake(t)
	f.locks[ordersDev] = &state.LockInfo{Who: "ci"}
	v := screen(pressKeys(t, started(t, f), "ctrl+x"))
	if !strings.Contains(v, "held by ci ") || strings.Contains(v, "for a  from") || !strings.Contains(v, "since an unknown time") {
		t.Errorf("a lock without details:\n%s", v)
	}
}

// TestDialogCentred checks every dialog sits in the middle of the content area, at the smallest
// terminal the layout allows and larger.
func TestDialogCentred(t *testing.T) {
	sizes := [][2]int{{78, 22}, {100, 30}, {200, 60}}
	for _, size := range sizes {
		f := newFake(t)
		f.locks[ordersDev] = &state.LockInfo{Who: "ci", Where: "github", Operation: "deploy", Since: time.Now()}
		m := started(t, f)
		m, _ = update(t, m, tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		failed := m
		failed.error = strings.Repeat("the deploy failed ", 40)
		cards := map[string]model{
			"remove": pressKeys(t, m, "ctrl+d"),
			"unlock": pressKeys(t, m, "ctrl+x"),
			"error":  failed,
		}
		for name, d := range cards {
			lines := strings.Split(ansi.Strip(d.content()), "\n")
			top, bottom, left, right := -1, -1, -1, -1
			for i, line := range lines {
				if before, _, ok := strings.Cut(line, "╭"); ok && top < 0 {
					top, left = i, ansi.StringWidth(before)
					right = contentWidth - ansi.StringWidth(line[:strings.LastIndex(line, "╮")]) - 1
				}
				if strings.Contains(line, "╰") {
					bottom = len(lines) - 1 - i
				}
			}
			if top < 0 || bottom < 0 {
				t.Errorf("%dx%d %s: no dialog in sight", size[0], size[1], name)
				continue
			}
			if left-right > 1 || right-left > 1 || top-bottom > 1 || bottom-top > 1 {
				t.Errorf("%dx%d %s: %d left, %d right, %d above, %d below", size[0], size[1], name, left, right, top, bottom)
			}
		}
	}
}

// TestJSONLines checks koldstart's own values show in parentheses by what they stand for, and
// what json would escape for html is written as it is.
func TestJSONLines(t *testing.T) {
	tests := []struct {
		name  string
		value any
		want  string
	}{
		{name: "secret", value: engine.SecretValue(config.Secret{Source: "ssm", Name: "/orders/key"}), want: `"v": (secret ssm:/orders/key)`},
		{name: "ref", value: engine.Ref("resource/table", "Arn"), want: `"v": (ref resource/table.Arn)`},
		{name: "join", value: engine.Join(engine.Ref("resource/table", "Arn"), "/index/*"), want: `"v": (${ref resource/table.Arn}/index/*)`},
		{name: "output", value: engine.StageOutput(config.OutputRef{App: "billing", Stage: "prod", Output: "url"}), want: `"v": (output billing/prod.url)`},
		{name: "html", value: "a<b&c>d", want: `"v": "a<b&c>d"`},
		{name: "nested in a list", value: []any{engine.Ref("role/api", "Arn")}, want: `(ref role/api.Arn)`},
		{name: "a string that looks like one", value: "\ue000x\ue001", want: `"v": "x"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := strings.Join(jsonLines(map[string]any{"v": tt.value}), "\n")
			if !strings.Contains(got, tt.want) {
				t.Errorf("jsonLines:\n%s\nwant %q in it", got, tt.want)
			}
		})
	}
}

// TestWrapIndented checks a long line of json wraps within the width, its continuation lines
// indented past its own.
func TestWrapIndented(t *testing.T) {
	tests := []struct {
		name  string
		line  string
		width int
		want  []string
	}{
		{name: "fits", line: `  "a": 1`, width: 20, want: []string{`  "a": 1`}},
		{name: "wraps at a space", line: `  "a": "one two three"`, width: 16, want: []string{`  "a": "one two`, `    three"`}},
		{name: "a word too long", line: `"abcdefghij"`, width: 6, want: []string{`"abcde`, `  fghi`, `  j"`}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := wrapIndented(tt.line, tt.width)
			if strings.Join(got, "\n") != strings.Join(tt.want, "\n") {
				t.Errorf("wrapIndented = %q, want %q", got, tt.want)
			}
			for _, l := range got {
				if ansi.StringWidth(l) > tt.width {
					t.Errorf("%q is wider than %d", l, tt.width)
				}
			}
		})
	}
}

// TestDiffScrollStops checks scrolling past the end of a diff stops there, so the first key back
// up moves it.
func TestDiffScrollStops(t *testing.T) {
	before, after := map[string]any{}, map[string]any{}
	for i := range 100 {
		after[strings.Repeat("k", i%7+1)+string(rune('a'+i%26))+string(rune('a'+i/26))] = i
	}
	m := started(t, newFake(t))
	m, _ = m.openDiff("function/api", "update", before, after)
	m = pressKeys(t, m, "G", "j", "j", "j")
	end := m.state.diff.scroll
	if want := len(m.diffLines()) - contentHeight; end != want {
		t.Fatalf("scrolled to %d, want the end at %d", end, want)
	}
	if m = pressKeys(t, m, "k"); m.state.diff.scroll != end-1 {
		t.Errorf("k at the end: scroll %d, want %d", m.state.diff.scroll, end-1)
	}
}
