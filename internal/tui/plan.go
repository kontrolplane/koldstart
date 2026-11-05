package tui

import (
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/kontrolplane/koldstart/internal/cli"
	"github.com/kontrolplane/koldstart/internal/engine"
	"github.com/kontrolplane/koldstart/internal/tui/styles"
)

// planState is a plan of a deploy or a remove, and the progress of each node while it applies.
type planState struct {
	ref      cli.StageRef
	function string
	remove   bool
	from     page
	gen      int
	plan     *engine.Plan
	applying bool
	done     bool
	progress map[string]nodeProgress
	scroll   int
	cursor   int
}

// nodeProgress is how far applying a node got.
type nodeProgress struct {
	started  bool
	done     bool
	err      error
	duration time.Duration
}

// planDeploy plans a deploy of a stage of the project here, or with function of its code.
func (m model) planDeploy(ref cli.StageRef, function string) (model, tea.Cmd) {
	if !m.b.Local(ref) {
		return m.setStatus("only the project here can be deployed: run kdst ui in its directory", styles.ToneWarning)
	}
	m.state.plan = planState{ref: ref, function: function, from: m.page}
	m = m.switchPage(planPage)
	m.loading, m.loadingMsg = true, "building and planning "+ref.String()+"…"
	if function != "" {
		m.loadingMsg = "building " + function + " and planning its code…"
	}
	return m.loadPlan()
}

// planRemove plans removing a stage, then asks to type its name over the plan.
func (m model) planRemove(ref cli.StageRef) (model, tea.Cmd) {
	m.state.plan = planState{ref: ref, remove: true, from: m.page}
	m = m.switchPage(planPage)
	m.loading, m.loadingMsg = true, "planning the remove of "+ref.String()+"…"
	return m.loadPlan()
}

func (m model) applyPlanLoaded(msg planLoadedMsg) (model, tea.Cmd) {
	p := &m.state.plan
	if msg.gen != p.gen || m.page != planPage {
		return m, nil
	}
	m.loading = false
	if msg.err != nil {
		m.error = "could not plan: " + msg.err.Error()
		return m.switchPage(p.from), nil
	}
	p.plan = msg.plan
	switch {
	case !p.plan.HasChanges() && p.remove:
		m = m.switchPage(p.from)
		return m.setStatus("nothing of "+p.ref.String()+" to remove", styles.ToneWarning)
	case p.remove:
		return m.askRemove()
	}
	return m, nil
}

// startApply applies the plan on screen.
func (m model) startApply() (model, tea.Cmd) {
	p := &m.state.plan
	p.applying, p.progress, p.scroll = true, map[string]nodeProgress{}, 0
	return m, m.apply()
}

func (m model) applyProgress(msg progressMsg) (model, tea.Cmd) {
	p := &m.state.plan
	ev := msg.event
	if p.progress != nil {
		p.progress[ev.ID] = nodeProgress{started: true, done: ev.Done, err: ev.Err, duration: ev.Duration}
	}
	return m, waitFor(msg.ch)
}

func (m model) applied(msg appliedMsg) (model, tea.Cmd) {
	p := &m.state.plan
	p.applying = false
	switch {
	case msg.err != nil:
		p.done = true
		m.error = "the " + m.planWhat() + " failed: " + msg.err.Error()
		return m, nil
	case !msg.applied:
		p.plan, p.progress = msg.plan, nil
		text := "the configuration or the state changed since: this is the plan now, ctrl+s applies it"
		if p.remove {
			m, cmd := m.askRemove()
			m, status := m.setStatus("the state changed since: this is the plan now", styles.ToneWarning)
			return m, tea.Batch(cmd, status)
		}
		return m.setStatus(text, styles.ToneWarning)
	case msg.gone:
		m.state.stage = newStageState()
		m = m.switchPage(overviewPage)
		m, cmd := m.loadStages()
		m, status := m.setStatus("removed "+p.ref.String(), styles.ToneSuccess)
		return m, tea.Batch(cmd, status)
	}
	p.done = true
	done := "deployed " + p.ref.String()
	switch {
	case p.remove:
		done = "removed " + p.ref.String() + ", and left what holds data in place"
	case p.function != "":
		done = "deployed the code of " + p.function
	}
	return m.setStatus(done, styles.ToneSuccess)
}

func (m model) planUpdate(msg tea.KeyPressMsg) (model, tea.Cmd) {
	p := &m.state.plan
	switch {
	case key.Matches(msg, m.keys.Back, m.keys.Quit):
		if p.applying {
			return m.setStatus("the "+m.planWhat()+" is running: wait for it, or ctrl+c to stop it", styles.ToneWarning)
		}
		return m.leavePlan()
	case key.Matches(msg, m.keys.Apply):
		switch {
		case p.plan == nil || p.applying || p.done:
		case p.remove:
			return m.askRemove()
		case !p.plan.HasChanges():
			return m.setStatus("no changes to apply", styles.ToneWarning)
		default:
			return m.startApply()
		}
		return m, nil
	case key.Matches(msg, m.keys.View):
		if changes := m.planChanges(); p.cursor < len(changes) {
			c := changes[p.cursor]
			return m.openDiff(c.ID, cli.Verb(c.Action)+" "+shortType(c.Type), c.Before, c.After)
		}
		return m, nil
	}
	lines, rows := m.planLines()
	if cursor, ok := moveCursor(msg, m.keys, p.cursor, len(rows), contentHeight); ok {
		p.cursor = cursor
		_, p.scroll = selectable(lines, rows, p.cursor, p.scroll, contentHeight)
	}
	return m, nil
}

// planChanges are the changes of the plan that do something, as the page lists them.
func (m model) planChanges() []engine.Change {
	p := m.state.plan
	if p.plan == nil {
		return nil
	}
	var out []engine.Change
	for _, c := range p.plan.Changes {
		if c.Action != engine.NoOp {
			out = append(out, c)
		}
	}
	return out
}

// The two kinds of plan.
const (
	planDeployWord = "deploy"
	planRemoveWord = "remove"
)

// planWhat says whether the plan deploys or removes.
func (m model) planWhat() string {
	if m.state.plan.remove {
		return planRemoveWord
	}
	return planDeployWord
}

// leavePlan goes back to where the plan was asked from, reading the stage again.
func (m model) leavePlan() (model, tea.Cmd) {
	switch from := m.state.plan.from; from {
	case overviewPage:
		m = m.switchPage(overviewPage)
		return m.loadStages()
	case functionPage:
		m = m.switchPage(functionPage)
		return m.loadStage()
	}
	return m.openStage(m.state.plan.ref)
}

// planSummary counts the changes of each action, as plan does.
func planSummary(plan *engine.Plan) string {
	counts := plan.Counts()
	var parts []string
	for _, a := range []engine.Action{engine.Create, engine.Update, engine.Replace, engine.Delete, engine.Keep, engine.Adopt} {
		if counts[a] > 0 {
			parts = append(parts, fmt.Sprintf("%d to %s", counts[a], cli.Verb(a)))
		}
	}
	if len(parts) == 0 {
		return "no changes"
	}
	return strings.Join(parts, ", ")
}

var actionTones = map[engine.Action]styles.Tone{
	engine.Create:  styles.ToneSuccess,
	engine.Update:  styles.ToneWarning,
	engine.Replace: styles.ToneDanger,
	engine.Delete:  styles.ToneDanger,
	engine.Keep:    styles.ToneMuted,
	engine.Adopt:   styles.ToneInfo,
}

// planLines are the lines of the plan, with the index of the line of each change.
func (m model) planLines() ([]string, []int) {
	p := m.state.plan
	width := contentWidth - 6
	if p.plan == nil {
		return nil, nil
	}
	title := "deploy of " + p.ref.String()
	switch {
	case p.remove:
		title = "remove of " + p.ref.String()
	case p.function != "":
		title = "deploy of the code of " + p.function
	}
	summary := planSummary(p.plan)
	if p.plan.OnlyCode() {
		summary += ": only code, replaced in place"
	}
	lines := []string{
		styles.SectionHeader(title+" in "+p.ref.Region, width),
		" " + styles.Render(styles.S(summary, styles.ToneBody)),
		"",
	}
	var rows []int
	for _, c := range m.planChanges() {
		rows = append(rows, len(lines))
		head := cell{
			styles.S(fmt.Sprintf(" %-4s", cli.Symbol(c.Action)), actionTones[c.Action]),
			styles.S(c.ID, styles.ToneText),
		}
		if c.Resume {
			head = append(head, styles.S("  finishing what a deploy started", styles.ToneFaint))
		}
		status := m.progressCell(c)
		lines = append(lines, spread(renderCell(head, column{width: width - 16}, nil), styles.Render(status...), width))
		if c.Action == engine.Update || c.Action == engine.Replace || c.Action == engine.Adopt {
			for _, change := range cli.FieldChanges(c) {
				lines = append(lines, renderCell(text("        "+change, styles.ToneMuted), column{width: width}, nil))
			}
		}
	}
	return lines, rows
}

// progressCell says how far a node got while applying.
func (m model) progressCell(c engine.Change) cell {
	p := m.state.plan
	pr, ok := p.progress[c.ID]
	switch {
	case p.progress == nil:
		return nil
	case !ok && p.applying:
		return cell{styles.S("waiting", styles.ToneFaint)}
	case !ok:
		return cell{styles.S("not applied", styles.ToneFaint)}
	case pr.err != nil:
		return cell{styles.S("✗ failed", styles.ToneDanger)}
	case pr.done:
		return cell{styles.S("✓ ", styles.ToneSuccess), styles.S(formatDuration(pr.duration), styles.ToneFaint)}
	}
	return cell{styles.S(m.spinner.View()+" ", styles.ToneAccent), styles.S(cli.Verb(c.Action), styles.ToneMuted)}
}

func (m model) planView() string {
	lines, rows := m.planLines()
	view, _ := selectable(lines, rows, m.state.plan.cursor, m.state.plan.scroll, contentHeight)
	return view
}
