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

// presentable replaces koldstart's own values, refs, secrets and outputs, with what they stand
// for, so a diff shows them as a person writes them.
func presentable(v any) any {
	switch x := v.(type) {
	case map[string]any:
		if _, ok := x["$koldstart"]; ok {
			return engine.Describe(x)
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

// jsonLines writes a value as indented json, a line each, or none for nil.
func jsonLines(v any) []string {
	if v == nil {
		return nil
	}
	data, err := json.MarshalIndent(presentable(v), "", "  ")
	if err != nil {
		return []string{err.Error()}
	}
	return strings.Split(styles.CleanBlock(string(data)), "\n")
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
	d.scroll = scrollBy(msg, m.keys, d.scroll, contentHeight)
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
		text := ansi.Truncate(l.text, width-2, "…")
		lines = append(lines, styles.Fg(tone).Render(mark+text))
	}
	return lines
}

func (m model) diffView() string {
	return indent(scrolled(m.diffLines(), m.state.diff.scroll, contentHeight))
}
