package tui

import (
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/kontrolplane/koldstart/internal/cli"
	"github.com/kontrolplane/koldstart/internal/state"
	"github.com/kontrolplane/koldstart/internal/tui/styles"
)

// navFake is the fake with more to move through: a second function, a resource with a long list
// of attributes, a lock, logs of three invocations and 200 more lines, and a plan whose change
// has a long diff.
func navFake(t *testing.T) *fakeBackend {
	t.Helper()
	f := newFake(t)
	for _, ref := range []cli.StageRef{ordersDev, ordersProd} {
		st := f.states[ref]
		worker := *st.Resources["function/api"]
		st.Resources["function/worker"] = &worker
		attrs := map[string]string{}
		for i := range 60 {
			attrs["Attribute"+formatInt(100+i)] = "value"
		}
		st.Resources["resource/table"].Attributes = attrs
	}
	f.locks[ordersDev] = &state.LockInfo{Who: "ci", Where: "github", Operation: "deploy", Since: time.Now().Add(-time.Hour)}
	f.logs = append(requestLogs(time.Now().Add(-time.Minute)), logSeries(time.Now().Add(-50*time.Second), 200, "line")...)
	before, after := map[string]any{}, map[string]any{}
	for i := range 60 {
		before["key"+formatInt(100+i)] = i
		after["key"+formatInt(100+i)] = i + 1
	}
	f.plan.Changes[0].Before, f.plan.Changes[0].After = before, after
	return f
}

// navFoot is the bottom edge of the frame on screen, where the position is set.
func navFoot(m model) string {
	for _, line := range slices.Backward(strings.Split(screen(m), "\n")) {
		if strings.Contains(line, "╰") {
			return line
		}
	}
	return ""
}

// TestNavPosition checks every page that scrolls says in the frame's foot which lines are in
// sight, from the first at the top to the last at the bottom, and every list which row is chosen.
func TestNavPosition(t *testing.T) {
	long := map[string]bool{"invoke": true}
	tests := []struct {
		name   string
		reach  []string
		top    string
		bottom []string
		end    string
	}{
		{name: "function overview", reach: []string{"enter"}, top: "lines 1–", bottom: []string{"G"}},
		{name: "logs", reach: []string{"enter", "tab"}, top: "lines 1–", bottom: []string{"G"}, end: "of 212"},
		{name: "invocation", reach: []string{"enter", "tab", "tab", "G", "enter"}, top: "lines 1–", bottom: []string{"G"}},
		{name: "invoke", reach: []string{"enter", "shift+tab", "i"}, top: "lines 1–", bottom: slices.Repeat([]string{"pgdown"}, 30)},
		{name: "resource", reach: []string{"tab", "G", "enter"}, top: "lines 1–", bottom: []string{"G"}},
		{name: "diff", reach: []string{"d", "enter"}, top: "lines 1–", bottom: []string{"G"}},
		{name: "plan", reach: []string{"d"}, top: "1 of 2", bottom: []string{"G"}, end: "2 of 2"},
		{name: "version", reach: []string{"shift+tab", "j", "enter"}, top: "1 of 3", bottom: []string{"G"}, end: "3 of 3"},
		{name: "stage", top: "1 of 2", bottom: []string{"G"}, end: "2 of 2"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := navFake(t)
			if long[tt.name] {
				f.result = cli.Invocation{Payload: []byte(`[` + strings.Repeat(`{"a":1},`, 80) + `{"a":2}]`)}
			}
			if tt.name == "invocation" {
				f.logs = navLongRequest()
			}
			m := pressKeys(t, started(t, f), tt.reach...)
			m, _ = update(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
			if tt.name == "logs" {
				m = pressKeys(t, m, "g")
			}
			if foot := navFoot(m); !strings.Contains(foot, tt.top) {
				t.Errorf("at the top the foot is %q, want %q in it", foot, tt.top)
			}
			m = pressKeys(t, m, tt.bottom...)
			end := tt.end
			if end == "" {
				p := m.position()
				_, total, _ := strings.Cut(p, " of ")
				_, last, _ := strings.Cut(strings.TrimPrefix(p, "lines "), "–")
				last, _, _ = strings.Cut(last, " ")
				if total == "" || last != total {
					t.Errorf("at the bottom the position is %q, want it to end on the last line", p)
				}
				end = " of " + total
			}
			if foot := navFoot(m); !strings.Contains(foot, end) {
				t.Errorf("at the bottom the foot is %q, want %q in it", foot, end)
			}
		})
	}
}

// navLongRequest is the log of one invocation that wrote 80 lines.
func navLongRequest() []cli.LogEvent {
	at := time.Now().Add(-time.Minute)
	id := "44444444-4444-4444-4444-444444444444"
	out := []cli.LogEvent{{Time: at, Message: "START RequestId: " + id + " Version: $LATEST"}}
	for i := range 80 {
		out = append(out, cli.LogEvent{Time: at.Add(time.Duration(i+1) * time.Millisecond), Message: "2026-11-05T10:00:00.001Z\t" + id + "\tINFO\tstep " + formatInt(i)})
	}
	return append(out,
		cli.LogEvent{Time: at.Add(time.Second), Message: "END RequestId: " + id},
		cli.LogEvent{Time: at.Add(time.Second), Message: "REPORT RequestId: " + id + "\tDuration: 5.00 ms\tBilled Duration: 5 ms\tMemory Size: 256 MB\tMax Memory Used: 71 MB"},
	)
}

// TestScrollCue checks the cue names the lines in sight, and says nothing when they all are.
func TestScrollCue(t *testing.T) {
	tests := []struct {
		name                string
		first, shown, total int
		want                string
	}{
		{name: "all in sight", first: 0, shown: 30, total: 12, want: ""},
		{name: "exactly", first: 0, shown: 12, total: 12, want: ""},
		{name: "top", first: 0, shown: 10, total: 230, want: "lines 1–10 of 230"},
		{name: "middle", first: 1199, shown: 30, total: 2400, want: "lines 1,200–1,229 of 2,400"},
		{name: "bottom, short", first: 225, shown: 10, total: 230, want: "lines 226–230 of 230"},
		{name: "none", first: 0, shown: 10, total: 0, want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := scrollCue(tt.first, tt.shown, tt.total); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

// navGround is the escape that paints the ground of the row under the cursor.
func navGround() string {
	seq, _, _ := strings.Cut(pad(1, styles.P.Selection), " ")
	return seq
}

// TestNavPickHighlight checks the chosen change of a plan and of a deploy is painted as a table
// paints the row under its cursor, the others not, and moves with the cursor.
func TestNavPickHighlight(t *testing.T) {
	tests := []struct {
		name          string
		reach         []string
		first, second string
	}{
		{name: "plan", reach: []string{"d"}, first: "function/api", second: "resource/table"},
		{name: "version", reach: []string{"shift+tab", "j", "enter"}, first: "function/api", second: "loggroup/api"},
	}
	ground := navGround()
	lineWith := func(m model, id string) string {
		for line := range strings.SplitSeq(m.pageView(), "\n") {
			if strings.Contains(ansi.Strip(line), " "+id) {
				return line
			}
		}
		return ""
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := pressKeys(t, started(t, navFake(t)), tt.reach...)
			if a, b := lineWith(m, tt.first), lineWith(m, tt.second); !strings.Contains(a, ground) || strings.Contains(b, ground) {
				t.Fatalf("the first change is not the one painted:\n%q\n%q", a, b)
			}
			m = pressKeys(t, m, "j")
			if a, b := lineWith(m, tt.first), lineWith(m, tt.second); strings.Contains(a, ground) || !strings.Contains(b, ground) {
				t.Errorf("after j the second change is not the one painted:\n%q\n%q", a, b)
			}
			if !strings.Contains(navFoot(m), "2 of ") {
				t.Errorf("the foot does not say which change is chosen: %q", navFoot(m))
			}
		})
	}
}

