package tui

import (
	"cmp"
	"encoding/json"
	"maps"
	"slices"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

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

// resourcesWord names the resources tab and the column of the overview that counts them, and
// functionWord the column of the functions tab and a function where the filter hides them all.
const (
	resourcesWord = "resources"
	functionWord  = "function"
)

// stageState is one stage: its functions, resources, outputs and the history of its state.
type stageState struct {
	ref    cli.StageRef
	gen    int
	loaded bool
	// err is why the state could not be read, before it ever was.
	err    string
	st     *state.State
	lock   *state.LockInfo
	tab    stageTab
	tables [4]dataTable
	filter filterBox
	// keys name the rows of each table, so the cursor stays on its row when the rows change.
	keys [4][]string

	functions []string
	resources []stageResource
	outputs   []string
	// shownDeploys are the deploys the history tab lists, by their index in deploys(history).
	shownDeploys []int

	historyGen    int
	historyLoaded bool
	// historyErr is why the history could not be listed, before it ever was.
	historyErr string
	history    []state.ObjectVersion

	metricsGen int
	metrics    map[string]cli.Metrics
	// metricsAt is when the metrics on screen were read; metricsErr is why the last read failed.
	metricsAt  time.Time
	metricsErr string
	queuesGen  int
	queues     map[string]cli.QueueDepth
	// queueAt is when each depth was read; queueErrs is why the last read of one failed, whose
	// depth from the read before stays.
	queueAt   map[string]time.Time
	queueErrs map[string]string
}

// lambdaNames are the names in lambda of the stage's functions, sorted.
func (s stageState) lambdaNames() []string {
	if s.st == nil {
		return nil
	}
	var out []string
	for id, r := range s.st.Resources {
		if r.Type == awsp.TypeFunction && strings.HasPrefix(id, "function/") && r.PhysicalID != "" {
			out = append(out, r.PhysicalID)
		}
	}
	slices.Sort(out)
	return out
}

// queueURLs are the urls of the stage's queues, by their ids in the state.
func (s stageState) queueURLs() map[string]string {
	out := map[string]string{}
	if s.st == nil {
		return out
	}
	for id, r := range s.st.Resources {
		if r.Type == "AWS::SQS::Queue" && r.PhysicalID != "" {
			out[id] = r.PhysicalID
		}
	}
	return out
}

// stageResource is a resource of the state, or one a remove left in place, with how deep it
// sits in the tree of the resources tab: 0 for what heads a group, 1 for what it brings. What is
// left of a group whose head is gone, as the log group a remove kept of a function, sits at 0.
type stageResource struct {
	id       string
	res      *state.Resource
	retained bool
	depth    int
	last     bool
}

var (
	functionColumns = []column{
		{title: functionWord, width: 12, grow: 2},
		{title: "runtime", width: 15, drop: 5},
		{title: "memory", width: 9, right: true, drop: 3},
		{title: "timeout", width: 7, right: true, drop: 4},
		{title: "calls 1h", width: 9, right: true},
		{title: "errors", width: 7, right: true, drop: 1},
		{title: "p99", width: 7, right: true, drop: 2},
		{title: "code", width: 12, drop: 6},
		{title: "in lambda", width: 14, grow: 3, drop: 7},
	}
	resourceColumns = []column{
		{title: "resource", width: 22, grow: 2},
		{title: "type", width: 18, grow: 2, drop: 2},
		{title: "id in aws", width: 16, grow: 3, drop: 1},
		{title: "", width: 24},
	}
	outputColumns = []column{
		{title: "output", width: 16, grow: 1},
		{title: "value", width: 20, grow: 4},
	}
	historyColumns = []column{
		{title: "deploy", width: 20},
		{title: "", width: 10},
		{title: "writes", width: 8, right: true},
		{title: "version", width: 20, grow: 1, drop: 1},
	}
)

// stageEmpty is what each tab says when it has no rows, and stageRowNames name one of its rows
// for when the filter hides them all.
var (
	stageEmpty    = [4]string{"no functions yet", "no resources yet", "no outputs yet", "no deploys yet"}
	stageRowNames = [4]string{functionWord, "resource", "output", "deploy"}
)

func newStageState() stageState {
	return stageState{
		tables: [4]dataTable{
			newTable(functionColumns, stageEmpty[functionsTab]),
			newTable(resourceColumns, stageEmpty[resourcesTab]),
			newTable(outputColumns, stageEmpty[outputsTab]),
			newTable(historyColumns, stageEmpty[historyTab]),
		},
		filter: newFilterBox("filter by name…"),
	}
}

// setRows fills a tab's table and keeps the cursor on the row it was on while that row is still
// there, as a refresh, the filter or a new deploy can move it.
func (s *stageState) setRows(tab stageTab, keys []string, rows []tableRow) {
	t := &s.tables[tab]
	var current string
	if old := s.keys[tab]; t.cursor < len(old) {
		current = old[t.cursor]
	}
	s.keys[tab] = keys
	t.empty = stageEmpty[tab]
	switch {
	case tab == historyTab && s.historyErr != "":
		t.empty = "could not list the history: " + s.historyErr + " · r tries again"
	case tab == historyTab && !s.historyLoaded:
		t.empty = readingNote("the history")
	case s.filter.text != "":
		t.empty = noMatch(stageRowNames[tab], s.filter.text)
	}
	t.setRows(rows)
	if i := slices.Index(keys, current); i >= 0 {
		t.setCursor(i)
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
		if !s.loaded {
			s.err = msg.err.Error()
		}
		return m.loadError("could not read the state of "+s.ref.String(), msg.err, s.loaded)
	}
	if msg.state == nil && !m.b.Local(s.ref) && s.loaded {
		return m.stageGone()
	}
	if msg.state == nil {
		msg.state = state.New(s.ref.App, s.ref.Stage, s.ref.Region, m.ident.Account)
	}
	s.st, s.lock, s.loaded, s.err = msg.state, msg.lock, true, ""
	if m.page == stagePage {
		m = m.markRead()
	}
	m = m.updateStageTables()
	if m.page == functionPage {
		m = m.syncFunction()
	}
	m, metrics := m.loadMetrics()
	m, queues := m.loadQueues()
	return m, tea.Batch(metrics, queues)
}

func (m model) applyMetrics(msg metricsLoadedMsg) (model, tea.Cmd) {
	s := &m.state.stage
	if msg.gen != s.metricsGen {
		return m, nil
	}
	if msg.err != nil {
		s.metricsErr = msg.err.Error()
	} else {
		s.metrics, s.metricsAt, s.metricsErr = msg.metrics, time.Now(), ""
	}
	return m.updateStageTables(), nil
}

func (m model) applyQueues(msg queuesLoadedMsg) (model, tea.Cmd) {
	s := &m.state.stage
	if msg.gen != s.queuesGen {
		return m, nil
	}
	if msg.depths == nil {
		msg.depths = map[string]cli.QueueDepth{}
	}
	at := map[string]time.Time{}
	for id := range msg.depths {
		at[id] = time.Now()
	}
	errs := map[string]string{}
	for id, err := range msg.errs {
		errs[id] = err.Error()
		if d, ok := s.queues[id]; ok {
			msg.depths[id], at[id] = d, s.queueAt[id]
		}
	}
	s.queues, s.queueAt, s.queueErrs = msg.depths, at, errs
	return m.updateStageTables(), nil
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
		if !s.historyLoaded {
			s.historyErr = msg.err.Error()
		}
		return m.updateStageTables().loadError("could not list the history of "+s.ref.String(), msg.err, s.historyLoaded)
	}
	s.history, s.historyLoaded, s.historyErr = msg.versions, true, ""
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
		calls, errs, p99 := s.metricCells(r.PhysicalID)
		rows = append(rows, tableRow{
			text(name, styles.ToneText),
			text(orDash(in.Runtime), styles.ToneBody),
			text(formatInt(int(in.Memory))+" mb", styles.ToneBody),
			text(formatInt(int(in.Timeout))+"s", styles.ToneBody),
			calls, errs, p99,
			text(shortSHA(in.Code.SHA256), styles.ToneFaint),
			text(r.PhysicalID, styles.ToneMuted),
		})
	}
	s.setRows(functionsTab, s.functions, rows)

	s.resources = groupResources(s.st, f)
	rows = nil
	keys := make([]string, 0, len(s.resources))
	for _, r := range s.resources {
		rows = append(rows, s.resourceRow(r))
		keys = append(keys, r.id)
	}
	s.setRows(resourcesTab, keys, rows)

	s.outputs, rows = nil, nil
	for _, k := range slices.Sorted(maps.Keys(s.st.Outputs)) {
		if !f.matches(k, s.st.Outputs[k]) {
			continue
		}
		s.outputs = append(s.outputs, k)
		rows = append(rows, tableRow{text(k, styles.ToneText), outputValue(s.st.Outputs[k])})
	}
	s.setRows(outputsTab, s.outputs, rows)

	s.shownDeploys, rows, keys = nil, nil, nil
	for i, d := range deploys(s.history) {
		v, at := d[0], formatTime(d[0].LastModified)
		if !f.matches(at, v.ID) {
			continue
		}
		note := text("", styles.ToneFaint)
		if v.Latest {
			note = text("current", styles.ToneAccent)
		}
		s.shownDeploys = append(s.shownDeploys, i)
		keys = append(keys, d[len(d)-1].ID)
		rows = append(rows, tableRow{text(at, styles.ToneBody), note, text(formatInt(len(d)), styles.ToneMuted), text(v.ID, styles.ToneFaint)})
	}
	s.setRows(historyTab, keys, rows)
	return m
}

// metricCells are a function's invocations, errors and p99 duration of the last hour: an
// ellipsis while they are read, and a dash where they cannot be, as for a function not created
// yet, which has no name in lambda.
func (s stageState) metricCells(name string) (calls, errs, p99 cell) {
	mt, ok := s.metrics[name]
	switch {
	case !ok && (s.metricsErr != "" || name == ""):
		return text("-", styles.ToneFaint), text("-", styles.ToneFaint), text("-", styles.ToneFaint)
	case !ok:
		return text("…", styles.ToneFaint), text("…", styles.ToneFaint), text("…", styles.ToneFaint)
	}
	sum := mt.Summary()
	calls = text(formatInt(int(sum.Invocations)), styles.ToneBody)
	if sum.Invocations == 0 {
		calls = text("0", styles.ToneFaint)
	}
	errs = text(formatInt(int(sum.Errors)), styles.ToneFaint)
	if sum.Errors > 0 {
		errs = text(formatInt(int(sum.Errors)), styles.ToneDanger)
	}
	switch {
	case sum.DurationP99 <= 0:
		p99 = text("-", styles.ToneFaint)
	case sum.DurationP99 < 1:
		p99 = text("<1ms", styles.ToneBody)
	default:
		p99 = text(formatMillis(sum.DurationP99), styles.ToneBody)
	}
	return calls, errs, p99
}

// outputValue draws an output on one line: the first line of a value that has more, with how
// many more. c copies all of it.
func outputValue(v string) cell {
	lines := strings.Split(strings.TrimRight(strings.ReplaceAll(v, "\r\n", "\n"), "\n"), "\n")
	switch {
	case v == "":
		return text("empty", styles.ToneFaint)
	case len(lines) > 1:
		return cell{styles.S(lines[0], styles.ToneBody), styles.S("  +"+plural(len(lines)-1, "line"), styles.ToneFaint)}
	}
	return text(lines[0], styles.ToneBody)
}

// groupKey orders the groups of the resources tab: functions, then resources, then the http api,
// then anything else, each by name.
type groupKey struct {
	order int
	name  string
}

// groupResources puts the resources of the state, and the ones a remove left in place, in a tree:
// each function with what it brings, each resource with its notifications, and the http api with
// its domain and authorizers. A filter keeps a group whose head or any member matches.
func groupResources(st *state.State, f filterBox) []stageResource {
	heads := map[groupKey]string{}
	members := map[groupKey][]stageResource{}
	add := func(list map[string]*state.Resource, retained bool) {
		for id, r := range list {
			kind, name := desired.Group(id, r.Dependencies)
			k := groupKey{order: 3, name: id}
			head := id
			switch kind {
			case desired.GroupFunction:
				k, head = groupKey{0, name}, desired.FunctionID(name)
			case desired.GroupResource:
				k, head = groupKey{1, name}, desired.ResourceID(name)
			case desired.GroupHTTP:
				k, head = groupKey{2, ""}, desired.HTTPAPIID
			}
			heads[k] = head
			members[k] = append(members[k], stageResource{id: id, res: r, retained: retained})
		}
	}
	add(st.Resources, false)
	add(st.Retained, true)
	keys := slices.SortedFunc(maps.Keys(members), func(a, b groupKey) int {
		return cmp.Or(cmp.Compare(a.order, b.order), cmp.Compare(a.name, b.name))
	})
	var out []stageResource
	for _, k := range keys {
		group := members[k]
		slices.SortFunc(group, func(a, b stageResource) int {
			return cmp.Or(cmp.Compare(boolInt(a.id != heads[k]), boolInt(b.id != heads[k])), cmp.Compare(a.id, b.id))
		})
		if !slices.ContainsFunc(group, func(r stageResource) bool { return f.matches(r.id, r.res.Type, r.res.PhysicalID) }) {
			continue
		}
		hasHead := group[0].id == heads[k]
		for i, r := range group {
			if hasHead && i > 0 {
				r.depth = 1
			}
			r.last = i == len(group)-1
			out = append(out, r)
		}
	}
	return out
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// shortType drops koldstart's prefix from its own types: koldstart::IAM::Role is IAM::Role.
func shortType(typ string) string {
	return strings.TrimPrefix(typ, "koldstart::")
}

// resourceRow draws a resource in the tree, with the depth of a queue when it has one. Beside a
// note the depth leaves out what is in flight, to fit the column.
func (s stageState) resourceRow(r stageResource) tableRow {
	name := cell{styles.S(r.id, styles.ToneText)}
	if r.depth > 0 {
		branch := "├─ "
		if r.last {
			branch = "└─ "
		}
		name = cell{styles.S(branch, styles.ToneRule), styles.S(r.id, styles.ToneBody)}
	}
	note := resourceNote(r.res, r.retained)
	_, read := s.queues[r.id]
	if _, failed := s.queueErrs[r.id]; failed && !read {
		if note[0].Text != "" {
			note = append(note, styles.S(" · ", styles.ToneFaint))
		}
		note = append(note, styles.S("could not read the depth", styles.ToneFaint))
	}
	if d, ok := s.queues[r.id]; ok {
		depth := cell{styles.S(formatInt(d.Visible), styles.ToneBody), styles.S(" queued", styles.ToneFaint)}
		switch {
		case note[0].Text != "":
			depth = append(cell{note[0], styles.S(" · ", styles.ToneFaint)}, depth...)
		case d.InFlight > 0:
			depth = append(depth, styles.S(" · "+formatInt(d.InFlight)+" in flight", styles.ToneFaint))
		}
		note = depth
	}
	return tableRow{name, text(shortType(r.res.Type), styles.ToneMuted), text(orDash(r.res.PhysicalID), styles.ToneBody), note}
}

// deployGap is the longest pause between two writes of the state that one deploy makes.
const deployGap = time.Minute

// deploys groups the versions of a state, newest first, into the deploys that wrote them: a
// deploy writes the state before and after every step, so its versions follow each other closely.
func deploys(history []state.ObjectVersion) [][]state.ObjectVersion {
	var out [][]state.ObjectVersion
	var prev time.Time
	for _, v := range history {
		if len(out) == 0 || prev.Sub(v.LastModified) > deployGap {
			out = append(out, nil)
		}
		out[len(out)-1] = append(out[len(out)-1], v)
		prev = v.LastModified
	}
	return out
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
		if !s.loaded {
			m.loading, m.loadingMsg = true, "reading the state of "+s.ref.String()+"…"
		}
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

// openSelected opens what is under the cursor: a function, also from the resources tab, a
// resource or a version.
func (m model) openSelected() (model, tea.Cmd) {
	s := m.state.stage
	i := s.tables[s.tab].cursor
	switch s.tab {
	case functionsTab:
		if i < len(s.functions) {
			return m.openFunction(s.functions[i])
		}
	case resourcesTab:
		if i >= len(s.resources) {
			break
		}
		if name, ok := strings.CutPrefix(s.resources[i].id, "function/"); ok && !s.resources[i].retained {
			return m.openFunction(name)
		}
		return m.openResource(s.resources[i])
	case historyTab:
		if i < len(s.shownDeploys) {
			return m.openVersion(s.shownDeploys[i])
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
	case s.tab == historyTab && i < len(s.shownDeploys):
		return deploys(s.history)[s.shownDeploys[i]][0].ID
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

// tabBar names the tabs with how many rows each holds, the open one in the accent. Each page
// sets it one column in, where its section headings start.
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
	return styles.Render(spans...)
}

// metricsNote says the metrics could not be read, or that those on screen are from an earlier
// read, faint as the table beside it says what it can.
func (m model) metricsNote() cell {
	s := m.state.stage
	if s.metrics == nil {
		return text("could not read the metrics: "+s.metricsErr, styles.ToneFaint)
	}
	return staleNote("metrics", s.metricsAt, s.metricsErr)
}

// stageNoteWidth is the narrowest a note beside the tabs is shown at.
const stageNoteWidth = 16

func (m model) stageView() string {
	s := m.state.stage
	if s.st == nil {
		what := "the state of " + s.ref.String()
		if s.err == "" {
			return place(contentWidth, contentHeight, styles.Muted(truncate(styles.Clean(readingNote(what)), contentWidth-4)))
		}
		return place(contentWidth, contentHeight, lipgloss.JoinVertical(lipgloss.Center,
			cellLines(m.couldNotRead(what, s.err, styles.ToneDanger), min(contentWidth-4, dialogTextWidth()))...))
	}
	counts := []string{formatInt(len(s.functions)), formatInt(len(s.resources)), formatInt(len(s.outputs)), ""}
	if s.historyLoaded {
		counts[historyTab] = formatInt(len(s.shownDeploys))
	}
	bar := indent(tabBar(stageTabs, counts, int(s.tab)))
	if room := contentWidth - ansi.StringWidth(bar) - 4; s.tab == functionsTab && s.metricsErr != "" && room >= stageNoteWidth {
		bar = spread(bar, ansi.Truncate(styles.Render(m.metricsNote()...), room, "…"), contentWidth-1)
	}
	return strings.Join([]string{
		m.stageFacts(),
		"",
		bar,
		"",
		indent(s.tables[s.tab].View()),
	}, "\n")
}
