package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestEventFrom checks the event comes from --data, --path or is {}, and one that is not json is
// refused with where it came from.
func TestEventFrom(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "good.json")
	bad := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(good, []byte(`{"id": 1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bad, []byte(`{id: 1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name     string
		data     string
		path     string
		want     string
		failWith string
	}{
		{name: "neither", want: "{}"},
		{name: "data", data: `{"job": 1}`, want: `{"job": 1}`},
		{name: "a string", data: `"hello"`, want: `"hello"`},
		{name: "path", path: good, want: `{"id": 1}`},
		{name: "both", data: "{}", path: good, failWith: "not both"},
		{name: "data not json", data: "{bad", failWith: "the event in --data is not json"},
		{name: "file not json", path: bad, failWith: "the event in " + bad + " is not json"},
		{name: "no file", path: filepath.Join(dir, "nope.json"), failWith: "reading the event"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := eventFrom(tt.data, tt.path)
			if tt.failWith != "" {
				if err == nil || !strings.Contains(err.Error(), tt.failWith) {
					t.Fatalf("eventFrom = %v, want it to say %q", err, tt.failWith)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tt.want {
				t.Errorf("event = %s, want %s", got, tt.want)
			}
		})
	}
}

// TestFunctionError checks a function that failed in aws says the type and message of its error
// with its stack trace indented under them, or what lambda calls the failure and what the
// function returned when it returned no error of its own.
func TestFunctionError(t *testing.T) {
	tests := []struct {
		name    string
		payload string
		want    string
	}{
		{name: "node", payload: `{"errorType":"Error","errorMessage":"it went boom","trace":["Error: it went boom"]}`, want: "Error: it went boom"},
		{name: "node with its stack", payload: `{"errorType":"Error","errorMessage":"boom","stackTrace":["Error: boom","    at handler (file:///var/task/index.mjs:2:9)","    at Runtime.handleOnceNonStreaming (file:///var/runtime/index.mjs:1:1)"]}`,
			want: "Error: boom\n    at handler (file:///var/task/index.mjs:2:9)\n    at Runtime.handleOnceNonStreaming (file:///var/runtime/index.mjs:1:1)"},
		{name: "python", payload: `{"errorMessage":"boom","errorType":"ValueError","requestId":"r","stackTrace":["  File \"/var/task/app.py\", line 2, in handler\n    raise ValueError(\"boom\")\n"]}`,
			want: "ValueError: boom\n    File \"/var/task/app.py\", line 2, in handler\n      raise ValueError(\"boom\")"},
		{name: "go", payload: `{"errorMessage":"no such order","errorType":"errorString"}`, want: "errorString: no such order"},
		{name: "go with its stack", payload: `{"errorMessage":"nil map","errorType":"runtime.Error","stackTrace":[{"path":"main.go","line":12,"label":"handler"}]}`,
			want: "runtime.Error: nil map\n    handler (main.go:12)"},
		{name: "message alone", payload: `{"errorMessage":"Task timed out after 3.00 seconds"}`, want: "Task timed out after 3.00 seconds"},
		{name: "not an error", payload: `{"ok":false,"why":"x"}`, want: "Unhandled, returning\n    {\n      \"ok\": false,\n      \"why\": \"x\"\n    }"},
		{name: "a string", payload: `"oops"`, want: "Unhandled, returning\n    \"oops\""},
		{name: "not json", payload: `oops`, want: "Unhandled"},
		{name: "empty", payload: ``, want: "Unhandled"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := functionError([]byte(tt.payload), "Unhandled"); got != tt.want {
				t.Errorf("functionError =\n%s\nwant\n%s", got, tt.want)
			}
		})
	}
}

// TestDescribeFailure checks a function that failed on this machine says its error as one that
// failed in aws does.
func TestDescribeFailure(t *testing.T) {
	tests := []struct {
		name    string
		typ     string
		message string
		trace   []string
		want    string
	}{
		{name: "no trace", typ: "Runtime.ExitError", message: "the function's process exited", want: "Runtime.ExitError: the function's process exited"},
		{name: "no type", message: "boom", want: "boom"},
		{name: "trace", typ: "TypeError", message: "x is undefined", trace: []string{"TypeError: x is undefined", "    at a (a.js:1:1)", "", "      at b (b.js:2:2)  "},
			want: "TypeError: x is undefined\n    at a (a.js:1:1)\n      at b (b.js:2:2)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := describeFailure(tt.typ, tt.message, tt.trace); got != tt.want {
				t.Errorf("describeFailure =\n%s\nwant\n%s", got, tt.want)
			}
		})
	}
}

// TestPrintPayload checks what a function returned is written byte for byte when piped, odd
// white space, unicode and a missing newline kept, and on a terminal indented, coloured unless
// colour is off, with a newline after.
func TestPrintPayload(t *testing.T) {
	odd := " {\"b\" :\t[1, 2.50 ],\r\n\"a\":\"日本 \\u00e9 🚀\"}  \n\n"
	tests := []struct {
		name     string
		payload  string
		terminal bool
		colour   bool
		want     string
	}{
		{name: "piped", payload: odd, want: odd},
		{name: "piped without a newline", payload: `{"ok":true}`, want: `{"ok":true}`},
		{name: "piped, not json", payload: "plain\x1b[0m", want: "plain\x1b[0m"},
		{name: "piped, empty", payload: "", want: ""},
		{name: "terminal", payload: odd, terminal: true, want: "{\n  \"b\": [\n    1,\n    2.50\n  ],\n  \"a\": \"日本 \\u00e9 🚀\"\n}\n"},
		{name: "terminal in colour", payload: `{"a":1}`, terminal: true, colour: true, want: "{\n  \x1b[36m\"a\"\x1b[0m: \x1b[33m1\x1b[0m\n}\n"},
		{name: "terminal, not json", payload: "plain", terminal: true, want: "plain\n"},
		{name: "terminal, null", payload: "null", terminal: true, colour: true, want: "null\n"},
		{name: "terminal, empty", payload: "", terminal: true, want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			if err := printPayload(&out, []byte(tt.payload), tt.terminal, palette{on: tt.colour}); err != nil {
				t.Fatal(err)
			}
			if out.String() != tt.want {
				t.Errorf("got:\n%q\nwant:\n%q", out.String(), tt.want)
			}
		})
	}
}
