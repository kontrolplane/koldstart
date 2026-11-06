package tui

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/kontrolplane/koldstart/internal/cli"
	"github.com/kontrolplane/koldstart/internal/tui/styles"
)

// invokeTabWith opens the invoke tab of api, with the events written to its events directory.
func invokeTabWith(t *testing.T, f *fakeBackend, events map[string]string) model {
	t.Helper()
	dir := filepath.Join(f.events, "api")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	for name, body := range events {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	m := pressKeys(t, started(t, f), "enter", "tab", "tab", "tab")
	if m.page != functionPage || m.state.function.tab != invokeTab {
		t.Fatalf("page %v tab %v, want the invoke tab of the function", m.page, m.state.function.tab)
	}
	return m
}

// TestInvokeEventNotJSON checks an event that is not json is marked in the list, says the line
// it breaks on, and is not sent.
func TestInvokeEventNotJSON(t *testing.T) {
	f := newFake(t)
	m := invokeTabWith(t, f, map[string]string{"broken.json": "{\n  \"id\": 1,\n  oops\n}"})
	if v := screen(m); !strings.Contains(v, "broken  not json") || !strings.Contains(v, "on line 3") {
		t.Errorf("the broken event is not marked, or does not say its line:\n%s", v)
	}
	m = pressKeys(t, m, "i")
	if len(f.invokes) != 0 || !strings.Contains(m.status, "broken.json is not json") {
		t.Errorf("%d invokes, status %q; want none and a warning", len(f.invokes), m.status)
	}
}

// ikReport is the log tail of an invocation in aws: its START, a line it logged, its END and the
// REPORT lambda writes, with a cold start, or a timeout when status is set.
func ikReport(id, status string) string {
	report := "REPORT RequestId: " + id + "\tDuration: 12.34 ms\tBilled Duration: 13 ms\tMemory Size: 128 MB\tMax Memory Used: 70 MB\tInit Duration: 200.00 ms"
	if status != "" {
		report += "\tStatus: " + status
	}
	return "START RequestId: " + id + " Version: $LATEST\n2026-11-05T10:00:00.001Z\t" + id + "\tINFO\thandling\nEND RequestId: " + id + "\n" + report + "\n"
}

// ikNodeFailure, ikPythonFailure and ikGoFailure are the errors node, python and go return,
// each with a frame of the function's own code and one of the runtime or a dependency.
const (
	ikNodeFailure   = `{"errorType":"TypeError","errorMessage":"x is undefined","stackTrace":["TypeError: x is undefined","    at handler (file:///var/task/index.mjs:3:9)","    at Runtime.handleOnceNonStreaming (file:///var/runtime/index.mjs:1173:29)"]}`
	ikPythonFailure = `{"errorType":"KeyError","errorMessage":"'id'","stackTrace":["  File \"/var/task/app.py\", line 4, in handler\n    return event[\"id\"]\n","  File \"/var/runtime/bootstrap.py\", line 60, in run\n    handle()\n"]}`
	ikGoFailure     = `{"errorType":"runtime.Error","errorMessage":"assignment to entry in nil map","stackTrace":[{"path":"shop/main.go","line":12,"label":"handler"},{"path":"github.com/aws/aws-lambda-go@v1.47.0/lambda/handler.go","line":3,"label":"lambda.callHandler"}]}`
	ikTimeout       = `{"errorType":"Sandbox.Timedout","errorMessage":"RequestId: r-1 Error: Task timed out after 3.00 seconds"}`
)

// TestInvokeResult checks what an invocation shows: how it ended, where it ran, what lambda
// measured of it and its request id, what it returned, the error and stack trace it failed with
// in node, python and go, a timeout, an empty payload, one that is not json, and an invocation
// that did not reach the function.
func TestInvokeResult(t *testing.T) {
	tests := []struct {
		name   string
		result cli.Invocation
		err    error
		want   []string
		status string
	}{
		{
			name:   "returned",
			result: cli.Invocation{Payload: []byte(`{"statusCode":200}`), Duration: 120e6},
			want:   []string{"✓ ok  in aws · ", "duration 120ms", "returned ─", `"statusCode": 200`},
			status: "api returned in 120ms",
		},
		{
			name:   "reported",
			result: cli.Invocation{Payload: []byte(`{}`), Duration: 300e6, RequestID: "r-1", Logs: ikReport("r-1", "")},
			want:   []string{"duration 12ms · billed 13ms · memory 70 of 128 mb · cold start 200ms · log 4 lines", "request r-1", "log ─", "INFO"},
		},
		{
			name:   "node",
			result: cli.Invocation{Payload: []byte(ikNodeFailure), Error: "Unhandled", Duration: 3e6},
			want:   []string{"✗ failed  in aws", "error ─", "TypeError: x is undefined", "  at handler (file:///var/task/index.mjs:3:9)", "  at Runtime.handleOnceNonStreaming"},
			status: "api failed: TypeError: x is undefined",
		},
		{
			name:   "python",
			result: cli.Invocation{Payload: []byte(ikPythonFailure), Error: "Unhandled"},
			want:   []string{"KeyError: 'id'", `  File "/var/task/app.py", line 4, in handler`, `    return event["id"]`, `  File "/var/runtime/bootstrap.py"`},
			status: "api failed: KeyError: 'id'",
		},
		{
			name:   "go",
			result: cli.Invocation{Payload: []byte(ikGoFailure), Error: "Unhandled"},
			want:   []string{"runtime.Error: assignment to entry in nil map", "  handler (shop/main.go:12)", "  lambda.callHandler (github.com/aws/aws-lambda-go@v1.47.0/lambda/handler.go:3)"},
		},
		{
			name:   "timed out",
			result: cli.Invocation{Payload: []byte(ikTimeout), Error: "Unhandled", RequestID: "r-1", Logs: ikReport("r-1", "timeout")},
			want:   []string{"⏱ timed out  in aws", "Sandbox.Timedout: RequestId: r-1 Error: Task timed out after 3.00 seconds", "request r-1"},
			status: "api timed out: Sandbox.Timedout: RequestId: r-1 Error: Task timed out after 3.00 seconds",
		},
		{
			name:   "failed without a payload",
			result: cli.Invocation{Error: "Unhandled"},
			want:   []string{"✗ failed  in aws", "Unhandled", "returned ─", "nothing"},
			status: "api failed: Unhandled",
		},
		{
			name:   "returned nothing",
			result: cli.Invocation{Duration: 5e6},
			want:   []string{"✓ ok", "duration 5ms", "nothing"},
		},
		{
			name:   "not json",
			result: cli.Invocation{Payload: []byte("plain words\nover two lines"), Duration: 5e6},
			want:   []string{"plain words", "over two lines"},
		},
		{
			name:   "not invoked",
			err:    errAWS,
			want:   []string{"✗ could not invoke api", "aws said no", "i tries again"},
			status: "could not invoke api",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFake(t)
			f.result = tt.result
			m := invokeTabWith(t, f, nil)
			m, _ = update(t, m, tea.WindowSizeMsg{Width: 160, Height: 50})
			f.err = tt.err
			m = pressKeys(t, m, "i")
			v := screen(m)
			for _, want := range tt.want {
				if !strings.Contains(v, want) {
					t.Errorf("no %q in:\n%s", want, v)
				}
			}
			if tt.status != "" && m.status != tt.status {
				t.Errorf("status %q, want %q", m.status, tt.status)
			}
		})
	}
}

// TestFailureLines checks the frames of a stack trace that are the runtime's or a dependency's
// are faint and the function's own are not, in node, python and go.
func TestFailureLines(t *testing.T) {
	tests := []struct {
		name     string
		payload  string
		own      string
		internal string
	}{
		{name: "node", payload: ikNodeFailure, own: "at handler (file:///var/task/index.mjs:3:9)", internal: "at Runtime.handleOnceNonStreaming (file:///var/runtime/index.mjs:1173:29)"},
		{name: "python", payload: ikPythonFailure, own: `File "/var/task/app.py", line 4, in handler`, internal: `File "/var/runtime/bootstrap.py", line 60, in run`},
		{name: "go", payload: ikGoFailure, own: "handler (shop/main.go:12)", internal: "lambda.callHandler (github.com/aws/aws-lambda-go@v1.47.0/lambda/handler.go:3)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newResult(cli.Invocation{Payload: []byte(tt.payload), Error: "Unhandled"}, false, nil)
			lines := failureLines(r, 200)
			for _, want := range []struct {
				text string
				tone styles.Tone
			}{{tt.own, styles.ToneBody}, {tt.internal, styles.ToneFaint}} {
				drawn := hanging(want.text, want.tone, 2, 4, 200)[0]
				if !slices.Contains(lines, drawn) {
					t.Errorf("%q is not drawn in tone %v among:\n%s", want.text, want.tone, strings.Join(lines, "\n"))
				}
			}
		})
	}
}

// TestResultErrorWraps checks the error a function failed with wraps between its words at every
// width, as a long message from node was broken in the middle of them. A word wider than half the
// result may still be cut, rather than leave a short line.
func TestResultErrorWraps(t *testing.T) {
	message := "This file is being treated as an ES module because it has a '.js' file extension and '/private/tmp/shop/package.json' contains \"type\": \"module\". To treat it as a CommonJS script, rename it to use the '.cjs' file extension."
	payload, _ := json.Marshal(map[string]string{"errorType": "ReferenceError", "errorMessage": message})
	r := newResult(cli.Invocation{Error: "Unhandled", Payload: payload}, false, nil)
	for width := 30; width <= 90; width++ {
		lines, _ := resultLines(r, r.view, width)
		v := ansi.Strip(strings.Join(lines.window(0, lines.len()), "\n"))
		for word := range strings.FieldsSeq(message) {
			if len(word) <= width/2 && !strings.Contains(v, word) {
				t.Fatalf("width %d: %q is broken over two lines:\n%s", width, word, v)
			}
		}
	}
}

// TestInvokeResultScrolls checks pgdn and pgup scroll a long result and stop at its ends, and
// choosing another event goes back to its top.
func TestInvokeResultScrolls(t *testing.T) {
	f := newFake(t)
	var logs strings.Builder
	for i := range 60 {
		fmt.Fprintf(&logs, "line %d\n", i)
	}
	f.result = cli.Invocation{Payload: []byte(`{"a":1}`), Logs: logs.String()}
	m := invokeTabWith(t, f, map[string]string{"a.json": "{}", "b.json": "{}"})
	m = pressKeys(t, m, "i")
	if v := screen(m); !strings.Contains(v, "lines 1–") || !strings.Contains(v, "pgup/pgdn scroll") {
		t.Fatalf("a long result does not say it scrolls:\n%s", v)
	}
	m = pressKeys(t, m, "pgdown")
	if m.state.function.scroll == 0 || m.state.function.events.cursor != 0 {
		t.Fatalf("pgdn: scroll %d, cursor %d; want the result scrolled and the event kept", m.state.function.scroll, m.state.function.events.cursor)
	}
	for range 20 {
		m = pressKeys(t, m, "pgdown")
	}
	bottom := m.state.function.scroll
	if v := screen(m); !strings.Contains(v, "line 59") {
		t.Errorf("pgdn to the end does not show the last line:\n%s", v)
	}
	if m = pressKeys(t, m, "pgup"); m.state.function.scroll >= bottom {
		t.Errorf("pgup at the bottom: scroll %d, was %d", m.state.function.scroll, bottom)
	}
	if m = pressKeys(t, m, "j"); m.state.function.scroll != 0 || m.selectedEvent() != "b.json" {
		t.Errorf("j: scroll %d on %q, want the top of b.json", m.state.function.scroll, m.selectedEvent())
	}
}

// TestInvokeRunning checks an invocation on its way shows on its own event, and where it runs.
func TestInvokeRunning(t *testing.T) {
	tests := []struct {
		name string
		key  string
		want string
	}{
		{name: "in aws", key: "i", want: "invoking api in aws…"},
		{name: "here", key: "I", want: "running api on this machine…"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := invokeTabWith(t, newFake(t), map[string]string{"a.json": "{}", "b.json": "{}"})
			m, _ = update(t, m, press(tt.key))
			if v := screen(m); !strings.Contains(v, tt.want) {
				t.Errorf("no %q while it runs:\n%s", tt.want, v)
			}
			m, _ = update(t, m, press("j"))
			if v := screen(m); strings.Contains(v, tt.want) || !strings.Contains(v, "i invokes api") {
				t.Errorf("the other event shows the invocation of the first:\n%s", v)
			}
		})
	}
}

// TestInvokeHints checks the result of an event not yet sent offers what works for the function:
// i once it is deployed, shift+i where it is the project here.
func TestInvokeHints(t *testing.T) {
	tests := []struct {
		name    string
		app     string
		lambda  string
		want    string
		notWant string
	}{
		{name: "deployed, here", app: "orders", lambda: "orders-dev-api", want: "shift+i runs it on this machine"},
		{name: "deployed, elsewhere", lambda: "orders-dev-api", want: "i invokes api in aws", notWant: "shift+i runs"},
		{name: "not deployed, here", app: "orders", want: "api is not deployed yet: shift+i runs it on this machine"},
		{name: "not deployed, elsewhere", want: "api is not deployed yet", notWant: "shift+i runs"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFake(t)
			m := pressKeys(t, invokeTabWith(t, f, nil), "v")
			f.app = tt.app
			m.state.function.lambdaName = tt.lambda
			v := screen(m)
			if !strings.Contains(v, tt.want) || (tt.notWant != "" && strings.Contains(v, tt.notWant)) {
				t.Errorf("want %q and not %q in:\n%s", tt.want, tt.notWant, v)
			}
		})
	}
}

// TestInvokeEvents checks the events are listed with their size and when they changed, the one
// that is not json marked, and the payload of the one under the cursor shown beside them.
func TestInvokeEvents(t *testing.T) {
	m := invokeTabWith(t, newFake(t), map[string]string{"get-order.json": `{"id": 1, "items": [1, 2]}`, "broken.json": "{oops"})
	v := screen(m)
	for _, want := range []string{"event", "size", "modified", "broken  not json", "5 b", "get-order", "26 b", "0s ago", "payload 5 b", "▲ broken.json is not json, on line 1", "{oops"} {
		if !strings.Contains(v, want) {
			t.Errorf("no %q in:\n%s", want, v)
		}
	}
	v = screen(pressKeys(t, m, "j"))
	for _, want := range []string{"payload 26 b", `"id": 1`, `"items": [`} {
		if !strings.Contains(v, want) {
			t.Errorf("get-order: no %q in:\n%s", want, v)
		}
	}
}

// TestInvokePane checks v switches the pane between the payload and the result and back, i shows
// the result, and c copies what the pane shows: the event as its file holds it, what the function
// returned, or the error it failed with as the cli writes it; shift+c copies the request id.
func TestInvokePane(t *testing.T) {
	tests := []struct {
		name    string
		result  cli.Invocation
		keys    []string
		want    string
		notWant string
		copied  string
	}{
		{name: "payload", keys: []string{"c"}, want: `"id": 7`, copied: `{"id": 7}`},
		{name: "result", result: cli.Invocation{Payload: []byte(`{"ok":true}`)}, keys: []string{"i", "c"}, want: `"ok": true`, copied: `{"ok":true}`},
		{name: "back to the payload", result: cli.Invocation{Payload: []byte(`{"ok":true}`)}, keys: []string{"i", "v", "c"}, want: `"id": 7`, notWant: `"ok"`, copied: `{"id": 7}`},
		{name: "to the result again", result: cli.Invocation{Payload: []byte(`{"ok":true}`)}, keys: []string{"i", "v", "v", "c"}, want: `"ok": true`, notWant: `"id"`, copied: `{"ok":true}`},
		{name: "failure", result: cli.Invocation{Payload: []byte(ikNodeFailure), Error: "Unhandled"}, keys: []string{"i", "c"}, want: "TypeError",
			copied: "TypeError: x is undefined\n    at handler (file:///var/task/index.mjs:3:9)\n    at Runtime.handleOnceNonStreaming (file:///var/runtime/index.mjs:1173:29)"},
		{name: "request id", result: cli.Invocation{Payload: []byte(`{}`), RequestID: "r-9"}, keys: []string{"i", "C"}, want: "request r-9", copied: "r-9"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFake(t)
			f.result = tt.result
			m := invokeTabWith(t, f, map[string]string{"a.json": `{"id": 7}`})
			m = pressKeys(t, m, tt.keys...)
			if v := screen(m); !strings.Contains(v, tt.want) || tt.notWant != "" && strings.Contains(v, tt.notWant) {
				t.Errorf("want %q and not %q in:\n%s", tt.want, tt.notWant, v)
			}
			if got := m.paneCopyValue(); tt.keys[len(tt.keys)-1] == "c" && got != tt.copied {
				t.Errorf("c copies %q, want %q", got, tt.copied)
			}
			if !strings.HasPrefix(m.status, "copied ") {
				t.Errorf("status %q, want what was copied", m.status)
			}
			if tt.keys[len(tt.keys)-1] == "C" && m.status != "copied "+tt.copied {
				t.Errorf("status %q, want the request id copied", m.status)
			}
		})
	}
}

// TestInvokeJSON checks enter gives the keys to the json of the pane, where space folds, z
// unfolds everything, / searches, c copies the value and shift+c the path under the cursor, and
// esc and q give the keys back to the events; and the json's folds stay with its result when
// the pane shows another and comes back.
func TestInvokeJSON(t *testing.T) {
	f := newFake(t)
	f.result = cli.Invocation{Payload: []byte(`{"order":{"id":7,"lines":[1,2]},"total":3}`)}
	m := invokeTabWith(t, f, map[string]string{"a.json": `{"id": 1}`, "b.json": `{"id": 2}`})
	m = pressKeys(t, m, "i", "enter")
	if !m.state.function.pane.focused || m.state.json(m.page) == nil {
		t.Fatal("enter does not give the keys to the json")
	}
	m = pressKeys(t, m, "j", "space")
	if v := screen(m); !strings.Contains(v, `"order": { … 2 keys }`) {
		t.Errorf("space does not fold the order:\n%s", v)
	}
	m = pressKeys(t, m, "C")
	if m.status != "copied the path .order" {
		t.Errorf("shift+c: status %q, want the path copied", m.status)
	}
	m = pressKeys(t, m, "c")
	if m.status != "copied the value of .order" {
		t.Errorf("c: status %q, want the value copied", m.status)
	}
	m = pressKeys(t, m, "/")
	m = typeText(t, m, "total")
	m = pressKeys(t, m, "enter")
	if p := m.state.function.pane.view.path(); p != ".total" {
		t.Errorf("the search for total is on %q", p)
	}
	m = pressKeys(t, m, "esc", "esc")
	if m.state.function.pane.focused || m.page != functionPage {
		t.Fatalf("esc: focused %v on page %v, want the keys back on the events", m.state.function.pane.focused, m.page)
	}
	m = pressKeys(t, m, "j", "k")
	if v := screen(m); !strings.Contains(v, `"order": { … 2 keys }`) {
		t.Errorf("the fold of the result is lost after choosing another event:\n%s", v)
	}
	m = pressKeys(t, m, "enter", "q")
	if m.state.function.pane.focused || m.page != functionPage {
		t.Errorf("q: focused %v on page %v, want the keys back on the events", m.state.function.pane.focused, m.page)
	}
}

// TestInvokeLayout draws the invoke tab at a narrow, a wide and a large terminal, for what an
// invocation ends in, a payload of 2 mb and an event that is not json, its payload and its result,
// from the events and with the keys in its json, and checks it stays inside the terminal, the
// events beside the pane when it is wide and over it when it is narrow.
func TestInvokeLayout(t *testing.T) {
	big := perfInvocation().Payload
	results := map[string]cli.Invocation{
		"ok":        {Payload: []byte(`{"statusCode":200,"body":"` + strings.Repeat("long words ", 40) + `"}`), RequestID: "r-1", Logs: ikReport("r-1", "")},
		"node":      {Payload: []byte(ikNodeFailure), Error: "Unhandled", Logs: ikReport("r-1", "error")},
		"python":    {Payload: []byte(ikPythonFailure), Error: "Unhandled"},
		"go":        {Payload: []byte(ikGoFailure), Error: "Unhandled"},
		"timed out": {Payload: []byte(ikTimeout), Error: "Unhandled", Logs: ikReport("r-1", "timeout")},
		"2 mb":      {Payload: big},
		"not json":  {Payload: []byte("plain \x1b[2J words")},
	}
	events := map[string]string{"big.json": string(big), "broken.json": "{\n\x1b[2J" + strings.Repeat("x", 300), "small.json": `{"id":1}`}
	for _, size := range [][2]int{{78, 22}, {120, 40}, {200, 60}} {
		w, h := size[0], size[1]
		for name, inv := range results {
			f := newFake(t)
			f.result = inv
			m := invokeTabWith(t, f, events)
			m, _ = update(t, m, tea.WindowSizeMsg{Width: w, Height: h})
			m = pressKeys(t, m, "j", "j")
			for _, step := range []string{"payload", "i", "enter", "z", "esc", "v", "k", "k", "enter"} {
				if step != "payload" {
					m = pressKeys(t, m, step)
				}
				v := m.render()
				lines := strings.Split(v, "\n")
				if len(lines) > h {
					t.Errorf("%dx%d %s after %s: %d lines", w, h, name, step, len(lines))
				}
				for i, line := range lines {
					if lw := ansi.StringWidth(line); lw > w {
						t.Errorf("%dx%d %s after %s: line %d is %d wide", w, h, name, step, i, lw)
						break
					}
				}
				if strings.Contains(v, "\x1b[2J") {
					t.Errorf("%dx%d %s after %s: an escape reached the view", w, h, name, step)
				}
			}
			if wide := m.invokeLayout().wide; wide != (w >= invokeWideWidth+chromeWidth) {
				t.Errorf("%dx%d: wide %v", w, h, wide)
			}
		}
	}
}

// TestEditEventWithoutEditor checks e says how to set the editor when it cannot find one, before
// it creates an event.
func TestEditEventWithoutEditor(t *testing.T) {
	tests := []struct {
		name   string
		editor string
		want   string
	}{
		{name: "unset", want: "could not find the editor vi"},
		{name: "blank", editor: "   ", want: "could not find the editor vi"},
		{name: "missing", editor: "no-such-editor --wait", want: "could not find the editor no-such-editor"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("PATH", t.TempDir())
			t.Setenv("VISUAL", "")
			t.Setenv("EDITOR", tt.editor)
			f := newFake(t)
			m := pressKeys(t, invokeTabWith(t, f, nil), "e")
			if !strings.Contains(m.error, tt.want) || !strings.Contains(m.error, "set $EDITOR") {
				t.Errorf("error %q, want %q", m.error, tt.want)
			}
			if _, err := os.Stat(filepath.Join(f.events, "api", defaultEventFile)); err == nil {
				t.Error("an event was created for an editor that is not there")
			}
		})
	}
}

// TestEventEdited checks what closing the editor shows: the events read again, a warning when
// the event is no longer json, and the editor's own failure.
func TestEventEdited(t *testing.T) {
	tests := []struct {
		name   string
		body   string
		err    error
		status string
		error  string
	}{
		{name: "saved", body: `{"id": 2}`},
		{name: "not json", body: `{"id": `, status: "a.json is not json: e edits it"},
		{name: "editor failed", body: "{}", err: errors.New("exit status 1"), error: "the editor exited with an error: exit status 1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFake(t)
			m := invokeTabWith(t, f, map[string]string{"a.json": "{}"})
			if err := os.WriteFile(filepath.Join(f.events, "api", "b.json"), []byte("{}"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(f.events, "api", "a.json"), []byte(tt.body), 0o600); err != nil {
				t.Fatal(err)
			}
			m, _ = update(t, m, eventEditedMsg{err: tt.err})
			if got := m.state.function.eventFiles; len(got) != 2 || m.selectedEvent() != "a.json" {
				t.Errorf("events %v on %q, want both and the cursor kept on a.json", got, m.selectedEvent())
			}
			if m.status != tt.status || m.error != tt.error {
				t.Errorf("status %q and error %q, want %q and %q", m.status, m.error, tt.status, tt.error)
			}
		})
	}
}

// TestFunctionError checks the error a function failed with is told by its type and message,
// or by the kind of error lambda says when what it returned has no message.
func TestFunctionError(t *testing.T) {
	tests := []struct {
		name    string
		payload string
		want    string
	}{
		{name: "type and message", payload: `{"errorType":"Error","errorMessage":"boom"}`, want: "Error: boom"},
		{name: "message", payload: `{"errorMessage":"Task timed out after 3.00 seconds"}`, want: "Task timed out after 3.00 seconds"},
		{name: "type", payload: `{"errorType":"Runtime.ExitError"}`, want: "Unhandled"},
		{name: "not json", payload: "exit status 2", want: "Unhandled"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newResult(cli.Invocation{Payload: []byte(tt.payload), Error: "Unhandled"}, false, nil)
			if got := functionError(r); got != tt.want {
				t.Errorf("functionError = %q, want %q", got, tt.want)
			}
		})
	}
}
