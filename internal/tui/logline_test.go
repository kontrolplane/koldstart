package tui

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/kontrolplane/koldstart/internal/cli"
)

// lgBig are lines of 50 kb: one of json, as a logger writes a large payload, and one of words.
func lgBig(at time.Time) []cli.LogEvent {
	return []cli.LogEvent{
		{Time: at.Add(20 * time.Millisecond), Stream: "c", Message: `{"level":"info","msg":"` + strings.Repeat("a long message, ", 3200) + `","order":{"id":1,"items":[1,2,3]}}`},
		{Time: at.Add(21 * time.Millisecond), Stream: "c", Message: strings.Repeat("a long line of words 漢字, ", 1800)},
	}
}

// lgOpen is the page of line i of the logs tab, counted from the first, opened from the tab.
func lgOpen(t *testing.T, m model, i int) model {
	t.Helper()
	m = pressKeys(t, m, "g")
	for range i {
		m = pressKeys(t, m, "j")
	}
	return pressKeys(t, m, "enter")
}

// TestLogLinePage checks the page of a line says when it was logged, in which stream, of which
// invocation and at what level, its message whole, and its json pretty-printed or the pairs of
// its logfmt, for the lines of node, python, powertools, pino, zap, slog and go.
func TestLogLinePage(t *testing.T) {
	tests := []struct {
		name string
		line int
		want []string
		not  []string
	}{
		{name: "node", line: 2, want: []string{"log line", "stream  a", "request  " + lgNodeID + " · ✓ ok  o opens it", "level  info", "message", "order placed"}, not: []string{"json ─", "INFO"}},
		{name: "node warning", line: 4, want: []string{"level  warn", "stock is low"}},
		{name: "python", line: 5, want: []string{"stream  b", "request  " + lgPythonID + " · ✗ failed", "level  error", "the card was declined"}},
		{name: "start", line: 0, want: []string{"level  none", "START RequestId: " + lgNodeID + " Version: $LATEST"}},
		{name: "report", line: 8, want: []string{"REPORT RequestId: " + lgNodeID + " Duration: 12.50 ms"}},
		{name: "powertools", line: 10, want: []string{"request  none in the logs read", "level  debug", "cache miss", "json ─", `"service": "orders"`, `"location": "handler:12"`}},
		{name: "pino", line: 11, want: []string{"level  error", "the database is down", `"pid": 1`, `"hostname": "lambda"`}},
		{name: "zap", line: 12, want: []string{"level  warn", "a slow query", `"caller": "orders/main.go:20"`, `"ts": 1762423200.012`}},
		{name: "slog text", line: 13, want: []string{"level  info", "listening for events", "fields ─", "port  8080", `msg  listening for events`}},
		{name: "go log", line: 14, want: []string{"level  none", "message ─", "connected to the queue"}, not: []string{"2026/11/06"}},
		{name: "go panic", line: 15, want: []string{"level  error", "panic: runtime error: index out of range [3] with length 3"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := lgOpen(t, lgOnLogs(t), tt.line)
			if m.page != logLinePage {
				t.Fatalf("enter did not open the line, the page is %d", m.page)
			}
			v := screen(m)
			for _, w := range tt.want {
				if !strings.Contains(v, w) {
					t.Errorf("the page does not say %q:\n%s", w, v)
				}
			}
			for _, w := range tt.not {
				if strings.Contains(v, w) {
					t.Errorf("the page says %q:\n%s", w, v)
				}
			}
		})
	}
}

// TestLogLineKeys checks n and p go to the next and the previous line the logs tab shows and say
// so at either end, the tab's cursor going with them, c copies the line as it was logged, o and
// enter open its invocation or say it has none, and esc goes back to the tab with the cursor on
// the line.
func TestLogLineKeys(t *testing.T) {
	tests := []struct {
		name   string
		line   int
		keys   []string
		page   page
		shown  string
		status string
		cursor string
	}{
		{name: "next", line: 2, keys: []string{"n"}, page: logLinePage, shown: "charging the card", cursor: "charging the card"},
		{name: "previous", line: 2, keys: []string{"p", "p"}, page: logLinePage, shown: "START RequestId: " + lgNodeID, cursor: "START RequestId: " + lgNodeID},
		{name: "the first", line: 0, keys: []string{"p"}, page: logLinePage, status: "this is the oldest line the logs tab shows: ↑ on its first line reads further back"},
		{name: "the newest", line: 15, keys: []string{"n"}, page: logLinePage, status: "this is the newest line the logs tab shows"},
		{name: "copy", line: 13, keys: []string{"c"}, page: logLinePage, status: "copied time=2026-11-06T10:00:00.013Z level=INFO msg=\"l…"},
		{name: "its invocation", line: 5, keys: []string{"o"}, page: invocationPage, shown: "invocation"},
		{name: "enter opens it too", line: 3, keys: []string{"enter"}, page: invocationPage, shown: lgPythonID},
		{name: "under the line", line: 5, keys: []string{"o"}, page: invocationPage, shown: "logs › line "},
		{name: "back from its invocation", line: 5, keys: []string{"o", "esc"}, page: logLinePage, shown: "the card was declined"},
		{name: "back from the next invocation", line: 5, keys: []string{"o", "n", "q"}, page: logLinePage, shown: "the card was declined"},
		{name: "none", line: 11, keys: []string{"o"}, page: logLinePage, status: "this line is of no invocation in the logs read"},
		{name: "back", line: 4, keys: []string{"n", "esc"}, page: functionPage, cursor: "the card was declined"},
		{name: "back with q", line: 4, keys: []string{"q"}, page: functionPage, cursor: "stock is low"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := pressKeys(t, lgOpen(t, lgOnLogs(t), tt.line), tt.keys...)
			if m.page != tt.page {
				t.Fatalf("the page is %d, want %d", m.page, tt.page)
			}
			if tt.shown != "" && !strings.Contains(screen(m), tt.shown) {
				t.Errorf("the page does not show %q:\n%s", tt.shown, screen(m))
			}
			if m.status != tt.status {
				t.Errorf("status %q, want %q", m.status, tt.status)
			}
			if tt.cursor != "" && !strings.Contains(lgCursor(m), tt.cursor) {
				t.Errorf("the cursor of the tab is on %q, want %q", lgCursor(m), tt.cursor)
			}
			if tt.page == invocationPage && m.state.invocation.req.id == "" {
				t.Error("the invocation page has no request")
			}
		})
	}
}

// TestLogLineJSON checks the json of a line is read in a json view in the order it was logged:
// its cursor moves, folds, searches and copies the value under it, its path, and on its top line
// the line as it was logged; n goes to the next match while a search holds text and to the next
// line once esc dropped it, p to the previous line all the same, and the frame shows the path.
func TestLogLineJSON(t *testing.T) {
	tests := []struct {
		name   string
		line   int
		keys   []string
		want   []string
		status string
		path   string
		page   page
		shown  string
	}{
		{name: "in order", line: 12, want: []string{"\"level\": \"warn\",", "\"ts\": 1762423200.012,", "\"caller\": \"orders/main.go:20\","}, path: ".", page: logLinePage},
		{name: "the top copies the line", line: 12, keys: []string{"c"}, status: "copied all of it", path: ".", page: logLinePage},
		{name: "a value", line: 12, keys: []string{"j", "j", "j", "c"}, status: "copied the value of .caller", path: ".caller", page: logLinePage},
		{name: "a path", line: 11, keys: []string{"j", "j", "j", "shift+c"}, status: "copied the path .pid", path: ".pid", page: logLinePage},
		{name: "fold", line: 10, keys: []string{"space"}, want: []string{"{ … 5 keys }"}, page: logLinePage},
		{name: "search", line: 12, keys: []string{"/", "s", "l", "o", "w", "enter"}, want: []string{"search slow · 1 of 1"}, path: ".msg", page: logLinePage},
		{name: "n goes to a match", line: 12, keys: []string{"/", "s", "enter", "n"}, want: []string{"search s · 2 of"}, page: logLinePage, shown: "a slow query"},
		{name: "n goes to the next line", line: 12, keys: []string{"/", "s", "enter", "esc", "n"}, page: logLinePage, shown: "listening for events"},
		{name: "p goes to the previous line", line: 12, keys: []string{"/", "s", "enter", "p"}, page: logLinePage, shown: "the database is down"},
		{name: "esc drops the search first", line: 12, keys: []string{"/", "s", "enter", "esc"}, page: logLinePage},
		{name: "esc goes back", line: 12, keys: []string{"/", "s", "enter", "esc", "esc"}, page: functionPage},
		{name: "enter folds, o opens", line: 2, keys: []string{"o"}, page: invocationPage, shown: lgNodeID},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := pressKeys(t, lgOpen(t, lgOnLogs(t), tt.line), tt.keys...)
			if m.page != tt.page {
				t.Fatalf("the page is %d, want %d", m.page, tt.page)
			}
			v := ansi.Strip(m.render())
			for _, w := range tt.want {
				if !strings.Contains(v, w) {
					t.Errorf("the page does not show %q:\n%s", w, v)
				}
			}
			if tt.shown != "" && !strings.Contains(v, tt.shown) {
				t.Errorf("the page does not show %q:\n%s", tt.shown, v)
			}
			if m.status != tt.status {
				t.Errorf("status %q, want %q", m.status, tt.status)
			}
			if _, foot := m.frameMeta(); tt.path != "" && !strings.HasPrefix(ansi.Strip(foot), tt.path) {
				t.Errorf("the frame's foot is %q, want the path %q", ansi.Strip(foot), tt.path)
			}
		})
	}
}

// TestLogLineJSONCopiesTheLine checks c on the top of the json of a line copies the line as it
// was logged, the prefix a runtime writes before the json too.
func TestLogLineJSONCopiesTheLine(t *testing.T) {
	m := lgOpen(t, lgOnLogs(t), 11)
	j := m.state.logLine.shown()
	if j == nil {
		t.Fatal("the line of pino has no json view")
	}
	if got, want := j.valueOf(0), lgLogs(time.Now())[11].Message; got != want {
		t.Errorf("the top copies %q, want the line %q", got, want)
	}
	m = lgOpen(t, lgOnLogs(t), 2)
	if m.state.logLine.shown() != nil {
		t.Error("a line of text has a json view")
	}
}

// TestLogLineScrolls checks a line of 50 kb is drawn whole and wrapped on its page, which
// scrolls to its end.
func TestLogLineScrolls(t *testing.T) {
	m, _ := onLogs(t, lgBig(time.Now().Add(-time.Minute)))
	for i, want := range []string{"a long message, a long message,", "a long line of words 漢字,"} {
		p := lgOpen(t, m, i)
		if n := p.logLineLines().len(); n < 400 {
			t.Errorf("line %d draws %d lines, too few to hold 50 kb", i, n)
		}
		if v := screen(p); !strings.Contains(v, want) || !strings.Contains(v, "size  ") {
			t.Errorf("line %d:\n%s", i, v)
		}
		end := pressKeys(t, p, "G")
		if end.state.logLine.scroll == 0 || !strings.Contains(end.position(), "of ") {
			t.Errorf("G did not scroll line %d: scroll %d, position %q", i, end.state.logLine.scroll, end.position())
		}
	}
}

// lgLayoutPages are the logs tab as each of its keys shows it, and the page of every line, at
// the top and scrolled to its end.
func lgLayoutPages(t *testing.T, w, h int) map[string]model {
	t.Helper()
	at := time.Now().Add(-time.Minute).Truncate(time.Millisecond)
	logs := append(lgLogs(at), lgBig(at)...)
	f := newFake(t)
	f.logs = logs
	m := newTestModel(t, f)
	m, _ = update(t, m, tea.WindowSizeMsg{Width: w, Height: h})
	m = settle(t, m, m.Init())
	m = pressKeys(t, m, "enter", "tab")
	filtered := pressKeys(t, m, "s", "s", "/")
	filtered = typeText(t, filtered, "card")
	pages := map[string]model{
		"logs":               m,
		"logs grouped":       pressKeys(t, m, "b"),
		"logs at the top":    pressKeys(t, m, "b", "g"),
		"logs ago":           pressKeys(t, m, "t"),
		"logs without times": pressKeys(t, m, "t", "t", "g"),
		"logs filtered":      pressKeys(t, filtered, "enter"),
		"logs typing":        filtered,
		"logs at error":      pressKeys(t, m, "s", "s", "s", "b"),
	}
	for i := range logs {
		p := lgOpen(t, m, i)
		pages["line "+formatInt(i)] = p
		pages["line "+formatInt(i)+" at its end"] = pressKeys(t, p, "G")
	}
	pages["line help"] = pressKeys(t, pages["line 0"], "?")
	pages["line searching its json"] = pressKeys(t, pages["line 16"], "/", "l", "o", "n", "g")
	pages["line of json folded"] = pressKeys(t, pages["line 16"], "space")
	return pages
}

// TestLogLineLayout draws the logs tab and the page of each line, of node, python, go,
// powertools, pino and zap, of 50 kb, and at a level with a filter, at the smallest terminal and
// two larger, and checks no line is wider than the terminal and there are no more lines than it is
// high.
func TestLogLineLayout(t *testing.T) {
	for _, size := range [][2]int{{78, 22}, {120, 40}, {200, 60}} {
		w, h := size[0], size[1]
		for name, m := range lgLayoutPages(t, w, h) {
			lines := strings.Split(m.render(), "\n")
			if len(lines) > h {
				t.Errorf("%dx%d %s: %d lines", w, h, name, len(lines))
			}
			for i, line := range lines {
				if lw := ansi.StringWidth(line); lw > w {
					t.Errorf("%dx%d %s: line %d is %d wide", w, h, name, i, lw)
					break
				}
			}
		}
	}
}

// TestLogLineEscapes puts escape sequences in the lines, streams and request ids of the logs, and
// checks none reaches the logs tab, grouped or not, or the page of a line.
func TestLogLineEscapes(t *testing.T) {
	evil := "x\x1b]8;;https://evil.example/\x07y\x1b[2J\x9b\u202e\xff"
	at := time.Now().Add(-time.Minute)
	f := newFake(t)
	f.logs = []cli.LogEvent{
		{Time: at, Stream: evil, Message: "START RequestId: " + evil + " Version: $LATEST"},
		{Time: at.Add(time.Millisecond), Stream: evil, Message: `{"level":"error","msg":"` + evil + `","` + evil + `":"` + evil + `"}`},
		{Time: at.Add(2 * time.Millisecond), Stream: evil, Message: "level=warn msg=" + evil + " " + evil + "=1"},
		{Time: at.Add(3 * time.Millisecond), Stream: evil, Message: "2026-11-06T10:00:00.000Z\t" + evil + "\tERROR\t" + evil},
		{Time: at.Add(4 * time.Millisecond), Stream: evil, Message: "REPORT RequestId: " + evil + "\tDuration: 1.00 ms"},
	}
	m := pressKeys(t, started(t, f), "enter", "tab")
	pages := map[string]model{"logs": m, "grouped": pressKeys(t, m, "b"), "at warn": pressKeys(t, m, "s", "s")}
	for i := range f.logs {
		pages["line "+formatInt(i)] = lgOpen(t, m, i)
	}
	for name, p := range pages {
		v := p.render()
		if strings.Contains(v, "\x1b]") || strings.Contains(v, "\x07") || strings.Contains(v, "\x1b[2J") || strings.ContainsRune(v, '\u009b') || strings.ContainsRune(v, '\u202e') || !utf8.ValidString(v) {
			t.Errorf("%s: an escape sequence reached the view", name)
		}
	}
}
