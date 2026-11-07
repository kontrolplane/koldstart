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

	// applied is the json koldstart applied the resource with, and props what aws says of it now,
	// its drifted keys marked.
	applied jsonView
	props   jsonView
	// drift is what was changed outside koldstart, and compared whether there was anything
	// applied to compare what aws says with.
	drift    []string
	compared bool
}

// openResource shows a resource, and reads it as aws has it now.
func (m model) openResource(r stageResource) (model, tea.Cmd) {
	m.state.resource = resourceState{res: r, applied: newJSONViewBytes(r.res.Inputs)}
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
		return m, nil
	case msg.err != nil:
		r.live, r.liveErr = nil, msg.err
	default:
		r.live, r.liveAt, r.liveErr = msg.props, time.Now(), nil
		m.state.stage.markDrift(r.res.id, len(cli.Drift(appliedProperties(r.res), msg.props)) > 0)
		m = m.updateStageTables()
	}
	r.props, r.drift, r.compared = r.liveView()
	return m, nil
}

// liveView is the json of what aws says of the resource, each key changed outside koldstart
// marked with what it was applied with, what was changed, and whether there was anything applied
// to compare with. What it was folded, searched and scrolled to stays where it is found again.
func (r resourceState) liveView() (jsonView, []string, bool) {
	if len(r.live) == 0 {
		return jsonView{}, nil, false
	}
	applied := appliedProperties(r.res)
	drift := cli.Drift(applied, r.live)
	v := newJSONView(r.live)
	for _, k := range drift {
		v.markKey(k, compactJSON(applied[k]))
	}
	return v.carry(r.props), drift, len(applied) > 0
}

// shown is the json of the tab on screen, or nil when the tab shows none.
func (r *resourceState) shown() *jsonView {
	v := &r.applied
	if r.tab == liveTab {
		v = &r.props
	}
	if v.empty() {
		return nil
	}
	return v
}

// resourceUpdate handles a key on the resource page. The keys of its json come first: they move
// the cursor through it, fold it, search it, and c copies the value under the cursor. r reads the
// resource from aws again, the spinner showing until it is in; without json on the tab, c copies
// its id in aws, or its arn when it has no id; q goes back to the function that opened it, or the
// stage.
func (m model) resourceUpdate(msg tea.KeyPressMsg) (model, tea.Cmd) {
	r := &m.state.resource
	if v := r.shown(); v != nil {
		before := v.at()
		if res, ok := v.update(msg, m.keys, resourceWidth(), contentHeight); ok {
			m = m.followResourceCursor(msg, before)
			return m.jsonDone(res)
		}
	}
	switch {
	case key.Matches(msg, m.keys.Back, m.keys.Quit) && r.fromFunction:
		return m.switchPage(functionPage).syncFunction(), nil
	case key.Matches(msg, m.keys.Back, m.keys.Quit):
		return m.switchPage(stagePage), nil
	case key.Matches(msg, m.keys.Tab):
		if n, ok := tabNumber(msg, len(resourceTabs)); ok && n != int(r.tab) {
			return m.resourceUpdate(tea.KeyPressMsg{Code: tea.KeyTab})
		}
		return m, nil
	case key.Matches(msg, m.keys.NextTab, m.keys.PrevTab, m.keys.Left, m.keys.Right):
		r.tab, r.scroll = 1-r.tab, 0
		if v := r.shown(); v != nil && v.at() > 0 {
			m = m.followResourceCursor(tea.KeyPressMsg{}, -1)
		}
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

// followResourceCursor scrolls the page after a key on its json so the cursor is in sight.
func (m model) followResourceCursor(msg tea.KeyPressMsg, before int) model {
	r := &m.state.resource
	lines, at := m.resourceBody()
	r.scroll = m.followJSON(r.shown(), msg, before, r.scroll, at, lines.len(), resourceWidth())
	return m
}

// resourceWidth is the width the resource page draws its lines in.
func resourceWidth() int { return detailWidth() }

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

// appliedLines are the applied tab: the resource's attributes, long values wrapped, and the json
// it was applied with, which starts at the line returned, or -1 when there is none.
func (m model) appliedLines(width int) (lazyLines, int) {
	r := m.state.resource
	res := r.res.res
	lines := []string{"", styles.SectionHeader("attributes", width)}
	names := slices.Sorted(maps.Keys(res.Attributes))
	labels := labelsFor(names, width)
	for _, k := range names {
		lines = append(lines, wrappedRow(labels, styles.Clean(k), text(orDash(res.Attributes[k]), styles.ToneBody), width)...)
	}
	if len(res.Attributes) == 0 {
		lines = append(lines, styles.Faint("none"))
	}
	lines = append(lines, "", styles.SectionHeader("applied with", width))
	if r.applied.empty() {
		return lazyOf(append(lines, styles.Faint("nothing"))), -1
	}
	out := lazyOf(lines)
	at := out.len()
	out.join(r.applied.lines(width))
	return out, at
}

// wrappedRow is rowIn with its value wrapped onto the lines below the first rather than cut.
func wrappedRow(labels int, label string, value cell, width int) []string {
	room := max(1, width-labels-rowInset)
	parts := wrapCell(value, room)
	out := make([]string, 0, len(parts))
	for i, part := range parts {
		if i == 0 {
			out = append(out, rowIn(labels, label, part, width))
			continue
		}
		out = append(out, strings.Repeat(" ", rowInset+labels)+renderCell(part, column{width: room}, nil))
	}
	return out
}

// liveLinesOf are the in aws tab: the resource as cloud control reads it now, what differs from
// what koldstart applied, or why it cannot be read. The properties are json, which starts at the
// line returned, or -1 when there is none, each line of what drifted marked ▲, and the first of a
// drifted key written as applied → now.
func (m model) liveLinesOf(width int) (lazyLines, int) {
	r := m.state.resource
	lines := []string{""}
	say := func(s string) {
		lines = append(lines, wrapped(s, styles.ToneFaint, width, 0)...)
	}
	reading := styles.Accent(m.spinner.View()) + " " + styles.Muted(readingNote("it from aws"))
	switch {
	case r.res.retained:
		say("a remove left it in place: koldstart does not read it until the next deploy takes it over")
		return lazyOf(lines), -1
	case r.res.res.PhysicalID == "":
		say("it has no id in aws yet: the next deploy creates it")
		return lazyOf(lines), -1
	case !r.liveLoaded && r.live == nil:
		return lazyOf(append(lines, reading)), -1
	case errors.Is(r.liveErr, cli.ErrNotLive):
		say("cloud control cannot read a " + shortType(r.res.res.Type) + ": what koldstart applied is on the applied tab")
		return lazyOf(lines), -1
	case r.liveErr != nil && r.live == nil:
		lines = append(lines, cellLines(m.couldNotRead("it from aws", r.liveErr.Error(), styles.ToneDanger), width)...)
		lines = append(lines, "")
		say("reading it takes cloud control, which an emulator may not have, and cloudformation:GetResource with the read permissions of its type. what koldstart applied is on the applied tab")
		return lazyOf(lines), -1
	case !r.liveLoaded:
		lines = append(lines, reading, "")
	case r.liveErr != nil:
		lines = append(lines, cellLines(staleNote("", r.liveAt, r.liveErr.Error()), width)...)
		lines = append(lines, "")
	}
	if r.props.empty() {
		say("aws returned no properties for it")
		return lazyOf(lines), -1
	}
	switch {
	case len(r.drift) > 0:
		lines = append(lines, cellLines(cell{styles.S("▲ ", styles.ToneWarning), styles.S("changed outside koldstart: "+strings.Join(r.drift, ", "), styles.ToneWarning)}, width)...)
		back := "the next deploy sets it back to what the configuration says"
		if len(r.drift) > 1 {
			back = "the next deploy sets them back to what the configuration says"
		}
		say(back + ". ▲ marks what changed below, as applied → now")
	case r.compared:
		lines = append(lines, styles.Render(styles.S("✓ ", styles.ToneSuccess), styles.S("as koldstart applied it", styles.ToneBody)))
	}
	lines = append(lines, "", styles.SectionHeader("properties in aws", width))
	out := lazyOf(lines)
	at := out.len()
	out.join(r.props.lines(width))
	return out, at
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
	lines, _ := m.resourceBody()
	return lines
}

// resourceBody is resourceLines, and the line its json starts at, or -1 when the tab shows none.
func (m model) resourceBody() (lazyLines, int) {
	r := m.state.resource
	width := resourceWidth()
	lines := lazyOf(append(append(m.headLines(width), ""), strings.Split(tabRow(resourceTabs, nil, int(r.tab), width), "\n")...))
	var tab lazyLines
	var at int
	if r.tab == liveTab {
		tab, at = m.liveLinesOf(width)
	} else {
		tab, at = m.appliedLines(width)
	}
	if at >= 0 {
		at += lines.len()
	}
	lines.join(tab)
	return lines, at
}

func (m model) resourceView() string {
	return indent(scrolledLines(m.resourceLines(), m.state.resource.scroll, contentHeight))
}
