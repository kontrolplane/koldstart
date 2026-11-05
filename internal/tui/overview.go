package tui

import (
	"cmp"
	"slices"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/kontrolplane/koldstart/internal/cli"
	"github.com/kontrolplane/koldstart/internal/tui/styles"
)

// overviewState is the list of every app and stage the account holds.
type overviewState struct {
	gen    int
	loaded bool
	// opened says the project's stage was opened on start, which happens once.
	opened bool
	// failed says a load of the stages failed, so the next failure goes to the footer rather
	// than raising the error again every refresh.
	failed bool
	// err is why the stages could not be listed, before they ever were.
	err    string
	stages []cli.StageSummary
	shown  []cli.StageSummary
	table  dataTable
	filter filterBox
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
		table:  newTable(overviewColumns, noStagesYet()),
		filter: newFilterBox("filter by app, stage or region…"),
	}
}

// noStagesYet is what the overview says when the account holds no stage.
func noStagesYet() string {
	return "no stages in this account yet: deploy one with " + binary + " deploy"
}

// emptyText is what the table says without a row: why, and the key that changes it.
func (o overviewState) emptyText() string {
	switch {
	case !o.loaded && o.failed:
		return "could not list the stages: " + o.err + " · r tries again"
	case o.filter.text != "":
		return noMatch("stage", o.filter.text)
	}
	return noStagesYet()
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
	at, _ := slices.BinarySearchFunc(stages, project, func(a, b cli.StageSummary) int {
		return cmp.Or(cmp.Compare(a.App, b.App), cmp.Compare(a.Stage, b.Stage), cmp.Compare(a.Region, b.Region))
	})
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
	var rows []tableRow
	for _, s := range o.stages {
		if !o.filter.matches(s.App, s.Stage, s.Region) {
			continue
		}
		o.shown = append(o.shown, s)
		rows = append(rows, overviewRow(s, s.App == m.project))
	}
	o.table.empty = o.emptyText()
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

// overviewRow draws a stage. One whose state or lock cannot be read says so in its lock column,
// too narrow for the error, which enter shows in full as it reads the stage again.
func overviewRow(s cli.StageSummary, local bool) tableRow {
	app := text(s.App, styles.ToneText)
	if local {
		app = append(app, styles.S(" ●", styles.ToneAccent))
	}
	row := tableRow{app, text(s.Stage, styles.ToneBody), text(s.Region, styles.ToneMuted)}
	switch {
	case s.Err != nil:
		return append(row, text("-", styles.ToneFaint), text("", styles.ToneFaint), text("", styles.ToneFaint), cell{styles.S("✗ ", styles.ToneDanger), styles.S("unreadable", styles.ToneDanger)})
	case notDeployed(s):
		return append(row, text("not yet", styles.ToneFaint), text("", styles.ToneFaint), text("", styles.ToneFaint), text("", styles.ToneFaint))
	}
	resources := text(formatInt(s.Resources), styles.ToneBody)
	if s.Retained > 0 {
		resources = cell{styles.S(formatInt(s.Resources), styles.ToneBody), styles.S(" +"+formatInt(s.Retained)+" kept", styles.ToneFaint)}
	}
	lock := text("free", styles.ToneFaint)
	if s.Lock != nil {
		lock = cell{styles.S("● ", styles.ToneWarning), styles.S(s.Lock.Who+" "+s.Lock.Operation, styles.ToneWarning)}
	}
	return append(row, text(formatAgo(s.DeployedAt), styles.ToneBody), text(orDash(s.DeployedBy), styles.ToneMuted), resources, lock)
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
	return m, nil
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
	return indent(m.state.overview.table.View())
}
