package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/kontrolplane/koldstart/internal/cli"
)

// The requests lgLogs holds: one of node and one of python, at once on two instances.
const (
	lgNodeID   = "aaaaaaaa-1111-1111-1111-111111111111"
	lgPythonID = "bbbbbbbb-2222-2222-2222-222222222222"
)

// lgLogs are two requests running at once on two instances, a node one that warns and a python
// one that fails, their lines interleaved, and after them lines of no request as powertools,
// pino, zap, slog and go's log write them, and a go panic.
func lgLogs(at time.Time) []cli.LogEvent {
	ms := func(n int) time.Time { return at.Add(time.Duration(n) * time.Millisecond) }
	stamp := "2026-11-06T10:00:00.000Z"
	return []cli.LogEvent{
		{Time: ms(0), Stream: "a", Message: "START RequestId: " + lgNodeID + " Version: $LATEST"},
		{Time: ms(1), Stream: "b", Message: "START RequestId: " + lgPythonID + " Version: $LATEST"},
		{Time: ms(2), Stream: "a", Message: stamp + "\t" + lgNodeID + "\tINFO\torder placed"},
		{Time: ms(3), Stream: "b", Message: "[INFO]\t" + stamp + "\t" + lgPythonID + "\tcharging the card"},
		{Time: ms(4), Stream: "a", Message: stamp + "\t" + lgNodeID + "\tWARN\tstock is low"},
		{Time: ms(5), Stream: "b", Message: "[ERROR]\t" + stamp + "\t" + lgPythonID + "\tthe card was declined"},
		{Time: ms(6), Stream: "a", Message: "END RequestId: " + lgNodeID},
		{Time: ms(7), Stream: "b", Message: "END RequestId: " + lgPythonID},
		{Time: ms(8), Stream: "a", Message: "REPORT RequestId: " + lgNodeID + "\tDuration: 12.50 ms\tBilled Duration: 13 ms\tMemory Size: 256 MB\tMax Memory Used: 70 MB"},
		{Time: ms(9), Stream: "b", Message: "REPORT RequestId: " + lgPythonID + "\tDuration: 5.00 ms\tBilled Duration: 5 ms\tMemory Size: 256 MB\tMax Memory Used: 71 MB"},
		{Time: ms(10), Stream: "c", Message: `{"level":"DEBUG","location":"handler:12","message":"cache miss","service":"orders","timestamp":"2026-11-06 10:00:00,010+0000"}`},
		{Time: ms(11), Stream: "c", Message: `{"level":50,"time":1762423200011,"pid":1,"hostname":"lambda","msg":"the database is down"}`},
		{Time: ms(12), Stream: "c", Message: `{"level":"warn","ts":1762423200.012,"caller":"orders/main.go:20","msg":"a slow query","took":"1.2s"}`},
		{Time: ms(13), Stream: "c", Message: `time=2026-11-06T10:00:00.013Z level=INFO msg="listening for events" port=8080`},
		{Time: ms(14), Stream: "c", Message: "2026/11/06 10:00:00 connected to the queue"},
		{Time: ms(15), Stream: "c", Message: "panic: runtime error: index out of range [3] with length 3"},
	}
}

// lgOnLogs is the logs tab of a function whose log holds lgLogs.
func lgOnLogs(t *testing.T) model {
	t.Helper()
	m, _ := onLogs(t, lgLogs(time.Now().Add(-time.Minute).Truncate(time.Millisecond)))
	return m
}

// lgShown is what the logs tab shows, in order: the message of each line, and rule and the
// request's short id for a rule.
func lgShown(m model) []string {
	l := m.state.function.logs
	var out []string
	for _, it := range m.logList().items {
		if it.line < 0 {
			out = append(out, "rule "+shortRequest(m.state.function.requests[it.req].id))
			continue
		}
		out = append(out, l.lines[it.line].Message)
	}
	return out
}

// lgCursor is the message of the line under the cursor of the logs tab.
func lgCursor(m model) string {
	e, _ := m.logsCursorLine()
	return e.Message
}

// lgMarked are the lines of the logs tab drawn with the cursor's marker.
func lgMarked(m model) []string {
	var out []string
	for line := range strings.SplitSeq(ansi.Strip(m.logsView()), "\n") {
		if strings.HasPrefix(line, "▌") {
			out = append(out, line)
		}
	}
	return out
}

// TestLineLevel checks the level of a line is read as each runtime and logger writes it, and a
// line that names none is error when it says something failed.
func TestLineLevel(t *testing.T) {
	tests := []struct {
		name string
		line string
		want string
	}{
		{name: "node info", line: "2026-11-06T10:00:00.000Z\t" + lgNodeID + "\tINFO\torder placed", want: levelInfo},
		{name: "node error", line: "2026-11-06T10:00:00.000Z\t" + lgNodeID + "\tERROR\tInvoke Error \t{}", want: levelError},
		{name: "python warning", line: "[WARNING]\t2026-11-06T10:00:00.000Z\t" + lgPythonID + "\tslow", want: levelWarn},
		{name: "python crash", line: "[ERROR] KeyError: 'id'\rTraceback (most recent call last):", want: levelError},
		{name: "powertools", line: `{"level":"DEBUG","message":"cache miss"}`, want: levelDebug},
		{name: "pino", line: `{"level":40,"msg":"retrying"}`, want: levelWarn},
		{name: "pino fatal", line: `{"level":60,"msg":"gone"}`, want: levelFatal},
		{name: "zap", line: `{"level":"warn","ts":1.7,"msg":"slow"}`, want: levelWarn},
		{name: "slog json", line: `{"time":"2026-11-06T10:00:00Z","level":"ERROR","msg":"no order"}`, want: levelError},
		{name: "slog text", line: `time=2026-11-06T10:00:00Z level=WARN msg="slow"`, want: levelWarn},
		{name: "severity", line: `{"severity":"CRITICAL","message":"down"}`, want: levelCrit},
		{name: "go log", line: "2026/11/06 10:00:00 connected", want: ""},
		{name: "go panic", line: "panic: boom", want: levelError},
		{name: "start", line: "START RequestId: " + lgNodeID + " Version: $LATEST", want: ""},
		{name: "timeout", line: "2026-11-06T10:00:03.000Z " + lgNodeID + " Task timed out after 3.00 seconds", want: levelError},
		{name: "report of a timeout", line: "REPORT RequestId: " + lgNodeID + "\tDuration: 3000.00 ms\tStatus: timeout", want: levelError},
		{name: "a level in a long line", line: `{"msg":"` + strings.Repeat("x", 2*levelScan) + `","level":"warn"}`, want: ""},
		{name: "plain", line: "hello", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := lineLevel(tt.line); got != tt.want {
				t.Errorf("level %q, want %q", got, tt.want)
			}
		})
	}
}

// TestLogsCursor checks the cursor of the logs tab: on the newest line while following, up and
// pgup stop following, down and pgdn do not follow again, g goes to the first line and G follows,
// f stops following on the line under the cursor, and the marker is drawn on each row of the line
// under it.
func TestLogsCursor(t *testing.T) {
	tests := []struct {
		name   string
		keys   []string
		cursor string
		follow bool
	}{
		{name: "following", cursor: "panic: runtime error", follow: true},
		{name: "up", keys: []string{"k"}, cursor: "2026/11/06 10:00:00 connected"},
		{name: "up and down", keys: []string{"k", "j"}, cursor: "panic: runtime error"},
		{name: "down at the newest", keys: []string{"j"}, cursor: "panic: runtime error", follow: true},
		{name: "page up", keys: []string{"pgup"}, cursor: "START RequestId: " + lgNodeID},
		{name: "page up and down", keys: []string{"pgup", "pgdown"}, cursor: "panic: runtime error"},
		{name: "top", keys: []string{"g"}, cursor: "START RequestId: " + lgNodeID},
		{name: "top and bottom", keys: []string{"g", "G"}, cursor: "panic: runtime error", follow: true},
		{name: "follow again", keys: []string{"f", "k", "k", "f"}, cursor: "panic: runtime error", follow: true},
		{name: "follow off", keys: []string{"f"}, cursor: "panic: runtime error"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := pressKeys(t, lgOnLogs(t), tt.keys...)
			l := m.state.function.logs
			if !strings.HasPrefix(lgCursor(m), tt.cursor) || l.follow != tt.follow {
				t.Errorf("cursor on %q, follow %t; want %q, %t", lgCursor(m), l.follow, tt.cursor, tt.follow)
			}
			marked := lgMarked(m)
			if len(marked) == 0 || !strings.Contains(marked[0], tt.cursor[:min(len(tt.cursor), 20)]) {
				t.Errorf("the marker is on %q, want the line %q", marked, tt.cursor)
			}
		})
	}
}

// TestLogsCursorStays checks the cursor stays on its line as older lines are read above it, and
// moves to the nearest line shown before it when the level hides it, or the first when none is,
// and back when the level shows it again.
func TestLogsCursorStays(t *testing.T) {
	m := pressKeys(t, lgOnLogs(t), "g", "j", "j", "j")
	want := lgCursor(m)
	l := &m.state.function.logs
	m, cmd := m.loadOlder()
	msg := cmd().(logsLoadedMsg)
	msg.events = logSeries(l.from.Add(-time.Hour), 5, "older")
	m, _ = update(t, m, msg)
	if got := lgCursor(m); got != want {
		t.Errorf("after reading older lines the cursor is on %q, want %q", got, want)
	}
	m = pressKeys(t, m, "v", "v")
	if got := lgCursor(m); !strings.Contains(got, "stock is low") {
		t.Errorf("at warn the cursor is on %q, want the first line shown, as none before its own is", got)
	}
	m = pressKeys(t, m, "v", "v")
	if got := lgCursor(m); got != want {
		t.Errorf("with every level again the cursor is on %q, want %q", got, want)
	}
}

// TestLogsLevel checks v cycles the level the lines are shown from, the frame says it, the head
// counts the lines kept, and the filter and the level both keep a line.
func TestLogsLevel(t *testing.T) {
	tests := []struct {
		name   string
		keys   []string
		filter string
		shown  int
		meta   string
		empty  string
	}{
		{name: "every level", shown: 16},
		{name: "info", keys: []string{"v"}, shown: 15, meta: "level ≥ info"},
		{name: "warn", keys: []string{"v", "v"}, shown: 5, meta: "level ≥ warn"},
		{name: "error", keys: []string{"v", "v", "v"}, shown: 3, meta: "level ≥ error"},
		{name: "all again", keys: []string{"v", "v", "v", "v"}, shown: 16},
		{name: "warn and a filter", keys: []string{"v", "v"}, filter: "card", shown: 1, meta: "filter card · level ≥ warn"},
		{name: "a filter alone", filter: "card", shown: 2, meta: "filter card"},
		{name: "error and a filter of none", keys: []string{"v", "v", "v"}, filter: "stock", shown: 0, meta: "filter stock · level ≥ error", empty: "no line at error or above matches stock: esc clears the filter"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := pressKeys(t, lgOnLogs(t), tt.keys...)
			if tt.filter != "" {
				m = pressKeys(t, m, "/")
				m = typeText(t, m, tt.filter)
				m = pressKeys(t, m, "enter")
			}
			if got := m.logList().lines; got != tt.shown {
				t.Errorf("%d lines shown, want %d: %q", got, tt.shown, lgShown(m))
			}
			meta, _ := m.frameMeta()
			if got := ansi.Strip(meta); got != tt.meta {
				t.Errorf("the frame says %q, want %q", got, tt.meta)
			}
			v := screen(m)
			if counted := formatInt(tt.shown) + " of 16 lines"; tt.meta != "" && !strings.Contains(v, counted) {
				t.Errorf("the head does not say %q:\n%s", counted, v)
			}
			if tt.empty != "" && !strings.Contains(v, tt.empty) {
				t.Errorf("the tab does not say %q:\n%s", tt.empty, v)
			}
		})
	}
}

// TestLogsLevelOnly checks the tab says no line is at the level shown from, and how to change it,
// when none is.
func TestLogsLevelOnly(t *testing.T) {
	m, _ := onLogs(t, logSeries(time.Now().Add(-time.Minute), 3, "line"))
	m = pressKeys(t, m, "v", "v")
	if v := screen(m); !strings.Contains(v, "no line at warn or above: v changes the level") {
		t.Errorf("the tab does not say no line is at warn:\n%s", v)
	}
}

// TestLogsGrouped checks b groups the lines of each request together where its first line is,
// with a rule after them saying how long it ran and how it failed, leaves the lines of no request
// where they are, draws no rule for a request whose lines are all hidden, and is off by default.
func TestLogsGrouped(t *testing.T) {
	interleaved := lgShown(lgOnLogs(t))
	if !strings.HasPrefix(interleaved[1], "START RequestId: "+lgPythonID) || strings.HasPrefix(interleaved[len(interleaved)-1], "rule") {
		t.Fatalf("the lines are grouped by default: %q", interleaved)
	}
	tests := []struct {
		name string
		keys []string
		want []string
	}{
		{name: "grouped", keys: []string{"b"}, want: []string{
			"START RequestId: " + lgNodeID, "order placed", "stock is low", "END RequestId: " + lgNodeID, "REPORT RequestId: " + lgNodeID, "rule aaaaaaaa",
			"START RequestId: " + lgPythonID, "charging the card", "the card was declined", "END RequestId: " + lgPythonID, "REPORT RequestId: " + lgPythonID, "rule bbbbbbbb",
			"cache miss", "the database is down", "a slow query", "listening for events", "connected to the queue", "panic:",
		}},
		{name: "grouped at error", keys: []string{"b", "v", "v", "v"}, want: []string{"the card was declined", "rule bbbbbbbb", "the database is down", "panic:"}},
		{name: "grouped and back", keys: []string{"b", "b"}, want: []string{"START RequestId: " + lgNodeID, "START RequestId: " + lgPythonID}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := pressKeys(t, lgOnLogs(t), tt.keys...)
			got := lgShown(m)
			if len(got) < len(tt.want) {
				t.Fatalf("shown %q, want %q", got, tt.want)
			}
			for i, w := range tt.want {
				if !strings.Contains(got[i], w) {
					t.Errorf("item %d is %q, want %q", i, got[i], w)
				}
			}
		})
	}
	v := screen(pressKeys(t, lgOnLogs(t), "b"))
	for _, want := range []string{"╌╌ aaaaaaaa · 13ms ╌", "╌╌ bbbbbbbb · 5ms · ✗ failed ╌", "grouped by request"} {
		if !strings.Contains(v, want) {
			t.Errorf("the grouped logs do not show %q:\n%s", want, v)
		}
	}
}

// TestLogsStamps checks t cycles the column in front of the lines from the time to how long ago
// it was, to none, which gives its room to the lines, and back.
func TestLogsStamps(t *testing.T) {
	at := time.Now().Add(-90 * time.Second).Truncate(time.Millisecond)
	long := strings.Repeat("word ", 60)
	tests := []struct {
		name   string
		keys   []string
		prefix string
		room   int
	}{
		{name: "time", prefix: at.Local().Format("15:04:05.000") + "  word", room: logWidth() - stampWidth},
		{name: "ago", keys: []string{"t"}, prefix: "  1m ago  word", room: logWidth() - agoWidth},
		{name: "none", keys: []string{"t", "t"}, prefix: "word", room: logWidth()},
		{name: "time again", keys: []string{"t", "t", "t"}, prefix: at.Local().Format("15:04:05.000") + "  word", room: logWidth() - stampWidth},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, _ := onLogs(t, []cli.LogEvent{{Time: at, Message: long}})
			m = pressKeys(t, m, tt.keys...)
			lines := strings.Split(ansi.Strip(m.logsView()), "\n")
			if len(lines) < 2 || !strings.HasPrefix(lines[1][len("▌"):], tt.prefix) {
				t.Fatalf("the line is drawn as %q, want it to start %q", lines, tt.prefix)
			}
			if got := m.state.function.logs.room(); got != tt.room {
				t.Errorf("the lines have %d columns, want %d", got, tt.room)
			}
		})
	}
}

// TestLogsCopy checks c on the logs tab copies the line under the cursor as it was logged.
func TestLogsCopy(t *testing.T) {
	m := pressKeys(t, lgOnLogs(t), "k", "c")
	if want := "copied 2026/11/06 10:00:00 connected to the queue"; m.status != want {
		t.Errorf("status %q, want %q", m.status, want)
	}
}

// BenchmarkLogsShown takes a key that changes what the logs tab of the big fixture shows, or
// moves its cursor or opens a line once it does, and draws the frame after it at 200 by 60: its
// 5000 lines grouped, at a level, without times, a line up when grouped at a level, the first
// line grouped, and the page of a line.
func BenchmarkLogsShown(b *testing.B) {
	logs := perfBench(b)["logs"]
	tests := []struct {
		name string
		keys []string
	}{
		{name: "grouped", keys: []string{"b"}},
		{name: "at warn", keys: []string{"v", "v"}},
		{name: "without times", keys: []string{"t", "t"}},
		{name: "up grouped at warn", keys: []string{"b", "v", "v", "k"}},
		{name: "top grouped", keys: []string{"b", "g"}},
		{name: "a line", keys: []string{"k", "enter"}},
	}
	for _, tt := range tests {
		m := perfKeys(logs, tt.keys[:len(tt.keys)-1]...)
		last := perfPress(tt.keys[len(tt.keys)-1])
		b.Run(tt.name, func(b *testing.B) {
			for b.Loop() {
				next, _ := perfUpdate(m, last)
				_ = next.View()
			}
		})
	}
}
