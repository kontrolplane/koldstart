package tui

import (
	"context"
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
	// cancel stops the plan being made, when the page is left before it is shown.
	cancel   context.CancelFunc
	plan     *engine.Plan
	applying bool
	done     bool
	// failed is an apply that ended in an error, part way or before it began.
	failed   bool
	progress map[string]nodeProgress
	// startedAt and finishedAt time the apply.
	startedAt  time.Time
	finishedAt time.Time
	scroll     int
	cursor     int
	// refresh plans with every resource read as it is in aws.
	refresh bool
	// earlier is the plan on screen when r planned again, which esc goes back to while that runs
	// and a failure shows again.
	earlier *engine.Plan
}

// nodeProgress is how far applying a node got, from the first event about it. action is what the
// engine does, which can differ from the plan's when the resource changed since.
type nodeProgress struct {
	action   engine.Action
	since    time.Time
	done     bool
	err      error
	duration time.Duration
}

// planDeploy plans a deploy of a stage of the project here, or with function of its code.
func (m model) planDeploy(ref cli.StageRef, function string) (model, tea.Cmd) {
	if !m.b.Local(ref) {
		return m.setStatus("only the project here can be deployed: run "+binary+" ui in its directory", styles.ToneWarning)
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
	m.loading, p.cancel = false, nil
	if msg.err != nil && p.earlier != nil {
		m.error = "could not plan again with every resource read from aws: " + msg.err.Error()
		p.plan, p.refresh, p.earlier = p.earlier, false, nil
		return m, nil
	}
	if msg.err != nil {
		m.error = "could not plan: " + msg.err.Error()
		return m.switchPage(p.from), nil
	}
	p.plan, p.earlier = msg.plan, nil
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
	p.applying, p.failed, p.progress, p.scroll = true, false, map[string]nodeProgress{}, 0
	p.startedAt, p.finishedAt = time.Now(), time.Time{}
	return m, m.apply()
}

func (m model) applyProgress(msg progressMsg) (model, tea.Cmd) {
	p := &m.state.plan
	ev := msg.event
	if p.progress != nil {
		pr, ok := p.progress[ev.ID]
		if !ok {
			pr.since = time.Now()
		}
		pr.action, pr.done, pr.err, pr.duration = ev.Action, ev.Done, ev.Err, ev.Duration
		p.progress[ev.ID] = pr
	}
	return m, waitFor(msg.ch)
}

func (m model) applied(msg appliedMsg) (model, tea.Cmd) {
	p := &m.state.plan
	p.applying, p.finishedAt = false, time.Now()
	switch {
	case msg.err != nil:
		p.done, p.failed = true, true
		m.error = "the " + m.planWhat() + " of " + p.ref.String() + " failed: " + msg.err.Error()
		if len(p.progress) > 0 {
			m.error += "\n\n" + m.planAgain()
		}
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

// planAgain says how to finish an apply that failed part way.
func (m model) planAgain() string {
	if m.state.plan.remove {
		return "what was done is in the state: fix the cause, then ctrl+d removes what is left"
	}
	return "what was done is in the state: fix the cause, then d plans what is left"
}

func (m model) planUpdate(msg tea.KeyPressMsg) (model, tea.Cmd) {
	p := &m.state.plan
	switch {
	case key.Matches(msg, m.keys.Back, m.keys.Quit):
		if p.applying {
			return m.setStatus("the "+m.planWhat()+" is running: wait for it, or ctrl+c to stop it and quit", styles.ToneWarning)
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
	case key.Matches(msg, m.keys.Refresh):
		if p.plan == nil || p.applying || p.done || p.remove || p.function != "" {
			return m, nil
		}
		p.refresh, p.earlier, p.plan, p.cursor, p.scroll = true, p.plan, nil, 0, 0
		m.loading, m.loadingMsg = true, "reading every resource of "+p.ref.String()+" as it is in aws…"
		return m.loadPlan()
	case key.Matches(msg, m.keys.View):
		if changes := m.planChanges(); p.cursor < len(changes) {
			c := changes[p.cursor]
			after := c.After
			if c.Action == engine.Keep {
				after = c.Before
			}
			return m.openDiff(c.ID, cli.Verb(c.Action)+" "+shortType(c.Type), c.Before, after)
		}
		return m, nil
	}
	head, body := m.planLines()
	height := planBodyHeight(head)
	if cursor, ok := moveCursor(msg, m.keys, p.cursor, len(body.rows), height); ok {
		p.cursor = cursor
		_, p.scroll = selectable(body, p.cursor, p.scroll, height)
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

// cancelPlan stops the plan being made: one r asked for goes back to the plan before it, and
// the first one leaves the page.
func (m model) cancelPlan() (model, tea.Cmd) {
	p := &m.state.plan
	if p.earlier == nil {
		return m.leavePlan()
	}
	if p.cancel != nil {
		p.cancel()
	}
	p.gen = m.next()
	p.plan, p.earlier, p.refresh, p.cancel = p.earlier, nil, false, nil
	m.loading = false
	return m.setStatus("stopped reading every resource from aws: this is the plan before", styles.ToneWarning)
}

// leavePlan goes back to where the plan was asked from, reading the stage again. A plan still
// being made is stopped.
func (m model) leavePlan() (model, tea.Cmd) {
	if cancel := m.state.plan.cancel; cancel != nil {
		cancel()
		m.state.plan.cancel = nil
	}
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

// planSummary counts the changes of each action, as plan does, each in the tone of its action.
func planSummary(plan *engine.Plan) []styles.Span {
	var spans []styles.Span
	for _, c := range cli.PlanCounts(plan) {
		if len(spans) > 0 {
			spans = append(spans, styles.S(", ", styles.ToneFaint))
		}
		spans = append(spans, styles.S(c.String(), actionTones[c.Action]))
	}
	if len(spans) == 0 {
		return []styles.Span{styles.S("no changes", styles.ToneBody)}
	}
	return spans
}

var actionTones = map[engine.Action]styles.Tone{
	engine.Create:  styles.ToneSuccess,
	engine.Update:  styles.ToneWarning,
	engine.Replace: styles.ToneDanger,
	engine.Delete:  styles.ToneDanger,
	engine.Keep:    styles.ToneMuted,
	engine.Adopt:   styles.ToneInfo,
}

// planStatusWidth is the room on the right of each change for its progress.
const planStatusWidth = 24

// maxErrorLines bounds the lines of a node's error under it in the plan.
const maxErrorLines = 3

// planLines are the lines of the plan: the head, which stays in place, and the body of changes,
// each of which can be chosen.
func (m model) planLines() ([]string, pickList) {
	p := m.state.plan
	width := contentWidth - 5
	if p.plan == nil {
		return nil, pickList{}
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
		summary = append(summary, styles.S(": only code, replaced in place", styles.ToneMuted))
	}
	if p.plan.Refreshed {
		summary = append(summary, styles.S(", with every resource read as it is in aws", styles.ToneMuted))
	}
	head := make([]string, 0, 5)
	head = append(head,
		styles.SectionHeader(title+" in "+p.ref.Region, width),
		" "+renderCell(summary, column{width: width - 1}, nil),
	)
	head = append(head, m.applyLines(width)...)
	head = append(head, "")
	var body pickList
	cols := []column{{width: width - planStatusWidth - 1}, {width: 1}, {width: planStatusWidth, right: true}}
	for _, c := range m.planChanges() {
		line := cell{
			styles.S(fmt.Sprintf(" %-4s", cli.Symbol(c.Action)), actionTones[c.Action]),
			styles.S(c.ID, styles.ToneText),
		}
		switch {
		case c.Resume:
			line = append(line, styles.S("  finishing what a deploy started", styles.ToneFaint))
		case c.Gone:
			line = append(line, styles.S("  deleted outside koldstart", styles.ToneWarning))
		}
		status := m.progressCell(c)
		body.add(renderCell(line, cols[0], nil)+" "+renderCell(status, cols[2], nil), litRow(cols, line, nil, status))
		if len(c.Drift) > 0 {
			body.lines = append(body.lines, renderCell(text("        ▲ changed outside koldstart: "+strings.Join(c.Drift, ", "), styles.ToneWarning), column{width: width}, nil))
		}
		if c.Action == engine.Update || c.Action == engine.Replace || c.Action == engine.Adopt {
			for _, change := range cli.FieldChanges(c) {
				body.lines = append(body.lines, renderCell(text("        "+change, styles.ToneMuted), column{width: width}, nil))
			}
		}
		if pr := p.progress[c.ID]; pr.err != nil {
			for _, l := range errorLines(c.ID, pr.err, width-8) {
				body.lines = append(body.lines, "        "+l)
			}
		}
	}
	if len(body.rows) == 0 {
		body.lines = append(body.lines, " "+styles.Faint(styles.Clean(p.ref.String())+" is as the configuration says: there is nothing to "+m.planWhat()))
	}
	return head, body
}

// errorLines are a node's error wrapped to width, without the node's id the engine puts first,
// at most maxErrorLines of them.
func errorLines(id string, err error, width int) []string {
	msg := strings.TrimPrefix(strings.Join(strings.Fields(err.Error()), " "), id+": ")
	return wrapped(msg, styles.ToneDanger, max(10, width), maxErrorLines)
}

// applyLines say how the apply goes: how many nodes are done and for how long, and once it ended,
// what it did and whether it failed, in the words deploy and remove end with.
func (m model) applyLines(width int) []string {
	p := m.state.plan
	if p.progress == nil {
		return nil
	}
	finished, failed, stopped := map[engine.Action]int{}, 0, 0
	for _, pr := range p.progress {
		switch {
		case pr.err != nil:
			failed++
		case pr.done:
			finished[pr.action]++
		default:
			stopped++
		}
	}
	var line cell
	switch {
	case p.applying:
		done := 0
		for _, n := range finished {
			done += n
		}
		counts := fmt.Sprintf("%d of %d done", done, len(m.planChanges()))
		if failed > 0 {
			counts += fmt.Sprintf(", %d failed", failed)
		}
		line = cell{styles.S(m.spinner.View()+" ", styles.ToneAccent), styles.S(applyingWord(p.remove)+" · "+counts, styles.ToneBody),
			styles.S(" · "+cli.Elapsed(time.Since(p.startedAt)), styles.ToneFaint)}
	case p.failed:
		summary := cli.ApplySummary(finished, failed, stopped)
		line = cell{styles.S("✗ ", styles.ToneDanger), styles.S("failed after "+cli.Duration(p.finishedAt.Sub(p.startedAt))+" · "+summary, styles.ToneDanger)}
		return []string{" " + renderCell(line, column{width: width - 1}, nil), " " + renderCell(text(m.planAgain(), styles.ToneFaint), column{width: width - 1}, nil)}
	default:
		what := "deployed"
		if p.remove {
			what = "removed"
		}
		line = cell{styles.S("✓ ", styles.ToneSuccess), styles.S(what+" in "+cli.Duration(p.finishedAt.Sub(p.startedAt)), styles.ToneSuccess),
			styles.S(" · "+cli.ApplySummary(finished, failed, stopped), styles.ToneFaint)}
	}
	return []string{" " + renderCell(line, column{width: width - 1}, nil)}
}

// applyingWord names an apply in flight.
func applyingWord(remove bool) string {
	if remove {
		return "removing"
	}
	return "deploying"
}

// progressCell says how far a node got while applying.
func (m model) progressCell(c engine.Change) cell {
	p := m.state.plan
	if p.progress == nil {
		return nil
	}
	pr, ok := p.progress[c.ID]
	switch {
	case !ok && p.applying:
		return text("waiting", styles.ToneFaint)
	case !ok && p.failed:
		return text("not applied", styles.ToneFaint)
	case !ok:
		return text("unchanged", styles.ToneFaint)
	case pr.err != nil:
		return cell{styles.S("✗ failed after ", styles.ToneDanger), styles.S(cli.Duration(pr.duration), styles.ToneFaint)}
	case pr.done:
		return cell{styles.S("✓ "+cli.PastTense(pr.action)+" in ", styles.ToneSuccess), styles.S(cli.Duration(pr.duration), styles.ToneFaint)}
	case p.applying:
		return cell{styles.S(m.spinner.View()+" ", styles.ToneAccent), styles.S(cli.Participle(pr.action)+" ", styles.ToneMuted), styles.S(cli.Elapsed(time.Since(pr.since)), styles.ToneFaint)}
	}
	return text("✗ stopped", styles.ToneDanger)
}

// planBodyHeight is the room the changes get below the head.
func planBodyHeight(head []string) int {
	return max(1, contentHeight-len(head))
}

func (m model) planView() string {
	head, body := m.planLines()
	view, _ := selectable(body, m.state.plan.cursor, m.state.plan.scroll, planBodyHeight(head))
	if len(head) == 0 {
		return view
	}
	return "  " + strings.Join(head, "\n  ") + "\n" + view
}

// planHints are the footer's hints on the plan page.
func (m model) planHints() [][2]string {
	k := m.keys
	p := m.state.plan
	switch {
	case m.loading:
		return [][2]string{hint(k.Help), hint(k.Back, "cancel")}
	case p.applying:
		return [][2]string{hint(k.View, "diff"), chooseHint, hint(k.ForceQuit, "stop and quit")}
	case p.plan == nil:
		return [][2]string{hint(k.Help), hint(k.Quit)}
	case p.done:
		return [][2]string{hint(k.View, "diff"), chooseHint, hint(k.Help), hint(k.Quit)}
	}
	var h [][2]string
	if p.plan.HasChanges() {
		h = append(h, hint(k.Apply, m.planWhat()), hint(k.View, "diff"))
	}
	if !p.remove && p.function == "" && !p.refresh {
		h = append(h, hint(k.Refresh, "read aws"))
	}
	if p.plan.HasChanges() {
		h = append(h, chooseHint)
	}
	return append(h, hint(k.Help), hint(k.Quit))
}
