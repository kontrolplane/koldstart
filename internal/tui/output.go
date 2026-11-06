package tui

import (
	"encoding/json"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/kontrolplane/koldstart/internal/cli"
	"github.com/kontrolplane/koldstart/internal/tui/styles"
)

// outputState is one output of the stage, its value in full.
type outputState struct {
	name   string
	scroll int
	// drawn is the value as the last frame wrapped it, so the next does not wrap it again.
	drawn *outputDrawn
}

// outputDrawn is an output's value wrapped to width, as plain lines.
type outputDrawn struct {
	value string
	width int
	lines []string
}

// openOutput shows an output of the stage, its value pretty-printed when it is json.
func (m model) openOutput(name string) model {
	m.state.output = outputState{name: name, drawn: &outputDrawn{}}
	return m.switchPage(outputPage)
}

// outputUpdate handles a key on the output page: c copies the value as it is, r reads the stage
// again, and the rest scroll.
func (m model) outputUpdate(msg tea.KeyPressMsg) (model, tea.Cmd) {
	o := &m.state.output
	switch {
	case key.Matches(msg, m.keys.Back, m.keys.Quit):
		return m.switchPage(stagePage), nil
	case key.Matches(msg, m.keys.Copy):
		v, _ := m.outputValueOf()
		return m.copy(v)
	case key.Matches(msg, m.keys.Refresh):
		return m.loadStage()
	}
	o.scroll = scrollBy(msg, m.keys, o.scroll, contentHeight, maxOffset(m.outputLines().len(), contentHeight))
	return m, nil
}

// outputValueOf is the value of the output on screen, and whether the stage still has it.
func (m model) outputValueOf() (string, bool) {
	st := m.state.stage.st
	if st == nil {
		return "", false
	}
	v, ok := st.Outputs[m.state.output.name]
	return v, ok
}

// outputKind says what an output holds: a json object or list and how many keys or items, a url,
// an arn, or text, with its lines when there is more than one.
func outputKind(v string) string {
	trimmed := strings.TrimSpace(v)
	if _, hint, ok := outputJSON(v); ok {
		if strings.HasPrefix(hint, "{") {
			return "json object · " + strings.TrimPrefix(hint, "{…} ")
		}
		return "json list · " + strings.TrimPrefix(hint, "[…] ")
	}
	lines := strings.Count(strings.TrimRight(strings.ReplaceAll(v, "\r\n", "\n"), "\n"), "\n") + 1
	switch {
	case v == "":
		return "nothing"
	case lines > 1:
		return "text · " + plural(lines, "line")
	case strings.HasPrefix(trimmed, "arn:"):
		return "arn"
	case strings.Contains(trimmed, "://") && !strings.ContainsAny(trimmed, " \t"):
		return "url"
	}
	return "text"
}

// outputValueLines are an output's value as plain lines in width: json pretty-printed by
// jsonLines, and anything else as it is, every line wrapped under its indent. This is where the
// json viewer takes over the json, when it is in.
func outputValueLines(v string, width int) []string {
	var raw []string
	if _, _, ok := outputJSON(v); ok {
		dec := json.NewDecoder(strings.NewReader(v))
		dec.UseNumber()
		var parsed any
		if dec.Decode(&parsed) == nil {
			raw = jsonLines(parsed)
		}
	}
	if raw == nil {
		raw = strings.Split(styles.CleanBlock(strings.TrimRight(strings.ReplaceAll(v, "\r\n", "\n"), "\n")), "\n")
	}
	out := make([]string, 0, len(raw))
	for _, line := range raw {
		out = append(out, wrapIndented(line, width)...)
	}
	return out
}

// valueLines are the value's lines in width, as the last frame wrapped them when the value and
// the width are the same.
func (o outputState) valueLines(v string, width int) []string {
	d := o.drawn
	if d != nil && d.lines != nil && d.width == width && d.value == v {
		return d.lines
	}
	lines := outputValueLines(v, width)
	if d != nil {
		*d = outputDrawn{value: v, width: width, lines: lines}
	}
	return lines
}

// outputLines are the lines of the output page: its name, what it holds and how big it is, and
// its value, drawn only where they are in sight.
func (m model) outputLines() lazyLines {
	o := m.state.output
	width := contentWidth - 4
	lines := []string{styles.SectionHeader(o.name, width)}
	v, ok := m.outputValueOf()
	if !ok {
		return lazyOf(append(lines, styles.Faint("the stage no longer has this output: a deploy removed it")))
	}
	lines = append(lines,
		row("holds", text(outputKind(v), styles.ToneBody), width),
		row("size", text(cli.Size(int64(len(v))), styles.ToneBody), width),
		"",
		styles.SectionHeader("value", width),
	)
	out := lazyOf(lines)
	if v == "" {
		out.add(styles.Faint("empty"))
		return out
	}
	body := o.valueLines(v, width)
	out.addDrawn(len(body), func(i int) string { return styles.Body(body[i]) })
	return out
}

func (m model) outputView() string {
	return indent(scrolledLines(m.outputLines(), m.state.output.scroll, contentHeight))
}
