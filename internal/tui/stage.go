package tui

import (
	"bytes"
	"cmp"
	"encoding/json"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

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

// resourcesWord names the resources tab and the column of the overview that counts them,
// functionWord the column of the functions tab and a function where the filter hides them all, and
// deployedWord the fact of the stage's head and the column of its history.
const (
	resourcesWord = "resources"
	functionWord  = "function"
	deployedWord  = "deployed"
)

// stageState is one stage: its functions, resources, outputs and the history of its state.
type stageState struct {
	ref cli.StageRef
	gen int
	// answered, and the others of each load, are the generation of its last load that came back.
	answered int
	loaded   bool
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
	// resources are the tree of the resources tab, every group open, and tree the rows it draws:
	// the heading of each kind of group, and the resources of the groups not folded.
	resources []stageResource
	tree      []treeRow
	// folded are the groups folded on the resources tab, by groupKey.fold. It, drifted and
	// summaries are copied before they change, as a copy of the model may hold them.
	folded map[string]bool
	// drifted are the resources a read on their own page found changed outside koldstart, until
	// the state is written again.
	drifted map[string]bool
	outputs []string
	// shownDeploys are the deploys the history tab lists, by their index in deploys(history).
	shownDeploys []int

	historyGen      int
	historyAnswered int
	historyLoaded   bool
	// historyErr is why the history could not be listed, before it ever was.
	historyErr string
	history    []state.ObjectVersion
	// summaries are what each deploy of the history changed, by the key of their deployPair, read as their rows
	// come in sight.
	summaries         map[string]deploySummary
	summariesGen      int
	summariesAnswered int

	metricsGen      int
	metricsAnswered int
	metrics         map[string]cli.Metrics
	// metricsAt is when the metrics on screen were read; metricsErr is why the last read failed.
	metricsAt      time.Time
	metricsErr     string
	queuesGen      int
	queuesAnswered int
	queues         map[string]cli.QueueDepth
	// queueAt is when each depth was read; queueErrs is why the last read of one failed, whose
	// depth from the read before stays.
	queueAt   map[string]time.Time
	queueErrs map[string]string
	// peekLive is what lambda said of each function the peek of the functions tab showed, by its
	// name in lambda, and peekGen the generation of the last read of one. It is copied before it
	// changes, as folded is.
	peekLive map[string]peekLive
	peekGen  int
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
// group is the group it is in, and head says it heads it.
type stageResource struct {
	id       string
	res      *state.Resource
	retained bool
	depth    int
	last     bool
	group    groupKey
	head     bool
}

// treeRow is a row of the resources tab: the heading of a kind of group, res -1, or a resource,
// by its index in resources.
type treeRow struct {
	kind int
	res  int
}

var (
	functionColumns = []column{
		{title: functionWord, width: 12, grow: 2},
		{title: "last hour", width: sparkWidth, drop: 2},
		{title: "calls", width: 7, right: true},
		{title: "errors", width: 6, right: true},
		{title: "p99", width: 6, right: true, drop: 3},
		{title: "memory", width: 8, right: true, drop: 4},
		{title: "timeout", width: 7, right: true, drop: 5},
		{title: "runtime", width: 15, drop: 6},
		{title: "code", width: 12, drop: 8},
		{title: "in lambda", width: 14, grow: 3, drop: 7, middle: true},
	}
	resourceColumns = []column{
		{title: "resource", width: 22, grow: 2},
		{title: "type", width: 16, grow: 1, drop: 1},
		{title: "id in aws", width: 16, grow: 3, drop: 2, middle: true},
		{title: "", width: 24},
	}
	outputColumns = []column{
		{title: "output", width: 16, grow: 1},
		{title: "value", width: 20, grow: 4, middle: true},
		{title: "", width: 12, drop: 1},
	}
	historyColumns = []column{
		{title: deployedWord, width: 8},
		{title: "", width: 15, drop: 3},
		{title: "by", width: 8, grow: 1, drop: 2},
		{title: "changes", width: 10},
		{title: "", width: 18, drop: 1},
		{title: "took", width: 6, right: true, drop: 4},
		{title: "writes", width: 6, right: true, drop: 5},
		{title: "version", width: 12, grow: 4, drop: 6},
	}
)

// sparkWidth is how wide the sparkline of a function's last hour is in its tab.
const sparkWidth = 10

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
		filter:    newFilterBox("filter by name…"),
		folded:    map[string]bool{},
		drifted:   map[string]bool{},
		summaries: map[string]deploySummary{},
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

// stageHeaderHeight is the tab bar and the rule under it; the facts of the stage are in the
// frame's top border.
const stageHeaderHeight = 2

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
	m.loading, m.loadingMsg = !s.loaded, readingNote("the state of "+ref.String())
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
	s.answered = msg.gen
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
	written := s.st != nil && s.st.Serial != msg.state.Serial
	if written {
		s.drifted = map[string]bool{}
	}
	s.st, s.lock, s.loaded, s.err = msg.state, msg.lock, true, ""
	if m.page == stagePage {
		m = m.markRead()
	}
	m = m.updateStageTables()
	switch m.page {
	case functionPage:
		m = m.syncFunction()
	case outputPage:
		m = m.syncOutput()
	}
	m, metrics := m.loadMetrics()
	m, queues := m.loadQueues()
	m, peek := m.loadPeekLive()
	var history tea.Cmd
	if written && s.historyLoaded {
		m, history = m.loadHistory()
	}
	return m, tea.Batch(metrics, queues, peek, history)
}

func (m model) applyMetrics(msg metricsLoadedMsg) (model, tea.Cmd) {
	s := &m.state.stage
	if msg.gen != s.metricsGen {
		return m, nil
	}
	s.metricsAnswered = msg.gen
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
	s.queuesAnswered = msg.gen
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
	s.historyAnswered = msg.gen
	if msg.err != nil {
		if !s.historyLoaded {
			s.historyErr = msg.err.Error()
		}
		return m.updateStageTables().loadError("could not list the history of "+s.ref.String(), msg.err, s.historyLoaded)
	}
	s.history, s.historyLoaded, s.historyErr = msg.versions, true, ""
	return m.updateStageTables().loadSummaries()
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
		nameCell := text(name, styles.ToneText)
		if s.drifted[id] {
			nameCell = append(nameCell, styles.S(" ▲", styles.ToneWarning))
		}
		rows = append(rows, tableRow{
			nameCell,
			s.activityCell(r.PhysicalID),
			calls, errs, p99,
			text(orDash(megabytes(in.Memory)), styles.ToneBody),
			text(orDash(seconds(in.Timeout)), styles.ToneBody),
			text(orDash(in.Runtime), styles.ToneMuted),
			text(shortSHA(in.Code.SHA256), styles.ToneFaint),
			text(r.PhysicalID, styles.ToneMuted),
		})
	}
	s.setRows(functionsTab, s.functions, rows)

	s.resources = groupResources(s.st, f)
	keys, rows := s.resourceRows()
	s.setRows(resourcesTab, keys, rows)

	s.outputs, rows = nil, nil
	for _, k := range slices.Sorted(maps.Keys(s.st.Outputs)) {
		if !f.matches(k, s.st.Outputs[k]) {
			continue
		}
		s.outputs = append(s.outputs, k)
		value, hint := outputValue(s.st.Outputs[k])
		rows = append(rows, tableRow{text(k, styles.ToneText), value, hint})
	}
	s.setRows(outputsTab, s.outputs, rows)

	keys, rows = s.historyRows()
	s.setRows(historyTab, keys, rows)
	return m
}

// metricCells are a function's invocations, errors and p99 duration of the last hour: an
// ellipsis while they are read, and a dash where they cannot be, as for a function not created
// yet, which has no name in lambda. Errors are in danger and bold, so a failing function is seen
// in a long list.
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
		errs = cell{styles.B(formatInt(int(sum.Errors)), styles.ToneDanger)}
	}
	p99 = text(formatMillis(sum.DurationP99), styles.ToneBody)
	if sum.DurationP99 <= 0 {
		p99 = text("-", styles.ToneFaint)
	}
	return calls, errs, p99
}

// activityCell draws a function's invocations of the last hour as a sparkline in the accent, the
// minutes that had errors in danger, its quiet minutes dotted as on the function's overview, so
// the line always spans the hour, ends at now, and shows when it failed rather than that it did;
// nothing while they are read or when there were none.
func (s stageState) activityCell(name string) cell {
	mt, ok := s.metrics[name]
	if !ok || peak(mt.Invocations) == 0 {
		return cell{}
	}
	return markedSpark(mt.Invocations, mt.Errors, sparkWidth, styles.ToneAccent, styles.ToneDanger)
}

// outputValue draws an output on one line, with a hint of what more it holds: json, a list or
// an object, compacted, with how many items or keys it has; and the first line of text that has
// more, with how many more. Enter shows all of it and c copies it.
func outputValue(v string) (value, hint cell) {
	if v == "" {
		return text("empty", styles.ToneFaint), nil
	}
	if compact, h, ok := outputJSON(v); ok {
		return text(compact, styles.ToneBody), text(h, styles.ToneFaint)
	}
	lines := strings.Split(strings.TrimRight(strings.ReplaceAll(v, "\r\n", "\n"), "\n"), "\n")
	if len(lines) > 1 {
		return text(lines[0], styles.ToneBody), text("+"+plural(len(lines)-1, "line"), styles.ToneFaint)
	}
	return text(lines[0], styles.ToneBody), nil
}

// outputJSON is an output that is a json list or object on one line, and what it holds: {…} 4
// keys, or […] 3 items. Anything else, as a string or a number, is not json here.
func outputJSON(v string) (compact, hint string, ok bool) {
	trimmed := strings.TrimSpace(v)
	if trimmed == "" || (trimmed[0] != '{' && trimmed[0] != '[') {
		return "", "", false
	}
	var parsed any
	if json.Unmarshal([]byte(trimmed), &parsed) != nil {
		return "", "", false
	}
	var b bytes.Buffer
	if json.Compact(&b, []byte(trimmed)) != nil {
		return "", "", false
	}
	switch x := parsed.(type) {
	case map[string]any:
		return b.String(), "{…} " + plural(len(x), "key"), true
	case []any:
		return b.String(), "[…] " + plural(len(x), "item"), true
	}
	return "", "", false
}

// groupKey orders the groups of the resources tab: functions, then resources, then the http api,
// then the rest api, then anything else, each by name.
type groupKey struct {
	order int
	name  string
}

// fold is the key the group is folded by.
func (k groupKey) fold() string {
	return strconv.Itoa(k.order) + "/" + k.name
}

// The kinds of group, by groupKey.order, and their headings.
const (
	functionGroups = iota
	resourceGroups
	httpGroup
	restGroup
	otherGroups
)

var kindNames = [5]string{"functions", "resources", "http api", "rest api", "other"}

// groupResources puts the resources of the state, and the ones a remove left in place, in a tree:
// each function with what it brings, each resource with its notifications, and the http api with
// its domain and authorizers, and the rest api with its resources, authorizers and stage. A
// filter keeps a group whose head or any member matches.
func groupResources(st *state.State, f filterBox) []stageResource {
	heads := map[groupKey]string{}
	members := map[groupKey][]stageResource{}
	add := func(list map[string]*state.Resource, retained bool) {
		for id, r := range list {
			kind, name := desired.Group(id, r.Dependencies)
			k := groupKey{order: otherGroups, name: id}
			head := id
			switch kind {
			case desired.GroupFunction:
				k, head = groupKey{functionGroups, name}, desired.FunctionID(name)
			case desired.GroupResource:
				k, head = groupKey{resourceGroups, name}, desired.ResourceID(name)
			case desired.GroupHTTP:
				k, head = groupKey{httpGroup, ""}, desired.HTTPAPIID
			case desired.GroupREST:
				k, head = groupKey{restGroup, ""}, desired.RESTAPIID
			}
			heads[k] = head
			members[k] = append(members[k], stageResource{id: id, res: r, retained: retained, group: k})
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
			return cmp.Or(cmp.Compare(boolInt(a.id != heads[k]), boolInt(b.id != heads[k])), cmp.Compare(a.id, b.id), cmp.Compare(boolInt(a.retained), boolInt(b.retained)))
		})
		if !slices.ContainsFunc(group, func(r stageResource) bool { return f.matches(r.id, r.res.Type, r.res.PhysicalID) }) {
			continue
		}
		hasHead := group[0].id == heads[k]
		for i, r := range group {
			if hasHead && i > 0 {
				r.depth = 1
			}
			r.head = hasHead && i == 0
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

// typeServices are services as a person names them, where that is not their name in the type
// in lowercase.
var typeServices = map[string]string{
	"ApiGateway": "api gateway", "ApiGatewayV2": "api gateway", "ElasticLoadBalancingV2": "load balancer",
	"Events": "eventbridge", "SecretsManager": "secrets manager", "StepFunctions": "step functions",
	"CertificateManager": "acm", "ElastiCache": "elasticache", "CloudWatch": "cloudwatch",
}

// typeName is a type as a person says it, for the tree, where the resource page shows it whole:
// AWS::Lambda::Function is lambda function, koldstart::IAM::Role iam role. A name of more than one
// word that starts with its service leaves the service out, so koldstart::Logs::LogGroup is log
// group.
func typeName(typ string) string {
	parts := strings.Split(strings.TrimPrefix(strings.TrimPrefix(typ, "koldstart::"), "AWS::"), "::")
	if len(parts) < 2 {
		return strings.ToLower(typ)
	}
	service, ok := typeServices[parts[0]]
	if !ok {
		service = strings.ToLower(parts[0])
	}
	words := camelWords(strings.Join(parts[1:], ""))
	if len(words) == 0 {
		return service
	}
	name := strings.Join(words, " ")
	if len(words) > 1 && strings.HasPrefix(strings.ReplaceAll(service, " ", ""), words[0]) {
		return name
	}
	return service + " " + name
}

// camelWords splits a name in camel case into its words, in lowercase, an acronym one word:
// EventSourceMapping is event source mapping, and DBCluster db cluster.
func camelWords(s string) []string {
	runes := []rune(s)
	var words []string
	start := 0
	for i := 1; i < len(runes); i++ {
		upper, prevUpper := unicode.IsUpper(runes[i]), unicode.IsUpper(runes[i-1])
		nextLower := i+1 < len(runes) && unicode.IsLower(runes[i+1])
		if upper && (!prevUpper || nextLower) {
			words = append(words, strings.ToLower(string(runes[start:i])))
			start = i
		}
	}
	if start < len(runes) {
		words = append(words, strings.ToLower(string(runes[start:])))
	}
	return words
}

// resourceRows are the rows of the resources tab, and the key of each: a heading for each kind of
// group with how many groups it holds, and every resource whose group is not folded. While the
// filter holds text, every group is open, so what it matches shows.
func (s *stageState) resourceRows() ([]string, []tableRow) {
	s.tree = nil
	var keys []string
	var rows []tableRow
	open := s.filter.text != ""
	groups := [len(kindNames)]int{}
	for _, r := range s.resources {
		if r.depth == 0 {
			groups[r.group.order]++
		}
	}
	hidden := map[groupKey]int{}
	attention := map[groupKey]bool{}
	for _, r := range s.resources {
		if r.depth > 0 && !open && s.folded[r.group.fold()] {
			hidden[r.group]++
			attention[r.group] = attention[r.group] || r.res.Pending != nil || s.drifted[r.id]
		}
	}
	for i, r := range s.resources {
		kind := r.group.order
		if i == 0 || s.resources[i-1].group.order != kind {
			s.tree = append(s.tree, treeRow{kind: kind, res: -1})
			keys = append(keys, "kind/"+kindNames[kind])
			heading := cell{styles.B(kindNames[kind], styles.ToneText)}
			if kind != httpGroup && kind != restGroup {
				heading = append(heading, styles.S("  "+formatInt(groups[kind]), styles.ToneFaint))
			}
			rows = append(rows, tableRow{heading})
		}
		if r.depth > 0 && hidden[r.group] > 0 {
			continue
		}
		s.tree = append(s.tree, treeRow{kind: kind, res: i})
		keys = append(keys, r.id)
		rows = append(rows, s.resourceRow(r, hidden[r.group], attention[r.group]))
	}
	return keys, rows
}

// markDrift notes whether a read of a resource on its own page found it changed outside
// koldstart, so the tree marks it until the state is written again.
func (s *stageState) markDrift(id string, drifted bool) {
	if s.drifted[id] == drifted {
		return
	}
	s.drifted = cloned(s.drifted)
	if drifted {
		s.drifted[id] = true
	} else {
		delete(s.drifted, id)
	}
}

// cloned is a copy of m to change, an empty one for nil.
func cloned[K comparable, V any](m map[K]V) map[K]V {
	if m == nil {
		return map[K]V{}
	}
	return maps.Clone(m)
}

// selectedRow is the row of the resources tab under the cursor, and whether there is one.
func (s stageState) selectedRow() (treeRow, bool) {
	i := s.tables[resourcesTab].cursor
	if i >= len(s.tree) {
		return treeRow{}, false
	}
	return s.tree[i], true
}

// selectedResource is the resource under the cursor of the resources tab, and whether there is
// one: a heading is not.
func (s stageState) selectedResource() (stageResource, bool) {
	row, ok := s.selectedRow()
	if !ok || row.res < 0 {
		return stageResource{}, false
	}
	return s.resources[row.res], true
}

// toggleFold folds the group under the cursor of the resources tab, or opens it, with the cursor
// on its head. On a heading it folds every group of its kind, or opens them all when they are
// folded already. While the filter holds text nothing folds.
func (m model) toggleFold() model {
	s := &m.state.stage
	row, ok := s.selectedRow()
	if !ok || s.filter.text != "" {
		return m
	}
	if row.res < 0 {
		var groups []string
		for _, r := range s.resources {
			if r.group.order == row.kind && r.head && !r.last {
				groups = append(groups, r.group.fold())
			}
		}
		fold := slices.ContainsFunc(groups, func(g string) bool { return !s.folded[g] })
		s.folded = cloned(s.folded)
		for _, g := range groups {
			s.folded[g] = fold
		}
		return m.updateStageTables()
	}
	r := s.resources[row.res]
	i := slices.IndexFunc(s.resources, func(o stageResource) bool { return o.group == r.group })
	head := s.resources[i]
	if !head.head || head.last {
		return m
	}
	s.folded = cloned(s.folded)
	s.folded[r.group.fold()] = !s.folded[r.group.fold()]
	m = m.updateStageTables()
	if at := slices.Index(s.keys[resourcesTab], head.id); at >= 0 {
		s.tables[resourcesTab].setCursor(at)
	}
	return m
}

// resourceRow draws a resource in the tree: a head with ▾ over what it brings, or ▸ and how many
// it hides when folded, ▲ where what it hides is pending or changed outside koldstart; what it
// brings on the branches below it. A head under its kind's heading is named without the kind.
// Beside a note the depth of a queue leaves out what is in flight, to fit the column.
func (s stageState) resourceRow(r stageResource, hidden int, attention bool) tableRow {
	var name cell
	switch {
	case r.depth > 0:
		branch := "  ├─ "
		if r.last {
			branch = "  └─ "
		}
		name = cell{styles.S(branch, styles.ToneRule), styles.S(r.id, styles.ToneBody)}
	case r.head && hidden > 0:
		count := styles.S("  +"+formatInt(hidden), styles.ToneFaint)
		if attention {
			count = styles.S("  ▲ +"+formatInt(hidden), styles.ToneWarning)
		}
		name = cell{styles.S("▸ ", styles.ToneFaint), styles.S(headName(r.id), styles.ToneText), count}
	case r.head && !r.last:
		name = cell{styles.S("▾ ", styles.ToneFaint), styles.S(headName(r.id), styles.ToneText)}
	case r.head:
		name = cell{styles.S("  ", styles.ToneFaint), styles.S(headName(r.id), styles.ToneText)}
	default:
		name = cell{styles.S("  ", styles.ToneFaint), styles.S(r.id, styles.ToneText)}
	}
	note := resourceNote(r.res, r.retained, s.drifted[r.id])
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
	return tableRow{name, text(typeName(r.res.Type), styles.ToneMuted), text(orDash(r.res.PhysicalID), styles.ToneMuted), note}
}

// headName is the name of the head of a group under the heading of its kind: function/api is api,
// and resource/table table.
func headName(id string) string {
	for _, kind := range []string{"function/", "resource/"} {
		if name, ok := strings.CutPrefix(id, kind); ok {
			return name
		}
	}
	return id
}

// resourceNote says what is special about a resource: caught halfway by a deploy that did not
// finish or changed outside koldstart, both with ▲, left in place, or taken over.
func resourceNote(r *state.Resource, retained, drifted bool) cell {
	switch {
	case r.Pending != nil:
		return text("▲ "+r.Pending.Operation+" pending", styles.ToneWarning)
	case drifted:
		return text("▲ drifted", styles.ToneWarning)
	case retained:
		return text("kept", styles.ToneMuted)
	case r.Imported:
		return text("imported", styles.ToneMuted)
	case r.DeletionPolicy == "retain":
		return text("retained", styles.ToneMuted)
	}
	return text("", styles.ToneFaint)
}

func shortSHA(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

// switchTab moves to another tab of the stage, reading the history each time it is shown, as a
// deploy may have written the state since; what was read before shows until it is in.
func (m model) switchTab(by int) (model, tea.Cmd) {
	s := &m.state.stage
	s.tab = stageTab((int(s.tab) + by + len(stageTabs)) % len(stageTabs))
	if s.tab == historyTab {
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
		if s.tab == historyTab {
			return m.loadSummaries()
		}
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
	case key.Matches(msg, m.keys.Tab):
		if n, ok := tabNumber(msg, len(stageTabs)); ok {
			return m.switchTab(n - int(s.tab))
		}
	case key.Matches(msg, m.keys.NextTab, m.keys.Right):
		return m.switchTab(1)
	case key.Matches(msg, m.keys.PrevTab, m.keys.Left):
		return m.switchTab(-1)
	case key.Matches(msg, m.keys.Filter):
		return m, s.filter.open()
	case key.Matches(msg, m.keys.Fold) && s.tab == resourcesTab:
		return m.toggleFold(), nil
	case key.Matches(msg, m.keys.Refresh):
		if !s.loaded {
			m.loading, m.loadingMsg = true, readingNote("the state of "+s.ref.String())
		}
		m, cmd := m.loadStage()
		if s.tab == historyTab {
			m.state.stage.forgetFailedSummaries()
			var history tea.Cmd
			m, history = m.loadHistory()
			cmd = tea.Batch(cmd, history)
		}
		return m, cmd
	case key.Matches(msg, m.keys.Invoke, m.keys.RunLocal) && s.tab == functionsTab:
		return m.invokeFromStage(key.Matches(msg, m.keys.RunLocal))
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
// resource, an output or a deploy. On the heading of a kind of resource it folds the kind.
func (m model) openSelected() (model, tea.Cmd) {
	s := m.state.stage
	i := s.tables[s.tab].cursor
	switch s.tab {
	case functionsTab:
		if i < len(s.functions) {
			return m.openFunction(s.functions[i])
		}
	case resourcesTab:
		row, ok := s.selectedRow()
		switch {
		case !ok:
		case row.res < 0:
			return m.toggleFold(), nil
		default:
			r := s.resources[row.res]
			if name, ok := strings.CutPrefix(r.id, "function/"); ok && !r.retained {
				return m.openFunction(name)
			}
			return m.openResource(r)
		}
	case outputsTab:
		if i < len(s.outputs) {
			return m.openOutput(s.outputs[i]), nil
		}
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
	case s.tab == resourcesTab:
		if r, ok := s.selectedResource(); ok {
			return r.res.PhysicalID
		}
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

// stageGoBack goes back to the overview with the cursor on the stage left, also when it was
// opened on start rather than from there.
func (m model) stageGoBack() (model, tea.Cmd) {
	m = m.switchPage(overviewPage).selectStage(m.state.stage.ref)
	return m.loadStages()
}

// headFact is a fact in the head of a page, with a shorter form for when the whole line does not
// fit, or none, which leaves it out.
type headFact struct {
	value cell
	short cell
}

// stageFacts are the facts of the stage the frame's top border holds beside the breadcrumb, which
// names it, in width: how many resources it has and a remove kept, its account and region where
// they are not the header's, when it was last deployed and by whom, and its lock. Where they do not
// fit, the date of the deploy and the account go, and then the first of them.
func (m model) stageFacts(width int) string {
	s := m.state.stage
	if s.st == nil {
		return ""
	}
	n, noun := len(s.st.Resources), " resources"
	if n == 1 {
		noun = " resource"
	}
	resources := cell{styles.S(formatInt(n), styles.ToneBody), styles.S(noun, styles.ToneFaint)}
	facts := []headFact{{value: resources, short: resources}}
	if kept := len(s.st.Retained); kept > 0 {
		k := cell{styles.S(formatInt(kept), styles.ToneBody), styles.S(" kept", styles.ToneFaint)}
		facts = append(facts, headFact{value: k, short: k})
	}
	if s.st.Account != "" && s.st.Account != m.ident.Account {
		facts = append(facts, headFact{value: text(s.st.Account, styles.ToneBody)})
	}
	if s.ref.Region != m.ident.Region {
		region := text(s.ref.Region, styles.ToneMuted)
		facts = append(facts, headFact{value: region, short: region})
	}
	switch {
	case s.st.DeployedAt.IsZero():
		never := text("not deployed yet", styles.ToneMuted)
		deployed := headFact{value: never, short: never}
		if m.b.Local(s.ref) {
			deployed.value = append(slices.Clone(never), styles.S(" · "+m.keys.Deploy.Help().Key+" plans its first deploy", styles.ToneFaint))
		}
		facts = append(facts, deployed)
	default:
		ago := cell{styles.S(formatAgo(s.st.DeployedAt), styles.ToneBody), styles.S(" by ", styles.ToneFaint), styles.S(orDash(s.st.DeployedBy), styles.ToneMuted)}
		deployed := headFact{value: append(cell{styles.S(deployedWord+" ", styles.ToneFaint)}, ago...), short: ago}
		facts = append(facts, deployed, headFact{value: text(formatTime(s.st.DeployedAt), styles.ToneMuted)})
	}
	lock := cell{styles.S("lock ", styles.ToneFaint), styles.S("free", styles.ToneFaint)}
	facts = append(facts, headFact{value: lock, short: lock})
	if l := s.lock; l != nil {
		who := cmpOr(l.Who, "someone")
		if l.Where != "" {
			who += " on " + l.Where
		}
		held := headFact{value: cell{styles.S("● ", styles.ToneWarning), styles.S(who, styles.ToneWarning)}}
		held.short = held.value
		if !l.Since.IsZero() {
			held.short = cell{styles.S("● ", styles.ToneWarning), styles.S(who+", "+formatAgo(l.Since), styles.ToneWarning)}
		}
		if l.Operation != "" || !l.Since.IsZero() {
			held.value = append(held.value, styles.S(" · "+strings.TrimSpace(l.Operation+" "+lockSince(l.Since)), styles.ToneWarning))
		}
		facts[len(facts)-1] = held
	}
	return styles.Render(factCell(facts, width)...)
}

// factCell is the facts of a head on one line of width, between faint dots: each whole where they
// all fit, and otherwise giving way from the last, each to its shorter form or left out where it
// has none. Where even the shorter forms do not fit, the first facts go, so what comes last, the
// lock, stays.
func factCell(facts []headFact, width int) cell {
	join := func(short int) cell {
		var line cell
		for i, f := range facts {
			v := f.value
			if i >= len(facts)-short {
				v = f.short
			}
			if v == nil {
				continue
			}
			if len(line) > 0 {
				line = append(line, sep())
			}
			line = append(line, v...)
		}
		return line
	}
	line := join(0)
	for short := 1; short <= len(facts) && cellWidth(line) > width; short++ {
		line = join(short)
	}
	if cellWidth(line) > width && len(facts) > 1 {
		return factCell(facts[1:], width)
	}
	return line
}

// lockSince says when a lock was taken: since 14:02:11, 4m ago, with the date when it was not
// today.
func lockSince(since time.Time) string {
	if since.IsZero() {
		return ""
	}
	at, now := since.Local(), time.Now()
	when := at.Format(time.TimeOnly)
	if at.YearDay() != now.YearDay() || at.Year() != now.Year() {
		when = formatTime(since)
	}
	return "since " + when + ", " + formatAgo(since)
}

// tabBar draws tabs on one line, the open one in the accent and bold, the others muted, with
// their counts faint, as the panes of the invoke tab switch.
func tabBar(names []string, counts []string, open int) string {
	var spans []styles.Span
	for i, name := range names {
		if i > 0 {
			spans = append(spans, styles.S("   ", styles.ToneFaint))
		}
		spans = append(spans, tabSpans(name, count(counts, i), i == open)...)
	}
	return styles.Render(spans...)
}

// count is the count of the i-th tab, or "" where it has none.
func count(counts []string, i int) string {
	if i < len(counts) {
		return counts[i]
	}
	return ""
}

// tabSpans are a tab's name and its count.
func tabSpans(name, count string, open bool) []styles.Span {
	spans := []styles.Span{styles.S(name, styles.ToneMuted)}
	if open {
		spans[0] = styles.B(name, styles.ToneText)
	}
	if count != "" {
		spans = append(spans, styles.S(" "+count, styles.ToneFaint))
	}
	return spans
}

// tabRow draws the tabs of a page, each after the number that opens it, over a rule as wide as
// width whose stretch under the open tab is in the accent, so which is open reads at a glance:
//
//	1 functions 3   2 resources 20   3 outputs 4   4 history
//	──────────────────────────────────────────────────────────
//
// The rule is the light one of the frame throughout: the heavy one is wide in some terminals,
// which wraps the line and leaves the rows under it out of step.
func tabRow(names []string, counts []string, open, width int) string {
	var spans []styles.Span
	var under strings.Builder
	rule := lipgloss.NewStyle().Foreground(styles.P.Rule)
	used := 0
	for i, name := range names {
		if i > 0 {
			spans = append(spans, styles.S(tabGap, styles.ToneFaint))
			under.WriteString(rule.Render(strings.Repeat("─", len(tabGap))))
			used += len(tabGap)
		}
		tab := append([]styles.Span{styles.S(strconv.Itoa(i+1)+" ", styles.ToneFaint)}, tabSpans(name, count(counts, i), i == open)...)
		w := ansi.StringWidth(plainText(cell(tab)))
		spans = append(spans, tab...)
		if i == open {
			under.WriteString(lipgloss.NewStyle().Foreground(styles.P.Accent).Render(strings.Repeat("─", w)))
		} else {
			under.WriteString(rule.Render(strings.Repeat("─", w)))
		}
		used += w
	}
	under.WriteString(rule.Render(strings.Repeat("─", max(0, width-used))))
	return styles.Render(spans...) + "\n" + under.String()
}

// invokeFromStage invokes the function under the cursor of the functions tab, in aws or on this
// machine, as i and shift+i do on its page: it opens on its invoke tab, where the event it is
// invoked with and what it returned are in sight.
func (m model) invokeFromStage(local bool) (model, tea.Cmd) {
	s := m.state.stage
	t := s.tables[functionsTab]
	if t.cursor >= len(s.keys[functionsTab]) {
		return m, nil
	}
	m, open := m.openFunction(s.keys[functionsTab][t.cursor])
	m, run := m.startInvoke(local)
	return m, tea.Batch(open, run)
}

// tabNumber is the tab a digit opens, counting from 0, and false for a page with fewer tabs.
func tabNumber(msg tea.KeyPressMsg, tabs int) (int, bool) {
	n, err := strconv.Atoi(msg.String())
	return n - 1, err == nil && n >= 1 && n <= tabs
}

// tabGap is the room between two tabs.
const tabGap = "   "

// metricsNote says the metrics could not be read, or that those on screen are from an earlier
// read, faint as the table beside it says what it can, in width: whole where it fits, and
// otherwise in the longest of its shorter forms that fits, as no metrics or as of 10:04:05,
// rather than cut short; nothing where none fits.
func (m model) metricsNote(width int) string {
	s := m.state.stage
	notes := []cell{
		text("could not read the metrics: "+s.metricsErr, styles.ToneFaint),
		text("could not read the metrics", styles.ToneFaint),
		text("no metrics", styles.ToneFaint),
	}
	if s.metrics != nil {
		at := s.metricsAt.Local().Format(time.TimeOnly)
		notes = []cell{staleNote("metrics", s.metricsAt, s.metricsErr), text("metrics as of "+at, styles.ToneFaint), text("as of "+at, styles.ToneFaint)}
	}
	for _, note := range notes {
		if cellWidth(note) <= width {
			return styles.Render(note...)
		}
	}
	return ""
}

// centredLines draws a cell in lines of width, each no wider than its text, so a note set in
// the middle of a page is centred line by line rather than as a block with its text at the left.
// A cell wider than width starts each span on a line of its own, without the dot before it, so
// the key a note ends with, · r tries again, is not split across two lines.
func centredLines(c cell, width int) []string {
	parts := []cell{c}
	if cellWidth(c) > width {
		parts = make([]cell, 0, len(c))
		for _, s := range c {
			s.Text = strings.TrimPrefix(s.Text, " · ")
			parts = append(parts, wrapCell(cell{s}, width)...)
		}
	}
	lines := make([]string, 0, len(parts))
	for _, part := range parts {
		lines = append(lines, renderCell(part, column{width: max(1, min(width, cellWidth(part)))}, nil))
	}
	return lines
}

func (m model) stageView() string {
	s := m.state.stage
	if s.st == nil {
		what := "the state of " + s.ref.String()
		if s.err == "" {
			return place(contentWidth, contentHeight, styles.Muted(truncate(styles.Clean(readingNote(what)), contentWidth-4)))
		}
		return place(contentWidth, contentHeight, lipgloss.JoinVertical(lipgloss.Center,
			centredLines(m.couldNotRead(what, s.err, styles.ToneDanger), min(contentWidth-4, dialogTextWidth()))...))
	}
	counts := []string{formatInt(len(s.functions)), formatInt(len(s.resources)), formatInt(len(s.outputs)), ""}
	if s.historyLoaded {
		counts[historyTab] = formatInt(len(s.shownDeploys))
	}
	bar, under, _ := strings.Cut(indent(tabRow(stageTabs, counts, int(s.tab), tableWidth())), "\n")
	if s.tab == functionsTab && s.metricsErr != "" {
		if note := m.metricsNote(contentWidth - ansi.StringWidth(bar) - 4); note != "" {
			bar = spread(bar, note, contentWidth-1)
		}
	}
	return strings.Join([]string{
		bar,
		under,
		indent(m.stageBody()),
	}, "\n")
}
