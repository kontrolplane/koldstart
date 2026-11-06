package tui

import (
	"context"
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
	// cursor is the row chosen among planRows: the heading of a group, or a change.
	cursor int
	// refresh plans with every resource read as it is in aws.
	refresh bool
	// earlier is the plan on screen when r planned again, which esc goes back to while that runs
	// and a failure shows again.
	earlier *engine.Plan
	// madeAt is when the plan on screen was made.
	madeAt time.Time
	// byAction groups the changes by what they do, rather than by the kind of node.
	byAction bool
	// folded are the groups folded, by planGroup.key.
	folded map[string]bool
	// cache keeps what the page works out of the plan on screen, so a frame does not again.
	cache *planCache
}

// planCache holds the facts of the plan last worked out.
type planCache struct {
	facts *planFacts
}

// facts are what the page works out of the plan on screen, kept for the frames after.
func (p planState) facts() *planFacts {
	if p.cache != nil && p.cache.facts != nil && p.cache.facts.plan == p.plan {
		return p.cache.facts
	}
	f := newPlanFacts(p.plan)
	if p.cache != nil {
		p.cache.facts = f
	}
	return f
}

// show puts plan on screen, made now, with the cursor on its first change.
func (p *planState) show(plan *engine.Plan) {
	p.plan, p.madeAt, p.scroll = plan, time.Now(), 0
	p.cursor = firstChangeRow(*p)
}

// nodeProgress is how far applying a node got, from the first event about it. action is what the
// engine does, which can differ from the plan's when the resource changed since. started is
// whether the engine said it began, which a node whose inputs could not be resolved never does.
type nodeProgress struct {
	action   engine.Action
	since    time.Time
	started  bool
	done     bool
	err      error
	duration time.Duration
}

// planDeploy plans a deploy of a stage of the project here, or with function of its code.
func (m model) planDeploy(ref cli.StageRef, function string) (model, tea.Cmd) {
	if !m.b.Local(ref) {
		return m.setStatus("only the project here can be deployed: run "+binary+" ui in its directory", styles.ToneWarning)
	}
	m.state.plan = planState{ref: ref, function: function, from: m.page, byAction: m.state.plan.byAction, cache: &planCache{}}
	m = m.switchPage(planPage)
	m.loading, m.loadingMsg = true, "building and planning "+ref.String()+"…"
	if function != "" {
		m.loadingMsg = "building " + function + " and planning its code…"
	}
	return m.loadPlan()
}

// planRemove plans removing a stage, then asks to type its name over the plan.
func (m model) planRemove(ref cli.StageRef) (model, tea.Cmd) {
	m.state.plan = planState{ref: ref, remove: true, from: m.page, byAction: m.state.plan.byAction, cache: &planCache{}}
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
		p.refresh = false
		p.show(p.earlier)
		p.earlier = nil
		return m, nil
	}
	if msg.err != nil {
		m.error = "could not plan: " + msg.err.Error()
		return m.switchPage(p.from), nil
	}
	p.show(msg.plan)
	p.earlier = nil
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
		pr.started = pr.started || !ev.Done
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
		why := msg.err.Error()
		if len(p.progress) > 0 {
			why = withoutCLIHint(why) + "\n\n" + m.planAgain()
		}
		m.error = "the " + m.planWhat() + " of " + p.ref.String() + " failed: " + why
		return m, nil
	case !msg.applied:
		p.progress = nil
		p.show(msg.plan)
		text := "the configuration or the state changed since: this is the plan now, ctrl+s applies it"
		if p.remove {
			m, cmd := m.askRemove()
			m, status := m.setStatus("the state changed since: this is the plan now", styles.ToneWarning)
			return m, tea.Batch(cmd, status)
		}
		return m.setStatus(text, styles.ToneWarning)
	case msg.gone:
		m.state.stage = newStageState()
		m = m.switchPage(overviewPage).selectStage(p.ref)
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

// cliHint starts the line the commands add to a failed apply, telling which command finishes it,
// which planAgain says here in the keys of the interface.
const cliHint = "\nwhat was done is in the state: "

// withoutCLIHint drops the command's hint from the end of a failed apply's error, so the dialog
// does not say twice that what was done is in the state, once naming a command.
func withoutCLIHint(err string) string {
	if i := strings.LastIndex(err, cliHint); i >= 0 && !strings.Contains(err[i+1:], "\n") {
		return err[:i]
	}
	return err
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
	case key.Matches(msg, m.keys.GroupBy):
		return m.regroupPlan(), nil
	case key.Matches(msg, m.keys.Fold):
		return m.togglePlanFold(), nil
	case key.Matches(msg, m.keys.View):
		row, ok := m.planRow()
		switch {
		case !ok:
		case row.change < 0:
			return m.togglePlanFold(), nil
		case opensInputs(p.facts().changes[row.change]):
			return m.openInputs(p.facts().changes[row.change])
		default:
			c := p.facts().changes[row.change]
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

// regroupPlan groups the changes by action when they are grouped by kind, and by kind when by
// action, keeping the cursor on the change it was on, or on the first change when it was on a
// heading or that change's group is folded.
func (m model) regroupPlan() model {
	p := &m.state.plan
	row, ok := m.planRow()
	p.byAction = !p.byAction
	_, _, rows := planRows(*p)
	p.cursor = firstChangeRow(*p)
	for i, r := range rows {
		if ok && row.change >= 0 && r.change == row.change {
			p.cursor = i
			break
		}
	}
	return m
}

// togglePlanFold folds the group under the cursor, or opens it, with the cursor on its heading.
func (m model) togglePlanFold() model {
	p := &m.state.plan
	row, ok := m.planRow()
	if !ok {
		return m
	}
	_, groups, _ := planRows(*p)
	k := groups[row.group].key
	p.folded = cloned(p.folded)
	p.folded[k] = !p.folded[k]
	_, _, rows := planRows(*p)
	for i, r := range rows {
		if r.group == row.group && r.change < 0 {
			p.cursor = i
			break
		}
	}
	return m
}

// planRow is the row under the cursor, and whether there is one.
func (m model) planRow() (planRow, bool) {
	_, _, rows := planRows(m.state.plan)
	if c := m.state.plan.cursor; c >= 0 && c < len(rows) {
		return rows[c], true
	}
	return planRow{}, false
}

// planChanges are the changes of the plan that do something, in the plan's order.
func (m model) planChanges() []engine.Change {
	if m.state.plan.plan == nil {
		return nil
	}
	return m.state.plan.facts().changes
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
	p.refresh, p.cancel = false, nil
	p.show(p.earlier)
	p.earlier = nil
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

// planTitle names the plan: a deploy of the stage, of the code of one function, or a remove.
func (p planState) planTitle() string {
	switch {
	case p.remove:
		return "remove of " + p.ref.String() + " in " + p.ref.Region
	case p.function != "":
		return "deploy of the code of " + p.function + " to " + p.ref.String() + " in " + p.ref.Region
	}
	return "deploy of " + p.ref.String() + " in " + p.ref.Region
}

// planHead is the lines above the changes, which stay in place: what the plan is of, its counts,
// with what it compared and when it was made, and how the apply goes.
func (m model) planHead(width int) []string {
	p := m.state.plan
	summary := planSummary(p.plan)
	if p.plan.OnlyCode() {
		summary = append(summary, styles.S(": only code, replaced in place", styles.ToneMuted))
	}
	if p.plan.Refreshed {
		summary = append(summary, styles.S(", with every resource read as it is in aws", styles.ToneMuted))
	}
	title := styles.SectionHeader(p.planTitle(), width)
	if !p.madeAt.IsZero() {
		made := "planned at " + p.madeAt.Local().Format(time.TimeOnly)
		title = styles.SectionHeader(p.planTitle(), width-len(made)-1) + " " + styles.Faint(made)
	}
	head := make([]string, 0, 5)
	head = append(head, title, " "+renderCell(summary, column{width: width - 1}, nil))
	head = append(head, m.applyLines(width)...)
	return append(head, "")
}

// noChanges are the lines of a plan with nothing to do: what it compared, as the commands say,
// and with what else it could compare.
func (p planState) noChanges(width int) []string {
	say := cli.NoChanges(p.plan)
	if p.function != "" {
		say = cli.UpToDate(p.function, p.ref.App, p.ref.Stage)
	}
	lines := []string{" " + renderCell(text(say, styles.ToneMuted), column{width: width - 1}, nil)}
	if !p.remove && p.function == "" && !p.plan.Refreshed {
		lines = append(lines, " "+renderCell(text("r reads every resource as it is in aws, to find what was changed outside koldstart", styles.ToneFaint), column{width: width - 1}, nil))
	}
	return lines
}

// planStatusWidth is the room on the right of each change for its progress.
const planStatusWidth = 24

// maxErrorLines bounds the lines of a node's error under it in the plan.
const maxErrorLines = 3

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
		counts := formatInt(done) + " of " + formatInt(len(m.planChanges())) + " done"
		if failed > 0 {
			counts += ", " + formatInt(failed) + " failed"
		}
		line = cell{styles.S(m.spinner.View()+" ", styles.ToneAccent), styles.S(applyingWord(p.remove)+" · "+counts, styles.ToneBody),
			styles.S(" · "+cli.Elapsed(time.Since(p.startedAt)), styles.ToneFaint)}
	case p.failed:
		summary := cli.ApplySummary(finished, failed, stopped, max(len(m.planChanges())-len(p.progress), 0))
		line = cell{styles.S("✗ ", styles.ToneDanger), styles.S("failed after "+cli.Duration(p.finishedAt.Sub(p.startedAt))+" · "+summary, styles.ToneDanger)}
		return []string{" " + renderCell(line, column{width: width - 1}, nil), " " + renderCell(text(m.planAgain(), styles.ToneFaint), column{width: width - 1}, nil)}
	default:
		what := "deployed"
		if p.remove {
			what = "removed"
		}
		line = cell{styles.S("✓ ", styles.ToneSuccess), styles.S(what+" in "+cli.Duration(p.finishedAt.Sub(p.startedAt)), styles.ToneSuccess),
			styles.S(" · "+cli.ApplySummary(finished, failed, stopped, 0), styles.ToneFaint)}
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
	case pr.err != nil && !pr.started:
		return text("✗ failed before starting", styles.ToneDanger)
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

// planPosition is the change under the cursor among the changes, as 12 of 240, or on a heading
// how many changes the plan has.
func (m model) planPosition() string {
	p := m.state.plan
	if p.plan == nil {
		return ""
	}
	f, groups, _ := planRows(p)
	row, ok := m.planRow()
	switch {
	case !ok:
		return ""
	case row.change < 0:
		return plural(len(f.changes), "change")
	}
	at := 0
	for g := range row.group {
		at += len(groups[g].members)
	}
	for i, c := range groups[row.group].members {
		if c == row.change {
			return ofCount(at+i, len(f.changes))
		}
	}
	return ""
}

// planHints are the footer's hints on the plan page.
func (m model) planHints() [][2]string {
	k := m.keys
	p := m.state.plan
	other := "by action"
	if p.byAction {
		other = "by kind"
	}
	switch {
	case m.loading:
		return [][2]string{hint(k.Help), hint(k.Back, "cancel")}
	case p.applying:
		return [][2]string{hint(k.View, "diff"), chooseHint, hint(k.Fold), hint(k.ForceQuit, "stop and quit")}
	case p.plan == nil:
		return [][2]string{hint(k.Help), hint(k.Quit)}
	case p.done:
		return [][2]string{hint(k.View, "diff"), chooseHint, hint(k.Fold), hint(k.GroupBy, other), hint(k.Help), hint(k.Quit)}
	}
	var h [][2]string
	if p.plan.HasChanges() {
		h = append(h, hint(k.Apply, m.planWhat()), hint(k.View, "diff"))
	}
	if !p.remove && p.function == "" && !p.refresh {
		h = append(h, hint(k.Refresh, "read aws"))
	}
	if p.plan.HasChanges() {
		h = append(h, chooseHint, hint(k.Fold), hint(k.GroupBy, other))
	}
	return append(h, hint(k.Help), hint(k.Quit))
}
