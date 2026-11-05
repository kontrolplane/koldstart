package styles

import "testing"

// TestClean checks control characters are escaped, and newlines kept only in blocks.
func TestClean(t *testing.T) {
	tests := []struct {
		name  string
		in    string
		block bool
		want  string
	}{
		{name: "plain", in: "orders-dev", want: "orders-dev"},
		{name: "escape", in: "a\x1b]0;title\x07b", want: `a\x1b]0;title\x07b`},
		{name: "csi", in: "\x1b[31mred", want: `\x1b[31mred`},
		{name: "c1", in: "a\u009bb", want: `a\x9bb`},
		{name: "newline", in: "a\nb", want: `a\x0ab`},
		{name: "tab", in: "a\tb", want: "a b"},
		{name: "block newline", in: "a\r\nb", block: true, want: "a\nb"},
		{name: "block tab", in: "a\tb", block: true, want: "a    b"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Clean(tt.in)
			if tt.block {
				got = CleanBlock(tt.in)
			}
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}
