package tui

import (
	"encoding/json"
	"slices"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/kontrolplane/koldstart/internal/engine"
	"github.com/kontrolplane/koldstart/internal/tui/styles"
)

// diffLine is a line of a diff: kept, removed or added.
type diffLine struct {
	op   byte
	text string
}

// maxDiffCells bounds the table lineDiff fills; past it the lines are shown removed and added
// whole.
const maxDiffCells = 4_000_000

// lineDiff is the shortest edit from a to b, line by line, as the longest common subsequence
// gives it.
func lineDiff(a, b []string) []diffLine {
	if len(a)*len(b) > maxDiffCells {
		out := make([]diffLine, 0, len(a)+len(b))
		for _, l := range a {
			out = append(out, diffLine{'-', l})
		}
		for _, l := range b {
			out = append(out, diffLine{'+', l})
		}
		return out
	}
	lcs := make([][]int, len(a)+1)
	for i := range lcs {
		lcs[i] = make([]int, len(b)+1)
	}
	for i := range slices.Backward(a) {
		for j := range slices.Backward(b) {
			if a[i] == b[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}
	var out []diffLine
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		switch {
		case a[i] == b[j]:
			out = append(out, diffLine{' ', a[i]})
			i++
			j++
		case lcs[i+1][j] >= lcs[i][j+1]:
			out = append(out, diffLine{'-', a[i]})
			i++
		default:
			out = append(out, diffLine{'+', b[j]})
			j++
		}
	}
	for ; i < len(a); i++ {
		out = append(out, diffLine{'-', a[i]})
	}
	for ; j < len(b); j++ {
		out = append(out, diffLine{'+', b[j]})
	}
	return out
}

// The runes around a value presentable stands in for, so jsonLines can write it without quotes.
// They are in the private use area, which json leaves as it is.
const (
	placeholderOpen  = ""
	placeholderClose = ""
)

// placeholders strips the runes of a placeholder from strings that hold them already.
var placeholders = strings.NewReplacer(placeholderOpen, "", placeholderClose, "")

// presentable replaces koldstart's own values, refs, secrets and outputs, with what they stand
// for, so a diff shows them as a person writes them.
func presentable(v any) any {
	switch x := v.(type) {
	case string:
		return placeholders.Replace(x)
	case map[string]any:
		if _, ok := x["$koldstart"]; ok {
			return placeholderOpen + placeholders.Replace(describe(x)) + placeholderClose
		}
		out := make(map[string]any, len(x))
		for k, e := range x {
			out[k] = presentable(e)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = presentable(e)
		}
		return out
	}
	return v
}

// describe says what one of koldstart's own values stands for: secret ssm:/orders/key, ref
// resource/table.Arn, and a join with its refs in ${}, as ${ref resource/table.Arn}/index/*.
func describe(v map[string]any) string {
	switch {
	case engine.IsSecret(v):
		return "secret " + engine.Describe(v)
	case v["$koldstart"] == "join":
		parts, _ := v["parts"].([]any)
		var b strings.Builder
		for _, part := range parts {
			if s, ok := part.(string); ok {
				b.WriteString(s)
				continue
			}
			b.WriteString("${" + engine.Describe(part) + "}")
		}
		return b.String()
	}
	return engine.Describe(v)
}

// unquote writes the placeholders json quoted in parentheses instead.
var unquote = strings.NewReplacer(`"`+placeholderOpen, "(", placeholderClose+`"`, ")")

// jsonLines writes a value as indented json, a line each, or none for nil. A ref, secret or
// output shows in parentheses, unquoted, so it does not read as a string set to that text.
func jsonLines(v any) []string {
	if v == nil {
		return nil
	}
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(presentable(v)); err != nil {
		return []string{styles.Clean(err.Error())}
	}
	data := unquote.Replace(strings.TrimSuffix(b.String(), "\n"))
	return strings.Split(styles.CleanBlock(data), "\n")
}

// diffState is the full change of one resource: in a plan, or between two versions of the state.
type diffState struct {
	title  string
	detail string
	lines  []diffLine
	scroll int
	from   page
	// wrapped is the lines as the last frame wrapped them, so the next does not wrap them again.
	wrapped *diffRows
}

// openDiff shows what changes in a resource, from before to after, scrolled so its first change
// is in sight, with a few lines above it: the inputs of a function run longer than a short
// terminal, and the line that changed is often below them.
func (m model) openDiff(title, detail string, before, after any) (model, tea.Cmd) {
	d := &m.state.diff
	*d = diffState{title: title, detail: detail, lines: lineDiff(jsonLines(before), jsonLines(after)), from: m.page, wrapped: &diffRows{}}
	total := m.diffLines().len()
	for j, r := range d.rows(diffWidth() - 2) {
		if i := diffHeaderLines + j; r.op != ' ' && r.first {
			if i >= contentHeight {
				d.scroll = min(i-diffContext, max(0, total-contentHeight))
			}
			break
		}
	}
	return m.switchPage(diffPage), nil
}

// diffWidth is the width the diff page draws its lines in.
func diffWidth() int { return contentWidth - 4 }

// diffHeaderLines are the lines above a diff's json, and diffContext the lines kept in sight
// above its first change.
const (
	diffHeaderLines = 3
	diffContext     = 3
)

func (m model) diffUpdate(msg tea.KeyPressMsg) (model, tea.Cmd) {
	d := &m.state.diff
	if key.Matches(msg, m.keys.Back, m.keys.Quit) {
		return m.switchPage(d.from), nil
	}
	d.scroll = scrollBy(msg, m.keys, d.scroll, contentHeight, maxOffset(m.diffLines().len(), contentHeight))
	return m, nil
}

// diffLines are the lines of the diff page, drawn only where they are in sight.
func (m model) diffLines() lazyLines {
	d := m.state.diff
	width := diffWidth()
	added, removed := 0, 0
	for _, l := range d.lines {
		switch l.op {
		case '+':
			added++
		case '-':
			removed++
		}
	}
	var lines lazyLines
	counts := cell{styles.S("  ", styles.ToneFaint), styles.S("+"+formatInt(added), styles.ToneSuccess), styles.S(" ", styles.ToneFaint), styles.S("-"+formatInt(removed), styles.ToneDanger)}
	countsWidth := ansi.StringWidth(plainText(counts))
	detail := truncate(styles.Clean(d.detail), width-1-countsWidth)
	lines.add(
		styles.SectionHeader(d.title, width),
		" "+styles.Render(append([]styles.Span{{Text: detail, Tone: styles.ToneMuted}}, counts...)...),
		"",
	)
	if len(d.lines) == 0 {
		lines.add(" " + styles.Faint("no inputs on either side"))
		return lines
	}
	rows := d.rows(width - 2)
	lines.addDrawn(len(rows), func(i int) string {
		r := rows[i]
		mark, tone := "  ", styles.ToneFaint
		switch r.op {
		case '+':
			mark, tone = "+ ", styles.ToneSuccess
		case '-':
			mark, tone = "- ", styles.ToneDanger
		}
		if !r.first {
			mark = "  "
		}
		return styles.Fg(tone).Render(mark + r.text)
	})
	return lines
}

// diffRows are the lines of a diff wrapped to width in the palette numbered palette. They hold no
// colour, but are wrapped again in a new palette all the same, so nothing kept outlives one.
type diffRows struct {
	width   int
	palette int
	rows    []diffRow
}

// diffRow is a part of a line of a diff as it is drawn: the line's op, and whether it is the
// line's first part.
type diffRow struct {
	op    byte
	first bool
	text  string
}

// rows wraps the lines of the diff to width, or takes them as the last frame wrapped them.
func (d diffState) rows(width int) []diffRow {
	if d.wrapped != nil && d.wrapped.width == width && d.wrapped.palette == styles.Gen() && width > 0 {
		return d.wrapped.rows
	}
	rows := make([]diffRow, 0, len(d.lines))
	for _, l := range d.lines {
		for i, part := range wrapIndented(l.text, width) {
			rows = append(rows, diffRow{op: l.op, first: i == 0, text: part})
		}
	}
	if d.wrapped != nil {
		*d.wrapped = diffRows{width: width, palette: styles.Gen(), rows: rows}
	}
	return rows
}

// wrapIndented wraps a line of indented json to width, its continuation lines indented two
// columns past the line's own indent, so a long value stays readable and in the json's shape.
func wrapIndented(s string, width int) []string {
	if ansi.StringWidth(s) <= width {
		return []string{s}
	}
	lead := min(len(s)-len(strings.TrimLeft(s, " ")), width/2, max(0, width-2))
	body := s[lead:]
	first := wrapWords(body, max(1, width-lead))[0]
	if !strings.HasPrefix(body, first) {
		first = ""
	}
	out := []string{s[:lead] + first}
	rest := strings.TrimLeft(body[len(first):], " ")
	if rest == "" {
		return out
	}
	hang, room := lead+2, width-lead-2
	if first == "" {
		out, hang = nil, lead
	}
	if room < 2 {
		room = max(1, min(2, width))
		hang = max(0, min(hang, width-room))
	}
	for _, part := range wrapWords(rest, room) {
		out = append(out, strings.Repeat(" ", hang)+part)
	}
	return out
}

// wrapWords wraps s to width at its spaces and hyphens, breaking a word only where it does not
// fit on a line of its own, as ansi.Wrap does, and hard where ansi.Wrap leaves a part too wide.
func wrapWords(s string, width int) []string {
	var out []string
	for part := range strings.SplitSeq(ansi.Wrap(s, width, ""), "\n") {
		if ansi.StringWidth(part) <= width {
			out = append(out, part)
			continue
		}
		out = append(out, strings.Split(ansi.Hardwrap(part, width, true), "\n")...)
	}
	return out
}

func (m model) diffView() string {
	return indent(scrolledLines(m.diffLines(), m.state.diff.scroll, contentHeight))
}
