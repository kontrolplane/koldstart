package tui

import (
	"encoding/json"
	"errors"
	"maps"
	"slices"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/kontrolplane/koldstart/internal/cli"
	"github.com/kontrolplane/koldstart/internal/tui/styles"
)

type resourceTab int

const (
	appliedTab resourceTab = iota
	liveTab
)

var resourceTabs = []string{"applied", "in aws"}

// resourceState is one resource of the stage: what koldstart applied, and what cloud control
// says of it now, with what differs.
type resourceState struct {
	res    stageResource
	tab    resourceTab
	scroll int
	// fromFunction is set when the overview of a function opened the resource, which q goes back
	// to.
	fromFunction bool

	liveGen int
	// liveLoaded is the last read done; live stays from the one before while r reads it again.
	liveLoaded bool
	live       map[string]any
	// liveAt is when live was read; liveErr is why the last read failed.
	liveAt  time.Time
	liveErr error

	// written is the json the page draws, written by the first frame that drew it.
	written *resourceJSON
}

// resourceJSON is the json of the resource page, written once rather than every frame: what
// koldstart applied, and what aws says, with what drifted, for the read of liveAt. It is written
// again in a new palette, though it holds no colour, so nothing kept outlives one.
type resourceJSON struct {
	palette     int
	applied     []string
	appliedDone bool

	liveAt   time.Time
	liveDone bool
	drift    []string
	props    []string
	marked   []bool
	// properties are what koldstart applied, as aws names them.
	properties map[string]any
}

// openResource shows a resource, and reads it as aws has it now.
func (m model) openResource(r stageResource) (model, tea.Cmd) {
	m.state.resource = resourceState{res: r, written: &resourceJSON{}}
	m = m.switchPage(resourcePage)
	if !readable(r) {
		return m, nil
	}
	return m.loadLive()
}

// readable reports whether there is something in aws to read the resource from: an id, and no
// remove that left it behind.
func readable(r stageResource) bool {
	return !r.retained && r.res.PhysicalID != ""
}

func (m model) applyLive(msg liveLoadedMsg) (model, tea.Cmd) {
	r := &m.state.resource
	if msg.gen != r.liveGen {
		return m, nil
	}
	r.liveLoaded = true
	switch {
	case msg.err != nil && r.live != nil && !errors.Is(msg.err, cli.ErrNotLive):
		r.liveErr = msg.err
	case msg.err != nil:
		r.live, r.liveErr = nil, msg.err
	default:
		r.live, r.liveAt, r.liveErr = msg.props, time.Now(), nil
		m.state.stage.markDrift(r.res.id, len(cli.Drift(appliedProperties(r.res), msg.props)) > 0)
		m = m.updateStageTables()
	}
	return m, nil
}

// resourceUpdate handles a key on the resource page. r reads it from aws again, the spinner
// showing until it is in; c copies its id in aws, or its arn when it has no id; q goes back to
// the function that opened it, or the stage.
func (m model) resourceUpdate(msg tea.KeyPressMsg) (model, tea.Cmd) {
	r := &m.state.resource
	switch {
	case key.Matches(msg, m.keys.Back, m.keys.Quit) && r.fromFunction:
		return m.switchPage(functionPage).syncFunction(), nil
	case key.Matches(msg, m.keys.Back, m.keys.Quit):
		return m.switchPage(stagePage), nil
	case key.Matches(msg, m.keys.NextTab, m.keys.PrevTab, m.keys.Left, m.keys.Right):
		r.tab, r.scroll = 1-r.tab, 0
		return m, nil
	case key.Matches(msg, m.keys.Copy):
		return m.copy(cmpOr(r.res.res.PhysicalID, r.res.res.Attributes["Arn"]))
	case key.Matches(msg, m.keys.Refresh):
		if !readable(r.res) {
			return m.loadQueues()
		}
		r.liveLoaded = false
		var live, queues tea.Cmd
		m, live = m.loadLive()
		m, queues = m.loadQueues()
		return m, tea.Batch(live, queues)
	}
	r.scroll = scrollBy(msg, m.keys, r.scroll, contentHeight, maxOffset(m.resourceLines().len(), contentHeight))
	return m, nil
}

// appliedProperties are the inputs a resource was applied with, or its properties for one of
// cloud control's.
func appliedProperties(r stageResource) map[string]any {
	var inputs map[string]any
	_ = json.Unmarshal(r.res.Inputs, &inputs)
	if props, ok := inputs["properties"].(map[string]any); ok && strings.HasPrefix(r.res.Type, "AWS::") {
		return props
	}
	return inputs
}

// headLines are the facts above both tabs: its type, its id, what keeps it, and how many
// messages a queue holds.
func (m model) headLines(width int) []string {
	r := m.state.resource.res
	res := r.res
	value := func(s string) cell { return text(orDash(s), styles.ToneBody) }
	policy := res.DeletionPolicy
	switch {
	case r.retained:
		policy = "left in place by a remove; the next deploy takes it over"
	case res.Imported:
		policy = "imported"
	}
	lines := []string{
		styles.SectionHeader(r.id, width),
		row("type", value(res.Type), width),
		row("id in aws", value(res.PhysicalID), width),
		row("policy", value(policy), width),
		row("depends on", value(strings.Join(res.Dependencies, ", ")), width),
	}
	s := m.state.stage
	d, read := s.queues[r.id]
	err, failed := s.queueErrs[r.id]
	switch {
	case read:
		lines = append(lines, row("messages", cell{
			styles.S(formatInt(d.Visible), styles.ToneText), styles.S(" queued", styles.ToneFaint), sep(),
			styles.S(formatInt(d.InFlight), styles.ToneBody), styles.S(" in flight", styles.ToneFaint), sep(),
			styles.S(formatInt(d.Delayed), styles.ToneBody), styles.S(" delayed", styles.ToneFaint),
		}, width))
		if failed {
			lines = append(lines, row("", staleNote("", s.queueAt[r.id], err), width))
		}
	case failed:
		lines = append(lines, row("messages", m.couldNotRead("the depth", err, styles.ToneMuted), width))
	}
	if res.Pending != nil {
		lines = append(lines, row("pending", text(res.Pending.Operation+" started "+formatAgo(res.Pending.StartedAt)+": deploy again to finish it", styles.ToneWarning), width))
	}
	return lines
}

// memo is the json kept for the page, emptied when the palette changed. Without one a frame
// writes the json again.
func (r resourceState) memo() *resourceJSON {
	w := r.written
	if w != nil && w.palette != styles.Gen() {
		*w = resourceJSON{palette: styles.Gen()}
	}
	return w
}

// appliedJSON is the json of what koldstart applied the resource with, none for nothing, as the
// first frame wrote it.
func (r resourceState) appliedJSON() []string {
	w := r.memo()
	if w != nil && w.appliedDone {
		return w.applied
	}
	var inputs any
	var lines []string
	if json.Unmarshal(r.res.res.Inputs, &inputs) == nil && inputs != nil {
		lines = jsonLines(inputs)
	}
	if w != nil {
		w.applied, w.appliedDone = lines, true
	}
	return lines
}

// liveJSON is what aws says of the resource as json, what drifted from what koldstart applied,
// and which lines of the json are of what drifted, as the first frame after the read wrote them.
func (r resourceState) liveJSON() resourceJSON {
	w := r.memo()
	if w != nil && w.liveDone && w.liveAt.Equal(r.liveAt) {
		return *w
	}
	applied := appliedProperties(r.res)
	drift := cli.Drift(applied, r.live)
	drifted := map[string]bool{}
	for _, k := range drift {
		drifted[k] = true
	}
	props := jsonLines(r.live)
	out := resourceJSON{liveAt: r.liveAt, liveDone: true, drift: drift, props: props, marked: driftedLines(props, drifted), properties: applied}
	if w != nil {
		w.liveAt, w.liveDone, w.drift, w.props, w.marked, w.properties = out.liveAt, true, out.drift, out.props, out.marked, out.properties
	}
	return out
}

// truncatedLines draws lines of json cut to width in a tone, only where they are in sight.
func truncatedLines(lines []string, width int, tone func(i int) styles.Tone) func(int) string {
	return func(i int) string { return styles.Fg(tone(i)).Render(ansi.Truncate(lines[i], width, "…")) }
}

func (m model) appliedLines(width int) lazyLines {
	r := m.state.resource
	res := r.res.res
	lines := []string{"", styles.SectionHeader("attributes", width)}
	keys := slices.Sorted(maps.Keys(res.Attributes))
	labels := labelsFor(keys, width)
	for _, k := range keys {
		lines = append(lines, rowIn(labels, styles.Clean(k), text(orDash(res.Attributes[k]), styles.ToneBody), width))
	}
	if len(res.Attributes) == 0 {
		lines = append(lines, styles.Faint("none"))
	}
	lines = append(lines, "", styles.SectionHeader("applied with", width))
	applied := r.appliedJSON()
	if len(applied) == 0 {
		return lazyOf(append(lines, styles.Faint("nothing")))
	}
	out := lazyOf(lines)
	out.addDrawn(len(applied), truncatedLines(applied, width, func(int) styles.Tone { return styles.ToneBody }))
	return out
}

// liveLinesOf are the in aws tab: the resource as cloud control reads it now, what differs from
// what koldstart applied, or why it cannot be read. The properties are drawn only where they are
// in sight.
func (m model) liveLinesOf(width int) lazyLines {
	r := m.state.resource
	lines := []string{""}
	say := func(s string) {
		lines = append(lines, wrapped(s, styles.ToneFaint, width, 0)...)
	}
	reading := styles.Accent(m.spinner.View()) + " " + styles.Muted(readingNote("it from aws"))
	switch {
	case r.res.retained:
		say("a remove left it in place: koldstart does not read it until the next deploy takes it over")
		return lazyOf(lines)
	case r.res.res.PhysicalID == "":
		say("it has no id in aws yet: the next deploy creates it")
		return lazyOf(lines)
	case !r.liveLoaded && r.live == nil:
		return lazyOf(append(lines, reading))
	case errors.Is(r.liveErr, cli.ErrNotLive):
		say("cloud control cannot read a " + shortType(r.res.res.Type) + ": what koldstart applied is on the applied tab")
		return lazyOf(lines)
	case r.liveErr != nil && r.live == nil:
		lines = append(lines, cellLines(m.couldNotRead("it from aws", r.liveErr.Error(), styles.ToneDanger), width)...)
		lines = append(lines, "")
		say("reading it takes cloud control, which an emulator may not have, and cloudformation:GetResource with the read permissions of its type. what koldstart applied is on the applied tab")
		return lazyOf(lines)
	case !r.liveLoaded:
		lines = append(lines, reading, "")
	case r.liveErr != nil:
		lines = append(lines, cellLines(staleNote("", r.liveAt, r.liveErr.Error()), width)...)
		lines = append(lines, "")
	}
	if len(r.live) == 0 {
		say("aws returned no properties for it")
		return lazyOf(lines)
	}
	live := r.liveJSON()
	applied, drift := live.properties, live.drift
	switch {
	case len(drift) > 0:
		lines = append(lines, renderCell(cell{styles.S("▲ ", styles.ToneWarning), styles.S("changed outside koldstart: "+strings.Join(drift, ", "), styles.ToneWarning)}, column{width: width}, nil))
		labels := labelsFor(drift, width)
		for _, k := range drift {
			lines = append(lines, rowIn(labels, styles.Clean(k), cell{
				styles.S(compactJSON(applied[k]), styles.ToneBody), styles.S("  →  ", styles.ToneFaint), styles.S(compactJSON(r.live[k]), styles.ToneWarning),
			}, width))
		}
		back := "the next deploy sets it back to what the configuration says"
		if len(drift) > 1 {
			back = "the next deploy sets them back to what the configuration says"
		}
		lines = append(lines, styles.Faint(back))
	case len(applied) > 0:
		lines = append(lines, styles.Render(styles.S("✓ ", styles.ToneSuccess), styles.S("as koldstart applied it", styles.ToneBody)))
	}
	lines = append(lines, "", styles.SectionHeader("properties in aws", width))
	out := lazyOf(lines)
	out.addDrawn(len(live.props), truncatedLines(live.props, width, func(i int) styles.Tone {
		if live.marked[i] {
			return styles.ToneWarning
		}
		return styles.ToneBody
	}))
	return out
}

// driftedLines marks the lines of an object written by jsonLines that belong to a drifted key at
// its top level: the key's own line, and every line of a value nested under it.
func driftedLines(lines []string, drifted map[string]bool) []bool {
	marked := make([]bool, len(lines))
	in := false
	for i, line := range lines {
		switch {
		case !strings.HasPrefix(line, "  "):
			in = false
		case strings.HasPrefix(line, `  "`):
			tok, err := json.NewDecoder(strings.NewReader(line[2:])).Token()
			k, ok := tok.(string)
			in = err == nil && ok && drifted[k]
		}
		marked[i] = in
	}
	return marked
}

// compactJSON writes a value on one line, with secrets and refs shown by what they stand for.
func compactJSON(v any) string {
	data, err := json.Marshal(presentable(v))
	if err != nil {
		return err.Error()
	}
	return string(data)
}

// resourceLines are the lines of the resource page: the facts above the tabs, the tabs, and the
// tab on screen.
func (m model) resourceLines() lazyLines {
	r := m.state.resource
	width := contentWidth - 4
	lines := lazyOf(append(m.headLines(width), "", tabBar(resourceTabs, nil, int(r.tab))))
	if r.tab == liveTab {
		lines.join(m.liveLinesOf(width))
	} else {
		lines.join(m.appliedLines(width))
	}
	return lines
}

func (m model) resourceView() string {
	return indent(scrolledLines(m.resourceLines(), m.state.resource.scroll, contentHeight))
}
