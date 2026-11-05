package tui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"time"
)

// formatInt writes n with thin groups of three, as 12,345.
func formatInt(n int) string {
	s := strconv.Itoa(n)
	if n < 0 || len(s) <= 3 {
		return s
	}
	var b []byte
	for i, c := range []byte(s) {
		if i > 0 && (len(s)-i)%3 == 0 {
			b = append(b, ',')
		}
		b = append(b, c)
	}
	return string(b)
}

func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return formatInt(n) + " " + noun + "s"
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// formatAgo says how long ago t was, in the largest unit that fits.
func formatAgo(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	d := time.Since(t)
	switch {
	case d < 0:
		return "just now"
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

// formatTime writes t in local time, without the year when it is this year.
func formatTime(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	t = t.Local()
	if t.Year() == time.Now().Year() {
		return t.Format("Jan 02 15:04:05")
	}
	return t.Format("2006-01-02 15:04:05")
}

// formatDuration writes a duration the way a person reads it: 340ms, 1.2s, 3m4s.
func formatDuration(d time.Duration) string {
	switch {
	case d < time.Second:
		return d.Round(time.Millisecond).String()
	case d < time.Minute:
		return d.Round(100 * time.Millisecond).String()
	default:
		return d.Round(time.Second).String()
	}
}

// prettyJSON indents json, or returns the bytes as they are when they are not json.
func prettyJSON(data []byte) string {
	var b bytes.Buffer
	if err := json.Indent(&b, bytes.TrimSpace(data), "", "  "); err != nil {
		return string(data)
	}
	return b.String()
}

// formatBytes writes a size in the largest unit that keeps it above 1: 512 b, 3.8 mb.
func formatBytes(n int64) string {
	units := []string{"b", "kb", "mb", "gb"}
	v := float64(n)
	i := 0
	for v >= 1024 && i < len(units)-1 {
		v /= 1024
		i++
	}
	if i == 0 {
		return fmt.Sprintf("%d b", n)
	}
	return fmt.Sprintf("%.1f %s", v, units[i])
}

// formatMillis writes milliseconds the way a person reads them: 3ms, 340ms, 1.2s.
func formatMillis(ms float64) string {
	return formatDuration(time.Duration(ms * float64(time.Millisecond)))
}
