package cli

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/kontrolplane/koldstart/internal/version"
)

// TestWriteJSON checks json for a script has its keys sorted, those of a struct too, nothing
// escaped for html, its numbers as they were, a newline after, and {} or [] when empty.
func TestWriteJSON(t *testing.T) {
	type pair struct {
		Zebra string `json:"zebra"`
		Apple int    `json:"apple"`
	}
	tests := []struct {
		name string
		v    any
		line bool
		want string
	}{
		{name: "struct", v: pair{Zebra: "<a&b>", Apple: 1}, want: "{\n  \"apple\": 1,\n  \"zebra\": \"<a&b>\"\n}\n"},
		{name: "nested", v: map[string]any{"b": []any{pair{}}, "a": json.Number("1.50")}, want: "{\n  \"a\": 1.50,\n  \"b\": [\n    {\n      \"apple\": 0,\n      \"zebra\": \"\"\n    }\n  ]\n}\n"},
		{name: "empty object", v: map[string]string{}, want: "{}\n"},
		{name: "empty list", v: []string{}, want: "[]\n"},
		{name: "unicode", v: map[string]string{"é": "日本\u2028"}, want: "{\n  \"é\": \"日本\\u2028\"\n}\n"},
		{name: "one line", v: pair{Zebra: "z"}, line: true, want: "{\"apple\":0,\"zebra\":\"z\"}\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			write := writeJSON
			if tt.line {
				write = writeJSONLine
			}
			if err := write(&out, tt.v); err != nil {
				t.Fatal(err)
			}
			if out.String() != tt.want {
				t.Errorf("got:\n%q\nwant:\n%q", out.String(), tt.want)
			}
		})
	}
}

// TestPrintVersionJSON checks version -o json is one object with its keys sorted, and nothing
// else.
func TestPrintVersionJSON(t *testing.T) {
	info := version.Info{Version: "v0.1.0", Commit: "abc", Date: "2026-11-04T00:00:00Z", Go: "go1.26.0", Platform: "linux/amd64"}
	tests := []struct {
		name   string
		output string
		want   string
	}{
		{name: "json", output: outputJSON, want: `{
  "commit": "abc",
  "date": "2026-11-04T00:00:00Z",
  "go": "go1.26.0",
  "modified": false,
  "platform": "linux/amd64",
  "version": "v0.1.0"
}
`},
		{name: "text", output: outputText, want: "v0.1.0 (abc, 2026-11-04T00:00:00Z, go1.26.0 linux/amd64)\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			if err := printVersion(&out, tt.output, info); err != nil {
				t.Fatal(err)
			}
			if out.String() != tt.want {
				t.Errorf("got:\n%s\nwant:\n%s", out.String(), tt.want)
			}
		})
	}
}

// TestPrettyJSON checks json for a person is indented in the order it was written, its keys,
// strings and numbers coloured only when colour is on, its controls escaped, and that what is not
// json is left alone.
func TestPrettyJSON(t *testing.T) {
	tests := []struct {
		name   string
		data   string
		colour bool
		want   string
		notOK  bool
	}{
		{name: "plain", data: `{"z":1,"a":[true,null,"x"]}`, want: "{\n  \"z\": 1,\n  \"a\": [\n    true,\n    null,\n    \"x\"\n  ]\n}\n"},
		{name: "coloured", data: `{"n":-1.5e3,"s":"a:b"}`, colour: true,
			want: "{\n  \x1b[36m\"n\"\x1b[0m: \x1b[33m-1.5e3\x1b[0m,\n  \x1b[36m\"s\"\x1b[0m: \x1b[32m\"a:b\"\x1b[0m\n}\n"},
		{name: "escaped quote", data: `{"k\"":"v\""}`, colour: true, want: "{\n  \x1b[36m\"k\\\"\"\x1b[0m: \x1b[32m\"v\\\"\"\x1b[0m\n}\n"},
		{name: "whitespace and unicode", data: " \t{ \"名前\" :\r\n \"café 🚀\" }\n\n", want: "{\n  \"名前\": \"café 🚀\"\n}\n"},
		{name: "controls", data: "{\"esc\":\"\\u001b[2J\",\"c1\":\"\u009b2J\u007f\"}", want: "{\n  \"esc\": \"\\u001b[2J\",\n  \"c1\": \"\\u009b2J\\u007f\"\n}\n"},
		{name: "a string", data: `"hello"`, want: "\"hello\"\n"},
		{name: "empty object", data: `{}`, want: "{}\n"},
		{name: "not json", data: `hello`, notOK: true},
		{name: "nothing", data: ``, notOK: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			ok := prettyJSON(&out, []byte(tt.data), palette{on: tt.colour})
			if ok == tt.notOK {
				t.Fatalf("prettyJSON = %v, want %v", ok, !tt.notOK)
			}
			if out.String() != tt.want {
				t.Errorf("got:\n%q\nwant:\n%q", out.String(), tt.want)
			}
			if !tt.colour && ok && !json.Valid(out.Bytes()) {
				t.Errorf("%q is no longer json", out.String())
			}
		})
	}
}
