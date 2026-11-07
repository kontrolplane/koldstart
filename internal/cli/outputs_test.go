package cli

import (
	"bytes"
	"testing"
)

// TestWriteOutputs checks outputs are sorted with their values lined up, after a deploy under a
// heading, and as json one object with nothing escaped.
func TestWriteOutputs(t *testing.T) {
	outputs := map[string]string{"httpApiUrl": "https://abc.execute-api.eu-west-1.amazonaws.com", "table": "orders-dev", "q": "https://x?a=1&b=2"}
	tests := []struct {
		name  string
		write func(*bytes.Buffer)
		want  string
	}{
		{
			name:  "text",
			write: func(b *bytes.Buffer) { writeOutputs(b, "", outputs) },
			want:  "httpApiUrl = https://abc.execute-api.eu-west-1.amazonaws.com\nq          = https://x?a=1&b=2\ntable      = orders-dev\n",
		},
		{
			name:  "after a deploy",
			write: func(b *bytes.Buffer) { printOutputs(b, map[string]string{"a": "1", "bcd": "2"}) },
			want:  "\noutputs:\n  a   = 1\n  bcd = 2\n",
		},
		{
			name:  "none after a deploy",
			write: func(b *bytes.Buffer) { printOutputs(b, nil) },
			want:  "",
		},
		{
			name: "json",
			write: func(b *bytes.Buffer) {
				_ = writeOutputsJSON(b, map[string]string{"q": "https://x?a=1&b=2", "a": "<1>"})
			},
			want: "{\n  \"a\": \"<1>\",\n  \"q\": \"https://x?a=1&b=2\"\n}\n",
		},
		{
			name:  "json of none",
			write: func(b *bytes.Buffer) { _ = writeOutputsJSON(b, nil) },
			want:  "{}\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			tt.write(&out)
			if out.String() != tt.want {
				t.Errorf("got:\n%q\nwant:\n%q", out.String(), tt.want)
			}
		})
	}
}
