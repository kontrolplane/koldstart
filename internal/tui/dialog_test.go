package tui

import (
	"fmt"
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

// TestUnlockSays checks the unlock dialog says who holds the lock and what to make sure has
// stopped, by its operation, and a lock that does not say what took it, or where, reads without
// gaps.
func TestUnlockSays(t *testing.T) {
	tests := []struct {
		name string
		lock state.LockInfo
		want []string
	}{
		{name: "a deploy", lock: state.LockInfo{Who: "ci", Where: "github", Operation: "deploy", Since: time.Now()},
			want: []string{"ci on github has been running deploy since", "make sure that deploy has stopped"}},
		{name: "a remove", lock: state.LockInfo{Who: "levi", Operation: "remove", Since: time.Now()},
			want: []string{"make sure that remove has stopped"}},
		{name: "without details", lock: state.LockInfo{Who: "ci"},
			want: []string{"ci has held it, since a time it did not record", "make sure what took it has stopped"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFake(t)
			f.locks[ordersDev] = &tt.lock
			v := screen(pressKeys(t, started(t, f), "ctrl+x"))
			for _, want := range tt.want {
				if !strings.Contains(v, want) {
					t.Errorf("want %q in:\n%s", want, v)
				}
			}
		})
	}
}

// TestRemoveSays checks the remove dialog counts what it deletes and what it leaves in place,
// and says when it deletes nothing.
func TestRemoveSays(t *testing.T) {
	tests := []struct {
		name    string
		actions []engine.Action
		want    []string
		not     string
	}{
		{name: "deletes and keeps", actions: []engine.Action{engine.Delete, engine.Delete, engine.Keep},
			want: []string{"deletes 2 resources of orders/dev in eu-west-1", "and leaves 1 resource that holds data in place"}},
		{name: "deletes only", actions: []engine.Action{engine.Delete}, want: []string{"deletes 1 resource of"}, not: "leaves"},
		{name: "keeps only", actions: []engine.Action{engine.Keep, engine.Keep},
			want: []string{"deletes nothing of orders/dev", "and leaves 2 resources that hold data in place"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := pressKeys(t, started(t, newFake(t)), "ctrl+d")
			plan := &engine.Plan{App: "orders", Stage: "dev"}
			for i, a := range tt.actions {
				plan.Changes = append(plan.Changes, engine.Change{ID: fmt.Sprintf("resource/r%d", i), Action: a})
			}
			m.state.plan.plan = plan
			v := ansi.Strip(m.removeView())
			for _, want := range tt.want {
				if !strings.Contains(v, want) {
					t.Errorf("want %q in:\n%s", want, v)
				}
			}
			if tt.not != "" && strings.Contains(v, tt.not) {
				t.Errorf("want no %q in:\n%s", tt.not, v)
			}
		})
	}
}

// TestDialogKeepsTheAnswer checks a dialog too tall for the terminal cuts its longest block,
// marking the cut, and keeps its heading and the answer below: the buttons, what enter does, or
// what closes it.
func TestDialogKeepsTheAnswer(t *testing.T) {
	long := strings.Repeat("very-long-name-", 12)
	tests := []struct {
		name string
		open func(*testing.T, model) model
		want []string
	}{
		{name: "unlock", want: []string{"release the lock of orders/dev?", "make sure that deploy has stopped", "yes", "…"},
			open: func(t *testing.T, m model) model { return pressKeys(t, m, "ctrl+x") }},
		{name: "remove", want: []string{"remove orders/dev?", "type dev to confirm", "esc cancels"},
			open: func(t *testing.T, m model) model { return pressKeys(t, m, "ctrl+d") }},
		{name: "error", want: []string{"error", "press any key to close", "…"},
			open: func(_ *testing.T, m model) model {
				m.error = strings.Repeat("the deploy failed ", 80)
				return m
			}},
	}
	for _, size := range [][2]int{{78, 22}, {80, 24}} {
		for _, tt := range tests {
			t.Run(fmt.Sprintf("%s %dx%d", tt.name, size[0], size[1]), func(t *testing.T) {
				f := newFake(t)
				f.locks[ordersDev] = &state.LockInfo{Who: long, Where: long, Operation: "deploy", Since: time.Now()}
				m := started(t, f)
				m, _ = update(t, m, tea.WindowSizeMsg{Width: size[0], Height: size[1]})
				v := screen(tt.open(t, m))
				for _, want := range tt.want {
					if !strings.Contains(v, want) {
						t.Errorf("want %q in:\n%s", want, v)
					}
				}
			})
		}
	}
}

// TestWrapEven checks a line too wide wraps into as many lines as lipgloss wraps it into, none
// wider than the width, and none much shorter than the others, so no word is left on its own.
func TestWrapEven(t *testing.T) {
	tests := []struct {
		name  string
		line  string
		width int
		lines int
	}{
		{name: "fits", line: "release the lock", width: 20, lines: 1},
		{name: "a word over", line: "make sure that deploy is really gone: two deploys at once can leave its state wrong.", width: 80, lines: 2},
		{name: "three lines", line: strings.Repeat("the deploy failed ", 10), width: 70, lines: 3},
		{name: "no spaces", line: strings.Repeat("x", 100), width: 60, lines: 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := wrapEven(tt.line, tt.width)
			if len(got) != tt.lines {
				t.Fatalf("wrapEven = %q, want %d lines", got, tt.lines)
			}
			widest, narrowest := 0, tt.width
			for _, l := range got {
				w := ansi.StringWidth(strings.TrimSpace(l))
				widest, narrowest = max(widest, w), min(narrowest, w)
			}
			if widest > tt.width || (len(got) > 1 && narrowest < widest/2) {
				t.Errorf("wrapEven = %q: lines from %d to %d wide, in %d", got, narrowest, widest, tt.width)
			}
		})
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
