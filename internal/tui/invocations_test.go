package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/kontrolplane/koldstart/internal/cli"
)

// TestRequestRow checks a row of the invocations lines up: every status after a symbol, the
// numbers of durations and of a missing one in one column, and memory without its unit where the
// unit would not fit.
func TestRequestRow(t *testing.T) {
	old := time.Now().Add(-time.Hour)
	tests := []struct {
		name string
		req  request
		col  int
		want string
	}{
		{name: "no report", req: request{start: old, last: old}, col: 0, want: "○ no report"},
		{name: "a duration in ms", req: request{start: old, reported: true, duration: 30}, col: 3, want: "    30ms"},
		{name: "a duration in s", req: request{start: old, reported: true, duration: 6000}, col: 3, want: "     6s "},
		{name: "no duration", req: request{start: old, last: old}, col: 3, want: "     -  "},
		{name: "a cold start in s", req: request{start: old, reported: true, init: 1234}, col: 6, want: "     1.2s "},
		{name: "memory", req: request{start: old, reported: true, memory: 256, maxMemory: 80}, col: 5, want: "   80/256 mb"},
		{name: "memory too wide for its unit", req: request{start: old, reported: true, memory: 1024, maxMemory: 1024}, col: 5, want: " 1,024/1,024"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			row := requestRow(tt.req, 6*time.Second)
			if got := ansi.Strip(renderCell(row[tt.col], invocationColumns[tt.col], nil)); got != tt.want {
				t.Errorf("%q, want %q", got, tt.want)
			}
		})
	}
}

// TestInvocationsNarrow checks the invocations tab keeps what a failed invocation failed with as
// the terminal narrows, leaving out the memory first, then what was billed and the cold start.
func TestInvocationsNarrow(t *testing.T) {
	tests := []struct {
		name   string
		w, h   int
		shown  []string
		hidden []string
	}{
		{name: "the smallest", w: 78, h: 22, shown: []string{"request", "duration", "error", "Error: connect ECONNREFU"}, hidden: []string{"memory", "billed", "cold start"}},
		{name: "80 wide", w: 80, h: 24, shown: []string{"request", "duration", "error", "Error: connect ECONNREFUSE"}, hidden: []string{"memory", "billed", "cold start"}},
		{name: "100 wide", w: 100, h: 30, shown: []string{"billed", "cold start", "error", "Error: connect ECONNREFUSE"}, hidden: []string{"memory"}},
		{name: "120 wide", w: 120, h: 40, shown: []string{"billed", "memory", "cold start", "Error: connect ECONNREFUSED 127."}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := screen(ivdOpen(t, tt.w, tt.h))
			var header string
			for line := range strings.SplitSeq(v, "\n") {
				if strings.Contains(line, "duration") {
					header = line
					break
				}
			}
			for _, want := range tt.shown {
				if !strings.Contains(v, want) {
					t.Errorf("%q is not shown:\n%s", want, v)
				}
			}
			for _, gone := range tt.hidden {
				if strings.Contains(header, gone) {
					t.Errorf("%q is shown: %s", gone, header)
				}
			}
		})
	}
}

// TestFailure checks which lines say an invocation failed or timed out.
func TestFailure(t *testing.T) {
	tests := []struct {
		name     string
		line     string
		failed   bool
		timedOut bool
	}{
		{name: "info", line: "2026-11-05T10:00:00.001Z\t1\tINFO\tok", failed: false},
		{name: "node error", line: "2026-11-05T10:00:00.001Z\t1\tERROR\tboom", failed: true},
		{name: "error type", line: `{"errorType":"Error","errorMessage":"boom"}`, failed: true},
		{name: "json error", line: `{"level":"error","msg":"boom"}`, failed: true},
		{name: "json error with spaces", line: `{"level": "ERROR", "msg": "boom"}`, failed: true},
		{name: "json fatal", line: `{"severity":"fatal","msg":"boom"}`, failed: true},
		{name: "logfmt error", line: `time=x level=error msg=boom`, failed: true},
		{name: "slog error", line: `time=x level=ERROR msg=boom`, failed: true},
		{name: "a level of errors", line: `level=errors msg=fine`, failed: false},
		{name: "json panic", line: `{"level":"panic","msg":"boom"}`, failed: true},
		{name: "logfmt severity", line: `severity=err msg=boom`, failed: true},
		{name: "a message about an error", line: `{"level":"info","msg":"no error here"}`, failed: false},
		{name: "exit", line: "RequestId: 1 Error: Runtime exited with error: exit status 2\nRuntime.ExitError", failed: true},
		{name: "report of an error", line: "REPORT RequestId: 1\tDuration: 2.00 ms\tStatus: error\tError Type: Runtime.Unknown", failed: true},
		{name: "timeout", line: "2026-11-05T10:00:06.000Z 1 Task timed out after 6.00 seconds", failed: true, timedOut: true},
		{name: "report of a timeout", line: "REPORT RequestId: 1\tDuration: 6000.00 ms\tStatus: timeout", failed: true, timedOut: true},
		{name: "a python crash", line: "[ERROR] KeyError: 'id'\rTraceback (most recent call last):", failed: true},
		{name: "python's logging at error", line: "[ERROR]\t2026-11-05T10:00:00.001Z\t1\tcould not store", failed: true},
		{name: "python's logging at info", line: "[INFO]\t2026-11-05T10:00:00.001Z\t1\terrors none", failed: false},
		{name: "pino's error", line: `{"level":50,"msg":"could not store"}`, failed: true},
		{name: "pino's fatal", line: `{"level": 60, "msg":"down"}`, failed: true},
		{name: "pino's info", line: `{"level":30,"msg":"stored"}`, failed: false},
		{name: "a go panic", line: "panic: assignment to entry in nil map", failed: true},
		{name: "a message about a panic", line: "no panic: all good", failed: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			failed, timedOut := failure(tt.line)
			if failed != tt.failed || timedOut != tt.timedOut {
				t.Errorf("failed %v, timed out %v; want %v, %v", failed, timedOut, tt.failed, tt.timedOut)
			}
		})
	}
}

// TestInvocationsCursorKept checks the cursor stays on its invocation when a newer one comes,
// and on the newest when it was there.
func TestInvocationsCursorKept(t *testing.T) {
	at := time.Now().Add(-time.Minute)
	newer := cli.LogEvent{Time: at.Add(time.Minute), Stream: "c", Message: "START RequestId: 44444444-4444-4444-4444-444444444444 Version: $LATEST"}
	tests := []struct {
		name string
		keys []string
		want string
	}{
		{name: "on the newest", keys: nil, want: "44444444"},
		{name: "on an older one", keys: []string{"j"}, want: "22222222"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, _ := onLogs(t, requestLogs(at))
			m = pressKeys(t, m, append([]string{"tab"}, tt.keys...)...)
			f := &m.state.function
			f.logs.lines = append(f.logs.lines, newer)
			m = m.updateRequests()
			f = &m.state.function
			if got := f.requests[f.invocations.cursor].id; !strings.HasPrefix(got, tt.want) {
				t.Errorf("the cursor is on %s, want %s", got, tt.want)
			}
		})
	}
}

// TestInvocationTakesItsReport checks an invocation opened while it runs shows its report once
// the report is read.
func TestInvocationTakesItsReport(t *testing.T) {
	at := time.Now().Add(-time.Second)
	id := "55555555-5555-5555-5555-555555555555"
	m, _ := onLogs(t, []cli.LogEvent{
		{Time: at, Stream: "a", Message: "START RequestId: " + id + " Version: $LATEST"},
		{Time: at.Add(time.Millisecond), Stream: "a", Message: "2026-11-05T10:00:00.001Z\t" + id + "\tINFO\tworking"},
	})
	m = pressKeys(t, m, "tab", "enter")
	if v := screen(m); m.page != invocationPage || !strings.Contains(v, "running") || strings.Contains(v, "duration") {
		t.Fatalf("the running invocation:\n%s", v)
	}
	f := &m.state.function
	f.logs.lines = append(f.logs.lines, cli.LogEvent{Time: at.Add(2 * time.Millisecond), Stream: "a",
		Message: "REPORT RequestId: " + id + "\tDuration: 2.00 ms\tBilled Duration: 2 ms\tMemory Size: 128 MB\tMax Memory Used: 60 MB"})
	m = m.updateRequests()
	if v := screen(m); !strings.Contains(v, "✓ ok") || !strings.Contains(v, "2ms · billed 2ms") || strings.Contains(v, "REPORT") {
		t.Errorf("the invocation once reported:\n%s", v)
	}
}
