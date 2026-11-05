package tui

import (
	"time"

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
	stages []cli.StageSummary
	shown  []cli.StageSummary
	table  dataTable
	filter filterBox
}

var overviewColumns = []column{
	{title: "app", width: 14, grow: 2},
	{title: "stage", width: 12, grow: 1},
	{title: "region", width: 14, drop: 2},
	{title: "deployed", width: 10},
	{title: "by", width: 10, grow: 1, drop: 3},
	{title: resourcesWord, width: 10, right: true, drop: 1},
	{title: "lock", width: 10, grow: 1},
}

func newOverviewState() overviewState {
	return overviewState{
		table:  newTable(overviewColumns, "no stages yet: deploy one with kdst deploy"),
		filter: newFilterBox("filter by app, stage or region…"),
	}
}

// notDeployed is the project's stage before its first deploy, listed so it can be planned.
func notDeployed(s cli.StageSummary) bool {
	return s.DeployedAt.IsZero() && s.Resources == 0 && s.Retained == 0 && s.Err == nil
}

// withProject lists the project's stage when it has no state yet, in the region of the session.
func (m model) withProject(stages []cli.StageSummary) []cli.StageSummary {
	if m.project == "" {
		return stages
	}
	for _, s := range stages {
		if s.App == m.project && s.Stage == m.projectStage {
			return stages
		}
	}
	return append([]cli.StageSummary{{StageRef: cli.StageRef{Region: m.ident.Region, App: m.project, Stage: m.projectStage}}}, stages...)
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

func overviewRow(s cli.StageSummary, local bool) tableRow {
	app := text(s.App, styles.ToneText)
	if local {
		app = append(app, styles.S(" ●", styles.ToneAccent))
	}
	row := tableRow{app, text(s.Stage, styles.ToneBody), text(s.Region, styles.ToneMuted)}
	switch {
	case s.Err != nil:
		return append(row, text("unreadable", styles.ToneDanger), text("", styles.ToneFaint), text("", styles.ToneFaint), text(s.Err.Error(), styles.ToneDanger))
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
		return m.loadError("could not list the stages", msg.err, o.loaded)
	}
	o.stages, o.loaded = m.withProject(msg.stages), true
	if m.page == overviewPage {
		m.refreshedAt = time.Now()
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
