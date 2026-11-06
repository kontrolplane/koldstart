package tui

import (
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
	// json is a value of json read with a cursor, and jsonOf the value it was read from.
	json   jsonView
	jsonOf string
}

// outputDrawn is an output's value wrapped to width, as plain lines.
type outputDrawn struct {
	value string
	width int
	lines []string
}

// openOutput shows an output of the stage, its value in a json view when it is json.
func (m model) openOutput(name string) model {
	m.state.output = outputState{name: name, drawn: &outputDrawn{}}
	return m.syncOutput().switchPage(outputPage)
}

// syncOutput reads the output's value as json again when a read of the stage changed it, keeping
// what was folded, searched and under the cursor where it is found again.
func (m model) syncOutput() model {
	o := &m.state.output
	v, ok := m.outputValueOf()
	if ok && v == o.jsonOf && !o.json.empty() {
		return m
	}
	o.jsonOf = v
	if _, _, isJSON := outputJSON(v); !ok || !isJSON {
		o.json = jsonView{}
		return m
	}
	o.json = newJSONViewBytes([]byte(v)).carry(o.json)
	return m
}

// shown is the json of the output, or nil when it is not json.
func (o *outputState) shown() *jsonView {
	if o.json.empty() {
		return nil
	}
	return &o.json
}

// outputUpdate handles a key on the output page. The keys of json come first, as on a resource:
// they move the cursor through it, fold it, search it, and c copies the value under the cursor,
// all of it as the stage holds it on the top line. On a value that is not json c copies it as it
// is; r reads the stage again, and the rest scroll.
func (m model) outputUpdate(msg tea.KeyPressMsg) (model, tea.Cmd) {
	o := &m.state.output
	if v := o.shown(); v != nil {
		before := v.at()
		if res, ok := v.update(msg, m.keys, outputWidth(), contentHeight); ok {
			m = m.followOutputCursor(msg, before)
			return m.jsonDone(res)
		}
	}
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

// followOutputCursor scrolls the page after a key on its json so the cursor is in sight.
func (m model) followOutputCursor(msg tea.KeyPressMsg, before int) model {
	o := &m.state.output
	lines, at := m.outputBody()
	o.scroll = m.followJSON(o.shown(), msg, before, o.scroll, at, lines.len(), outputWidth())
	return m
}

// outputWidth is the width the output page draws its lines in.
func outputWidth() int { return detailWidth() }

// outputValueLines are a value that is not json as plain lines in width, as it is, every line
// wrapped under its indent. json is read in a jsonView.
func outputValueLines(v string, width int) []string {
	raw := strings.Split(styles.CleanBlock(strings.TrimRight(strings.ReplaceAll(v, "\r\n", "\n"), "\n")), "\n")
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
	lines, _ := m.outputBody()
	return lines
}

// outputBody is outputLines, and the line its json starts at, or -1 when the value is not json.
func (m model) outputBody() (lazyLines, int) {
	o := m.state.output
	width := outputWidth()
	lines := []string{styles.SectionHeader(o.name, width)}
	v, ok := m.outputValueOf()
	if !ok {
		return lazyOf(append(lines, styles.Faint("the stage no longer has this output: a deploy removed it"))), -1
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
		return out, -1
	}
	if j := o.shown(); j != nil && o.jsonOf == v {
		at := out.len()
		out.join(j.lines(width))
		return out, at
	}
	body := o.valueLines(v, width)
	out.addDrawn(len(body), func(i int) string { return styles.Body(body[i]) })
	return out, -1
}

func (m model) outputView() string {
	return indent(scrolledLines(m.outputLines(), m.state.output.scroll, contentHeight))
}
