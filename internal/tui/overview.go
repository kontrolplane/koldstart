package tui

import (
	"slices"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/kontrolplane/koldstart/internal/cli"
	"github.com/kontrolplane/koldstart/internal/state"
	"github.com/kontrolplane/koldstart/internal/tui/keys"
	"github.com/kontrolplane/koldstart/internal/tui/styles"
)

// overviewState is the list of every app and stage the account holds.
type overviewState struct {
	gen int
	// answered is the generation of the last load that came back; a refresh does not ask again
	// while gen is still out, or a read slower than the refresh would be dropped every time.
	answered int
	loaded   bool
	// opened says the project's stage was opened on start, which happens once.
	opened bool
	// failed says a load of the stages failed, so the next failure goes to the footer rather
	// than raising the error again every refresh.
	failed bool
	// err is why the stages could not be listed, before they ever were.
	err    string
	stages []cli.StageSummary
	shown  []cli.StageSummary
	// peeks are the states the peek of the stage under the cursor read, by the stage, and peekGen
	// the generation of the last read. It is copied before it changes.
	peeks   map[cli.StageRef]stagePeek
	peekGen int
	table   dataTable
	filter  filterBox
}

// overviewColumns are the columns of the stages. On a narrow terminal who deployed goes first,
// then the count of resources: the region stays, as it tells apart a stage of the same name in
// two regions.
var overviewColumns = []column{
	{title: "app", width: 14, grow: 2},
	{title: "stage", width: 12, grow: 1},
	{title: "region", width: 14, drop: 1},
	{title: "deployed", width: 10},
	{title: "by", width: 10, grow: 1, drop: 3},
	{title: resourcesWord, width: 10, right: true, drop: 2},
	{title: "lock", width: 10, grow: 1},
}

func newOverviewState() overviewState {
	return overviewState{
		table:  newTable(overviewColumns, ""),
		filter: newFilterBox("filter by app, stage or region…"),
	}
}

// noStagesYet is what the overview says when the account holds no stage.
func noStagesYet() string {
	return "no stages in this account yet: deploy one with " + binary + " deploy"
}

// emptyNote is what the overview says without a row: why, and the key that changes it. It is
// wrapped under the table's header, so a long error keeps its end and the key after it.
func (o overviewState) emptyNote(k keys.KeyMap) cell {
	switch {
	case !o.loaded && o.failed:
		return cell{styles.S("could not list the stages: "+styles.Clean(o.err), styles.ToneMuted), styles.S(" · "+k.Refresh.Help().Key+" tries again", styles.ToneFaint)}
	case o.filter.text != "":
		return text(noMatch("stage", styles.Clean(o.filter.text)), styles.ToneMuted)
	}
	return text(noStagesYet(), styles.ToneMuted)
}

// notDeployed is the project's stage before its first deploy, listed so it can be planned.
func notDeployed(s cli.StageSummary) bool {
	return s.DeployedAt.IsZero() && s.Resources == 0 && s.Retained == 0 && s.Err == nil
}

// withProject lists the project's stage when it has no state yet, in the region of the session,
// in its place in the order of app, stage and region the stages come in.
func (m model) withProject(stages []cli.StageSummary) []cli.StageSummary {
	if m.project == "" {
		return stages
	}
	for _, s := range stages {
		if s.App == m.project && s.Stage == m.projectStage {
			return stages
		}
	}
	project := cli.StageSummary{StageRef: cli.StageRef{Region: m.ident.Region, App: m.project, Stage: m.projectStage}}
	at, _ := slices.BinarySearchFunc(stages, project, func(a, b cli.StageSummary) int { return a.Compare(b.StageRef) })
	return slices.Insert(slices.Clone(stages), at, project)
}

func (m model) updateOverviewTable() model {
	o := &m.state.overview
	var cur *cli.StageRef
	if o.table.cursor < len(o.shown) {
		ref := o.shown[o.table.cursor].StageRef
		cur = &ref
	}
	o.shown = nil
	for _, s := range o.stages {
		if o.filter.matches(s.App, s.Stage, s.Region) {
			o.shown = append(o.shown, s)
		}
	}
	w := stageCountsOf(o.shown)
	rows := make([]tableRow, 0, len(o.shown))
	for _, s := range o.shown {
		rows = append(rows, overviewRow(s, s.App == m.project, w))
	}
	o.table.base = overviewColumnsFor(w)
	o.table.setRows(rows)
	if cur != nil {
		for i, s := range o.shown {
			if s.StageRef == *cur {
				o.table.setCursor(i)
			}
		}
	}
	return m
}

// stageCounts are how wide the counts of the stages listed are: the widest count of resources and
// the widest note of what is kept beside it, as " +3 kept", so the counts line up in their column.
type stageCounts struct {
	resources int
	kept      int
}

// stageCountsOf is how wide the counts of the deployed stages are.
func stageCountsOf(stages []cli.StageSummary) stageCounts {
	var w stageCounts
	for _, s := range stages {
		if s.Err == nil && !notDeployed(s) {
			w.resources = max(w.resources, len(formatInt(s.Resources)))
			w.kept = max(w.kept, len(keptNote(s.Retained)))
		}
	}
	return w
}

// overviewColumnsFor are the columns of the stages, the count of resources as wide as its widest
// count and note of what is kept, so neither is cut while the table has room.
func overviewColumnsFor(w stageCounts) []column {
	cols := slices.Clone(overviewColumns)
	for i, c := range cols {
		if c.title == resourcesWord {
			cols[i].width = max(c.width, w.resources+w.kept)
		}
	}
	return cols
}

// keptNote says how many resources a remove left in place, or nothing for none.
func keptNote(n int) string {
	if n == 0 {
		return ""
	}
	return " +" + formatInt(n) + " kept"
}

// overviewRow draws a stage, its counts padded to w. One whose state or lock cannot be read says
// so in its lock column, too narrow for the error, which its peek and enter show in full.
func overviewRow(s cli.StageSummary, local bool, w stageCounts) tableRow {
	app := text(styles.Clean(s.App), styles.ToneText)
	if local {
		app = append(app, styles.S(" ●", styles.ToneAccent))
	}
	row := tableRow{app, text(styles.Clean(s.Stage), styles.ToneBody), text(styles.Clean(s.Region), styles.ToneMuted)}
	lock := text("free", styles.ToneFaint)
	if s.Lock != nil {
		lock = lockCell(s.Lock)
	}
	switch {
	case s.Err != nil:
		return append(row, text("-", styles.ToneFaint), text("", styles.ToneFaint), text("", styles.ToneFaint), cell{styles.S("✗ ", styles.ToneDanger), styles.S("unreadable", styles.ToneDanger)})
	case notDeployed(s):
		if s.Lock == nil {
			lock = text("", styles.ToneFaint)
		}
		return append(row, text("not yet", styles.ToneFaint), text("", styles.ToneFaint), text("", styles.ToneFaint), lock)
	}
	count, kept := formatInt(s.Resources), keptNote(s.Retained)
	resources := cell{
		styles.S(strings.Repeat(" ", max(0, w.resources-len(count)))+count, styles.ToneBody),
		styles.S(strings.Repeat(" ", max(0, w.kept-len(kept)))+kept, styles.ToneFaint),
	}
	return append(row, text(formatAgo(s.DeployedAt), styles.ToneBody), text(styles.Clean(orDash(s.DeployedBy)), styles.ToneMuted), resources, lock)
}

// lockCell is who holds a stage's lock and for what, the operation first, so a long name is what
// a narrow column cuts: deploy by github-actions[bot].
func lockCell(l *state.LockInfo) cell {
	return cell{
		styles.S("● ", styles.ToneWarning),
		styles.S(styles.Clean(cmpOr(l.Operation, "locked")), styles.ToneWarning),
		styles.S(" by ", styles.ToneFaint),
		styles.S(styles.Clean(cmpOr(l.Who, "someone")), styles.ToneWarning),
	}
}

// selectStage puts the overview's cursor on ref, when it is listed, so going back from a stage,
// also the project's that opened on start, lands on it.
func (m model) selectStage(ref cli.StageRef) model {
	o := &m.state.overview
	if i := slices.IndexFunc(o.shown, func(s cli.StageSummary) bool { return s.StageRef == ref }); i >= 0 {
		o.table.setCursor(i)
	}
	return m
}

// selectedStage is the stage under the cursor.
func (m model) selectedStage() (cli.StageSummary, bool) {
	o := m.state.overview
	if o.table.cursor >= len(o.shown) || len(o.shown) == 0 {
		return cli.StageSummary{}, false
	}
	return o.shown[o.table.cursor], true
}

func (m model) applyStages(msg stagesLoadedMsg) (model, tea.Cmd) {
	o := &m.state.overview
	if msg.gen != o.gen {
		return m, nil
	}
	o.answered = msg.gen
	if m.page == overviewPage {
		m.loading = false
	}
	if msg.err != nil {
		onScreen := o.loaded || o.failed
		o.failed = true
		if !o.loaded {
			o.err = msg.err.Error()
		}
		m = m.updateOverviewTable()
		return m.loadError("could not list the stages", msg.err, onScreen)
	}
	o.stages, o.loaded = m.withProject(msg.stages), true
	if m.page == overviewPage {
		m = m.markRead()
	}
	m = m.updateOverviewTable()
	if !o.opened && m.page == overviewPage {
		o.opened = true
		for _, s := range o.stages {
			if s.App == m.project && s.Stage == m.projectStage {
				return m.openStage(s.StageRef)
			}
		}
	}
	return m.loadStagePeek()
}

func (m model) overviewUpdate(msg tea.KeyPressMsg) (model, tea.Cmd) {
	o := &m.state.overview
	if o.filter.active {
		changed, cmd := o.filter.update(msg, m.keys)
		if changed {
			m = m.updateOverviewTable()
		}
		return m, cmd
	}
	if o.table.move(msg, m.keys) {
		return m, nil
	}
	s, ok := m.selectedStage()
	switch {
	case key.Matches(msg, m.keys.Quit):
		return m, tea.Quit
	case key.Matches(msg, m.keys.Back):
		if o.filter.clear() {
			return m.updateOverviewTable(), nil
		}
	case key.Matches(msg, m.keys.Filter):
		return m, o.filter.open()
	case key.Matches(msg, m.keys.Refresh):
		m.loading, m.loadingMsg = !o.loaded, "listing the stages…"
		return m.loadStages()
	case !ok:
	case key.Matches(msg, m.keys.View):
		return m.openStage(s.StageRef)
	case key.Matches(msg, m.keys.Copy):
		return m.copy(s.String())
	case key.Matches(msg, m.keys.Unlock):
		return m.askUnlock(s.StageRef, s.Lock)
	case key.Matches(msg, m.keys.Remove):
		return m.planRemove(s.StageRef)
	case key.Matches(msg, m.keys.Deploy):
		return m.planDeploy(s.StageRef, "")
	}
	return m, nil
}

func (m model) overviewView() string {
	o := m.state.overview
	if len(o.table.rows) == 0 {
		return indent(withNote(o.table, o.emptyNote(m.keys)))
	}
	return indent(withPeek(o.table, func(height int) []string { return m.overviewPeek(tableWidth()) }))
}

// withNote is a table without rows and, from where its second row would be, a note wrapped to
// its width, cut at the table's last row.
func withNote(t dataTable, note cell) string {
	t.empty = ""
	lines := strings.Split(t.View(), "\n")
	first := len(lines) - t.height + 1
	for i, l := range cellLines(note, t.width()-tableGutter) {
		if first+i < len(lines) {
			lines[first+i] = pad(tableGutter, nil) + l
		}
	}
	return strings.Join(lines, "\n")
}
