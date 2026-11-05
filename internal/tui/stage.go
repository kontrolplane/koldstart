package tui

import (
	"encoding/json"
	"maps"
	"slices"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/kontrolplane/koldstart/internal/cli"
	"github.com/kontrolplane/koldstart/internal/desired"
	"github.com/kontrolplane/koldstart/internal/provider/awsp"
	"github.com/kontrolplane/koldstart/internal/state"
	"github.com/kontrolplane/koldstart/internal/tui/styles"
)

type stageTab int

const (
	functionsTab stageTab = iota
	resourcesTab
	outputsTab
	historyTab
)

var stageTabs = []string{"functions", resourcesWord, "outputs", "history"}

// resourcesWord names the resources tab and the column of the overview that counts them.
const resourcesWord = "resources"

// stageState is one stage: its functions, resources, outputs and the history of its state.
type stageState struct {
	ref    cli.StageRef
	gen    int
	loaded bool
	st     *state.State
	lock   *state.LockInfo
	tab    stageTab
	tables [4]dataTable
	filter filterBox

	functions []string
	resources []stageResource
	outputs   []string

	historyGen    int
	historyLoaded bool
	history       []state.ObjectVersion
}

// stageResource is a resource of the state, or one a remove left in place.
type stageResource struct {
	id       string
	res      *state.Resource
	retained bool
}

var (
	functionColumns = []column{
		{title: "function", width: 12, grow: 2},
		{title: "runtime", width: 15, drop: 3},
		{title: "memory", width: 7, right: true, drop: 2},
		{title: "timeout", width: 7, right: true, drop: 2},
		{title: "arch", width: 7, drop: 4},
		{title: "code", width: 12, drop: 1},
		{title: "in lambda", width: 14, grow: 3, drop: 5},
	}
	resourceColumns = []column{
		{title: "resource", width: 16, grow: 2},
		{title: "type", width: 18, grow: 2, drop: 2},
		{title: "id in aws", width: 16, grow: 3, drop: 1},
		{title: "", width: 10},
	}
	outputColumns = []column{
		{title: "output", width: 16, grow: 1},
		{title: "value", width: 20, grow: 4},
	}
	historyColumns = []column{
		{title: "saved", width: 20},
		{title: "", width: 10},
		{title: "version", width: 20, grow: 1, drop: 1},
	}
)

func newStageState() stageState {
	return stageState{
		tables: [4]dataTable{
			newTable(functionColumns, "no functions"),
			newTable(resourceColumns, "no resources"),
			newTable(outputColumns, "no outputs"),
			newTable(historyColumns, "no earlier versions"),
		},
		filter: newFilterBox("filter by name…"),
	}
}

// stageHeaderHeight is the facts above the tabs, the tab bar, and the space under each.
const stageHeaderHeight = 5

func stageTableHeight() int {
	return contentHeight - stageHeaderHeight - tableHeaderRows
}

// functionInputs decodes what a function was last deployed with.
func functionInputs(r *state.Resource) awsp.FunctionInputs {
	var in awsp.FunctionInputs
	if r != nil {
		_ = json.Unmarshal(r.Inputs, &in)
	}
	return in
}

// openStage shows a stage, reading it again when it is another than the one shown last.
func (m model) openStage(ref cli.StageRef) (model, tea.Cmd) {
	s := &m.state.stage
	if s.ref != ref {
		fresh := newStageState()
		for i := range fresh.tables {
			fresh.tables[i].setSize(tableWidth(), stageTableHeight())
		}
		fresh.ref = ref
		*s = fresh
	}
	m = m.switchPage(stagePage)
	m.loading, m.loadingMsg = !s.loaded, "reading the state of "+ref.String()+"…"
	m, cmd := m.loadStage()
	if s.tab == historyTab {
		var history tea.Cmd
		m, history = m.loadHistory()
		cmd = tea.Batch(cmd, history)
	}
	return m, cmd
}

func (m model) applyStage(msg stageLoadedMsg) (model, tea.Cmd) {
	s := &m.state.stage
	if msg.gen != s.gen {
		return m, nil
	}
	if m.page == stagePage {
		m.loading = false
	}
	if msg.err != nil {
		return m.loadError("could not read the state of "+s.ref.String(), msg.err, s.loaded)
	}
	if msg.state == nil && !m.b.Local(s.ref) && s.loaded {
		return m.stageGone()
	}
	if msg.state == nil {
		msg.state = state.New(s.ref.App, s.ref.Stage, s.ref.Region, m.ident.Account)
	}
	s.st, s.lock, s.loaded = msg.state, msg.lock, true
	if m.page == stagePage {
		m.refreshedAt = time.Now()
	}
	m = m.updateStageTables()
	if m.page == functionPage {
		m = m.syncFunction()
	}
	return m, nil
}

// stageGone leaves a stage whose state was removed elsewhere.
func (m model) stageGone() (model, tea.Cmd) {
	ref := m.state.stage.ref
	m.state.stage = newStageState()
	m = m.switchPage(overviewPage)
	m, cmd := m.loadStages()
	m, status := m.setStatus(ref.String()+" is no longer deployed", styles.ToneWarning)
	return m, tea.Batch(cmd, status)
}

func (m model) applyHistory(msg historyLoadedMsg) (model, tea.Cmd) {
	s := &m.state.stage
	if msg.gen != s.historyGen {
		return m, nil
	}
	if msg.err != nil {
		return m.loadError("could not list the history of "+s.ref.String(), msg.err, s.historyLoaded)
	}
	s.history, s.historyLoaded = msg.versions, true
	return m.updateStageTables(), nil
}

func (m model) updateStageTables() model {
	s := &m.state.stage
	if s.st == nil {
		return m
	}
	f := s.filter

	s.functions = nil
	var rows []tableRow
	for _, id := range slices.Sorted(maps.Keys(s.st.Resources)) {
		r := s.st.Resources[id]
		name, ok := strings.CutPrefix(id, "function/")
		if r.Type != awsp.TypeFunction || !ok || !f.matches(name, r.PhysicalID) {
			continue
		}
		in := functionInputs(r)
		s.functions = append(s.functions, name)
		rows = append(rows, tableRow{
			text(name, styles.ToneText),
			text(in.Runtime, styles.ToneBody),
			text(formatInt(int(in.Memory)), styles.ToneBody),
			text(formatInt(int(in.Timeout))+"s", styles.ToneBody),
			text(in.Architecture, styles.ToneMuted),
			text(shortSHA(in.Code.SHA256), styles.ToneFaint),
			text(r.PhysicalID, styles.ToneMuted),
		})
	}
	s.tables[functionsTab].setRows(rows)

	s.resources, rows = nil, nil
	add := func(list map[string]*state.Resource, retained bool) {
		for _, id := range slices.Sorted(maps.Keys(list)) {
			r := list[id]
			if !f.matches(id, r.Type, r.PhysicalID) {
				continue
			}
			s.resources = append(s.resources, stageResource{id: id, res: r, retained: retained})
			rows = append(rows, tableRow{text(id, styles.ToneText), text(r.Type, styles.ToneMuted), text(orDash(r.PhysicalID), styles.ToneBody), resourceNote(r, retained)})
		}
	}
	add(s.st.Resources, false)
	add(s.st.Retained, true)
	s.tables[resourcesTab].setRows(rows)

	s.outputs, rows = nil, nil
	for _, k := range slices.Sorted(maps.Keys(s.st.Outputs)) {
		if !f.matches(k, s.st.Outputs[k]) {
			continue
		}
		s.outputs = append(s.outputs, k)
		rows = append(rows, tableRow{text(k, styles.ToneText), text(s.st.Outputs[k], styles.ToneBody)})
	}
	s.tables[outputsTab].setRows(rows)

	rows = nil
	for _, v := range s.history {
		note := text("", styles.ToneFaint)
		if v.Latest {
			note = text("current", styles.ToneAccent)
		}
		rows = append(rows, tableRow{text(formatTime(v.LastModified), styles.ToneBody), note, text(v.ID, styles.ToneFaint)})
	}
	s.tables[historyTab].setRows(rows)
	return m
}

// resourceNote says what is special about a resource: left in place, taken over, or caught
// halfway by a deploy that did not finish.
func resourceNote(r *state.Resource, retained bool) cell {
	switch {
	case r.Pending != nil:
		return text(r.Pending.Operation+" pending", styles.ToneWarning)
	case retained:
		return text("kept", styles.ToneMuted)
	case r.Imported:
		return text("imported", styles.ToneMuted)
	case r.DeletionPolicy == "retain":
		return text("retained", styles.ToneFaint)
	}
	return text("", styles.ToneFaint)
}

func shortSHA(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

// switchTab moves to another tab of the stage, reading the history the first time it is shown.
func (m model) switchTab(by int) (model, tea.Cmd) {
	s := &m.state.stage
	s.tab = stageTab((int(s.tab) + by + len(stageTabs)) % len(stageTabs))
	if s.tab == historyTab && !s.historyLoaded {
		return m.loadHistory()
	}
	return m, nil
}

func (m model) stageUpdate(msg tea.KeyPressMsg) (model, tea.Cmd) {
	s := &m.state.stage
	if s.filter.active {
		changed, cmd := s.filter.update(msg, m.keys)
		if changed {
			m = m.updateStageTables()
		}
		return m, cmd
	}
	t := &s.tables[s.tab]
	if t.move(msg, m.keys) {
		return m, nil
	}
	switch {
	case key.Matches(msg, m.keys.Back):
		if s.filter.clear() {
			return m.updateStageTables(), nil
		}
		return m.stageGoBack()
	case key.Matches(msg, m.keys.Quit):
		return m.stageGoBack()
	case key.Matches(msg, m.keys.NextTab, m.keys.Right):
		return m.switchTab(1)
	case key.Matches(msg, m.keys.PrevTab, m.keys.Left):
		return m.switchTab(-1)
	case key.Matches(msg, m.keys.Filter):
		return m, s.filter.open()
	case key.Matches(msg, m.keys.Refresh):
		m, cmd := m.loadStage()
		if s.tab == historyTab {
			var history tea.Cmd
			m, history = m.loadHistory()
			cmd = tea.Batch(cmd, history)
		}
		return m, cmd
	case key.Matches(msg, m.keys.Deploy):
		return m.planDeploy(s.ref, "")
	case key.Matches(msg, m.keys.Remove):
		return m.planRemove(s.ref)
	case key.Matches(msg, m.keys.Unlock):
		return m.askUnlock(s.ref, s.lock)
	case key.Matches(msg, m.keys.View):
		return m.openSelected()
	case key.Matches(msg, m.keys.Copy):
		if v := m.stageCopyValue(); v != "" {
			return m.copy(v)
		}
	}
	return m, nil
}

// openSelected opens what is under the cursor: a function, a resource or a version.
func (m model) openSelected() (model, tea.Cmd) {
	s := m.state.stage
	i := s.tables[s.tab].cursor
	switch s.tab {
	case functionsTab:
		if i < len(s.functions) {
			return m.openFunction(s.functions[i])
		}
	case resourcesTab:
		if i < len(s.resources) {
			return m.openResource(s.resources[i])
		}
	case historyTab:
		if i < len(s.history) {
			return m.openVersion(i)
		}
	}
	return m, nil
}

// stageCopyValue is what c copies on the stage: the function's arn, the resource's id in aws, the
// output's value, or the version's id.
func (m model) stageCopyValue() string {
	s := m.state.stage
	i := s.tables[s.tab].cursor
	switch {
	case s.tab == functionsTab && i < len(s.functions):
		r := s.st.Resources[desired.FunctionID(s.functions[i])]
		return cmpOr(r.Attributes["Arn"], r.PhysicalID)
	case s.tab == resourcesTab && i < len(s.resources):
		return s.resources[i].res.PhysicalID
	case s.tab == outputsTab && i < len(s.outputs):
		return s.st.Outputs[s.outputs[i]]
	case s.tab == historyTab && i < len(s.history):
		return s.history[i].ID
	}
	return ""
}

func cmpOr(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func (m model) stageGoBack() (model, tea.Cmd) {
	m = m.switchPage(overviewPage)
	return m.loadStages()
}

// stageFacts are the lines above the tabs: when and by whom the stage was deployed, and its lock.
func (m model) stageFacts() string {
	s := m.state.stage
	if s.st == nil {
		return ""
	}
	var deployed []styles.Span
	if s.st.DeployedAt.IsZero() {
		deployed = []styles.Span{styles.S("not deployed yet", styles.ToneMuted)}
		if m.b.Local(s.ref) {
			deployed = append(deployed, styles.S(" · d plans its first deploy", styles.ToneFaint))
		}
	} else {
		deployed = []styles.Span{
			styles.S("deployed ", styles.ToneFaint), styles.S(formatAgo(s.st.DeployedAt), styles.ToneBody),
			styles.S(" by ", styles.ToneFaint), styles.S(orDash(s.st.DeployedBy), styles.ToneBody),
			styles.S(" · serial ", styles.ToneFaint), styles.S(formatInt(int(s.st.Serial)), styles.ToneBody),
			styles.S(" · ", styles.ToneFaint), styles.S(s.ref.Region, styles.ToneBody),
		}
	}
	lock := []styles.Span{styles.S("not locked", styles.ToneFaint)}
	if s.lock != nil {
		lock = []styles.Span{
			styles.S("● locked ", styles.ToneWarning), styles.S("by "+s.lock.Who+", "+s.lock.Operation+" from "+s.lock.Where, styles.ToneWarning),
			styles.S(" since "+formatAgo(s.lock.Since), styles.ToneFaint),
		}
	}
	return clip(" "+styles.Render(deployed...)+"\n "+styles.Render(lock...), contentWidth-2, 2)
}

// tabBar names the tabs with how many rows each holds, the open one in the accent.
func tabBar(names []string, counts []string, open int) string {
	var spans []styles.Span
	for i, name := range names {
		if i > 0 {
			spans = append(spans, styles.S("   ", styles.ToneFaint))
		}
		tone := styles.ToneMuted
		if i == open {
			spans = append(spans, styles.B(name, styles.ToneAccent))
		} else {
			spans = append(spans, styles.S(name, tone))
		}
		if i < len(counts) && counts[i] != "" {
			spans = append(spans, styles.S(" "+counts[i], styles.ToneFaint))
		}
	}
	return " " + styles.Render(spans...)
}

func (m model) stageView() string {
	s := m.state.stage
	counts := []string{formatInt(len(s.functions)), formatInt(len(s.resources)), formatInt(len(s.outputs)), ""}
	if s.historyLoaded {
		counts[historyTab] = formatInt(len(s.history))
	}
	return strings.Join([]string{
		m.stageFacts(),
		"",
		tabBar(stageTabs, counts, int(s.tab)),
		"",
		indent(s.tables[s.tab].View()),
	}, "\n")
}
