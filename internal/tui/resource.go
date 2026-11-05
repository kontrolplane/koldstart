package tui

import (
	"encoding/json"
	"errors"
	"maps"
	"slices"
	"strings"

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

	liveGen    int
	liveLoaded bool
	live       map[string]any
	liveErr    error
}

// openResource shows a resource, and reads it as aws has it now.
func (m model) openResource(r stageResource) (model, tea.Cmd) {
	m.state.resource = resourceState{res: r}
	m = m.switchPage(resourcePage)
	if r.retained || r.res.PhysicalID == "" {
		return m, nil
	}
	return m.loadLive()
}

func (m model) applyLive(msg liveLoadedMsg) (model, tea.Cmd) {
	r := &m.state.resource
	if msg.gen != r.liveGen {
		return m, nil
	}
	r.live, r.liveErr, r.liveLoaded = msg.props, msg.err, true
	return m, nil
}

func (m model) resourceUpdate(msg tea.KeyPressMsg) (model, tea.Cmd) {
	r := &m.state.resource
	switch {
	case key.Matches(msg, m.keys.Back, m.keys.Quit):
		return m.switchPage(stagePage), nil
	case key.Matches(msg, m.keys.NextTab, m.keys.PrevTab):
		r.tab, r.scroll = 1-r.tab, 0
		return m, nil
	case key.Matches(msg, m.keys.Copy):
		return m.copy(r.res.res.PhysicalID)
	case key.Matches(msg, m.keys.Refresh):
		return m.loadLive()
	}
	r.scroll = scrollBy(msg, m.keys, r.scroll, contentHeight)
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
	if d, ok := m.state.stage.queues[r.id]; ok {
		lines = append(lines, row("messages", cell{
			styles.S(formatInt(d.Visible), styles.ToneText), styles.S(" queued", styles.ToneFaint), sep(),
			styles.S(formatInt(d.InFlight), styles.ToneBody), styles.S(" in flight", styles.ToneFaint), sep(),
			styles.S(formatInt(d.Delayed), styles.ToneBody), styles.S(" delayed", styles.ToneFaint),
		}, width))
	}
	if res.Pending != nil {
		lines = append(lines, row("pending", text(res.Pending.Operation+" since "+formatAgo(res.Pending.StartedAt)+": deploy again to finish it", styles.ToneWarning), width))
	}
	return lines
}

func (m model) appliedLines(width int) []string {
	res := m.state.resource.res.res
	lines := []string{"", styles.SectionHeader("attributes", width)}
	for _, k := range slices.Sorted(maps.Keys(res.Attributes)) {
		lines = append(lines, row(k, text(orDash(res.Attributes[k]), styles.ToneBody), width))
	}
	if len(res.Attributes) == 0 {
		lines = append(lines, styles.Faint("none"))
	}
	lines = append(lines, "", styles.SectionHeader("applied with", width))
	var inputs any
	if json.Unmarshal(res.Inputs, &inputs) == nil {
		for _, line := range jsonLines(inputs) {
			lines = append(lines, styles.Body(ansi.Truncate(line, width, "…")))
		}
	}
	return lines
}

func (m model) liveLinesOf(width int) []string {
	r := m.state.resource
	lines := []string{""}
	switch {
	case r.res.retained:
		return append(lines, styles.Faint("a remove left it in place: koldstart does not read it until the next deploy takes it over"))
	case !r.liveLoaded:
		return append(lines, styles.Accent(m.spinner.View())+" "+styles.Muted("reading it from aws…"))
	case errors.Is(r.liveErr, cli.ErrNotLive):
		return append(lines, styles.Faint("cloud control cannot read a "+shortType(r.res.res.Type)+": what koldstart applied is on the applied tab"))
	case r.liveErr != nil:
		return append(lines, renderCell(text(r.liveErr.Error(), styles.ToneDanger), column{width: width}, nil))
	}
	applied := appliedProperties(r.res)
	drift := cli.Drift(applied, r.live)
	switch {
	case len(drift) > 0:
		lines = append(lines, renderCell(cell{styles.S("▲ ", styles.ToneWarning), styles.S("changed outside koldstart: "+strings.Join(drift, ", "), styles.ToneWarning)}, column{width: width}, nil),
			styles.Faint("the next deploy sets them back to what the configuration says"))
	case len(applied) > 0:
		lines = append(lines, styles.Render(styles.S("✓ ", styles.ToneSuccess), styles.S("what koldstart applied, as far as aws says", styles.ToneBody)))
	}
	lines = append(lines, "", styles.SectionHeader("properties in aws", width))
	for _, line := range jsonLines(r.live) {
		tone := styles.ToneBody
		for _, k := range drift {
			if strings.HasPrefix(strings.TrimSpace(line), `"`+k+`":`) {
				tone = styles.ToneWarning
			}
		}
		lines = append(lines, styles.Fg(tone).Render(ansi.Truncate(line, width, "…")))
	}
	return lines
}

func (m model) resourceView() string {
	r := m.state.resource
	width := contentWidth - 4
	lines := m.headLines(width)
	lines = append(lines, "", tabBar(resourceTabs, nil, int(r.tab)))
	if r.tab == liveTab {
		lines = append(lines, m.liveLinesOf(width)...)
	} else {
		lines = append(lines, m.appliedLines(width)...)
	}
	return indent(scrolled(lines, r.scroll, contentHeight))
}
