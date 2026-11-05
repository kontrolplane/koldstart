package tui

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/kontrolplane/koldstart/internal/cli"
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

// TestInvokeResult checks what an invocation shows: what it returned, the error it failed with,
// an empty payload, and an invocation that did not reach the function.
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
			want:   []string{"✓ returned in 120ms in aws", "returned ─", `"statusCode": 200`},
			status: "api returned in 120ms",
		},
		{
			name:   "failed",
			result: cli.Invocation{Payload: []byte(`{"errorType":"TypeError","errorMessage":"x is undefined","stackTrace":["at handler"]}`), Error: "Unhandled", Duration: 3e6},
			want:   []string{"✗ failed in 3ms in aws", "TypeError: x is undefined", "error ─", `"at handler"`},
			status: "api failed: TypeError: x is undefined",
		},
		{
			name:   "failed without a payload",
			result: cli.Invocation{Error: "Unhandled"},
			want:   []string{"✗ failed in aws", "Unhandled", "nothing"},
			status: "api failed: Unhandled",
		},
		{
			name:   "returned nothing",
			result: cli.Invocation{Duration: 5e6},
			want:   []string{"✓ returned in 5ms", "nothing"},
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

// TestResultErrorWraps checks the error a function failed with wraps between its words at every
// width, as a long message from node was broken in the middle of them. A word wider than half the
// result may still be cut, rather than leave a short line.
func TestResultErrorWraps(t *testing.T) {
	message := "This file is being treated as an ES module because it has a '.js' file extension and '/private/tmp/shop/package.json' contains \"type\": \"module\". To treat it as a CommonJS script, rename it to use the '.cjs' file extension."
	payload, _ := json.Marshal(map[string]string{"errorType": "ReferenceError", "errorMessage": message})
	r := invocationResult{inv: cli.Invocation{Error: "Unhandled", Payload: payload}}
	for width := 30; width <= 90; width++ {
		lines := resultLines(r, width)
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
	f.result = cli.Invocation{Payload: []byte(`[` + strings.Repeat(`{"a":1},`, 60) + `{"a":2}]`)}
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
	if v := screen(m); !strings.Contains(v, `"a": 2`) {
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

// TestInvokeHints checks the invoke tab offers what works for the function: i once it is
// deployed, shift+i where it is the project here.
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
			m := invokeTabWith(t, f, nil)
			f.app = tt.app
			m.state.function.lambdaName = tt.lambda
			v := screen(m)
			if !strings.Contains(v, tt.want) || (tt.notWant != "" && strings.Contains(v, tt.notWant)) {
				t.Errorf("want %q and not %q in:\n%s", tt.want, tt.notWant, v)
			}
		})
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

// TestFunctionError checks the error a function failed with is told by its type and message.
func TestFunctionError(t *testing.T) {
	tests := []struct {
		name    string
		payload string
		want    string
	}{
		{name: "type and message", payload: `{"errorType":"Error","errorMessage":"boom"}`, want: "Error: boom"},
		{name: "message", payload: `{"errorMessage":"Task timed out after 3.00 seconds"}`, want: "Task timed out after 3.00 seconds"},
		{name: "type", payload: `{"errorType":"Runtime.ExitError"}`, want: "Runtime.ExitError"},
		{name: "not json", payload: "exit status 2", want: "Unhandled"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := functionError(cli.Invocation{Payload: []byte(tt.payload), Error: "Unhandled"}); got != tt.want {
				t.Errorf("functionError = %q, want %q", got, tt.want)
			}
		})
	}
}
