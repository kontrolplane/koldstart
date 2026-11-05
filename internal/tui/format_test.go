package tui

import (
	"testing"
	"time"
)

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

// TestFormatDuration checks durations read in the unit that suits them, without the zero units
// time.Duration writes.
func TestFormatDuration(t *testing.T) {
	tests := []struct {
		d    time.Duration
		want string
	}{
		{0, "0ms"},
		{400 * time.Microsecond, "<1ms"},
		{3 * time.Millisecond, "3ms"},
		{340*time.Millisecond + 400*time.Microsecond, "340ms"},
		{999600 * time.Microsecond, "1s"},
		{1250 * time.Millisecond, "1.3s"},
		{6 * time.Second, "6s"},
		{59970 * time.Millisecond, "1m"},
		{90 * time.Second, "1m30s"},
		{15 * time.Minute, "15m"},
		{time.Hour + 2*time.Minute + 20*time.Second, "1h2m"},
		{2 * time.Hour, "2h"},
	}
	for _, tt := range tests {
		if got := formatDuration(tt.d); got != tt.want {
			t.Errorf("formatDuration(%v) = %q, want %q", tt.d, got, tt.want)
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

// TestFormatBytes checks sizes take the largest unit above 1, with a decimal only below 10, and
// never round up to 1024 of a unit.
func TestFormatBytes(t *testing.T) {
	tests := []struct {
		n    int64
		want string
	}{
		{0, "0 b"},
		{512, "512 b"},
		{1024, "1.0 kb"},
		{1536, "1.5 kb"},
		{4 << 20, "4.0 mb"},
		{3_800_000, "3.6 mb"},
		{42 << 20, "42 mb"},
		{512 << 20, "512 mb"},
		{1<<20 - 1, "1.0 mb"},
		{5 << 30, "5.0 gb"},
	}
	for _, tt := range tests {
		if got := formatBytes(tt.n); got != tt.want {
			t.Errorf("formatBytes(%d) = %q, want %q", tt.n, got, tt.want)
		}
	}
}
