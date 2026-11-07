package tui

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// dvOps are the ops of a diff, a byte each.
func dvOps(lines []diffLine) string {
	var b strings.Builder
	for _, l := range lines {
		b.WriteByte(l.op)
	}
	return b.String()
}

// dvDoc decodes a document written as json.
func dvDoc(t *testing.T, s string) any {
	t.Helper()
	if s == "" {
		return nil
	}
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		t.Fatalf("decoding %s: %v", s, err)
	}
	return v
}

// dvMarked are the parts of a line its marks mark.
func dvMarked(l diffLine) []string {
	out := make([]string, 0, len(l.marks))
	for _, m := range l.marks {
		out = append(out, l.text[m[0]:m[1]])
	}
	return out
}

// dvDiff opens the diff of before and after on a fake, at width by height.
func dvDiff(t *testing.T, width, height int, before, after any) model {
	t.Helper()
	m := started(t, newFake(t))
	m, _ = update(t, m, tea.WindowSizeMsg{Width: width, Height: height})
	m, _ = m.openDiff("function/api", "update function", before, after)
	return m
}

// dvKeys is a document of n keys, key000 to key n-1, each its own number, but for the keys in
// changed, which are "changed".
func dvKeys(n int, changed ...int) map[string]any {
	doc := map[string]any{}
	for i := range n {
		doc[fmt.Sprintf("key%03d", i)] = i
	}
	for _, i := range changed {
		doc[fmt.Sprintf("key%03d", i)] = "changed"
	}
	return doc
}

// TestLineDiff checks the diff keeps the common lines and marks what was removed and added: a
// comma gained by the line before an added one is no change, and a line or an object inserted
// into an array is marked alone, not everything after it.
func TestLineDiff(t *testing.T) {
	tests := []struct {
		name string
		a, b []string
		want string
	}{
		{name: "the same", a: []string{"{", `  "a": 1`, "}"}, b: []string{"{", `  "a": 1`, "}"}, want: "   "},
		{name: "a value", a: []string{"{", `  "memory": 128,`, `  "timeout": 6`, "}"}, b: []string{"{", `  "memory": 256,`, `  "timeout": 6`, "}"}, want: " -+  "},
		{name: "a key at the end", a: []string{"{", `  "a": 1`, "}"}, b: []string{"{", `  "a": 1,`, `  "b": 2`, "}"}, want: "  + "},
		{name: "added whole", a: nil, b: []string{"{", `  "a": 1`, "}"}, want: "+++"},
		{name: "removed whole", a: []string{"{", `  "a": 1`, "}"}, b: nil, want: "---"},
		{name: "an item inserted", a: []string{"[", `  "a",`, `  "c"`, "]"}, b: []string{"[", `  "a",`, `  "b",`, `  "c"`, "]"}, want: "  +  "},
		{
			name: "an object inserted first",
			a:    []string{"[", "  {", `    "a": 1`, "  },", "  {", `    "a": 2`, "  }", "]"},
			b:    []string{"[", "  {", `    "a": 0`, "  },", "  {", `    "a": 1`, "  },", "  {", `    "a": 2`, "  }", "]"},
			want: " +++       ",
		},
		{
			name: "an object inserted last",
			a:    []string{"[", "  {", `    "a": 1`, "  }", "]"},
			b:    []string{"[", "  {", `    "a": 1`, "  },", "  {", `    "a": 2`, "  }", "]"},
			want: "    +++ ",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := lineDiff(tt.a, tt.b)
			if ops := dvOps(got); ops != tt.want {
				t.Errorf("ops %q, want %q", ops, tt.want)
			}
			var after []string
			for _, l := range got {
				if l.op != '-' {
					after = append(after, l.text)
				}
			}
			if strings.Join(after, "\n") != strings.Join(tt.b, "\n") {
				t.Errorf("the lines kept and added are %q, want %q", after, tt.b)
			}
		})
	}
}

// TestMarkChanged checks the part of a value that changed is marked by word: the whole of a
// number, the part of an arn that differs, and the words of a sentence, with what lies between
// two changed words marked with them.
func TestMarkChanged(t *testing.T) {
	tests := []struct {
		name         string
		a, b         string
		wantA, wantB []string
	}{
		{name: "a number", a: "512", b: "1024", wantA: []string{"512"}, wantB: []string{"1024"}},
		{name: "a digit more", a: "6", b: "60", wantA: []string{"6"}, wantB: []string{"60"}},
		{
			name: "an arn",
			a:    `"arn:aws:iam::123456789012:role/orders-dev-api"`, b: `"arn:aws:iam::123456789012:role/orders-prod-api"`,
			wantA: []string{"dev"}, wantB: []string{"prod"},
		},
		{name: "a runtime", a: `"nodejs20.x"`, b: `"nodejs22.x"`, wantA: []string{"nodejs20"}, wantB: []string{"nodejs22"}},
		{name: "words", a: `"hello big world"`, b: `"goodbye big moon"`, wantA: []string{"hello", "world"}, wantB: []string{"goodbye", "moon"}},
		{name: "two words together", a: `"red fox"`, b: `"blue cat"`, wantA: []string{"red fox"}, wantB: []string{"blue cat"}},
		{name: "a part added", a: `"api"`, b: `"api-v2"`, wantB: []string{"-v2"}},
		{name: "a type changed", a: "true", b: `"true"`, wantB: []string{`"`, `"`}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ma, mb := markChanged(tt.a, tt.b)
			got := func(s string, marks [][2]int) []string {
				return dvMarked(diffLine{text: s, marks: marks})
			}
			if a := got(tt.a, ma); strings.Join(a, "|") != strings.Join(tt.wantA, "|") {
				t.Errorf("marked %q in %s, want %q", a, tt.a, tt.wantA)
			}
			if b := got(tt.b, mb); strings.Join(b, "|") != strings.Join(tt.wantB, "|") {
				t.Errorf("marked %q in %s, want %q", b, tt.b, tt.wantB)
			}
		})
	}
}

// TestDiffDocs checks the diff of two documents: what it counts as added, changed and removed,
// that a field changed is a removed line right before its added one with the change marked in
// both, that the order of keys is no change, and that json in a string is diffed as json.
func TestDiffDocs(t *testing.T) {
	policy := func(actions string) string {
		return `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":[` + actions + `],"Resource":"*"}]}`
	}
	tests := []struct {
		name                    string
		before, after           string
		added, changed, removed int
		ops                     string
		marked                  []string
		want                    []string
	}{
		{name: "the same", before: `{"a":1,"b":[1,2]}`, after: `{"a":1,"b":[1,2]}`, ops: "       "},
		{name: "key order", before: `{"a":1,"b":2}`, after: `{"b":2,"a":1}`, ops: "    "},
		{
			name: "a field", before: `{"memory":512,"name":"api"}`, after: `{"memory":1024,"name":"api"}`,
			changed: 1, ops: " -+  ", marked: []string{"512", "1024"},
		},
		{
			name: "two fields in one change", before: `{"a":"x-dev","b":"y-dev","c":1}`, after: `{"a":"x-prod","b":"y-prod","c":1}`,
			changed: 2, ops: " -+-+  ", marked: []string{"dev", "prod", "dev", "prod"},
			want: []string{`  "a": "x-dev",`, `  "a": "x-prod",`, `  "b": "y-dev",`, `  "b": "y-prod",`},
		},
		{name: "added whole", after: `{"a":1,"b":{"c":2}}`, added: 2, ops: "++++++"},
		{name: "removed whole", before: `{"a":1,"b":2}`, removed: 2, ops: "----"},
		{name: "a scalar", before: `"x"`, after: `"y"`, changed: 1, ops: "-+", marked: []string{"x", "y"}},
		{name: "a key added", before: `{"a":1}`, after: `{"a":1,"b":{"c":1}}`, added: 1, ops: "  +++ "},
		{
			name: "an item inserted", before: `{"l":[{"a":1},{"a":2}]}`, after: `{"l":[{"a":0},{"a":1},{"a":2}]}`,
			added: 1, ops: "  +++        ", want: []string{`    {`, `      "a": 0`, `    },`},
		},
		{
			name:   "json in a string",
			before: `{"policy":` + dvQuote(policy(`"s3:GetObject"`)) + `}`, after: `{"policy":` + dvQuote(policy(`"s3:GetObject","s3:PutObject"`)) + `}`,
			added: 1, ops: "      +        ",
			want: []string{`  "policy": (json) {`, `          "s3:GetObject",`, `          "s3:PutObject"`},
		},
		{
			name:   "json in a string changed",
			before: `{"policy":` + dvQuote(policy(`"s3:GetObject"`)) + `}`, after: `{"policy":` + dvQuote(policy(`"s3:PutObject"`)) + `}`,
			changed: 1, marked: []string{"GetObject", "PutObject"},
		},
		{name: "json in a string spaced", before: `{"p":"{\"a\":1}"}`, after: `{"p":"{ \"a\": 1 }"}`, ops: "     "},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lines := diffDocs(dvDoc(t, tt.before), dvDoc(t, tt.after))
			d := diffState{lines: lines}
			added, changed, removed := d.diffCounts()
			if added != tt.added || changed != tt.changed || removed != tt.removed {
				t.Errorf("+%d ~%d -%d, want +%d ~%d -%d", added, changed, removed, tt.added, tt.changed, tt.removed)
			}
			if ops := dvOps(lines); tt.ops != "" && ops != tt.ops {
				t.Errorf("ops %q, want %q", ops, tt.ops)
			}
			var marked, text []string
			for _, l := range lines {
				marked = append(marked, dvMarked(l)...)
				text = append(text, l.text)
			}
			if strings.Join(marked, "|") != strings.Join(tt.marked, "|") {
				t.Errorf("marked %q, want %q", marked, tt.marked)
			}
			all := "\n" + strings.Join(text, "\n") + "\n"
			for _, want := range tt.want {
				if !strings.Contains(all, "\n"+want+"\n") {
					t.Errorf("the lines:%s\nwant %q in them", all, want)
				}
			}
		})
	}
}

// dvQuote writes s as a json string.
func dvQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// TestDiffSummary checks the line under a diff's title says how many fields it adds, changes and
// removes, and what it says when it changes none.
func TestDiffSummary(t *testing.T) {
	tests := []struct {
		name          string
		before, after any
		want          string
	}{
		{name: "changes", before: map[string]any{"a": 1, "b": 2}, after: map[string]any{"a": 2, "c": 3}, want: "update function · +1 added  ~1 changed  -1 removed"},
		{name: "none", before: map[string]any{"a": 1}, after: map[string]any{"a": 1}, want: "update function · no change"},
		{name: "spaced", before: map[string]any{"p": `{"a":1}`}, after: map[string]any{"p": `{ "a": 1 }`}, want: "no change but the spacing of json in a string"},
		{name: "nothing", want: "no inputs on either side"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if v := screen(dvDiff(t, 120, 40, tt.before, tt.after)); !strings.Contains(v, tt.want) {
				t.Errorf("no %q in:\n%s", tt.want, v)
			}
		})
	}
}

// TestDiffFolds checks a long diff shows diffContext lines around its change and folds the rest,
// enter opens the fold in sight, z shows every line and z again folds them.
func TestDiffFolds(t *testing.T) {
	m := dvDiff(t, 120, 40, dvKeys(60), dvKeys(60, 30))
	v := screen(m)
	for _, want := range []string{"⋯ 28 unchanged lines", `"key027": 27`, `"key029": 29`, `-   "key030": 30`, `+   "key030": "changed"`, `"key033": 33`, "⋯ 27 unchanged lines", "enter unfold", "z unfold all"} {
		if !strings.Contains(v, want) {
			t.Errorf("no %q in:\n%s", want, v)
		}
	}
	if strings.Contains(v, `"key026": 26`) || strings.Contains(v, `"key034": 34`) {
		t.Errorf("a line folded is in sight:\n%s", v)
	}
	m = pressKeys(t, m, "enter")
	if v := screen(m); strings.Contains(v, "⋯ 28 unchanged lines") || !strings.Contains(v, `"key000": 0`) {
		t.Errorf("enter opens the first fold:\n%s", v)
	}
	if v := screen(pressKeys(t, m, "G")); !strings.Contains(v, "⋯ 27 unchanged lines") {
		t.Errorf("enter opens the second fold as well:\n%s", v)
	}
	m = pressKeys(t, m, "z", "G")
	if v := screen(m); strings.Contains(v, "unchanged lines") || !strings.Contains(v, `"key059": 59`) || !strings.Contains(v, "z fold all") {
		t.Errorf("z unfolds all:\n%s", v)
	}
	m = pressKeys(t, m, "z")
	if v := screen(m); !strings.Contains(v, "⋯ 28 unchanged lines") || !strings.Contains(v, "⋯ 27 unchanged lines") {
		t.Errorf("z again folds all:\n%s", v)
	}
	same := screen(dvDiff(t, 120, 40, dvKeys(60), dvKeys(60)))
	if !strings.Contains(same, "⋯ 62 unchanged lines") || !strings.Contains(same, "no change") {
		t.Errorf("the same document is not folded whole:\n%s", same)
	}
}

// TestDiffNavigation checks ] and [ go from change to change, each in sight with the lines above
// it, the foot saying which of how many it is, and stop at the first and the last.
func TestDiffNavigation(t *testing.T) {
	m := dvDiff(t, 80, 24, dvKeys(100), dvKeys(100, 10, 50, 90))
	tests := []struct {
		key  string
		want string
		line string
	}{
		{key: "]", want: "change 2 of 3", line: `+   "key050": "changed"`},
		{key: "]", want: "change 3 of 3", line: `+   "key090": "changed"`},
		{key: "]", want: "change 3 of 3", line: `+   "key090": "changed"`},
		{key: "[", want: "change 2 of 3", line: `+   "key050": "changed"`},
		{key: "[", want: "change 1 of 3", line: `+   "key010": "changed"`},
		{key: "[", want: "change 1 of 3", line: `+   "key010": "changed"`},
	}
	if foot := navFoot(m); !strings.Contains(foot, "change 1 of 3") {
		t.Fatalf("the diff opens on the first change: %q", foot)
	}
	for i, tt := range tests {
		m = pressKeys(t, m, tt.key)
		v := screen(m)
		if foot := navFoot(m); !strings.Contains(foot, tt.want) {
			t.Errorf("%d: %s: the foot is %q, want %q", i, tt.key, foot, tt.want)
		}
		if !strings.Contains(v, "▌ "+tt.line) {
			t.Errorf("%d: %s: %q is not in sight and marked:\n%s", i, tt.key, tt.line, v)
		}
	}
	m = pressKeys(t, m, "G")
	if foot := navFoot(m); strings.Contains(foot, "change 1 of 3") {
		t.Errorf("scrolled away from the change, the foot still names it: %q", foot)
	}
	m = pressKeys(t, m, "g", "j", "j", "j", "j", "j", "j", "j", "j", "j", "j", "j", "j", "j")
	if foot := navFoot(m); !strings.Contains(foot, "change 2 of 3") {
		t.Errorf("scrolled to the second change, the foot is %q", foot)
	}
	m = pressKeys(t, m, "]")
	if foot := navFoot(m); !strings.Contains(foot, "change 3 of 3") {
		t.Errorf("] goes on from the change in sight: %q", foot)
	}
}

// TestDiffRowsInset checks the gutter of + and - starts rowInset past the page's column, as the
// rows of every page do, with the marker of the change navigated to in the cursor's column, and a
// note in place of a diff starts there too.
func TestDiffRowsInset(t *testing.T) {
	tests := []struct {
		name          string
		before, after any
		want          string
	}{
		{name: "the change navigated to", before: dvKeys(10), after: dvKeys(10, 2, 7), want: ` ▌ +   "key002": "changed"`},
		{name: "another change", before: dvKeys(10), after: dvKeys(10, 2, 7), want: `   +   "key007": "changed"`},
		{name: "no inputs", want: "   no inputs on either side"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := screen(dvDiff(t, 80, 30, tt.before, tt.after))
			found := false
			for line := range strings.SplitSeq(v, "\n") {
				found = found || strings.HasPrefix(strings.TrimPrefix(line, "│"), tt.want)
			}
			if !found {
				t.Errorf("no line starts %q:\n%s", tt.want, v)
			}
		})
	}
}

// TestDiffWrapsUnderItsKey checks a long value changed wraps under its key, every part of it with
// the gutter of its line, and nothing past the width.
func TestDiffWrapsUnderItsKey(t *testing.T) {
	long := strings.Repeat("a long description ", 12)
	m := dvDiff(t, 80, 40, map[string]any{"description": long + "old"}, map[string]any{"description": long + "new"})
	var removed, added int
	for line := range strings.SplitSeq(screen(m), "\n") {
		inner := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(strings.Trim(line, "│")), "▌"))
		switch {
		case strings.HasPrefix(inner, "-     "):
			removed++
		case strings.HasPrefix(inner, "+     "):
			added++
		}
		if ansi.StringWidth(line) > 80 {
			t.Errorf("%q is wider than 80", line)
		}
	}
	if removed == 0 || added == 0 {
		t.Errorf("%d removed and %d added continuation lines, want the gutter on each:\n%s", removed, added, screen(m))
	}
}

// TestDiffLayout lays a diff out at every width from 40 to 200 columns, and draws the page at
// terminal widths from 78 to 200: no row is wider than its width, and every line of the json is
// there, the folds aside.
func TestDiffLayout(t *testing.T) {
	before := map[string]any{"Role": "arn:aws:iam::123456789012:role/" + strings.Repeat("orders-dev-", 20), "Memory": 512, "Env": dvKeys(30), "日本": strings.Repeat("漢字", 50)}
	after := map[string]any{"Role": "arn:aws:iam::123456789012:role/" + strings.Repeat("orders-prd-", 20), "Memory": 1024, "Env": dvKeys(30, 15), "日本": strings.Repeat("漢字", 51)}
	lines := diffDocs(before, after)
	for width := 40; width <= 200; width++ {
		d := diffState{lines: lines, unfolded: true}
		rows := d.layout(width).rows
		var text strings.Builder
		for _, r := range rows {
			var row strings.Builder
			for _, s := range r.spans {
				row.WriteString(s.text)
			}
			if w := ansi.StringWidth(row.String()); w > width {
				t.Fatalf("%d: a row %d wide: %q", width, w, row.String())
			}
			text.WriteString(strings.ReplaceAll(row.String(), " ", ""))
		}
		var want strings.Builder
		for _, l := range lines {
			want.WriteString(strings.ReplaceAll(l.text, " ", ""))
		}
		if text.String() != want.String() {
			t.Fatalf("%d: the rows lose text", width)
		}
	}
	for width := 78; width <= 200; width += 7 {
		m := dvDiff(t, width, 30, before, after)
		for _, k := range []string{"", "]", "z", "G"} {
			if k != "" {
				m = pressKeys(t, m, k)
			}
			for line := range strings.SplitSeq(m.render(), "\n") {
				if w := ansi.StringWidth(line); w > width {
					t.Fatalf("%d after %q: a line %d wide: %q", width, k, w, ansi.Strip(line))
				}
			}
		}
	}
}
