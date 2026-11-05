package cli

import (
	"bytes"
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
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			p := &logPrinter{w: &out, layout: tt.layout}
			p.print(ts, tt.message)
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

// TestShortDuration checks --since is written without zero units.
func TestShortDuration(t *testing.T) {
	tests := []struct {
		in   time.Duration
		want string
	}{
		{in: 10 * time.Minute, want: "10m"},
		{in: time.Hour, want: "1h"},
		{in: 90 * time.Minute, want: "1h30m"},
		{in: 90 * time.Second, want: "1m30s"},
		{in: 48 * time.Hour, want: "48h"},
		{in: 30 * time.Second, want: "30s"},
	}
	for _, tt := range tests {
		if got := shortDuration(tt.in); got != tt.want {
			t.Errorf("shortDuration(%s) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
