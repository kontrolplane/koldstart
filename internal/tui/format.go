package tui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// formatInt writes n in groups of three, as 12,345 and -1,024.
func formatInt(n int) string {
	s := strconv.Itoa(n)
	sign, digits := "", s
	if n < 0 {
		sign, digits = "-", s[1:]
	}
	if len(digits) <= 3 {
		return s
	}
	b := []byte(sign)
	for i, c := range []byte(digits) {
		if i > 0 && (len(digits)-i)%3 == 0 {
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

// formatDuration writes a duration the way a person reads it: 340ms, 1.2s, 3m4s, 15m, 1h2m.
// Under a millisecond is <1ms, and from an hour the seconds are left out.
func formatDuration(d time.Duration) string {
	switch {
	case d <= 0:
		return "0ms"
	case d < time.Millisecond:
		return "<1ms"
	case d < time.Second-time.Millisecond/2:
		return strconv.FormatInt(d.Round(time.Millisecond).Milliseconds(), 10) + "ms"
	case d < time.Minute-50*time.Millisecond:
		return strings.TrimSuffix(strconv.FormatFloat(d.Round(100*time.Millisecond).Seconds(), 'f', 1, 64), ".0") + "s"
	}
	d = d.Round(time.Second)
	h, m, s := int(d/time.Hour), int(d/time.Minute)%60, int(d/time.Second)%60
	if h > 0 {
		d = d.Round(time.Minute)
		h, m, s = int(d/time.Hour), int(d/time.Minute)%60, 0
	}
	var b strings.Builder
	for _, part := range []struct {
		n    int
		unit string
	}{{h, "h"}, {m, "m"}, {s, "s"}} {
		if part.n > 0 {
			fmt.Fprintf(&b, "%d%s", part.n, part.unit)
		}
	}
	return b.String()
}

// prettyJSON indents json, or returns the bytes as they are when they are not json.
func prettyJSON(data []byte) string {
	var b bytes.Buffer
	if err := json.Indent(&b, bytes.TrimSpace(data), "", "  "); err != nil {
		return string(data)
	}
	return b.String()
}

// formatBytes writes a size in the largest unit that keeps it above 1, with a decimal below
// 10: 512 b, 3.8 mb, 42 mb.
func formatBytes(n int64) string {
	if n < 1024 {
		return fmt.Sprintf("%d b", n)
	}
	units := []string{"kb", "mb", "gb", "tb"}
	v := float64(n) / 1024
	i := 0
	for v >= 1023.5 && i < len(units)-1 {
		v /= 1024
		i++
	}
	if v < 9.95 {
		return fmt.Sprintf("%.1f %s", v, units[i])
	}
	return fmt.Sprintf("%.0f %s", v, units[i])
}

// formatMillis writes milliseconds the way a person reads them: 3ms, 340ms, 1.2s.
func formatMillis(ms float64) string {
	return formatDuration(time.Duration(ms * float64(time.Millisecond)))
}
