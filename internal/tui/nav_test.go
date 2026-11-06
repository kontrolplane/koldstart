package tui

import (
	"cmp"
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

// navSnapshot is what a page is left with: the page, what it draws, its filter and where it is.
// How many seconds ago something was is left out, as it can tick between two snapshots.
func navSnapshot(m model) string {
	meta, _ := m.frameMeta()
	snap := strings.Join([]string{formatInt(int(m.page)), ansi.Strip(meta), m.position(), ansi.Strip(m.pageView())}, "\n")
	return themeSeconds.ReplaceAllString(snap, "Ns ago")
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

// navTop is the top edge of the frame on screen, where the breadcrumb is set.
func navTop(m model) string {
	for line := range strings.SplitSeq(screen(m), "\n") {
		if strings.Contains(line, "╭") {
			return line
		}
	}
	return ""
}

// TestNavBack checks q and esc go back from every page and dialog to the page it was opened from,
// with its cursor, tab, filter and scroll as they were left. Each case presses reach from the
// project's stage to get to the page left, prep there to move on it, and into to open the page
// gone back from.
func TestNavBack(t *testing.T) {
	tests := []struct {
		name              string
		reach, prep, into []string
		inside            page
		escOnly           bool
	}{
		{name: "overview from a stage", reach: []string{"q"}, prep: []string{"/", "o", "r", "d", "enter", "j"}, into: []string{"enter"}, inside: stagePage},
		{name: "stage from a function", prep: []string{"j"}, into: []string{"enter"}, inside: functionPage},
		{name: "stage resources from a function", prep: []string{"tab", "j"}, into: []string{"enter"}, inside: functionPage},
		{name: "stage from a resource", prep: []string{"tab", "G"}, into: []string{"enter"}, inside: resourcePage},
		{name: "function from an invocation", reach: []string{"enter"}, prep: []string{"tab", "tab", "j"}, into: []string{"enter"}, inside: invocationPage},
		{name: "function logs from a plan", reach: []string{"enter"}, prep: []string{"tab", "/", "l", "i", "n", "e", "enter", "k", "k"}, into: []string{"d"}, inside: planPage},
		{name: "function overview from a plan", reach: []string{"enter"}, prep: []string{"j", "j"}, into: []string{"d"}, inside: planPage},
		{name: "function overview from its log group", reach: []string{"enter"}, prep: slices.Repeat([]string{"j"}, 6), into: []string{"enter"}, inside: resourcePage},
		{name: "history from a version", prep: []string{"shift+tab", "j"}, into: []string{"enter"}, inside: versionPage},
		{name: "version from a diff", reach: []string{"shift+tab", "j", "enter"}, prep: []string{"j", "j"}, into: []string{"enter"}, inside: diffPage},
		{name: "plan from a diff", reach: []string{"d"}, prep: []string{"j", "k"}, into: []string{"enter"}, inside: diffPage},
		{name: "plan from a create's inputs", reach: []string{"d"}, prep: []string{"j", "j"}, into: []string{"enter"}, inside: inputsPage},
		{name: "stage from a plan", prep: []string{"tab", "j"}, into: []string{"d"}, inside: planPage},
		{name: "overview from a plan", reach: []string{"q"}, prep: []string{"j", "k"}, into: []string{"d"}, inside: planPage},
		{name: "stage from the unlock dialog", prep: []string{"tab", "j"}, into: []string{"ctrl+x"}, inside: unlockPage},
		{name: "overview from the unlock dialog", reach: []string{"q"}, into: []string{"ctrl+x"}, inside: unlockPage},
		{name: "stage from the remove dialog", prep: []string{"tab", "j"}, into: []string{"ctrl+d"}, inside: removePage, escOnly: true},
		{name: "overview from the remove dialog", reach: []string{"q"}, prep: []string{"j"}, into: []string{"ctrl+d"}, inside: removePage, escOnly: true},
	}
	for _, tt := range tests {
		for _, back := range []string{"q", "esc"} {
			if back == "q" && tt.escOnly {
				continue
			}
			t.Run(tt.name+" by "+back, func(t *testing.T) {
				m := pressKeys(t, started(t, navFake(t)), tt.reach...)
				m = pressKeys(t, m, tt.prep...)
				left := navSnapshot(m)
				m = pressKeys(t, m, tt.into...)
				if m.page != tt.inside {
					t.Fatalf("page %v after %v, want %v", m.page, tt.into, tt.inside)
				}
				m = pressKeys(t, m, back)
				if got := navSnapshot(m); got != left {
					t.Errorf("came back to\n%s\nwant\n%s", got, left)
				}
			})
		}
	}
}

// TestNavRunLocalBack checks the dialog before running a function of prod here goes back to the
// function as it was, on q, esc and n.
func TestNavRunLocalBack(t *testing.T) {
	for _, back := range []string{"q", "esc", "n"} {
		t.Run(back, func(t *testing.T) {
			m := started(t, navFake(t))
			m, cmd := m.openStage(ordersProd)
			m = pressKeys(t, settle(t, m, cmd), "enter", "tab", "tab", "tab")
			left := navSnapshot(m)
			m = pressKeys(t, m, "I")
			if m.page != runLocalPage {
				t.Fatalf("page %v, want the dialog", m.page)
			}
			if m = pressKeys(t, m, back); navSnapshot(m) != left {
				t.Errorf("came back to\n%s\nwant\n%s", navSnapshot(m), left)
			}
		})
	}
}

// TestNavBackToTheStageLeft checks leaving the stage opened on start puts the overview's cursor
// on it.
func TestNavBackToTheStageLeft(t *testing.T) {
	m := pressKeys(t, started(t, navFake(t)), "q")
	if s, ok := m.selectedStage(); !ok || s.StageRef != ordersDev {
		t.Errorf("the cursor is on %v, want %v", s.StageRef, ordersDev)
	}
}

// TestNavFilter checks the filter works alike on the overview, every tab of a stage and the
// logs: letters typed into it do nothing else, backspace empties it, the frame shows it while it
// is typed or holds text, enter keeps it and esc clears it.
func TestNavFilter(t *testing.T) {
	tests := []struct {
		name  string
		reach []string
		rows  func(m model) int
	}{
		{name: "overview", reach: []string{"q"}, rows: func(m model) int { return len(m.state.overview.shown) }},
		{name: "functions", rows: func(m model) int { return len(m.state.stage.functions) }},
		{name: "resources", reach: []string{"tab"}, rows: func(m model) int { return len(m.state.stage.resources) }},
		{name: "outputs", reach: []string{"tab", "tab"}, rows: func(m model) int { return len(m.state.stage.outputs) }},
		{name: "history", reach: []string{"shift+tab"}, rows: func(m model) int { return len(m.state.stage.shownDeploys) }},
		{name: "logs", reach: []string{"enter", "tab"}, rows: func(m model) int { return len(navAllRows(m)) }},
	}
	const typed = "dqrpfiIecyn?xjkgG"
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := navFake(t)
			m := pressKeys(t, started(t, f), tt.reach...)
			page, all := m.page, tt.rows(m)
			m = pressKeys(t, m, "/")
			if !m.typing() || !strings.Contains(navTop(m), "filter") {
				t.Fatalf("/: typing %t, top %q", m.typing(), navTop(m))
			}
			for _, r := range typed {
				var cmd tea.Cmd
				m, cmd = update(t, m, tea.KeyPressMsg{Code: r, Text: string(r)})
				if quits(cmd) {
					t.Fatalf("%q quit", r)
				}
			}
			if m.page != page || m.showHelp || m.paused || len(f.deploys)+len(f.invokes)+f.local+len(f.unlocks) != 0 || m.status != "" {
				t.Fatalf("a letter did something: page %v, help %t, paused %t, status %q", m.page, m.showHelp, m.paused, m.status)
			}
			if got := m.pageFilter().text; got != typed {
				t.Errorf("filter %q, want %q", got, typed)
			}
			if !strings.Contains(navTop(m), "filter "+typed[:8]) {
				t.Errorf("the frame does not show the filter: %q", navTop(m))
			}
			for range typed {
				m = pressKeys(t, m, "backspace")
			}
			if !m.typing() || m.pageFilter().text != "" || tt.rows(m) != all {
				t.Fatalf("backspace to empty: typing %t, filter %q, %d rows of %d", m.typing(), m.pageFilter().text, tt.rows(m), all)
			}
			m = pressKeys(t, m, "z", "enter")
			if m.typing() || m.pageFilter().text != "z" || !strings.Contains(navTop(m), "filter z") {
				t.Fatalf("enter: typing %t, filter %q, top %q", m.typing(), m.pageFilter().text, navTop(m))
			}
			m = pressKeys(t, m, "/", "esc")
			if m.typing() || m.pageFilter().text != "" || strings.Contains(navTop(m), "filter") || tt.rows(m) != all || m.page != page {
				t.Errorf("esc: typing %t, filter %q, %d rows of %d, page %v", m.typing(), m.pageFilter().text, tt.rows(m), all, m.page)
			}
		})
	}
}

// navAllRows is every drawn row of the logs the filter keeps.
func navAllRows(m model) []logRow {
	rows, _ := m.lastRows(1 << 30)
	return rows
}

// navPages are the interface on every page, by name, with its first loads in.
func navPages(t *testing.T) map[string]model {
	t.Helper()
	m := started(t, navFake(t))
	fn := pressKeys(t, m, "enter")
	prod, cmd := m.openStage(ordersProd)
	prod = settle(t, prod, cmd)
	return map[string]model{
		"overview":    pressKeys(t, m, "q"),
		"stage":       m,
		"function":    fn,
		"logs":        pressKeys(t, fn, "tab"),
		"invocations": pressKeys(t, fn, "tab", "tab"),
		"invoke":      pressKeys(t, fn, "shift+tab"),
		"invocation":  pressKeys(t, fn, "tab", "tab", "enter"),
		"resource":    pressKeys(t, m, "tab", "G", "enter"),
		"version":     pressKeys(t, m, "shift+tab", "enter"),
		"diff":        pressKeys(t, m, "d", "enter"),
		"plan":        pressKeys(t, m, "d"),
		"unlock":      pressKeys(t, m, "ctrl+x"),
		"run here":    pressKeys(t, prod, "enter", "I"),
	}
}

// TestNavHelp checks ? opens the key reference on every page, any key closes it and leaves the
// page as it was, and ctrl+c still quits while it is open. On the remove dialog ? is typed.
func TestNavHelp(t *testing.T) {
	for name, m := range navPages(t) {
		t.Run(name, func(t *testing.T) {
			left := navSnapshot(m)
			open := pressKeys(t, m, "?")
			if !open.showHelp || !strings.Contains(screen(open), "everywhere") {
				t.Fatalf("? did not open the help:\n%s", screen(open))
			}
			if _, cmd := update(t, open, press("ctrl+c")); !quits(cmd) {
				t.Error("ctrl+c with the help open does not quit")
			}
			for _, k := range []string{"x", "q", "esc", "enter", "j"} {
				closed := pressKeys(t, open, k)
				if closed.showHelp || navSnapshot(closed) != left {
					t.Errorf("%s: help %t, page\n%s\nwant\n%s", k, closed.showHelp, navSnapshot(closed), left)
				}
			}
		})
	}
	m := pressKeys(t, started(t, navFake(t)), "ctrl+d", "?")
	if m.showHelp || m.page != removePage || !strings.Contains(m.state.remove.confirm.input.Value(), "?") {
		t.Errorf("? on the remove dialog: help %t, page %v, typed %q", m.showHelp, m.page, m.state.remove.confirm.input.Value())
	}
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
		{name: "invoke", reach: []string{"enter", "shift+tab", "i"}, top: "lines 1–", bottom: slices.Repeat([]string{"pgdown"}, 60)},
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
				p, _, _ := strings.Cut(m.position(), " · ")
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
// paints the row under its cursor, the others not, and moves with the cursor, past the heading of
// a plan's group.
func TestNavPickHighlight(t *testing.T) {
	tests := []struct {
		name          string
		reach         []string
		next          []string
		first, second string
	}{
		{name: "plan", reach: []string{"d"}, next: []string{"j", "j"}, first: "function/api", second: "resource/table"},
		{name: "version", reach: []string{"shift+tab", "j", "enter"}, next: []string{"j"}, first: "function/api", second: "loggroup/api"},
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
			m = pressKeys(t, m, tt.next...)
			if a, b := lineWith(m, tt.first), lineWith(m, tt.second); strings.Contains(a, ground) || !strings.Contains(b, ground) {
				t.Errorf("after %v the second change is not the one painted:\n%q\n%q", tt.next, a, b)
			}
			if !strings.Contains(navFoot(m), "2 of ") {
				t.Errorf("the foot does not say which change is chosen: %q", navFoot(m))
			}
		})
	}
}

// TestNavBreadcrumb checks the frame names where every page sits, a diff and an invocation under
// the page they came from.
func TestNavBreadcrumb(t *testing.T) {
	pages := navPages(t)
	tests := []struct {
		page string
		want string
	}{
		{page: "overview", want: "╭─ stages ─"},
		{page: "stage", want: "stages › orders/dev › functions ─"},
		{page: "function", want: "stages › orders/dev › functions › api › overview ─"},
		{page: "logs", want: "stages › orders/dev › functions › api › logs ─"},
		{page: "invocation", want: "stages › orders/dev › functions › api › invocations › 33333333 ─"},
		{page: "resource", want: "stages › orders/dev › resources › resource/table ─"},
		{page: "plan", want: "stages › orders/dev › deploy ─"},
		{page: "diff", want: "stages › orders/dev › deploy › function/api ─"},
		{page: "unlock", want: "stages › orders/dev › unlock ─"},
		{page: "run here", want: "stages › orders/prod › functions › api › invoke ─"},
	}
	for _, tt := range tests {
		t.Run(tt.page, func(t *testing.T) {
			if top := navTop(pages[tt.page]); !strings.Contains(top, tt.want) {
				t.Errorf("top %q, want %q in it", top, tt.want)
			}
		})
	}
	version := pages["version"]
	if top := navTop(pressKeys(t, version, "enter")); !strings.Contains(top, "› history › ") || !strings.HasSuffix(strings.TrimRight(top, "─╮ "), "resource/table") {
		t.Errorf("a diff of a deploy does not sit under it: %q", top)
	}
}

// TestNavBreadcrumbNarrow checks a breadcrumb that does not fit loses its middle, keeping the
// first crumb and the page on screen whole: the crumbs between fold into an ellipsis, then the
// function is cut.
func TestNavBreadcrumbNarrow(t *testing.T) {
	long := strings.Repeat("checkout", 4)
	tests := []struct {
		name     string
		function string
		width    int
		want     string
	}{
		{name: "fits", function: "api", want: "stages › orders/dev › functions › api › logs"},
		{name: "middle folds", function: long, want: "stages › … › functions › " + long + " › logs"},
		{name: "function cut", function: long, width: 30, want: "stages › … › checkoutc… › logs"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := navFake(t)
			st := f.states[ordersDev]
			fn := *st.Resources["function/api"]
			st.Resources["function/"+tt.function] = &fn
			m := started(t, f)
			for i, name := range m.state.stage.functions {
				if name == tt.function {
					m.state.stage.tables[functionsTab].setCursor(i)
				}
			}
			m = pressKeys(t, m, "enter", "tab")
			m, _ = update(t, m, tea.WindowSizeMsg{Width: 78, Height: 24})
			width := cmp.Or(tt.width, titleRoom(""))
			if crumb := ansi.Strip(m.breadcrumb(width)); crumb != tt.want {
				t.Errorf("breadcrumb %q in %d, want %q", crumb, width, tt.want)
			}
			if !strings.Contains(navTop(m), "logs ─") {
				t.Errorf("the frame lost the page: %q", navTop(m))
			}
		})
	}
}

// TestNavFootRoom checks the frame's foot holds the follow state, the position, a stale read and
// the pause together: in full where there is room, and shortened, the stale read and the pause
// kept, on the narrowest terminal.
func TestNavFootRoom(t *testing.T) {
	tests := []struct {
		name  string
		width int
		want  []string
	}{
		{name: "wide", width: 160, want: []string{"follow off", "lines ", " of 212", "as of ", ", could not read again", "⏸ paused"}},
		{name: "narrowest", width: 78, want: []string{"follow off", " of 212", "as of ", "⏸ paused"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := pressKeys(t, started(t, navFake(t)), "enter", "tab", "pgup")
			m, _ = update(t, m, tea.WindowSizeMsg{Width: tt.width, Height: 24})
			m.stale, m.paused, m.refreshedAt = true, true, time.Now()
			foot := navFoot(m)
			for _, w := range tt.want {
				if !strings.Contains(foot, w) {
					t.Errorf("the foot %q has no %q", foot, w)
				}
			}
			if strings.Contains(foot, "…") {
				t.Errorf("the foot %q was cut", foot)
			}
		})
	}
}
