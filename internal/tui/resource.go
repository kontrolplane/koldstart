package tui

import (
	"encoding/json"
	"maps"
	"slices"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/kontrolplane/koldstart/internal/tui/styles"
)

// resourceState is one resource of the stage: what koldstart knows of it, and the inputs it was
// last applied with.
type resourceState struct {
	res    stageResource
	scroll int
}

func (m model) openResource(r stageResource) (model, tea.Cmd) {
	m.state.resource = resourceState{res: r}
	return m.switchPage(resourcePage), nil
}

func (m model) resourceUpdate(msg tea.KeyPressMsg) (model, tea.Cmd) {
	r := &m.state.resource
	switch {
	case key.Matches(msg, m.keys.Back, m.keys.Quit):
		return m.switchPage(stagePage), nil
	case key.Matches(msg, m.keys.Copy):
		return m.copy(r.res.res.PhysicalID)
	}
	r.scroll = scrollBy(msg, m.keys, r.scroll, contentHeight)
	return m, nil
}

func (m model) resourceLines() []string {
	r := m.state.resource.res
	res := r.res
	width := contentWidth - 4
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
	if res.Pending != nil {
		lines = append(lines, row("pending", text(res.Pending.Operation+" since "+formatAgo(res.Pending.StartedAt)+": deploy again to finish it", styles.ToneWarning), width))
	}
	lines = append(lines, "", styles.SectionHeader("attributes", width))
	for _, k := range slices.Sorted(maps.Keys(res.Attributes)) {
		lines = append(lines, row(k, value(res.Attributes[k]), width))
	}
	if len(res.Attributes) == 0 {
		lines = append(lines, styles.Faint("none"))
	}
	lines = append(lines, "", styles.SectionHeader("applied with", width))
	var inputs any
	if json.Unmarshal(res.Inputs, &inputs) == nil {
		data, _ := json.MarshalIndent(inputs, "", "  ")
		for line := range strings.SplitSeq(ansi.Hardwrap(styles.CleanBlock(string(data)), width, true), "\n") {
			lines = append(lines, styles.Body(line))
		}
	}
	return lines
}

func (m model) resourceView() string {
	return indent(scrolled(m.resourceLines(), m.state.resource.scroll, contentHeight))
}
