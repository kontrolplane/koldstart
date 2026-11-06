package tui

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"

	"github.com/kontrolplane/koldstart/internal/engine"
	"github.com/kontrolplane/koldstart/internal/tui/keys"
	"github.com/kontrolplane/koldstart/internal/tui/styles"
)

// jvPress is the key s as the terminal sends it, space too.
func jvPress(s string) tea.KeyPressMsg {
	if s == "space" {
		return tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}
	}
	return perfPress(s)
}

// jvKeys presses keys on a view drawn at width in height lines, and returns what the last did.
func jvKeys(t *testing.T, v jsonView, width, height int, pressed ...string) (jsonView, jsonResult) {
	t.Helper()
	var res jsonResult
	for _, k := range pressed {
		res, _ = v.update(jvPress(k), keys.Keys, width, height)
	}
	return v, res
}

// jvLines are the lines a view draws at width, without their styles or the spaces they end in.
func jvLines(v jsonView, width int) []string {
	l := v.lines(width)
	out := make([]string, 0, l.len())
	for _, line := range l.window(0, l.len()) {
		out = append(out, strings.TrimRight(ansi.Strip(line), " "))
	}
	return out
}

// jvAway is the view drawn without its cursor.
func jvAway(v jsonView) jsonView {
	v.blurred = true
	return v
}

// jvPolicy is a role as cloud control reads one, its policy document a string of json.
const jvPolicy = `{"Policies":[{"PolicyName":"read","PolicyDocument":"{\"Statement\":[{\"Effect\":\"Allow\",\"Action\":\"s3:GetObject\"},{\"Effect\":\"Allow\",\"Action\":[\"sqs:SendMessage\",\"sqs:ReceiveMessage\"],\"Resource\":\"arn:aws:sqs:eu-west-1:123456789012:orders\"}]}"}],"RoleName":"orders-dev"}`

// TestJSONViewRender checks how each kind of value is drawn: scalars, nesting, empty objects and
// arrays, a string of json shown nested, refs and secrets by what they stand for, a document that
// is not an object, and lines that are not json.
func TestJSONViewRender(t *testing.T) {
	tests := []struct {
		name string
		view jsonView
		want []string
	}{
		{
			name: "scalars",
			view: newJSONViewBytes([]byte(`{"s":"x","n":-1.5e3,"t":true,"f":false,"z":null}`)),
			want: []string{"  {", `    "s": "x",`, `    "n": -1.5e3,`, `    "t": true,`, `    "f": false,`, `    "z": null`, "  }"},
		},
		{
			name: "nesting",
			view: newJSONViewBytes([]byte(`{"a":{"b":[1,{"c":[]}],"d":{}}}`)),
			want: []string{"  {", `    "a": {`, `      "b": [`, "        1,", "        {", `          "c": []`, "        }", "      ],", `      "d": {}`, "    }", "  }"},
		},
		{
			name: "a string of json",
			view: newJSONViewBytes([]byte(`{"body":"{\"id\":1,\"tags\":[\"a\"]}","note":"[INFO] not json"}`)),
			want: []string{"  {", `    "body": {  json in a string`, `      "id": 1,`, `      "tags": [`, `        "a"`, "      ]", "    },", `    "note": "[INFO] not json"`, "  }"},
		},
		{
			name: "refs, secrets and outputs",
			view: newJSONView(map[string]any{
				"KEY":   map[string]any{"$koldstart": "secret", "source": "ssm", "name": "/orders/key"},
				"TABLE": engine.Ref("resource/table", "Arn"),
			}),
			want: []string{"  {", `    "KEY": (secret ssm:/orders/key),`, `    "TABLE": (ref resource/table.Arn)`, "  }"},
		},
		{
			name: "an array",
			view: newJSONViewBytes([]byte(`[1, "two", [3]]`)),
			want: []string{"  [", "    1,", `    "two",`, "    [", "      3", "    ]", "  ]"},
		},
		{
			name: "a scalar",
			view: newJSONViewBytes([]byte(` "only" `)),
			want: []string{`  "only"`},
		},
		{
			name: "not json",
			view: newJSONViewBytes([]byte("line one\r\nline two {\n")),
			want: []string{"  line one", "  line two {"},
		},
		{
			name: "nothing",
			view: newJSONViewBytes([]byte(" null ")),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := jvLines(jvAway(tt.view), 60)
			if strings.Join(got, "\n") != strings.Join(tt.want, "\n") {
				t.Errorf("drawn:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(tt.want, "\n"))
			}
		})
	}
}

// TestJSONViewTones checks each part of json takes the tone of its role, in the view and in
// jsonLineCell: keys muted, strings in the body ink, numbers and booleans warm, null and
// punctuation faint, a ref in info, and the note on a string of json faint.
func TestJSONViewTones(t *testing.T) {
	v := newJSONView(map[string]any{
		"a": "x", "b": 2, "c": true, "d": nil, "e": engine.Ref("resource/t", "Arn"), "f": `{"g":1}`,
	})
	drawn := make(cell, 0, 64)
	for _, r := range v.rows() {
		drawn = append(drawn, v.rowCell(nil, r)...)
	}
	lines := jsonLines(map[string]any{"a": "x", "b": 2, "c": true, "d": nil, "e": engine.Ref("resource/t", "Arn")})
	tinted := make(cell, 0, 64)
	for _, l := range lines {
		tinted = append(tinted, jsonLineCell(l)...)
	}
	tests := []struct {
		name string
		text string
		want styles.Tone
	}{
		{name: "a key", text: `"a"`, want: styles.ToneMuted},
		{name: "a string", text: `"x"`, want: styles.ToneBody},
		{name: "a number", text: "2", want: styles.ToneWarm},
		{name: "a boolean", text: "true", want: styles.ToneWarm},
		{name: "null", text: "null", want: styles.ToneFaint},
		{name: "punctuation", text: ": ", want: styles.ToneFaint},
		{name: "a ref", text: "(ref resource/t.Arn)", want: styles.ToneInfo},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for name, c := range map[string]cell{"the view": drawn, "jsonLineCell": tinted} {
				found := false
				for _, s := range c {
					if strings.TrimSpace(s.Text) == strings.TrimSpace(tt.text) {
						found = true
						if s.Tone != tt.want {
							t.Errorf("%s: %q in tone %d, want %d", name, s.Text, s.Tone, tt.want)
						}
					}
				}
				if !found && name == "the view" {
					t.Errorf("%s: no span %q in %+v", name, tt.text, c)
				}
			}
		})
	}
	for _, s := range drawn {
		if strings.Contains(s.Text, jsonEmbedNote) && s.Tone != styles.ToneFaint {
			t.Errorf("the note on a string of json is in tone %d", s.Tone)
		}
	}
}

// TestJSONLineCell checks a line of indented json keeps its text whole, split by role, and comes
// out clean.
func TestJSONLineCell(t *testing.T) {
	tests := []struct {
		name string
		line string
		want []string
	}{
		{name: "a key and a string", line: `  "a": "b c",`, want: []string{"  ", `"a"`, ": ", `"b c"`, ","}},
		{name: "a number", line: `    "n": 12.5`, want: []string{"    ", `"n"`, ": ", "12.5"}},
		{name: "a placeholder", line: `  "k": (secret ssm:/a, b),`, want: []string{"  ", `"k"`, ": ", "(secret ssm:/a, b)", ","}},
		{name: "a bracket", line: "  ],", want: []string{"  ],"}},
		{name: "escapes", line: "\"\x1b[2J\": 1", want: []string{`"\x1b[2J"`, ": ", "1"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := jsonLineCell(tt.line)
			got := make([]string, len(c))
			for i, s := range c {
				got[i] = s.Text
				fuzzPlain(t, "a span", s.Text)
			}
			if strings.Join(got, "|") != strings.Join(tt.want, "|") {
				t.Errorf("spans %q, want %q", got, tt.want)
			}
		})
	}
}

// TestJSONViewWrap checks a long value wraps under its key at every width from 20 to 200, its
// lines hanging two columns further in, none wider than the width, and none of it lost.
func TestJSONViewWrap(t *testing.T) {
	arn := "arn:aws:iam::123456789012:role/" + strings.Repeat("orders-dev-a-very-long-role-name-", 6) + "end"
	words := strings.Repeat("the quick brown fox jumps over the lazy dog ", 8) + "end"
	tests := []struct {
		name  string
		value string
	}{
		{name: "an arn", value: arn},
		{name: "words", value: words},
		{name: "wide runes", value: strings.Repeat("日本語のテキスト", 20)},
	}
	for _, tt := range tests {
		for width := 20; width <= 200; width += 9 {
			t.Run(fmt.Sprintf("%s at %d", tt.name, width), func(t *testing.T) {
				data, err := json.Marshal(map[string]any{"Outer": map[string]any{"Value": tt.value}})
				if err != nil {
					t.Fatal(err)
				}
				v := jvAway(newJSONViewBytes(data))
				lines := v.lines(width)
				drawn := lines.window(0, lines.len())
				var joined strings.Builder
				for i, line := range drawn {
					if w := ansi.StringWidth(line); w > width {
						t.Fatalf("line %d is %d wide, more than %d: %q", i, w, width, ansi.Strip(line))
					}
					plain := ansi.Strip(line)
					if i > 2 && i < len(drawn)-2 {
						lead, _, _ := jsonRoom(width, 2)
						if want := strings.Repeat(" ", jsonGutter+lead+2); !strings.HasPrefix(plain, want) || plain[len(want)] == ' ' && tt.name != "words" {
							t.Errorf("line %d does not hang under its key: %q", i, plain)
						}
					}
					if i >= 2 && i < len(drawn)-2 {
						joined.WriteString(strings.TrimSpace(plain))
						joined.WriteString(" ")
					}
				}
				got := strings.ReplaceAll(joined.String(), " ", "")
				want := strings.ReplaceAll(`"Value":"`+tt.value+`"`, " ", "")
				if got != want {
					t.Errorf("wrapped, it reads:\n%s\nwant:\n%s", got, want)
				}
			})
		}
	}
}

// TestJSONViewFolds checks what starts folded, that enter and space fold and unfold what the
// cursor is on, and the object a value is in, that z folds all and then unfolds all, and that the
// cursor stays on what it was on.
func TestJSONViewFolds(t *testing.T) {
	many := make([]any, 60)
	for i := range many {
		many[i] = i
	}
	deep := any("bottom")
	for range jsonFoldDepth + 1 {
		deep = map[string]any{"d": deep}
	}
	doc := map[string]any{"a": map[string]any{"b": 1, "c": 2}, "deep": deep, "many": many, "few": []any{1, 2}}
	tests := []struct {
		name    string
		keys    []string
		want    []string
		not     []string
		path    string
		ofLines int
	}{
		{name: "by default", want: []string{`"many": [ … 60 items ]`, `"d": { … 1 key }`, `"few": [`}, path: "."},
		{name: "enter on an object", keys: []string{"j", "enter"}, want: []string{`"a": { … 2 keys },`}, path: ".a"},
		{name: "enter again", keys: []string{"j", "enter", "enter"}, want: []string{`"a": {`, `"b": 1,`}, path: ".a"},
		{name: "space on a value", keys: []string{"j", "j", "space"}, want: []string{`"a": { … 2 keys },`}, path: ".a"},
		{name: "a closing bracket", keys: []string{"j", "j", "j", "j", "enter"}, want: []string{`"a": { … 2 keys },`}, path: ".a"},
		{name: "an array folded by default", keys: []string{"G", "k", "enter"}, want: []string{`"many": [`, "59"}, path: ".many"},
		{name: "z folds all", keys: []string{"j", "j", "z"}, want: []string{`"a": { … 2 keys },`, `"few": [ … 2 items ],`, `"deep": { … 1 key },`}, not: []string{`"b"`}, path: ".a"},
		{name: "z again unfolds all", keys: []string{"z", "z"}, want: []string{`"many": [`, `"d": "bottom"`, "59"}, path: "."},
		{name: "the top", keys: []string{"enter"}, want: []string{"{ … 4 keys }"}, path: ".", ofLines: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v, _ := jvKeys(t, newJSONView(doc), 80, 20, tt.keys...)
			got := strings.Join(jvLines(v, 80), "\n")
			for _, w := range tt.want {
				if !strings.Contains(got, w) {
					t.Errorf("no %q in:\n%s", w, got)
				}
			}
			for _, w := range tt.not {
				if strings.Contains(got, w) {
					t.Errorf("%q in:\n%s", w, got)
				}
			}
			if p := v.path(); p != tt.path {
				t.Errorf("the cursor is on %s, want %s", p, tt.path)
			}
			if tt.ofLines > 0 && v.lineCount(80) != tt.ofLines {
				t.Errorf("%d lines, want %d", v.lineCount(80), tt.ofLines)
			}
		})
	}
}

// TestJSONViewPaths checks the path of the line under the cursor, through arrays, a string of
// json, and keys that cannot follow a dot.
func TestJSONViewPaths(t *testing.T) {
	tests := []struct {
		name string
		doc  string
		find string
		want string
	}{
		{name: "the top", doc: jvPolicy, find: "{", want: "."},
		{name: "into a string of json", doc: jvPolicy, find: `"sqs:ReceiveMessage"`, want: ".Policies[0].PolicyDocument.Statement[1].Action[1]"},
		{name: "a key of it", doc: jvPolicy, find: `"Action": [`, want: ".Policies[0].PolicyDocument.Statement[1].Action"},
		{name: "a closing bracket", doc: `{"a":[1]}`, find: "]", want: ".a"},
		{name: "a key with a space", doc: `{"a b":{"c-d":1}}`, find: `"c-d": 1`, want: `.["a b"]["c-d"]`},
		{name: "an array at the top", doc: `[[0,{"k":true}]]`, find: `"k": true`, want: ".[0][1].k"},
		{name: "not json", doc: "plain text", find: "plain text", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := newJSONViewBytes([]byte(tt.doc))
			lines := jvLines(jvAway(v), 200)
			for i, line := range lines {
				if strings.TrimSpace(line) == tt.find {
					v.cursor = i
					if got := v.path(); got != tt.want {
						t.Errorf("path %q, want %q", got, tt.want)
					}
					return
				}
			}
			t.Fatalf("no line %q in:\n%s", tt.find, strings.Join(lines, "\n"))
		})
	}
}

// TestJSONViewCopy checks c copies the value under the cursor, a string as its text, an object as
// indented json and a string of json as the string it was, and shift+c its path, saying what was
// copied.
func TestJSONViewCopy(t *testing.T) {
	tests := []struct {
		name   string
		keys   []string
		copy   string
		copied string
	}{
		{name: "all of it", keys: []string{"c"}, copy: "{\n  \"a\": \"x\\\"y\",\n  \"doc\": \"{\\\"k\\\":[1]}\",\n  \"n\": 1.50\n}", copied: "all of it"},
		{name: "a string", keys: []string{"j", "c"}, copy: `x"y`, copied: "the value of .a"},
		{name: "a string of json", keys: []string{"j", "j", "c"}, copy: `{"k":[1]}`, copied: "the value of .doc"},
		{name: "an array in it", keys: []string{"j", "j", "j", "c"}, copy: "[\n  1\n]", copied: "the value of .doc.k"},
		{name: "a number as written", keys: []string{"G", "k", "c"}, copy: "1.50", copied: "the value of .n"},
		{name: "a path", keys: []string{"j", "j", "j", "shift+c"}, copy: ".doc.k", copied: "the path .doc.k"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, res := jvKeys(t, newJSONViewBytes([]byte(`{"a":"x\"y","doc":"{\"k\":[1]}","n":1.50}`)), 80, 20, tt.keys...)
			if res.copy != tt.copy || res.copied != tt.copied {
				t.Errorf("copied %q as %q, want %q as %q", res.copy, res.copied, tt.copy, tt.copied)
			}
		})
	}
	m := started(t, newFake(t))
	m, cmd := m.jsonDone(jsonResult{copy: "v", copied: "the value of .a"})
	if m.status != "copied the value of .a" || cmd == nil {
		t.Errorf("status %q, command %v, want copied the value of .a and the clipboard", m.status, cmd)
	}
}

// TestJSONViewSearch checks / searches as it is typed, with letters typed rather than taken as
// keys, the cursor on the first match from where it was, folded matches unfolded, and the matches
// marked, that n and N go round them, that a search that finds nothing says so, and that esc
// drops it.
func TestJSONViewSearch(t *testing.T) {
	many := map[string]any{}
	for i := range 60 {
		many[fmt.Sprintf("k%02d", i)] = "v"
	}
	many["k59"] = "needle"
	doc := map[string]any{"a": "needle one", "b": map[string]any{"many": many}, "c": "Needle two"}
	tests := []struct {
		name   string
		keys   []string
		path   string
		query  string
		status string
		at     string
	}{
		{name: "typed", keys: []string{"/", "n", "e", "e", "d"}, path: ".a", query: "need", at: "1 of 3"},
		{name: "letters are typed", keys: []string{"/", "q", "c", "j"}, path: ".", query: "qcj", at: "no match"},
		{name: "enter keeps it", keys: []string{"/", "n", "e", "e", "d", "enter"}, path: ".a", query: "need", at: "1 of 3"},
		{name: "n into a fold", keys: []string{"/", "n", "e", "e", "d", "enter", "n"}, path: ".b.many.k59", query: "need", at: "2 of 3"},
		{name: "n ignoring case", keys: []string{"/", "n", "e", "e", "d", "enter", "n", "n"}, path: ".c", query: "need", at: "3 of 3"},
		{name: "n goes round", keys: []string{"/", "n", "e", "e", "d", "enter", "n", "n", "n"}, path: ".a", query: "need", at: "1 of 3"},
		{name: "N goes back round", keys: []string{"/", "n", "e", "e", "d", "enter", "N"}, path: ".c", query: "need", at: "3 of 3"},
		{name: "nothing found", keys: []string{"/", "z", "z", "enter"}, path: ".", query: "zz", status: "no match for zz", at: "no match"},
		{name: "esc while typed", keys: []string{"/", "n", "e", "e", "d", "esc"}, path: "."},
		{name: "esc after", keys: []string{"/", "n", "e", "e", "d", "enter", "esc"}, path: ".a"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v, res := jvKeys(t, newJSONView(doc), 80, 20, tt.keys...)
			if v.path() != tt.path || v.query != tt.query || res.status != tt.status || v.matchPosition() != tt.at {
				t.Errorf("on %s, query %q, status %q, at %q; want %s, %q, %q, %q", v.path(), v.query, res.status, v.matchPosition(), tt.path, tt.query, tt.status, tt.at)
			}
		})
	}
	v, _ := jvKeys(t, newJSONView(doc), 80, 20, "/", "n", "e", "e", "d", "enter")
	marked := false
	for _, line := range v.lines(80).window(0, 10) {
		marked = marked || strings.Contains(line, styles.Render(styles.B("Need", styles.ToneAccent)))
	}
	if !marked {
		t.Error("the matches are not marked")
	}
}

// TestJSONViewKeepsInSight checks the cursor stays in sight of the view as it moves, and that the
// keys it does not take are left to the page.
func TestJSONViewKeepsInSight(t *testing.T) {
	doc := map[string]any{}
	for i := range 40 {
		doc[fmt.Sprintf("k%02d", i)] = strings.Repeat("long value ", 12)
	}
	tests := []struct {
		name string
		keys []string
	}{
		{name: "down", keys: []string{"j", "j", "j", "j", "j", "j", "j", "j"}},
		{name: "the end", keys: []string{"G"}},
		{name: "a page", keys: []string{"pgdown", "pgdown"}},
		{name: "back up", keys: []string{"G", "pgup", "k"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v, _ := jvKeys(t, newJSONView(doc), 60, 10, tt.keys...)
			if !strings.Contains(v.view(60, 10), "▌") {
				t.Errorf("the cursor is out of sight:\n%s", ansi.Strip(v.view(60, 10)))
			}
		})
	}
	v := newJSONView(doc)
	for _, k := range []string{"q", "tab", "r", "n", "esc"} {
		if _, ok := v.update(jvPress(k), keys.Keys, 60, 10); ok {
			t.Errorf("%s is taken by the view", k)
		}
	}
	empty := jsonView{}
	if _, ok := empty.update(jvPress("j"), keys.Keys, 60, 10); ok {
		t.Error("an empty view takes keys")
	}
}

// TestJSONViewCarry checks a document read again keeps its folds, the cursor and the search where
// their paths are found.
func TestJSONViewCarry(t *testing.T) {
	old, _ := jvKeys(t, newJSONView(map[string]any{"a": map[string]any{"x": 1}, "b": map[string]any{"y": 2}}), 80, 20, "j", "enter", "/", "y", "enter")
	got := newJSONView(map[string]any{"a": map[string]any{"x": 3}, "b": map[string]any{"y": 4}, "c": 5}).carry(old)
	lines := strings.Join(jvLines(got, 80), "\n")
	if !strings.Contains(lines, `"a": { … 1 key },`) || got.path() != ".b.y" || got.query != "y" {
		t.Errorf("on %s searching %q:\n%s", got.path(), got.query, lines)
	}
}

// TestJSONViewClean checks keys and values that hold escape sequences, bidi controls and bytes
// that are not utf-8 are drawn escaped, at any width, in a string of json too.
func TestJSONViewClean(t *testing.T) {
	evil := "x\x1b]8;;https://evil.example/\x07y\x1b[2J\x9b\u202e\u2028\xff"
	inner, err := json.Marshal(map[string]string{evil: evil})
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		view jsonView
	}{
		{name: "a value", view: newJSONView(map[string]any{evil: evil, "n": []any{evil}})},
		{name: "a string of json", view: newJSONView(map[string]any{"doc": string(inner)})},
		{name: "raw bytes", view: newJSONViewBytes([]byte("{\"\u202e\": \"\u009b\u2066\"}"))},
		{name: "not json", view: newJSONViewBytes([]byte(evil))},
	}
	for _, tt := range tests {
		for _, width := range []int{4, 20, 80} {
			t.Run(fmt.Sprintf("%s at %d", tt.name, width), func(t *testing.T) {
				for _, k := range []int{0, 1, 2} {
					v := tt.view
					v.cursor = k
					for _, line := range v.lines(width).window(0, 100) {
						fuzzDrawn(t, "a line", line, width)
					}
				}
			})
		}
	}
}

// TestJSONViewPalette checks the lines a view keeps drawn are drawn again in a new palette.
func TestJSONViewPalette(t *testing.T) {
	defer styles.UseProfile(colorprofile.TrueColor)
	defer styles.Use(true)
	v := jvAway(newJSONViewBytes([]byte(jvPolicy)))
	before := strings.Join(v.lines(80).window(0, 30), "\n")
	styles.Use(false)
	after := strings.Join(v.lines(80).window(0, 30), "\n")
	fresh := v
	fresh.cache = &jsonCache{}
	if after == before || after != strings.Join(fresh.lines(80).window(0, 30), "\n") {
		t.Error("the view draws what it kept from the palette before")
	}
}
