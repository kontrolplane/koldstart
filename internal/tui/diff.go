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
}

// openDiff shows what changes in a resource, from before to after.
func (m model) openDiff(title, detail string, before, after any) (model, tea.Cmd) {
	m.state.diff = diffState{title: title, detail: detail, lines: lineDiff(jsonLines(before), jsonLines(after)), from: m.page}
	return m.switchPage(diffPage), nil
}

func (m model) diffUpdate(msg tea.KeyPressMsg) (model, tea.Cmd) {
	d := &m.state.diff
	if key.Matches(msg, m.keys.Back, m.keys.Quit) {
		return m.switchPage(d.from), nil
	}
	d.scroll = min(scrollBy(msg, m.keys, d.scroll, contentHeight), max(0, len(m.diffLines())-contentHeight))
	return m, nil
}

func (m model) diffLines() []string {
	d := m.state.diff
	width := contentWidth - 4
	added, removed := 0, 0
	for _, l := range d.lines {
		switch l.op {
		case '+':
			added++
		case '-':
			removed++
		}
	}
	lines := []string{
		styles.SectionHeader(d.title, width),
		" " + styles.Render(styles.S(d.detail, styles.ToneMuted), styles.S("  ", styles.ToneFaint),
			styles.S("+"+formatInt(added), styles.ToneSuccess), styles.S(" ", styles.ToneFaint), styles.S("-"+formatInt(removed), styles.ToneDanger)),
		"",
	}
	if len(d.lines) == 0 {
		return append(lines, " "+styles.Faint("no inputs on either side"))
	}
	for _, l := range d.lines {
		mark, tone := "  ", styles.ToneFaint
		switch l.op {
		case '+':
			mark, tone = "+ ", styles.ToneSuccess
		case '-':
			mark, tone = "- ", styles.ToneDanger
		}
		style := styles.Fg(tone)
		for i, part := range wrapIndented(l.text, width-2) {
			if i > 0 {
				mark = "  "
			}
			lines = append(lines, style.Render(mark+part))
		}
	}
	return lines
}

// wrapIndented wraps a line of indented json to width, its continuation lines indented two
// columns past the line's own indent, so a long value stays readable and in the json's shape.
func wrapIndented(s string, width int) []string {
	if ansi.StringWidth(s) <= width {
		return []string{s}
	}
	lead := min(len(s)-len(strings.TrimLeft(s, " ")), width/2)
	body := s[lead:]
	first, _, _ := strings.Cut(ansi.Wrap(body, max(1, width-lead), ""), "\n")
	if !strings.HasPrefix(body, first) {
		first = ""
	}
	out := []string{s[:lead] + first}
	rest := strings.TrimLeft(body[len(first):], " ")
	if rest == "" {
		return out
	}
	hang := strings.Repeat(" ", lead+2)
	if first == "" {
		out, hang = nil, s[:lead]
	}
	for part := range strings.SplitSeq(ansi.Wrap(rest, max(1, width-lead-2), ""), "\n") {
		out = append(out, hang+part)
	}
	return out
}

func (m model) diffView() string {
	return indent(scrolled(m.diffLines(), m.state.diff.scroll, contentHeight))
}
