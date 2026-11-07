package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// sdOutputs opens the outputs tab of the fixture of a stage, at 120 by 40.
func sdOutputs(t *testing.T, f *fakeBackend) model {
	t.Helper()
	return pressKeys(t, started(t, f), "tab", "tab")
}

// sdOpenOutput opens the output called name from the outputs tab.
func sdOpenOutput(t *testing.T, m model, name string) model {
	t.Helper()
	for range m.state.stage.outputs {
		if m.state.stage.outputs[m.state.stage.tables[outputsTab].cursor] == name {
			break
		}
		m = pressKeys(t, m, "j")
	}
	m = pressKeys(t, m, "enter")
	if m.page != outputPage || m.state.output.name != name {
		t.Fatalf("page %v on %q, want the output %s", m.page, m.state.output.name, name)
	}
	return m
}

// TestOutputPage checks enter opens an output with what it holds, its size and its value, json in
// a json view and text as it is, c copies the value as the stage holds it, all of a json value
// from its top line, and esc goes back to the output it came from.
func TestOutputPage(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  []string
		copy  string
	}{
		{name: "database", want: []string{"holds  json object · 4 keys", "size  118 b", `  "port": 5432,`, `    "team": "payments"`}, copy: "copied all of it"},
		{name: "queueUrls", want: []string{"holds  json list · 2 items", `  "https://sqs.eu-west-1.amazonaws.com/123456789012/orders-dev-jobs-01",`}},
		{name: "certificate", want: []string{"holds  text · 3 lines", "-----BEGIN CERTIFICATE-----", "MIIBszCCAVmgAwIBAgIUQ", "-----END CERTIFICATE-----"}},
		{name: "apiUrl", want: []string{"holds  url", "https://a1b2c3d4e5.execute-api.eu-west-1.amazonaws.com"}, copy: "copied https://a1b2c3d4e5.execute-api.eu-west-1.amazon…"},
		{name: "tableArn", want: []string{"holds  arn", "arn:aws:dynamodb:eu-west-1:123456789012:table/orders-dev-table-00"}},
		{name: "emptyNote", want: []string{"holds  nothing", "size  0 b", "empty"}},
		{name: "spaced", value: "{\n  \"a\": [1, 2]\n}", want: []string{"holds  json object · 1 key", `  "a": [`, "    1,"}, copy: "copied all of it"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := sdStage(t, 3)
			if tt.value != "" {
				f.states[ordersDev].Outputs[tt.name] = tt.value
			}
			m := sdOpenOutput(t, sdOutputs(t, f), tt.name)
			v := screen(m)
			for _, want := range append(tt.want, "stage dev › output "+tt.name) {
				if !strings.Contains(v, want) {
					t.Errorf("no %q:\n%s", want, v)
				}
			}
			if tt.copy != "" {
				if c := pressKeys(t, m, "c"); c.status != tt.copy {
					t.Errorf("c says %q, want %q", c.status, tt.copy)
				}
			}
			back := pressKeys(t, m, "esc")
			if s := back.state.stage; back.page != stagePage || s.tab != outputsTab || s.outputs[s.tables[outputsTab].cursor] != tt.name {
				t.Errorf("esc went to page %v, tab %v", back.page, s.tab)
			}
		})
	}
}

// TestOutputGone checks an output a deploy removed while its page is open says so, and r reads
// the stage again.
func TestOutputGone(t *testing.T) {
	f := sdStage(t, 3)
	m := sdOpenOutput(t, sdOutputs(t, f), "apiUrl")
	f.mu.Lock()
	delete(f.states[ordersDev].Outputs, "apiUrl")
	f.mu.Unlock()
	m = pressKeys(t, m, "r")
	if v := screen(m); !strings.Contains(v, "the stage no longer has this output: a deploy removed it") {
		t.Errorf("the page of an output removed:\n%s", v)
	}
	if m = pressKeys(t, m, "c"); m.status != "" {
		t.Errorf("c copied %q of an output that is gone", m.status)
	}
}

// TestOutputValueLines checks the lines of an output's value that is not json: text as it is,
// every line wrapped under its indent within the width.
func TestOutputValueLines(t *testing.T) {
	tests := []struct {
		name  string
		value string
		width int
		want  []string
	}{
		{name: "text", value: "line one\r\n  indented line\n", width: 40, want: []string{"line one", "  indented line"}},
		{name: "a url too long", value: "https://example.com/" + strings.Repeat("a", 30), width: 24, want: []string{"https://example.com/aaaa", "  aaaaaaaaaaaaaaaaaaaaaa", "  aaaa"}},
		{name: "escapes", value: "a\x1b[2Jb\n\x07", width: 40, want: []string{`a\x1b[2Jb`, `\x07`}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := outputValueLines(tt.value, tt.width)
			if strings.Join(got, "\n") != strings.Join(tt.want, "\n") {
				t.Errorf("lines:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(tt.want, "\n"))
			}
			for _, line := range got {
				if w := ansi.StringWidth(line); w > tt.width {
					t.Errorf("%q is %d wide, more than %d", line, w, tt.width)
				}
			}
		})
	}
}

// TestOutputJSON checks an output's json reads as a resource's: in the order the stage holds it,
// its numbers as written, a string of json nested, folded, searched and copied by path, and kept
// folded where it was when a read of the stage changes it.
func TestOutputJSON(t *testing.T) {
	value := `{"b":1.50,"a":[true,null],"policy":"{\"Statement\":[{\"Effect\":\"Allow\"}]}"}`
	tests := []struct {
		name   string
		keys   []string
		want   []string
		not    []string
		status string
	}{
		{name: "in order", want: []string{`    "b": 1.50,`, `    "a": [`, `      true,`, `    "policy": (json) {`, `          "Effect": "Allow"`}},
		{name: "folded", keys: []string{"j", "j", "space"}, want: []string{`"a": [ … 2 items ],`}, not: []string{"true,"}},
		{name: "all folded", keys: []string{"z", "z"}, want: []string{`"policy": (json) { … 1 key }`}},
		{name: "a path", keys: []string{"/", "e", "f", "f", "enter", "C"}, want: []string{".policy.Statement[0].Effect", "search eff · 1 of 1"}, status: "copied the path .policy.Statement[0].Effect"},
		{name: "a value", keys: []string{"j", "c"}, status: "copied the value of .b"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := sdStage(t, 3)
			f.states[ordersDev].Outputs["config"] = value
			m := pressKeys(t, sdOpenOutput(t, sdOutputs(t, f), "config"), tt.keys...)
			v := screen(m)
			for _, want := range tt.want {
				if !strings.Contains(v, want) {
					t.Errorf("no %q:\n%s", want, v)
				}
			}
			for _, not := range tt.not {
				if strings.Contains(v, not) {
					t.Errorf("%q:\n%s", not, v)
				}
			}
			if m.status != tt.status {
				t.Errorf("status %q, want %q", m.status, tt.status)
			}
		})
	}
	f := sdStage(t, 3)
	f.states[ordersDev].Outputs["config"] = value
	m := pressKeys(t, sdOpenOutput(t, sdOutputs(t, f), "config"), "j", "j", "space")
	f.mu.Lock()
	f.states[ordersDev].Outputs["config"] = `{"b":2,"a":[true,null,false],"c":3}`
	f.mu.Unlock()
	m = pressKeys(t, m, "r")
	if v := screen(m); !strings.Contains(v, `"a": [ … 3 items ],`) || !strings.Contains(v, `"c": 3`) || m.state.output.json.path() != ".a" {
		t.Errorf("read again, on %s:\n%s", m.state.output.json.path(), v)
	}
}

// TestOutputKind checks what an output is said to hold.
func TestOutputKind(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  string
	}{
		{name: "empty", value: "", want: "nothing"},
		{name: "an object", value: `{"a":1,"b":2}`, want: "json object · 2 keys"},
		{name: "a list", value: `[1]`, want: "json list · 1 item"},
		{name: "a url", value: "https://api.example.com/v1", want: "url"},
		{name: "an arn", value: "arn:aws:sqs:eu-west-1:123456789012:jobs", want: "arn"},
		{name: "a sentence with a url", value: "see https://example.com", want: "text"},
		{name: "lines", value: "a\nb\n", want: "text · 2 lines"},
		{name: "a json string", value: `"x"`, want: "text"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := outputKind(tt.value); got != tt.want {
				t.Errorf("outputKind(%q) = %q, want %q", tt.value, got, tt.want)
			}
		})
	}
}
