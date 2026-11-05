package tui

import "testing"

// TestFormatInt checks numbers are grouped in threes, negative ones too.
func TestFormatInt(t *testing.T) {
	tests := []struct {
		n    int
		want string
	}{
		{0, "0"},
		{999, "999"},
		{1000, "1,000"},
		{1234567, "1,234,567"},
		{-12, "-12"},
		{-1024, "-1,024"},
		{-123456, "-123,456"},
	}
	for _, tt := range tests {
		if got := formatInt(tt.n); got != tt.want {
			t.Errorf("formatInt(%d) = %q, want %q", tt.n, got, tt.want)
		}
	}
}

// TestFormatMillis checks lambda's milliseconds, as cloudwatch reports them, read as durations.
func TestFormatMillis(t *testing.T) {
	tests := []struct {
		ms   float64
		want string
	}{
		{0, "0ms"},
		{0.4, "<1ms"},
		{240, "240ms"},
		{1234.5, "1.2s"},
		{900000, "15m"},
	}
	for _, tt := range tests {
		if got := formatMillis(tt.ms); got != tt.want {
			t.Errorf("formatMillis(%v) = %q, want %q", tt.ms, got, tt.want)
		}
	}
}
