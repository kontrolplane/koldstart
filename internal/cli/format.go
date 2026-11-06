package cli

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Duration writes a duration the way a person reads it: 340ms, 1.2s, 3m4s, 15m, 1h2m. Under a
// millisecond is <1ms, and from an hour the seconds are left out.
func Duration(d time.Duration) string {
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

// Elapsed writes how long something has been going, in whole seconds, as Duration does from a
// second: 0s, 4s, 3m5s, 1h2m. A clock redrawn many times a second reads it without flickering.
func Elapsed(d time.Duration) string {
	d = max(0, d).Truncate(time.Second)
	if d == 0 {
		return "0s"
	}
	return Duration(d)
}

// Size writes a size in the largest unit that keeps it above 1, with a decimal below 10: 512 b,
// 3.8 mb, 42 mb.
func Size(n int64) string {
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
