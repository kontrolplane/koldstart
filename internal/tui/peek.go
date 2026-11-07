package tui

import (
	"bytes"
	"encoding/json"
	"maps"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/kontrolplane/koldstart/internal/cli"
	"github.com/kontrolplane/koldstart/internal/desired"
	"github.com/kontrolplane/koldstart/internal/state"
	"github.com/kontrolplane/koldstart/internal/tui/styles"
)

// peekMin is the fewest lines a peek is drawn in: its heading and enough under it to say
// something. A tab whose rows leave less shows its table alone.
const peekMin = 6

// stageBody is the table of the open tab and, in the room its rows leave, a peek of the row
// under the cursor, so a stage of a few functions or outputs fills its page with what they are
// rather than with nothing. A table of more rows than fit takes the page, as it did.
func (m model) stageBody() string {
	s := m.state.stage
	return withPeek(s.tables[s.tab], func(height int) []string { return m.peekLines(tableWidth(), height) })
}

// withPeek is a table, and under it in the room its rows leave, what peek draws in that many
// lines; the table alone where its rows leave less than peekMin, or peek draws nothing.
func withPeek(t dataTable, peek func(height int) []string) string {
	rows := len(t.rows)
	room := t.height - rows - 1
	if rows == 0 || room < peekMin {
		return t.View()
	}
	lines := peek(room)
	if len(lines) == 0 {
		return t.View()
	}
	if len(lines) > room {
		lines = lines[:room]
	}
	t.height, t.offset = rows, 0
	for range room - len(lines) {
		lines = append(lines, "")
	}
	return strings.Join(append([]string{t.View(), ""}, lines...), "\n")
}

// peekLines are the lines of the peek of the open tab's row under the cursor, at most height, or
// none for a tab without one.
func (m model) peekLines(width, height int) []string {
	s := m.state.stage
	t := s.tables[s.tab]
	if t.cursor >= len(s.keys[s.tab]) {
		return nil
	}
	key := s.keys[s.tab][t.cursor]
	var lines []string
	switch s.tab {
	case functionsTab:
		lines = m.functionPeek(key, height)
	case outputsTab:
		lines = m.outputPeek(key, width)
	}
	if len(lines) > height {
		lines = lines[:height]
	}
	return lines
}

// functionPeek is the overview of a function, as its page draws it, in the room under the
// table: its name and facts, then its configuration, what lambda says of it now, its permissions,
// its last hour, what invokes it and its environment, as far as they fit. Enter opens the page,
// where they scroll.
func (m model) functionPeek(name string, height int) []string {
	s := m.state.stage
	res := s.st.Resources[desired.FunctionID(name)]
	if res == nil {
		return nil
	}
	pm := m
	pm.state.function = functionState{name: name}
	pm = pm.syncFunction()
	if p, ok := s.peekLive[res.PhysicalID]; ok {
		pm.state.function.live, pm.state.function.liveAt, pm.state.function.liveErr = p.live, p.at, p.err
	}
	o := pm.functionOverview()
	head := slices.Clone(o.head)
	width := contentWidth - 2
	head[0] = styles.SectionHeader(styles.Clean(res.PhysicalID), width-ansi.StringWidth(openHint)-1) + " " + styles.Faint(openHint)
	rows := height - len(head)
	if rows < peekMin-len(head) || rows < 2 {
		return nil
	}
	blocks := make([]string, 0, 2*len(o.cols))
	for i, c := range o.cols {
		if i > 0 {
			blocks = append(blocks, fill("", overviewGap, rows))
		}
		blocks = append(blocks, c.draw(0, rows, -1))
	}
	return append(head, strings.Split(lipgloss.JoinHorizontal(lipgloss.Top, blocks...), "\n")...)
}

// openHint says enter opens what a peek shows.
const openHint = "enter opens it"

// peekLive is what lambda said of a function its peek showed, when, and why the last read of it
// failed.
type peekLive struct {
	live *cli.FunctionState
	at   time.Time
	err  string
}

// peekLiveMsg is what lambda said of a function, by its name in lambda, for its peek.
type peekLiveMsg struct {
	gen  int
	name string
	live cli.FunctionState
	err  error
}

// peekEvery is how long what lambda said of a function stays on its peek before it is read again.
const peekEvery = 5 * time.Second

// loadPeekLive reads what lambda says of the function under the cursor of the functions tab, for
// its peek, unless it was read in the last peekEvery. A read of another function before it came
// back is dropped, as the cursor has moved on.
func (m model) loadPeekLive() (model, tea.Cmd) {
	s := &m.state.stage
	t := s.tables[functionsTab]
	if m.page != stagePage || s.tab != functionsTab || s.st == nil || t.cursor >= len(s.keys[functionsTab]) {
		return m, nil
	}
	res := s.st.Resources[desired.FunctionID(s.keys[functionsTab][t.cursor])]
	if res == nil || res.PhysicalID == "" {
		return m, nil
	}
	if p, ok := s.peekLive[res.PhysicalID]; ok && time.Since(p.at) < peekEvery {
		return m, nil
	}
	gen := m.next()
	s.peekGen = gen
	ref, name := s.ref, res.PhysicalID
	ctx, cancel := m.withTimeout(loadTimeout)
	return m, func() tea.Msg {
		defer cancel()
		live, err := m.b.Function(ctx, ref, name)
		return peekLiveMsg{gen: gen, name: name, live: live, err: err}
	}
}

// applyPeekLive keeps what lambda said of a function for its peek; what the last read of it
// said stays when this one failed.
func (m model) applyPeekLive(msg peekLiveMsg) (model, tea.Cmd) {
	s := &m.state.stage
	if msg.gen != s.peekGen {
		return m, nil
	}
	s.peekLive = cloned(s.peekLive)
	p := s.peekLive[msg.name]
	p.at = time.Now()
	if msg.err != nil {
		p.err = msg.err.Error()
	} else {
		live := msg.live
		p.live, p.err = &live, ""
	}
	s.peekLive[msg.name] = p
	return m, nil
}

// outputPeek is an output's value whole: json indented in the order the stage holds it, and text
// wrapped.
func (m model) outputPeek(name string, width int) []string {
	v, ok := m.state.stage.st.Outputs[name]
	if !ok {
		return nil
	}
	head := spread(styles.SectionHeader(name, width-ansi.StringWidth(openHint)-1), styles.Faint(openHint), width)
	var body []string
	var indented bytes.Buffer
	if trimmed := strings.TrimSpace(v); (strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[")) && json.Indent(&indented, []byte(trimmed), "", "  ") == nil {
		for l := range strings.SplitSeq(indented.String(), "\n") {
			body = append(body, renderCell(jsonLineCell(l), column{width: width - rowInset}, nil))
		}
	} else {
		for l := range strings.SplitSeq(v, "\n") {
			body = append(body, wrapped(l, styles.ToneBody, width-rowInset, 0)...)
		}
	}
	return append([]string{head}, insetLines(body)...)
}

// stagePeek is the state of a stage the peek of the stages showed, when it was read, and why the
// last read failed.
type stagePeek struct {
	st  *state.State
	at  time.Time
	err string
}

// stagePeekMsg is the state of a stage, for its peek on the stages.
type stagePeekMsg struct {
	gen int
	ref cli.StageRef
	st  *state.State
	err error
}

// loadStagePeek reads the state of the stage under the cursor of the stages, for its peek,
// unless it was read in the last peekEvery or it was never deployed.
func (m model) loadStagePeek() (model, tea.Cmd) {
	o := &m.state.overview
	sel, ok := m.selectedStage()
	if m.page != overviewPage || !ok || notDeployed(sel) || sel.Err != nil {
		return m, nil
	}
	if p, ok := o.peeks[sel.StageRef]; ok && time.Since(p.at) < peekEvery {
		return m, nil
	}
	gen := m.next()
	o.peekGen = gen
	ref := sel.StageRef
	ctx, cancel := m.withTimeout(loadTimeout)
	return m, func() tea.Msg {
		defer cancel()
		st, _, err := m.b.Stage(ctx, ref)
		return stagePeekMsg{gen: gen, ref: ref, st: st, err: err}
	}
}

// applyStagePeek keeps the state of a stage for its peek; what the last read said stays when
// this one failed.
func (m model) applyStagePeek(msg stagePeekMsg) (model, tea.Cmd) {
	o := &m.state.overview
	if msg.gen != o.peekGen {
		return m, nil
	}
	o.peeks = cloned(o.peeks)
	p := o.peeks[msg.ref]
	p.at = time.Now()
	if msg.err != nil {
		p.err = msg.err.Error()
	} else {
		p.st, p.err = msg.st, ""
	}
	o.peeks[msg.ref] = p
	return m, nil
}

// overviewPeek is the stage under the cursor of the stages at a glance, below its name: its
// functions, each with its runtime, memory and what invokes it, beside its outputs.
func (m model) overviewPeek(width int) []string {
	sel, ok := m.selectedStage()
	if !ok || notDeployed(sel) || sel.Err != nil {
		return nil
	}
	head := styles.SectionHeader(styles.Clean(sel.String()), width-ansi.StringWidth(openHint)-1) + " " + styles.Faint(openHint)
	p, ok := m.state.overview.peeks[sel.StageRef]
	switch {
	case !ok || p.st == nil && p.err == "":
		return []string{head, row("", text(readingNote("the state of "+sel.String()), styles.ToneMuted), width)}
	case p.st == nil:
		return append([]string{head}, insetLines(cellLines(m.couldNotRead("the state", styles.Clean(p.err), styles.ToneMuted), width-rowInset))...)
	}
	left := width
	split := width >= overviewTwoColumns
	if split {
		left = (width - overviewGap) / 2
	}
	functions := stageFunctionLines(p.st, left)
	outputs := stageOutputLines(p.st, width-left-overviewGap)
	if !split {
		outputs = stageOutputLines(p.st, width)
		return append(append(append([]string{head}, functions...), ""), outputs...)
	}
	lines := []string{head}
	for i := range max(len(functions), len(outputs)) {
		var l, r string
		if i < len(functions) {
			l = functions[i]
		}
		if i < len(outputs) {
			r = outputs[i]
		}
		lines = append(lines, l+strings.Repeat(" ", max(0, left+overviewGap-ansi.StringWidth(l)))+r)
	}
	return lines
}

// stageFunctionLines are a stage's functions under a heading, each by its name with its runtime,
// its memory and what invokes it, its url first, counted by kind.
func stageFunctionLines(st *state.State, width int) []string {
	var names []string
	for id := range st.Resources {
		if name, ok := strings.CutPrefix(id, desired.FunctionID("")); ok {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	lines := []string{sectionHeader("functions", countOf(len(names)), width)}
	if len(names) == 0 {
		return append(lines, row("", text("none", styles.ToneFaint), width))
	}
	labels := labelsFor(names, width)
	for _, name := range names {
		in := functionInputs(st.Resources[desired.FunctionID(name)])
		value := cell{styles.S(cmpOr(in.Runtime, "container image"), styles.ToneBody)}
		if in.Memory > 0 {
			value = append(value, sep(), styles.S(formatInt(int(in.Memory)), styles.ToneBody), styles.S(" mb", styles.ToneFaint))
		}
		kinds := map[string]int{}
		var order []string
		if st.Resources[desired.URLID(name)] != nil {
			kinds[kindURL], order = 1, []string{kindURL}
		}
		for _, t := range invokersOf(st, name) {
			if kinds[t.kind] == 0 {
				order = append(order, t.kind)
			}
			kinds[t.kind]++
		}
		for _, k := range order {
			value = append(value, sep(), styles.S(plural(kinds[k], styles.Clean(k)), styles.ToneMuted))
		}
		lines = append(lines, rowIn(labels, styles.Clean(name), value, width))
	}
	return lines
}

// stageOutputLines are a stage's outputs under a heading, each value on one line, cut in its
// middle where it does not fit.
func stageOutputLines(st *state.State, width int) []string {
	names := slices.Sorted(maps.Keys(st.Outputs))
	lines := []string{sectionHeader("outputs", countOf(len(names)), width)}
	if len(names) == 0 {
		return append(lines, row("", text("none", styles.ToneFaint), width))
	}
	labels := labelsFor(names, width)
	for _, name := range names {
		value, _ := outputValue(st.Outputs[name])
		lines = append(lines, rowIn(labels, styles.Clean(name), value, width))
	}
	return lines
}

// kindURL is what a function's url is, among what invokes it.
const kindURL = "url"

// countOf is a count beside a heading, or nothing for none.
func countOf(n int) string {
	if n == 0 {
		return ""
	}
	return formatInt(n)
}
