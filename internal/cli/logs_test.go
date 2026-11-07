package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// TestLogPrinter checks an event is written after its time, a trailing newline dropped, and the
// lines after the first indented under it.
func TestLogPrinter(t *testing.T) {
	const ts = 1767225600123
	tests := []struct {
		name    string
		layout  string
		message string
		want    string
	}{
		{name: "one line", layout: timeLayout, message: "hello\n", want: "{t}  hello\n"},
		{name: "crlf", layout: timeLayout, message: "hello\r\n", want: "{t}  hello\n"},
		{name: "stack trace", layout: timeLayout, message: "Error: boom\n    at handler\n", want: "{t}  Error: boom\n                  at handler\n"},
		{name: "with the date", layout: dateTimeLayout, message: "a\nb", want: "{t}  a\n                         b\n"},
		{name: "json when piped", layout: timeLayout, message: `{"level":"INFO","msg":"hi"}`, want: `{t}  {"level":"INFO","msg":"hi"}` + "\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			p := &logPrinter{w: &out, layout: tt.layout}
			p.print(ts, "stream", tt.message)
			stamp := time.UnixMilli(ts).Local().Format(tt.layout)
			want := stamp + tt.want[len("{t}"):]
			if out.String() != want {
				t.Errorf("printed %q, want %q", out.String(), want)
			}
			if p.printed != 1 {
				t.Errorf("printed = %d, want 1", p.printed)
			}
		})
	}
}

// TestStructuredLine checks a line of json a structured logger wrote is shown on a terminal with
// its level and message first and its other fields after them, in the order they were written,
// tinted only in colour, and that any other line is left as it is.
func TestStructuredLine(t *testing.T) {
	tests := []struct {
		name   string
		line   string
		colour bool
		want   string
		notOK  bool
	}{
		{name: "powertools", line: `{"level":"INFO","location":"handler:12","message":"order placed","timestamp":"2026-11-06T10:00:00.123Z","service":"orders","cold_start":true}`,
			want: "INFO   order placed  location=handler:12 timestamp=2026-11-06T10:00:00.123Z service=orders cold_start=true"},
		{name: "pino", line: `{"level":50,"time":1762423200123,"pid":7,"hostname":"169.254.1.1","msg":"payment failed","err":{"type":"Error"}}`,
			want: `ERROR  payment failed  time=1762423200123 pid=7 hostname=169.254.1.1 err={"type":"Error"}`},
		{name: "zap", line: `{"level":"warn","ts":1762423200.5,"caller":"main.go:20","msg":"slow query","took":"1.2 s"}`,
			want: `WARN   slow query  ts=1762423200.5 caller=main.go:20 took="1.2 s"`},
		{name: "python", line: `{"levelname": "DEBUG", "message": "cache miss", "name": "app", "key": ""}`,
			want: `DEBUG  cache miss  name=app key=""`},
		{name: "lambda's own", line: `{"timestamp":"2026-11-06T10:00:00Z","level":"ERROR","requestId":"r-1","message":"boom\n  at handler"}`,
			want: "ERROR  boom\n  at handler  timestamp=2026-11-06T10:00:00Z requestId=r-1"},
		{name: "no level", line: `{"msg":"hello"}`, want: "hello"},
		{name: "coloured", line: `{"level":"error","msg":"boom","id":1}`, colour: true,
			want: "\x1b[31mERROR\x1b[0m  boom  \x1b[2mid=1\x1b[0m"},
		{name: "info in colour", line: `{"msg":"ok","severity":"info"}`, colour: true, want: "\x1b[32mINFO \x1b[0m  ok"},
		{name: "controls", line: `{"msg":"a\u001b[2Jb","x":"\u009b"}`, want: `a\u001b[2Jb  x="\u009b"`},
		{name: "no message", line: `{"level":"INFO","event":"x"}`, notOK: true},
		{name: "not json", line: `START RequestId: abc Version: $LATEST`, notOK: true},
		{name: "broken json", line: `{"msg":"hi"`, notOK: true},
		{name: "two objects", line: `{"msg":"a"} {"msg":"b"}`, notOK: true},
		{name: "a list", line: `[{"msg":"a"}]`, notOK: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := structuredLine(tt.line, palette{on: tt.colour})
			if ok == tt.notOK {
				t.Fatalf("structuredLine = %q, %v, want %v", got, ok, !tt.notOK)
			}
			if got != tt.want {
				t.Errorf("got:\n%q\nwant:\n%q", got, tt.want)
			}
		})
	}
}

// TestLogPrinterTerminal checks a structured line on a terminal is indented under its time as any
// other line is.
func TestLogPrinterTerminal(t *testing.T) {
	const ts = 1767225600123
	var out bytes.Buffer
	p := &logPrinter{w: &out, layout: timeLayout, structured: true}
	p.print(ts, "s", `{"level":"ERROR","msg":"boom\nat handler"}`)
	p.print(ts, "s", "REPORT RequestId: abc")
	stamp := time.UnixMilli(ts).Local().Format(timeLayout)
	want := stamp + "  ERROR  boom\n" + strings.Repeat(" ", len(stamp)+2) + "at handler\n" + stamp + "  REPORT RequestId: abc\n"
	if out.String() != want {
		t.Errorf("got:\n%q\nwant:\n%q", out.String(), want)
	}
}

// TestLogPrinterJSON checks logs -o json writes one object a line, its keys sorted, with the
// fields of a message that is a json object, their numbers as they were written.
func TestLogPrinterJSON(t *testing.T) {
	const ts = 1767225600123
	const stream = "2026/01/01/[$LATEST]0123abcd"
	tests := []struct {
		name    string
		message string
		want    string
	}{
		{name: "text", message: "hello <world> & you\n",
			want: `{"message":"hello <world> & you","stream":"2026/01/01/[$LATEST]0123abcd","timestamp":"2026-01-01T00:00:00.123Z"}`},
		{name: "json", message: `{"msg":"hi","level":30,"n":1.50}`,
			want: `{"fields":{"level":30,"msg":"hi","n":1.50},"message":"{\"msg\":\"hi\",\"level\":30,\"n\":1.50}","stream":"2026/01/01/[$LATEST]0123abcd","timestamp":"2026-01-01T00:00:00.123Z"}`},
		{name: "empty object", message: `{}`,
			want: `{"message":"{}","stream":"2026/01/01/[$LATEST]0123abcd","timestamp":"2026-01-01T00:00:00.123Z"}`},
		{name: "a list", message: `[1]`,
			want: `{"message":"[1]","stream":"2026/01/01/[$LATEST]0123abcd","timestamp":"2026-01-01T00:00:00.123Z"}`},
		{name: "unicode and a stack", message: "Error: héllo 🚀\n    at x",
			want: `{"message":"Error: héllo 🚀\n    at x","stream":"2026/01/01/[$LATEST]0123abcd","timestamp":"2026-01-01T00:00:00.123Z"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			p := &logPrinter{w: &out, layout: timeLayout, json: true, structured: true}
			p.print(ts, stream, tt.message)
			if out.String() != tt.want+"\n" {
				t.Errorf("got:\n%s\nwant:\n%s", out.String(), tt.want)
			}
			if !json.Valid(out.Bytes()) {
				t.Errorf("%s is not json", out.String())
			}
		})
	}
}
