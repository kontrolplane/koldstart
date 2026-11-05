package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/kontrolplane/koldstart/internal/tui/styles"
)

// toneOf is the tone of the span of c that holds part, and whether there is one.
func toneOf(c cell, part string) (styles.Tone, bool) {
	for _, s := range c {
		if strings.Contains(s.Text, part) {
			return s.Tone, true
		}
	}
	return 0, false
}

// TestWrapCell checks a long line breaks at a space or after a comma that fills at least half
// the width, and hard where none does, that no line is wider than the width, wide runes
// included, and that no text is lost but the spaces broken at.
func TestWrapCell(t *testing.T) {
	tests := []struct {
		name  string
		line  string
		width int
		want  []string
	}{
		{name: "fits", line: "hello world", width: 20, want: []string{"hello world"}},
		{name: "at a space", line: "hello there world", width: 12, want: []string{"hello there", "world"}},
		{name: "after a comma", line: `{"a":"bbbb","c":"dddd"}`, width: 14, want: []string{`{"a":"bbbb",`, `"c":"dddd"}`}},
		{name: "hard without a space", line: "abcdefghijkl", width: 5, want: []string{"abcde", "fghij", "kl"}},
		{name: "hard when the space is early", line: "a bcdefghijkl", width: 6, want: []string{"a bcde", "fghijk", "l"}},
		{name: "wide runes", line: "漢字漢字漢", width: 5, want: []string{"漢字", "漢字", "漢"}},
		{name: "a trailing space", line: "abcd ", width: 4, want: []string{"abcd"}},
		{name: "empty", line: "", width: 4, want: []string{""}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := wrapCell(text(tt.line, styles.ToneBody), tt.width)
			var lines []string
			for _, c := range got {
				line := plainText(c)
				if w := ansi.StringWidth(line); w > tt.width {
					t.Errorf("%q is %d wide, more than %d", line, w, tt.width)
				}
				lines = append(lines, line)
			}
			if strings.Join(lines, "|") != strings.Join(tt.want, "|") {
				t.Errorf("got %q, want %q", lines, tt.want)
			}
		})
	}
}

// TestWrapCellKeepsTones checks the spans keep their tones across a break.
func TestWrapCellKeepsTones(t *testing.T) {
	c := cell{styles.S("level=", styles.ToneFaint), styles.S("error", styles.ToneDanger), styles.S(" something went wrong", styles.ToneBody)}
	got := wrapCell(c, 12)
	if len(got) != 3 {
		t.Fatalf("%d lines, want 3: %+v", len(got), got)
	}
	if tone, _ := toneOf(got[0], "err"); tone != styles.ToneDanger {
		t.Errorf("the level is in tone %v", tone)
	}
	if tone, _ := toneOf(got[2], "wrong"); tone != styles.ToneBody {
		t.Errorf("the message is in tone %v", tone)
	}
}

// TestTint checks the tone of the part of a line that matters: lambda's own lines faint, a
// timeout in danger, a message in the text ink, a level by how bad it is, and broken or nested
// json, logfmt with quoted values, colours and other escapes handled.
func TestTint(t *testing.T) {
	tests := []struct {
		name string
		line string
		part string
		tone styles.Tone
	}{
		{name: "start", line: "START RequestId: 1 Version: $LATEST", part: "START", tone: styles.ToneFaint},
		{name: "report", line: "REPORT RequestId: 1\tDuration: 2.00 ms", part: "Duration", tone: styles.ToneFaint},
		{name: "a report of a timeout", line: "REPORT RequestId: 1\tDuration: 6000.00 ms\tStatus: timeout", part: "timeout", tone: styles.ToneDanger},
		{name: "init report", line: "INIT_REPORT Init Duration: 10000.00 ms\tPhase: init\tStatus: error", part: "Phase", tone: styles.ToneDanger},
		{name: "extension", line: "EXTENSION\tName: x\tState: Ready\tEvents: [INVOKE]", part: "Name", tone: styles.ToneFaint},
		{name: "a timeout", line: "2026-11-05T10:00:00.000Z 1 Task timed out after 6.00 seconds", part: "Task timed out", tone: styles.ToneDanger},
		{name: "json message", line: `{"level":"info","msg":"order placed"}`, part: "order placed", tone: styles.ToneText},
		{name: "json key", line: `{"level":"info","msg":"order placed"}`, part: `"msg"`, tone: styles.ToneFaint},
		{name: "json error level", line: `{"level": "error", "msg":"x"}`, part: "error", tone: styles.ToneDanger},
		{name: "json warning level", line: `{"severity":"WARNING","msg":"x"}`, part: "WARNING", tone: styles.ToneWarning},
		{name: "nested message", line: `{"msg":"outer","data":{"msg":"inner"}}`, part: "inner", tone: styles.ToneBody},
		{name: "broken json", line: `{"level":"info","msg":"cut off`, part: "cut off", tone: styles.ToneText},
		{name: "logfmt message", line: `time=2026-11-05T10:00:00Z level=info msg="hello world"`, part: "hello world", tone: styles.ToneText},
		{name: "logfmt escaped quote", line: `level=info msg="say \"hi\" now" user=levi`, part: `say \"hi\" now`, tone: styles.ToneText},
		{name: "logfmt error level", line: `level=error msg=boom`, part: "error", tone: styles.ToneDanger},
		{name: "node error", line: "2026-11-05T10:00:00.001Z\t1\tERROR\tboom", part: "ERROR", tone: styles.ToneDanger},
		{name: "node message as json", line: "2026-11-05T10:00:00.001Z\t1\tINFO\t{\"msg\":\"stored\"}", part: "stored", tone: styles.ToneText},
		{name: "colours dropped", line: "\x1b[31mred\x1b[0m text", part: "red text", tone: styles.ToneBody},
		{name: "a leading timestamp", line: "2026-11-05 10:00:00.123 started", part: "2026", tone: styles.ToneFaint},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := tintLine(tt.line)
			tone, ok := toneOf(c, tt.part)
			if !ok || tone != tt.tone {
				t.Errorf("%q in %q: tone %v (found %v), want %v; spans %+v", tt.part, tt.line, tone, ok, tt.tone, c)
			}
		})
	}
}

// TestTintLineEscapes checks escape sequences other than colours are shown escaped, so none
// reaches the terminal.
func TestTintLineEscapes(t *testing.T) {
	tests := []struct {
		name string
		line string
	}{
		{name: "a hyperlink", line: "x\x1b]8;;https://evil.example/\x07y"},
		{name: "clear the screen", line: "x\x1b[2Jy"},
		{name: "a c1 csi", line: "x\u009b31my"},
		{name: "in a node line", line: "2026-11-05T10:00:00.001Z\t1\tERROR\tx\x1b[2Jy"},
		{name: "in json", line: `{"msg":"x` + "\x1b[2J" + `y"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := plainText(tintLine(tt.line))
			if strings.ContainsAny(got, "\x1b\x07\u009b") {
				t.Errorf("an escape is left in %q", got)
			}
		})
	}
}

// TestHighlight checks what a filter looks for is marked wherever it is, ignoring case, and the
// rest keeps its tones.
func TestHighlight(t *testing.T) {
	tests := []struct {
		name string
		c    cell
		want string
		hits int
	}{
		{name: "none", c: text("order placed", styles.ToneBody), want: "refund", hits: 0},
		{name: "one", c: text("order placed", styles.ToneBody), want: "placed", hits: 1},
		{name: "ignoring case", c: text("Order placed, ORDER kept", styles.ToneBody), want: "order", hits: 2},
		{name: "across spans", c: cell{styles.S("msg=", styles.ToneFaint), styles.S("order", styles.ToneText)}, want: "=ord", hits: 1},
		{name: "nothing to find", c: text("order", styles.ToneBody), want: "", hits: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := highlight(tt.c, tt.want)
			if plainText(got) != plainText(tt.c) {
				t.Fatalf("the text changed: %q", plainText(got))
			}
			hits := 0
			for _, s := range got {
				if s.Tone == styles.ToneAccent && s.Bold {
					hits++
					if !strings.EqualFold(s.Text, tt.want) {
						t.Errorf("marked %q, looking for %q", s.Text, tt.want)
					}
				}
			}
			if hits != tt.hits {
				t.Errorf("%d spans marked, want %d: %+v", hits, tt.hits, got)
			}
		})
	}
}

// TestLogCells checks a message draws a line for each of its lines, and none for the newlines
// it ends in.
func TestLogCells(t *testing.T) {
	tests := []struct {
		name    string
		message string
		want    []string
	}{
		{name: "one line", message: "hello", want: []string{"hello"}},
		{name: "a trailing newline", message: "hello\n", want: []string{"hello"}},
		{name: "crlf", message: "one\r\ntwo\r\n", want: []string{"one", "two"}},
		{name: "a stack", message: "Error: boom\n    at handler (index.js:1)\n", want: []string{"Error: boom", "    at handler (index.js:1)"}},
		{name: "a blank line inside", message: "one\n\ntwo", want: []string{"one", "", "two"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got []string
			for _, c := range logCells(tt.message, 80) {
				got = append(got, plainText(c))
			}
			if strings.Join(got, "|") != strings.Join(tt.want, "|") {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}
