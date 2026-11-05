package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/kontrolplane/koldstart/internal/cli"
	"github.com/kontrolplane/koldstart/internal/tui/styles"
)

// logSeries are n lines a millisecond apart from at, each saying its number.
func logSeries(at time.Time, n int, prefix string) []cli.LogEvent {
	out := make([]cli.LogEvent, n)
	for i := range out {
		out[i] = cli.LogEvent{Time: at.Add(time.Duration(i) * time.Millisecond), Stream: "a", Message: fmt.Sprintf("%s %03d", prefix, i)}
	}
	return out
}

// onLogs is the logs tab of a function whose log holds lines.
func onLogs(t *testing.T, lines []cli.LogEvent) (model, *fakeBackend) {
	t.Helper()
	f := newFake(t)
	f.logs = lines
	return pressKeys(t, started(t, f), "enter", "tab"), f
}

// TestLogsScrolling checks g goes to the top, where the head says how far back the logs go, k
// stops following, G follows again, and ↑ at the top reads the hour before and keeps in sight
// what was on screen.
func TestLogsScrolling(t *testing.T) {
	m, f := onLogs(t, logSeries(time.Now().Add(-time.Minute).Truncate(time.Millisecond), 100, "line"))
	if v := screen(m); !strings.Contains(v, "line 099") || strings.Contains(v, "line 000") {
		t.Fatalf("following does not show the newest line:\n%s", v)
	}
	m = pressKeys(t, m, "g")
	if v := screen(m); !strings.Contains(v, "↑ at the top reads the hour before") || !strings.Contains(v, "line 000") {
		t.Errorf("the top does not show the head and the first line:\n%s", v)
	}
	if m.state.function.logs.follow {
		t.Error("going to the top still follows")
	}
	f.older = logSeries(m.state.function.logs.from.Add(-time.Hour), 3, "older")
	m = pressKeys(t, m, "k")
	if v := screen(m); f.olderReads != 1 || !strings.Contains(v, "line 000") || !strings.Contains(v, "older 002") {
		t.Errorf("%d reads of older lines; the view after them:\n%s", f.olderReads, v)
	}
	m = pressKeys(t, m, "k", "k", "k")
	if v := screen(m); !strings.Contains(v, "older 000") || !strings.Contains(v, "from ") {
		t.Errorf("scrolling up does not show the older lines and the head:\n%s", v)
	}
	m = pressKeys(t, m, "G")
	if l := m.state.function.logs; !l.follow || l.scroll != 0 || !strings.Contains(screen(m), "line 099") {
		t.Errorf("G does not follow again: follow %v, scroll %d", l.follow, l.scroll)
	}
}

// TestLoadOlder checks ↑ at the top reads the hour that ends a millisecond before the oldest
// window read, so no line is read twice or skipped.
func TestLoadOlder(t *testing.T) {
	m, _ := onLogs(t, logSeries(time.Now().Add(-time.Minute), 3, "line"))
	from := m.state.function.logs.from
	m, cmd := m.loadOlder()
	if !m.state.function.logs.loadingOlder {
		t.Fatal("it is not reading the lines before")
	}
	if again, more := m.loadOlder(); more != nil || again.state.function.logs.olderGen != m.state.function.logs.olderGen {
		t.Error("a second read of the older lines started while one runs")
	}
	msg, ok := cmd().(logsLoadedMsg)
	if !ok || msg.how != logsPrepend || !msg.from.Equal(from.Add(-olderStep)) {
		t.Fatalf("read %+v, want the hour before %v", msg, from)
	}
	m, _ = update(t, m, msg)
	if !m.state.function.logs.from.Equal(from.Add(-olderStep)) || m.state.function.logs.loadingOlder {
		t.Errorf("from %v, want %v", m.state.function.logs.from, from.Add(-olderStep))
	}
}

// TestReadWindow checks a read that hit the most lines one read returns starts its window after
// the millisecond of its first line, dropping that millisecond's lines, so the read before
// takes them all.
func TestReadWindow(t *testing.T) {
	at := time.Now().Truncate(time.Millisecond)
	full := logSeries(at, cli.MaxLogEvents, "line")
	full[1].Time = full[0].Time
	tests := []struct {
		name   string
		events []cli.LogEvent
		from   time.Time
		kept   int
	}{
		{name: "under the cap", events: logSeries(at, 10, "line"), from: at.Add(-time.Hour), kept: 10},
		{name: "at the cap", events: full, from: at.Add(time.Millisecond), kept: cli.MaxLogEvents - 2},
		{name: "none", events: nil, from: at.Add(-time.Hour), kept: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			events, from := readWindow(tt.events, at.Add(-time.Hour))
			if len(events) != tt.kept || !from.Equal(tt.from) {
				t.Errorf("%d lines from %v, want %d from %v", len(events), from, tt.kept, tt.from)
			}
		})
	}
}

// TestTrimLines checks the lines kept are bounded, cut at a millisecond so from, or the newest
// line kept, is where the next read starts without a gap.
func TestTrimLines(t *testing.T) {
	at := time.Now().Truncate(time.Millisecond)
	lines := func() []cli.LogEvent {
		l := logSeries(at, maxLogLines+10, "line")
		l[10].Time, l[maxLogLines-1].Time = l[9].Time, l[maxLogLines].Time
		return l
	}
	t.Run("oldest", func(t *testing.T) {
		l := logState{lines: lines(), from: at.Add(-time.Hour)}
		l.trimOldest()
		if len(l.lines) != maxLogLines+1 || !l.from.Equal(l.lines[0].Time) || l.lines[0].Message != "line 009" {
			t.Errorf("%d lines from %v, the first %q", len(l.lines), l.from, l.lines[0].Message)
		}
	})
	t.Run("newest", func(t *testing.T) {
		l := logState{lines: lines()}
		dropped := l.trimNewest()
		if len(l.lines) != maxLogLines+1 || len(dropped) != 9 || dropped[0].Time.Equal(l.lines[len(l.lines)-1].Time) {
			t.Errorf("%d lines kept, %d dropped", len(l.lines), len(dropped))
		}
	})
}

// TestPrependKeepsTheView checks reading older lines when the most are kept drops the newest,
// and the view stays on the lines it showed.
func TestPrependKeepsTheView(t *testing.T) {
	at := time.Now().Add(-time.Hour).Truncate(time.Millisecond)
	m, _ := onLogs(t, nil)
	l := &m.state.function.logs
	l.lines, l.from, l.follow, l.scroll = logSeries(at, maxLogLines, "line"), at, false, 100
	shown := screen(m)
	m, cmd := m.loadOlder()
	msg := cmd().(logsLoadedMsg)
	msg.events = logSeries(at.Add(-time.Hour), 10, "older")
	m, _ = update(t, m, msg)
	l = &m.state.function.logs
	if len(l.lines) != maxLogLines || l.lines[0].Message != "older 000" || l.scroll != 90 {
		t.Fatalf("%d lines, the first %q, scroll %d", len(l.lines), l.lines[0].Message, l.scroll)
	}
	if screen(m) != shown {
		t.Errorf("the view moved:\n%s\nwas:\n%s", screen(m), shown)
	}
}

// TestAppendNew checks lines a tail and a read both bring are added once, however many lines
// share their time, and that a late line is put in its place.
func TestAppendNew(t *testing.T) {
	at := time.Now().Truncate(time.Millisecond)
	same := make([]cli.LogEvent, 600)
	for i := range same {
		same[i] = cli.LogEvent{Time: at, Stream: "a", Message: fmt.Sprintf("line %d", i)}
	}
	tests := []struct {
		name  string
		have  []cli.LogEvent
		lines []cli.LogEvent
		want  int
		first string
	}{
		{name: "into none", have: nil, lines: logSeries(at, 3, "line"), want: 3, first: "line 000"},
		{name: "nothing new", have: logSeries(at, 3, "line"), lines: nil, want: 3, first: "line 000"},
		{name: "the same again", have: logSeries(at, 3, "line"), lines: logSeries(at, 3, "line"), want: 3, first: "line 000"},
		{name: "many at one time", have: same, lines: same, want: 600, first: "line 0"},
		{name: "a late line", have: logSeries(at, 3, "line"), lines: []cli.LogEvent{{Time: at.Add(-time.Second), Message: "late"}}, want: 4, first: "late"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := appendNew(tt.have, tt.lines)
			if len(got) != tt.want || got[0].Message != tt.first {
				t.Errorf("%d lines, the first %q; want %d, %q", len(got), got[0].Message, tt.want, tt.first)
			}
		})
	}
}

// TestLogsStates checks what the logs tab and the invocations tab say before the logs are read,
// when they cannot be, and when there are none.
func TestLogsStates(t *testing.T) {
	tests := []struct {
		name        string
		set         func(m *model)
		logs        string
		invocations string
	}{
		{name: "no log group", set: func(m *model) { m.state.function.logGroup = "" }, logs: "no log group: deploy the function first", invocations: "no log group"},
		{name: "reading", set: func(m *model) { m.state.function.logs = newLogState() }, logs: "reading the logs…", invocations: "reading the logs…"},
		{name: "failed", set: func(m *model) {
			m.state.function.logs = newLogState()
			*m, _ = m.applyLogs(logsLoadedMsg{gen: m.state.function.logs.gen, err: errAWS})
		}, logs: "could not read the logs: aws said no · r reads them again", invocations: "could not read the logs"},
		{name: "none", set: func(m *model) {}, logs: "nothing logged since", invocations: "no invocations in the logs read"},
		{name: "none match", set: func(m *model) {
			m.state.function.logs.lines = logSeries(time.Now(), 3, "line")
			m.state.function.logs.filter.text = "refund"
		}, logs: "no line has refund", invocations: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, _ := onLogs(t, nil)
			tt.set(&m)
			m = m.updateRequests()
			if v := ansi.Strip(m.logsView()); !strings.Contains(v, tt.logs) {
				t.Errorf("the logs say:\n%s\nwant %q", v, tt.logs)
			}
			if v := ansi.Strip(m.invocationsView()); !strings.Contains(v, tt.invocations) {
				t.Errorf("the invocations say:\n%s\nwant %q", v, tt.invocations)
			}
		})
	}
}

// TestLogsFilterMarks checks the lines a filter keeps have what it looks for marked, and the
// head counts them.
func TestLogsFilterMarks(t *testing.T) {
	m, _ := onLogs(t, requestLogs(time.Now().Add(-time.Minute)))
	m = pressKeys(t, m, "/")
	m = typeText(t, m, "ORDER")
	m = pressKeys(t, m, "enter")
	rows := m.logRows(m.state.function.logs.lines)
	if len(rows) == 0 {
		t.Fatal("no line has order")
	}
	for _, r := range rows {
		if strings.Contains(strings.ToLower(plainText(r.c)), "order") {
			if tone, _ := toneOf(r.c, "order"); tone != styles.ToneAccent {
				t.Errorf("order is not marked in %+v", r.c)
			}
		}
	}
	if v := screen(m); !strings.Contains(v, "3 of 12 lines") {
		t.Errorf("the head does not count the lines kept:\n%s", v)
	}
}

// TestLogsNarrow checks a long line wraps under its time, and no line of the logs is wider than
// the content area.
func TestLogsNarrow(t *testing.T) {
	m, _ := onLogs(t, []cli.LogEvent{{Time: time.Now(), Message: strings.Repeat("a long line 漢字, ", 30) + "\n"}})
	for line := range strings.SplitSeq(m.logsView(), "\n") {
		if w := ansi.StringWidth(line); w > contentWidth {
			t.Errorf("a line is %d wide, more than %d: %q", w, contentWidth, ansi.Strip(line))
		}
	}
	lines := strings.Split(ansi.Strip(m.logsView()), "\n")
	if len(lines) < 3 {
		t.Fatalf("the line did not wrap:\n%s", strings.Join(lines, "\n"))
	}
	for _, line := range lines[2:] {
		if rest, ok := strings.CutPrefix(line, strings.Repeat(" ", stampWidth+1)); !ok || strings.HasPrefix(rest, " ") {
			t.Errorf("a wrapped line does not sit under the message: %q", line)
		}
	}
}
