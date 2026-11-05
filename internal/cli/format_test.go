package cli

import (
	"testing"
	"time"
)

// TestDuration checks durations read in the unit that suits them, without the zero units
// time.Duration writes.
func TestDuration(t *testing.T) {
	tests := []struct {
		d    time.Duration
		want string
	}{
		{0, "0ms"},
		{-time.Second, "0ms"},
		{400 * time.Microsecond, "<1ms"},
		{3 * time.Millisecond, "3ms"},
		{12345 * time.Microsecond, "12ms"},
		{340*time.Millisecond + 400*time.Microsecond, "340ms"},
		{999600 * time.Microsecond, "1s"},
		{1250 * time.Millisecond, "1.3s"},
		{4240 * time.Millisecond, "4.2s"},
		{6 * time.Second, "6s"},
		{30 * time.Second, "30s"},
		{59970 * time.Millisecond, "1m"},
		{90 * time.Second, "1m30s"},
		{185*time.Second + 400*time.Millisecond, "3m5s"},
		{10 * time.Minute, "10m"},
		{time.Hour, "1h"},
		{90 * time.Minute, "1h30m"},
		{time.Hour + 2*time.Minute + 20*time.Second, "1h2m"},
		{48 * time.Hour, "48h"},
	}
	for _, tt := range tests {
		if got := Duration(tt.d); got != tt.want {
			t.Errorf("Duration(%v) = %q, want %q", tt.d, got, tt.want)
		}
	}
}

// TestElapsed checks a clock reads in whole seconds, never below none, as Duration does from a
// second.
func TestElapsed(t *testing.T) {
	tests := []struct {
		d    time.Duration
		want string
	}{
		{-time.Second, "0s"},
		{0, "0s"},
		{900 * time.Millisecond, "0s"},
		{4900 * time.Millisecond, "4s"},
		{185*time.Second + 900*time.Millisecond, "3m5s"},
		{50 * time.Hour, "50h"},
	}
	for _, tt := range tests {
		if got := Elapsed(tt.d); got != tt.want {
			t.Errorf("Elapsed(%v) = %q, want %q", tt.d, got, tt.want)
		}
	}
}

// TestSize checks sizes take the largest unit above 1, with a decimal only below 10, and never
// round up to 1024 of a unit.
func TestSize(t *testing.T) {
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
		if got := Size(tt.n); got != tt.want {
			t.Errorf("Size(%d) = %q, want %q", tt.n, got, tt.want)
		}
	}
}
