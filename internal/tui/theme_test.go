package tui

import (
	"image/color"
	"math"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"

	"github.com/kontrolplane/koldstart/internal/cli"
	"github.com/kontrolplane/koldstart/internal/state"
	"github.com/kontrolplane/koldstart/internal/tui/styles"
)

// themeUse selects a theme and a colour profile for one test, and goes back to dark, truecolor
// and a detected theme after it.
func themeUse(t *testing.T, dark, paint bool, profile colorprofile.Profile) {
	t.Helper()
	styles.Use(dark)
	styles.UseProfile(profile)
	styles.Paint = paint
	t.Cleanup(func() {
		styles.Use(true)
		styles.UseProfile(colorprofile.TrueColor)
		styles.Paint = false
	})
}

// themeLuminance is the relative luminance of a colour, as wcag 2 defines it.
func themeLuminance(c color.Color) float64 {
	r, g, b, _ := c.RGBA()
	channel := func(v uint32) float64 {
		s := float64(v>>8) / 255
		if s <= 0.04045 {
			return s / 12.92
		}
		return math.Pow((s+0.055)/1.055, 2.4)
	}
	return 0.2126*channel(r) + 0.7152*channel(g) + 0.0722*channel(b)
}

// themeContrast is the wcag 2 contrast ratio of two colours.
func themeContrast(a, b color.Color) float64 {
	la, lb := themeLuminance(a), themeLuminance(b)
	return (max(la, lb) + 0.05) / (min(la, lb) + 0.05)
}

// themeTextTones are the tones text is painted in.
var themeTextTones = map[string]styles.Tone{
	"text": styles.ToneText, "body": styles.ToneBody, "muted": styles.ToneMuted, "faint": styles.ToneFaint,
	"accent": styles.ToneAccent, "warm": styles.ToneWarm, "success": styles.ToneSuccess,
	"warning": styles.ToneWarning, "info": styles.ToneInfo, "danger": styles.ToneDanger,
}

// TestContrast checks every tone of text reads at 4.5:1 on the base, and on the selection as a
// selected row paints it, once lifted; that the cursor's marker and the symbols of the statuses
// read at 3:1 on both; and that the labels of the buttons read at 4.5:1, in both themes, in
// truecolor and in 256 colours.
func TestContrast(t *testing.T) {
	tests := []struct {
		name    string
		dark    bool
		profile colorprofile.Profile
	}{
		{name: "dark", dark: true, profile: colorprofile.TrueColor},
		{name: "light", profile: colorprofile.TrueColor},
		{name: "dark 256", dark: true, profile: colorprofile.ANSI256},
		{name: "light 256", profile: colorprofile.ANSI256},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			themeUse(t, tt.dark, false, tt.profile)
			p := styles.P
			check := func(what string, fg, bg color.Color, want float64) {
				t.Helper()
				if got := themeContrast(fg, bg); got < want {
					t.Errorf("%s: %.2f:1, want %.1f:1", what, got, want)
				}
			}
			for name, tone := range themeTextTones {
				check(name+" on the base", p.Color(tone), p.Base, 4.5)
				lifted := lift(cell{styles.S("x", tone)})[0].Tone
				check(name+" on the selection", p.Color(lifted), p.Selection, 4.5)
			}
			check("the cursor's marker", p.Accent, p.Selection, 3)
			for _, symbol := range []styles.Tone{styles.ToneSuccess, styles.ToneWarning, styles.ToneDanger, styles.ToneInfo, styles.ToneAccent} {
				check("a symbol on the base", p.Color(symbol), p.Base, 3)
				check("a symbol on the selection", p.Color(symbol), p.Selection, 3)
			}
			check("an unfocused button", p.Muted, p.Surface, 4.5)
			for name, tone := range map[string]styles.Tone{"text": styles.ToneText, "warning": styles.ToneWarning, "danger": styles.ToneDanger} {
				check("a focused "+name+" button", p.Base, p.Color(tone), 4.5)
			}
		})
	}
}

// TestToneRules checks the same kind of thing takes the same tone on every page, as the styles
// package's doc comment sets out: what is in flight the accent, a cold start info, an event that
// is not json a warning, what a failure stopped and an unreadable stage danger, with their
// symbols, a note on a resource muted, an id in aws in a table muted, a hash faint, and a time or
// a count in a column body.
func TestToneRules(t *testing.T) {
	now := time.Now()
	running := requestRow(request{id: "r1", start: now, last: now}, time.Minute)
	cold := requestRow(request{id: "r2", start: now, last: now, reported: true, init: 120, duration: 5}, time.Minute)
	stage := cli.StageSummary{StageRef: ordersDev, Err: errAWS}
	tests := []struct {
		name string
		c    cell
		part string
		want styles.Tone
	}{
		{name: "a running invocation", c: running[6], part: "● running", want: styles.ToneAccent},
		{name: "the time of an invocation", c: running[0], part: ":", want: styles.ToneBody},
		{name: "a request id", c: running[1], part: "r1", want: styles.ToneFaint},
		{name: "a cold start", c: cold[5], part: "ms", want: styles.ToneInfo},
		{name: "a pending function", c: cell{styles.S("pending", stateTone("Pending"))}, part: "pending", want: styles.ToneAccent},
		{name: "an inactive function", c: cell{styles.S("inactive", stateTone("Inactive"))}, part: "inactive", want: styles.ToneWarning},
		{name: "a retained resource", c: resourceNote(&state.Resource{DeletionPolicy: "retain"}, false), part: "retained", want: styles.ToneMuted},
		{name: "a kept resource", c: resourceNote(&state.Resource{}, true), part: "kept", want: styles.ToneMuted},
		{name: "an unreadable stage", c: overviewRow(stage, false)[len(overviewRow(stage, false))-1], part: "unreadable", want: styles.ToneDanger},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tone, ok := toneOf(tt.c, tt.part)
			if !ok {
				t.Fatalf("no %q in %+v", tt.part, tt.c)
			}
			if tone != tt.want {
				t.Errorf("tone %d, want %d", tone, tt.want)
			}
		})
	}
}

// TestBackgroundAnswer checks the palette follows the background the terminal answers with when
// the theme is auto, and keeps the one --theme chose otherwise; and that the profile the program
// detects picks the palette for it.
func TestBackgroundAnswer(t *testing.T) {
	cornsilk := color.RGBA{R: 0xFE, G: 0xFA, B: 0xE0, A: 0xFF}
	tests := []struct {
		name    string
		paint   bool
		msg     tea.Msg
		want    styles.Palette
		profile colorprofile.Profile
	}{
		{name: "light answer", msg: tea.BackgroundColorMsg{Color: cornsilk}, want: styles.Light},
		{name: "dark answer", msg: tea.BackgroundColorMsg{Color: color.Black}, want: styles.Dark},
		{name: "chosen theme kept", paint: true, msg: tea.BackgroundColorMsg{Color: cornsilk}, want: styles.Dark},
		{name: "256 colours", msg: tea.ColorProfileMsg{Profile: colorprofile.ANSI256}, want: styles.Dark256},
		{name: "16 colours", msg: tea.ColorProfileMsg{Profile: colorprofile.ANSI}, want: styles.Dark16},
		{name: "no colour", msg: tea.ColorProfileMsg{Profile: colorprofile.ASCII}, want: styles.Dark},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			themeUse(t, true, tt.paint, colorprofile.TrueColor)
			m := newTestModel(t, newFake(t))
			update(t, m, tt.msg)
			if styles.P != tt.want {
				t.Errorf("palette %+v, want %+v", styles.P, tt.want)
			}
		})
	}
}

// TestDarkGround checks what COLORFGBG says of the terminal's background before it answers.
func TestDarkGround(t *testing.T) {
	tests := []struct {
		name      string
		colorfgbg string
		want      bool
	}{
		{name: "unset", want: true},
		{name: "white on black", colorfgbg: "15;0", want: true},
		{name: "black on white", colorfgbg: "0;15", want: false},
		{name: "light grey", colorfgbg: "0;7", want: false},
		{name: "dark grey", colorfgbg: "15;8", want: true},
		{name: "three fields", colorfgbg: "0;default;15", want: false},
		{name: "default", colorfgbg: "15;default", want: true},
		{name: "out of range", colorfgbg: "0;231", want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := darkGround(tt.colorfgbg); got != tt.want {
				t.Errorf("darkGround(%q) = %v, want %v", tt.colorfgbg, got, tt.want)
			}
		})
	}
}

// themeUnpainted finds the first cell of s drawn without a background, as line and column, or
// -1 when every cell has one.
func themeUnpainted(s string) (int, int) {
	p := ansi.GetParser()
	defer ansi.PutParser(p)
	for i, line := range strings.Split(s, "\n") {
		painted, col := false, 0
		var parse byte
		for rest := line; rest != ""; {
			p.Reset()
			seq, width, n, next := ansi.DecodeSequence(rest, parse, p)
			switch {
			case ansi.HasCsiPrefix(seq) && p.Command() == 'm':
				painted = themeBackground(p.Params(), painted)
			case width > 0 && !painted:
				return i, col
			}
			col += width
			rest, parse = rest[n:], next
		}
	}
	return -1, -1
}

// themeBackground is whether a background is set after an sgr sequence with params, given whether
// one was before it.
func themeBackground(params ansi.Params, painted bool) bool {
	if len(params) == 0 {
		return false
	}
	for i := 0; i < len(params); i++ {
		switch n := params[i].Param(0); {
		case n == 0, n == 49:
			painted = false
		case n == 38 || n == 58:
			var c color.Color
			if read := ansi.ReadStyleColor(params[i:], &c); read > 0 {
				i += read - 1
			}
		case n == 48:
			var c color.Color
			if read := ansi.ReadStyleColor(params[i:], &c); read > 0 {
				i += read - 1
			}
			painted = true
		case n >= 40 && n <= 47, n >= 100 && n <= 107:
			painted = true
		}
	}
	return painted
}

// TestPaintedGround draws every page with --theme dark and light at sizes from below the minimum
// to large, and checks the view fills the terminal, every line as wide as it, and that every
// cell has a background, so none shows the terminal's own where it does not take the view's.
func TestPaintedGround(t *testing.T) {
	sizes := [][2]int{{40, 10}, {80, 24}, {120, 40}, {300, 80}}
	for _, dark := range []bool{true, false} {
		themeUse(t, dark, true, colorprofile.TrueColor)
		for _, size := range sizes {
			w, h := size[0], size[1]
			for name, m := range layoutPages(t, w, h) {
				v := m.View()
				if v.BackgroundColor != styles.P.Base {
					t.Errorf("dark %v %dx%d %s: background %v, want the base", dark, w, h, name, v.BackgroundColor)
				}
				lines := strings.Split(v.Content, "\n")
				if len(lines) != h {
					t.Errorf("dark %v %dx%d %s: %d lines", dark, w, h, name, len(lines))
				}
				for i, line := range lines {
					if lw := ansi.StringWidth(line); lw != w {
						t.Errorf("dark %v %dx%d %s: line %d is %d wide", dark, w, h, name, i, lw)
						break
					}
				}
				if line, col := themeUnpainted(v.Content); line >= 0 {
					t.Errorf("dark %v %dx%d %s: line %d column %d has no background", dark, w, h, name, line, col)
				}
			}
		}
	}
}

// TestGround checks styles.Ground paints the base under text after every kind of style, a
// foreground whose components hold the number of a background among them, and leaves a
// background a span sets of its own.
func TestGround(t *testing.T) {
	themeUse(t, true, true, colorprofile.TrueColor)
	tests := []struct {
		name string
		in   string
	}{
		{name: "plain", in: "abc"},
		{name: "empty", in: ""},
		{name: "a reset", in: "a\x1b[mb"},
		{name: "a foreground holding 48", in: "\x1b[0;38;2;48;0;0mred\x1b[m after"},
		{name: "a foreground holding 41", in: "\x1b[0;38;5;41mgreen\x1b[0m after"},
		{name: "a background of its own", in: "\x1b[48;2;1;2;3mrow\x1b[m after"},
		{name: "reverse video", in: "\x1b[7mrow\x1b[m"},
		{name: "two lines", in: "a\nb"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := styles.Ground(tt.in, 12, 3)
			if line, col := themeUnpainted(got); line >= 0 {
				t.Errorf("%q: line %d column %d has no background", got, line, col)
			}
			lines := strings.Split(got, "\n")
			if len(lines) != 3 {
				t.Fatalf("%q: %d lines, want 3", got, len(lines))
			}
			for i, line := range lines {
				if w := ansi.StringWidth(line); w != 12 {
					t.Errorf("%q: line %d is %d wide, want 12", got, i, w)
				}
			}
			for i, want := range strings.Split(ansi.Strip(tt.in), "\n") {
				if text := strings.TrimRight(ansi.Strip(lines[i]), " "); text != want {
					t.Errorf("line %d is %q, want %q", i, text, want)
				}
			}
		})
	}
}

// themeDownsample writes s as a terminal of the profile receives it.
func themeDownsample(s string, p colorprofile.Profile) string {
	var b strings.Builder
	w := &colorprofile.Writer{Forward: &b, Profile: p}
	_, _ = w.WriteString(s)
	return b.String()
}

// themeColours names the kinds of colour s sets: rgb, 256 and 16.
func themeColours(s string) map[string]bool {
	kinds := map[string]bool{}
	p := ansi.GetParser()
	defer ansi.PutParser(p)
	var parse byte
	for rest := s; rest != ""; {
		p.Reset()
		seq, _, n, next := ansi.DecodeSequence(rest, parse, p)
		if ansi.HasCsiPrefix(seq) && p.Command() == 'm' {
			params := p.Params()
			for i := 0; i < len(params); i++ {
				switch c := params[i].Param(0); {
				case c == 38 || c == 48 || c == 58:
					if i+1 < len(params) && params[i+1].Param(0) == 2 {
						kinds["rgb"] = true
					} else {
						kinds["256"] = true
					}
					var col color.Color
					if read := ansi.ReadStyleColor(params[i:], &col); read > 0 {
						i += read - 1
					}
				case c >= 30 && c <= 37, c >= 40 && c <= 47, c >= 90 && c <= 97, c >= 100 && c <= 107:
					kinds["16"] = true
				}
			}
		}
		rest, parse = rest[n:], next
	}
	return kinds
}

// themeAttr reports whether s sets the sgr attribute n, as 1 for bold or 7 for reverse video.
func themeAttr(s string, n int) bool {
	p := ansi.GetParser()
	defer ansi.PutParser(p)
	var parse byte
	for rest := s; rest != ""; {
		p.Reset()
		seq, _, read, next := ansi.DecodeSequence(rest, parse, p)
		if ansi.HasCsiPrefix(seq) && p.Command() == 'm' {
			params := p.Params()
			for i := 0; i < len(params); i++ {
				c := params[i].Param(0)
				if c == n {
					return true
				}
				if c == 38 || c == 48 || c == 58 {
					var col color.Color
					if r := ansi.ReadStyleColor(params[i:], &col); r > 0 {
						i += r - 1
					}
				}
			}
		}
		rest, parse = rest[read:], next
	}
	return false
}

// themeSeconds matches how long ago something was in seconds, which can tick between two draws.
var themeSeconds = regexp.MustCompile(`\d+s ago`)

// TestColourProfiles draws every page in each colour profile, as the terminal receives it, in
// both themes. Each shows the same text as in truecolor; 256 and 16 colours use colours of their
// own profile only, so nothing is left to a nearest match; and in 16 colours and without colour
// the selected row is drawn in reverse video, with danger and warning bold without colour.
func TestColourProfiles(t *testing.T) {
	tables := []string{"overview", "stage", "stage resources", "invocations"}
	tests := []struct {
		name    string
		profile colorprofile.Profile
		kinds   []string
		reverse bool
	}{
		{name: "truecolor", profile: colorprofile.TrueColor, kinds: []string{"rgb"}},
		{name: "256 colours", profile: colorprofile.ANSI256, kinds: []string{"256"}},
		{name: "16 colours", profile: colorprofile.ANSI, kinds: []string{"16"}, reverse: true},
		{name: "no colour", profile: colorprofile.ASCII, reverse: true},
		{name: "no terminal", profile: colorprofile.NoTTY},
	}
	for _, dark := range []bool{true, false} {
		themeUse(t, dark, false, colorprofile.TrueColor)
		pages := layoutPages(t, 120, 40)
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				themeUse(t, dark, false, tt.profile)
				for name, m := range pages {
					styles.UseProfile(colorprofile.TrueColor)
					want := themeSeconds.ReplaceAllString(ansi.Strip(m.render()), "Ns ago")
					styles.UseProfile(tt.profile)
					raw := m.restyleInputs().render()
					got := themeDownsample(raw, tt.profile)
					if text := themeSeconds.ReplaceAllString(ansi.Strip(got), "Ns ago"); text != want {
						t.Errorf("dark %v %s: the text differs from truecolor's:\n%s\nwant:\n%s", dark, name, text, want)
					}
					for kind := range themeColours(raw) {
						if tt.kinds != nil && !slices.Contains(tt.kinds, kind) {
							t.Errorf("dark %v %s: sets %s colours", dark, name, kind)
						}
					}
					if tt.profile <= colorprofile.ASCII && len(themeColours(got)) > 0 {
						t.Errorf("dark %v %s: a colour reaches a terminal without colour", dark, name)
					}
				}
				for _, name := range tables {
					got := themeDownsample(pages[name].render(), tt.profile)
					if reversed := themeAttr(got, 7); reversed != tt.reverse {
						t.Errorf("dark %v %s: selected row reversed %v, want %v", dark, name, reversed, tt.reverse)
					}
				}
				for _, tone := range []styles.Tone{styles.ToneDanger, styles.ToneWarning} {
					got := themeDownsample(styles.Render(styles.S("x", tone)), tt.profile)
					if bold := themeAttr(got, 1); bold != (tt.profile == colorprofile.ASCII) {
						t.Errorf("dark %v tone %d: bold %v without colour only", dark, tone, bold)
					}
				}
			})
		}
	}
}
