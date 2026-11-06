package tui

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"

	"github.com/kontrolplane/koldstart/internal/cli"
	"github.com/kontrolplane/koldstart/internal/tui/keys"
	"github.com/kontrolplane/koldstart/internal/tui/styles"
)

// fuzzSGR is the one escape sequence the interface writes itself: select graphic rendition, the
// colours and weight of what follows.
var fuzzSGR = regexp.MustCompile(`^\x1b\[[0-9;:]*m`)

// fuzzUnsafe reports whether the terminal would act on r rather than draw it: a control
// character, C0, DEL or C1, a bidi control, or a line or paragraph separator.
func fuzzUnsafe(r rune) bool {
	switch {
	case unicode.IsControl(r):
		return true
	case r == '\u061c', r == '\u200e', r == '\u200f', r == '\u2028', r == '\u2029':
		return true
	case r >= '\u202a' && r <= '\u202e', r >= '\u2066' && r <= '\u2069':
		return true
	}
	return false
}

// fuzzPlain checks text before it is painted: utf-8, and nothing the terminal would act on.
func fuzzPlain(t *testing.T, what, s string) {
	t.Helper()
	if !utf8.ValidString(s) {
		t.Fatalf("%s: %q is not utf-8", what, s)
	}
	for _, r := range s {
		if fuzzUnsafe(r) {
			t.Fatalf("%s: %q holds %U", what, s, r)
		}
	}
}

// fuzzDrawn checks painted text: utf-8, no escape sequence but a colour, no control character
// but the newlines between lines, and no line wider than width.
func fuzzDrawn(t *testing.T, what, s string, width int) {
	t.Helper()
	if !utf8.ValidString(s) {
		t.Fatalf("%s: %q is not utf-8", what, s)
	}
	for i := 0; i < len(s); {
		if s[i] == ansi.ESC {
			sgr := fuzzSGR.FindString(s[i:])
			if sgr == "" {
				t.Fatalf("%s: an escape sequence that is not a colour, at %d of %q", what, i, s)
			}
			i += len(sgr)
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if r != '\n' && fuzzUnsafe(r) {
			t.Fatalf("%s: %U at %d of %q", what, r, i, s)
		}
		i += size
	}
	for line := range strings.SplitSeq(s, "\n") {
		if w := ansi.StringWidth(line); w > width {
			t.Fatalf("%s: a line %d wide, more than %d: %q", what, w, width, ansi.Strip(line))
		}
	}
}

// fuzzLayout fits the layout to a terminal of width by height for one input, and puts back the
// layout the tests start with afterwards.
func fuzzLayout(t *testing.T, width, height int) {
	t.Helper()
	setLayout(width, height)
	t.Cleanup(func() { setLayout(120+chromeWidth, 30+chromeHeight) })
}

// fuzzWidth turns a fuzzed int into a width from 0 to n.
func fuzzWidth(w, n int) int {
	return int(uint(w) % uint(n+1))
}

// FuzzRenderCell checks a cell of any two spans, cleaned or not, is drawn exactly as wide as its
// column, painted, with nothing in it the terminal would act on.
func FuzzRenderCell(f *testing.F) {
	f.Add("abc", "", 5, false, false)
	f.Add("日本語", "🚀 go", 5, true, true)
	f.Add("a\x1b[2Jb", "\x9b31m", 8, false, true)
	f.Add("\U0001F469\u200d\U0001F4BB", "e\u0301\u20dd", 3, false, false)
	f.Add("\x01\ufe0f", "\t\n\r", 6, true, false)
	f.Add(strings.Repeat("x", 300), "y", 40, false, true)
	f.Fuzz(func(t *testing.T, a, b string, width int, right, selected bool) {
		width = fuzzWidth(width, 120)
		var bg = styles.P.Selection
		if !selected {
			bg = nil
		}
		c := cell{{Text: a, Tone: styles.ToneBody}, {Text: b, Tone: styles.ToneFaint, Bold: true}}
		got := renderCell(c, column{width: width, right: right}, bg)
		fuzzDrawn(t, "renderCell", got, width)
		if strings.Contains(got, "\n") {
			t.Fatalf("renderCell(%q, %q) breaks the line: %q", a, b, got)
		}
		if w := ansi.StringWidth(got); w != width {
			t.Fatalf("renderCell(%q, %q) is %d wide, want %d: %q", a, b, w, width, ansi.Strip(got))
		}
	})
}

// FuzzTint checks a line of a log of anything is tinted into clean spans, wrapped to the width,
// with what a filter looks for marked, and drawn beside its time no wider than the logs. A width
// is at least 2, the room a wide rune takes.
func FuzzTint(f *testing.F) {
	f.Add("START RequestId: 1 Version: $LATEST", "", 80)
	f.Add("2026-11-05T10:00:00.001Z\t1\tERROR\t{\"msg\":\"x\x1b[2Jy\",\"level\":\"error\"}", "x", 30)
	f.Add(`{"level":"warn","msg":"`+strings.Repeat("a long message, with commas, ", 9)+`"}`, "LONG", 24)
	f.Add("time=2026-11-05T10:00:00Z level=error msg=\"no \\\"order\\\"\" took=12ms", "order", 20)
	f.Add("\x1b[31mred\x1b[0m \x1b]8;;https://evil.example/\x07link\u202e\xff", "red", 10)
	f.Add("漢字漢字漢字 ☺\ufe0f 👍🏽 🇳🇱 e\u0301", "字", 5)
	f.Add("REPORT RequestId: 1\tDuration: 1 ms\tStatus: timeout\r\nTask timed out after 3.00 seconds", "", 1)
	f.Fuzz(func(t *testing.T, line, mark string, width int) {
		width = 2 + fuzzWidth(width, 160)
		for _, c := range markedLogCells(line, mark, width) {
			for _, s := range c {
				fuzzPlain(t, "a span of the logs", s.Text)
			}
			if w := ansi.StringWidth(plainText(c)); w > width {
				t.Fatalf("a line of the logs %d wide, more than %d: %q", w, width, plainText(c))
			}
			fuzzDrawn(t, "a line of the logs", renderCell(c, column{width: width}, nil), width)
		}
		e := cli.LogEvent{Time: time.Now(), Message: line}
		for _, r := range entryRows(e, mark, width+stampWidth) {
			fuzzDrawn(t, "a row of the logs", r.paint(width+stampWidth), width+stampWidth)
		}
	})
}

// FuzzJSONLines checks a value of any json is written as lines with nothing the terminal would act
// on, and that they wrap to the width. A width is at least 2, the room a wide rune takes. What an
// invocation returns is drawn by a jsonView, which FuzzJSONView checks.
func FuzzJSONLines(f *testing.F) {
	f.Add([]byte(`{"memory":128,"name":"orders-dev-api"}`), 40)
	f.Add([]byte(`{"KEY":{"$koldstart":"secret","source":"ssm","name":"/orders/key"},"x":"\u001b[2J\u202e"}`), 20)
	f.Add([]byte(`{"$koldstart":"join","parts":["arn:",{"$koldstart":"ref","resource":"table","attribute":"Arn"},"/*"]}`), 12)
	f.Add([]byte(`["`+strings.Repeat("v", 200)+`", 1e400, null, true, {"a":[[]]}]`), 7)
	f.Add([]byte("not json \x1b]0;t\x07 \xff"), 3)
	f.Add([]byte(`"\u2028\u0085\u009b"`), 1)
	f.Fuzz(func(t *testing.T, data []byte, width int) {
		width = 2 + fuzzWidth(width, 120)
		var v any
		if json.Unmarshal(data, &v) != nil {
			return
		}
		lines := jsonLines(v)
		for _, line := range lines {
			fuzzPlain(t, "a line of json", line)
			for _, part := range wrapIndented(line, width) {
				if w := ansi.StringWidth(part); w > width {
					t.Fatalf("wrapIndented(%q, %d) has a part %d wide: %q", line, width, w, part)
				}
			}
		}
		fuzzPlain(t, "compact json", styles.Clean(compactJSON(v)))
	})
}

// FuzzDiff checks the diff of any two values keeps every line of each, in order, and that the
// diff page draws it at any width with nothing the terminal would act on and no line too wide.
func FuzzDiff(f *testing.F) {
	f.Add([]byte(`{"memory":128,"timeout":6}`), []byte(`{"memory":256,"timeout":6}`), 80, "function/api")
	f.Add([]byte(`{"a":"`+strings.Repeat("x ", 100)+`"}`), []byte(`null`), 0, "x\x1b[2J\u202e")
	f.Add([]byte(`[1,2,3]`), []byte(`[3,2,1,{"k":"v"}]`), 300, "")
	f.Add([]byte(`{"deep":{"deeper":{"deepest":"`+strings.Repeat("漢", 60)+`"}}}`), []byte(`{}`), 9, "日本")
	f.Add([]byte(`{"p":"{\"a\":[1,\"{\\\"b\\\":2}\"]}"}`), []byte(`{"p":"{\"a\":[0,1,\"{\\\"b\\\":3}\"]}"}`), 40, "policy")
	f.Add([]byte(`[{"a":1},{"a":2}]`), []byte(`[{"a":0},{"a":1},{"a":2},{"a":2}]`), 76, "layers")
	f.Add([]byte(`{"arn":"arn:aws:iam::1:role/a-dev\u202e"}`), []byte(`{"arn":"arn:aws:iam::1:role/a-prod\u0007"}`), 1, "\x9b")
	f.Fuzz(func(t *testing.T, a, b []byte, width int, title string) {
		var before, after any
		if json.Unmarshal(a, &before) != nil {
			before = string(a)
		}
		if json.Unmarshal(b, &after) != nil {
			after = string(b)
		}
		was, is := docLines(before), docLines(after)
		lines := diffDocs(before, after)
		var gotWas, gotIs []string
		wantWas := make([]string, 0, len(was))
		for _, l := range lines {
			if l.op != '+' {
				gotWas = append(gotWas, diffKey(l.text))
			}
			if l.op != '-' {
				gotIs = append(gotIs, l.text)
			}
			for _, mk := range l.marks {
				if mk[0] < 0 || mk[0] >= mk[1] || mk[1] > len(l.text) || !utf8.ValidString(l.text[mk[0]:mk[1]]) {
					t.Fatalf("a mark %v of %q is not a part of it", mk, l.text)
				}
			}
		}
		for _, l := range was {
			wantWas = append(wantWas, diffKey(l))
		}
		if strings.Join(gotWas, "\n") != strings.Join(wantWas, "\n") || strings.Join(gotIs, "\n") != strings.Join(is, "\n") {
			t.Fatalf("the diff of %q and %q loses lines: %v", a, b, lines)
		}
		w := fuzzWidth(width, 300)
		fuzzLayout(t, w, 40)
		m := model{}
		m.state.diff = diffState{title: title, detail: title, lines: lines, change: -1, wrapped: &diffRows{}}
		for _, unfolded := range []bool{false, true} {
			m.state.diff.unfolded = unfolded
			m.state.diff.folds++
			for _, scroll := range []int{0, 1 << 30} {
				m.state.diff.scroll = scroll
				fuzzDrawn(t, "the diff page", m.diffView(), contentWidth)
				fuzzDrawn(t, "the diff's position", m.diffPosition(), contentWidth)
			}
			m.state.diff.jump(1)
			fuzzDrawn(t, "the diff on a change", m.diffView(), contentWidth)
		}
		for _, line := range is {
			for _, p := range wrapParts(line, w) {
				if p.from >= 0 && line[p.from:p.from+len(p.text)] != p.text {
					t.Fatalf("wrapParts(%q, %d) puts %q at %d", line, w, p.text, p.from)
				}
				if p.from < 0 && strings.TrimSpace(p.text) != "" {
					t.Fatalf("wrapParts(%q, %d) does not find %q in the line", line, w, p.text)
				}
			}
		}
	})
}

// fuzzJSONKeys are the keys FuzzJSONView presses, one for each byte of what it is given.
var fuzzJSONKeys = []string{"j", "k", "pgdown", "pgup", "g", "G", "enter", "space", "z", "c", "C", "/", "a", "enter", "n", "N", "esc"}

// FuzzJSONView checks a json view draws any bytes, json or not, at any width from 4, after any
// keys, as utf-8 with nothing the terminal would act on and no line wider than the width, and
// that jsonLineCell keeps every line of the json it tints whole and clean.
func FuzzJSONView(f *testing.F) {
	f.Add([]byte(`{"memory":128,"name":"orders-dev-api","tags":[1,2,{"a":null}]}`), 40, []byte{0, 0, 6, 8})
	f.Add([]byte(jvPolicy), 20, []byte{1, 5, 6, 6, 11, 12, 13, 14})
	f.Add([]byte(`{"KEY":{"$koldstart":"secret","source":"ssm","name":"/orders/key"},"x":"\u001b[2J\u202e"}`), 4, []byte{0, 9, 10})
	f.Add([]byte(`["`+strings.Repeat("日本", 100)+`", 1e400, true, {"a":[[]]}, "{\"b\":\"[1]\"}"]`), 7, []byte{5, 7, 7, 3})
	f.Add([]byte("not json \x1b]0;t\x07 \xff\n  line"), 200, []byte{0, 1})
	f.Add([]byte(`"`+strings.Repeat("a,b ", 300)+`"`), 23, []byte{8, 8})
	f.Fuzz(func(t *testing.T, data []byte, width int, pressed []byte) {
		width = 4 + fuzzWidth(width, 196)
		v := newJSONViewBytes(data)
		for _, b := range pressed {
			v.update(jvPress(fuzzJSONKeys[int(b)%len(fuzzJSONKeys)]), keys.Keys, width, 10)
		}
		for _, blurred := range []bool{false, true} {
			v.blurred = blurred
			for _, line := range v.lines(width).window(0, 400) {
				fuzzDrawn(t, "a line of the json view", line, width)
			}
		}
		fuzzPlain(t, "its path", styles.Clean(v.path()))
		for line := range strings.SplitSeq(prettyJSON(data), "\n") {
			c := jsonLineCell(line)
			for _, s := range c {
				fuzzPlain(t, "a span of a line of json", s.Text)
			}
			if plainText(c) != styles.Clean(line) {
				t.Fatalf("jsonLineCell(%q) reads %q", line, plainText(c))
			}
		}
	})
}
