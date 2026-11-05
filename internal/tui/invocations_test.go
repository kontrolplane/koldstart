package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/kontrolplane/koldstart/internal/cli"
)

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
	if v := screen(m); !strings.Contains(v, "✓ ok") || !strings.Contains(v, "duration 2ms") || !strings.Contains(v, "REPORT") {
		t.Errorf("the invocation once reported:\n%s", v)
	}
}
