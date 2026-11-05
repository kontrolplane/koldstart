package tui

import (
	"errors"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/kontrolplane/koldstart/internal/cli"
	"github.com/kontrolplane/koldstart/internal/engine"
	"github.com/kontrolplane/koldstart/internal/state"
	"github.com/kontrolplane/koldstart/internal/tui/styles"
)

type stagesLoadedMsg struct {
	gen    int
	stages []cli.StageSummary
	err    error
}

type stageLoadedMsg struct {
	gen   int
	state *state.State
	lock  *state.LockInfo
	err   error
}

type historyLoadedMsg struct {
	gen      int
	versions []state.ObjectVersion
	err      error
}

type versionLoadedMsg struct {
	gen               int
	current, previous *state.State
	err               error
}

type logsLoadedMsg struct {
	gen    int
	events []cli.LogEvent
	// appended says the events follow the ones on screen, rather than replace them.
	appended bool
	err      error
}

type invokedMsg struct {
	gen   int
	event string
	local bool
	inv   cli.Invocation
	err   error
}

type planLoadedMsg struct {
	gen  int
	plan *engine.Plan
	err  error
}

// progressMsg is a node starting or finishing while a plan is applied. More follow on the
// channel until the appliedMsg.
type progressMsg struct {
	event engine.Event
	ch    <-chan tea.Msg
}

type appliedMsg struct {
	plan    *engine.Plan
	applied bool
	gone    bool
	err     error
}

type unlockedMsg struct {
	ref cli.StageRef
	err error
}

type eventEditedMsg struct {
	err error
}

type refreshTickMsg struct {
	gen int
}

type statusClearMsg struct {
	gen int
}

func (m model) loadStages() (model, tea.Cmd) {
	gen := m.next()
	m.state.overview.gen = gen
	return m, m.stagesCmd(gen)
}

func (m model) stagesCmd(gen int) tea.Cmd {
	ctx, cancel := m.withTimeout(loadTimeout)
	return func() tea.Msg {
		defer cancel()
		stages, err := m.b.Stages(ctx)
		return stagesLoadedMsg{gen: gen, stages: stages, err: err}
	}
}

func (m model) loadStage() (model, tea.Cmd) {
	gen := m.next()
	s := &m.state.stage
	s.gen = gen
	ref := s.ref
	ctx, cancel := m.withTimeout(loadTimeout)
	return m, func() tea.Msg {
		defer cancel()
		st, lock, err := m.b.Stage(ctx, ref)
		return stageLoadedMsg{gen: gen, state: st, lock: lock, err: err}
	}
}

func (m model) loadHistory() (model, tea.Cmd) {
	gen := m.next()
	m.state.stage.historyGen = gen
	ref := m.state.stage.ref
	ctx, cancel := m.withTimeout(loadTimeout)
	return m, func() tea.Msg {
		defer cancel()
		versions, err := m.b.History(ctx, ref)
		return historyLoadedMsg{gen: gen, versions: versions, err: err}
	}
}

// loadVersion reads a version of the state and the one before it, to tell what it changed.
func (m model) loadVersion(id, previous string) (model, tea.Cmd) {
	gen := m.next()
	m.state.version.gen = gen
	ref := m.state.stage.ref
	ctx, cancel := m.withTimeout(loadTimeout)
	return m, func() tea.Msg {
		defer cancel()
		current, err := m.b.Version(ctx, ref, id)
		if err != nil || previous == "" {
			return versionLoadedMsg{gen: gen, current: current, err: err}
		}
		before, err := m.b.Version(ctx, ref, previous)
		return versionLoadedMsg{gen: gen, current: current, previous: before, err: err}
	}
}

// loadLogs reads the function's logs since a time, appended to the ones on screen or in their
// place.
func (m model) loadLogs(since time.Time, appended bool) (model, tea.Cmd) {
	gen := m.next()
	f := &m.state.function
	f.logsGen = gen
	ref, group := m.state.stage.ref, f.logGroup
	ctx, cancel := m.withTimeout(loadTimeout)
	return m, func() tea.Msg {
		defer cancel()
		events, err := m.b.Logs(ctx, ref, group, since)
		return logsLoadedMsg{gen: gen, events: events, appended: appended, err: err}
	}
}

func (m model) invoke(event string, payload []byte, local bool) (model, tea.Cmd) {
	gen := m.next()
	f := &m.state.function
	f.invokeGen, f.invoking = gen, true
	ref, lambdaName, name := m.state.stage.ref, f.lambdaName, f.name
	ctx, cancel := m.withTimeout(invokeTimeout)
	return m, func() tea.Msg {
		defer cancel()
		var inv cli.Invocation
		var err error
		if local {
			inv, err = m.b.InvokeLocal(ctx, ref, name, payload)
		} else {
			inv, err = m.b.Invoke(ctx, ref, lambdaName, payload)
		}
		return invokedMsg{gen: gen, event: event, local: local, inv: inv, err: err}
	}
}

// loadPlan plans a deploy, of the stage or with function of its code, or a remove.
func (m model) loadPlan() (model, tea.Cmd) {
	gen := m.next()
	p := &m.state.plan
	p.gen = gen
	ref, function, remove := p.ref, p.function, p.remove
	ctx, cancel := m.withTimeout(10 * time.Minute)
	return m, func() tea.Msg {
		defer cancel()
		var plan *engine.Plan
		var err error
		if remove {
			plan, err = m.b.PlanRemove(ctx, ref)
		} else {
			plan, err = m.b.Plan(ctx, ref, function)
		}
		return planLoadedMsg{gen: gen, plan: plan, err: err}
	}
}

// apply applies the plan on screen in the background, sending its progress on a channel the
// returned command reads from. Quitting stops it and waits for it.
func (m model) apply() tea.Cmd {
	p := m.state.plan
	ch := make(chan tea.Msg, 64)
	name := "the deploy of " + p.ref.String()
	if p.remove {
		name = "the remove of " + p.ref.String()
	}
	m.ops.start(name)
	go func() {
		defer m.ops.done(name)
		defer close(ch)
		onEvent := func(ev engine.Event) { ch <- progressMsg{event: ev, ch: ch} }
		var msg appliedMsg
		if p.remove {
			msg.plan, msg.applied, msg.gone, msg.err = m.b.Remove(m.ctx, p.ref, p.plan, onEvent)
		} else {
			msg.plan, msg.applied, msg.err = m.b.Deploy(m.ctx, p.ref, p.function, p.plan, onEvent)
		}
		ch <- msg
	}()
	return waitFor(ch)
}

// waitFor reads the next message of an apply in flight.
func waitFor(ch <-chan tea.Msg) tea.Cmd {
	return func() tea.Msg {
		msg, ok := <-ch
		if !ok {
			return nil
		}
		return msg
	}
}

func (m model) unlock(ref cli.StageRef) tea.Cmd {
	ctx, cancel := m.withTimeout(loadTimeout)
	return func() tea.Msg {
		defer cancel()
		err := m.b.Unlock(ctx, ref)
		if errors.Is(err, state.ErrNotLocked) {
			err = nil
		}
		return unlockedMsg{ref: ref, err: err}
	}
}

func (m model) scheduleRefresh() (model, tea.Cmd) {
	m.refreshGen++
	return m, refreshTick(m.refreshGen)
}

func refreshTick(gen int) tea.Cmd {
	return tea.Tick(refreshEvery, func(time.Time) tea.Msg { return refreshTickMsg{gen: gen} })
}

// setStatus shows text in the footer for a few seconds, in a tone.
func (m model) setStatus(text string, tone styles.Tone) (model, tea.Cmd) {
	m.statusGen++
	gen := m.statusGen
	m.status, m.statusTone = text, tone
	return m, tea.Tick(4*time.Second, func(time.Time) tea.Msg { return statusClearMsg{gen: gen} })
}
