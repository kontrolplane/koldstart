package tui

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
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
	// how says where the lines go: after the ones on screen, before them, or in their place.
	how  logsHow
	from time.Time
	err  error
}

// logsHow is where a read of the logs goes.
type logsHow int

const (
	logsReplace logsHow = iota
	logsAppend
	logsPrepend
)

// tailMsg is lines a live tail of the logs sent, or its end with why. More follow on the channel
// until it ends.
type tailMsg struct {
	gen   int
	lines []cli.LogEvent
	ended bool
	err   error
	ch    <-chan tea.Msg
}

type functionLiveMsg struct {
	gen  int
	live cli.FunctionState
	err  error
}

type metricsLoadedMsg struct {
	gen     int
	metrics map[string]cli.Metrics
	err     error
}

// queuesLoadedMsg is the depth of each queue read, and why each of the others could not be.
type queuesLoadedMsg struct {
	gen    int
	depths map[string]cli.QueueDepth
	errs   map[string]error
}

type liveLoadedMsg struct {
	gen   int
	props map[string]any
	err   error
}

type invokedMsg struct {
	gen   int
	event string
	local bool
	inv   cli.Invocation
	err   error
	// body is the payload's lines, indented and cleaned off the interface's goroutine, as a
	// payload can be megabytes.
	body []string
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

// loadLogs reads the function's logs from since until until, a zero until meaning now, for how
// they go with the ones on screen.
func (m model) loadLogs(since, until time.Time, how logsHow) (model, tea.Cmd) {
	gen := m.next()
	f := &m.state.function
	if how == logsPrepend {
		f.logs.olderGen, f.logs.loadingOlder = gen, true
	} else {
		f.logs.gen = gen
	}
	ref, group := m.state.stage.ref, f.logGroup
	ctx, cancel := m.withTimeout(loadTimeout)
	return m, func() tea.Msg {
		defer cancel()
		events, err := m.b.LogsBetween(ctx, ref, group, since, until, "")
		return logsLoadedMsg{gen: gen, events: events, how: how, from: since, err: err}
	}
}

// startTail follows the function's logs with a live tail, until the function is left or
// following stops, its lines sent on a channel the returned command reads from. Once it is
// stopped nothing may read the channel, so a send gives up when its context ends rather than
// leave the goroutine waiting.
func (m model) startTail() (model, tea.Cmd) {
	f := &m.state.function
	if f.logGroupARN == "" || f.logs.tailing || f.logs.noTail {
		return m, nil
	}
	gen := m.next()
	ctx, cancel := context.WithCancel(m.ctx)
	ch := make(chan tea.Msg, 64)
	f.logs.tailGen, f.logs.tailing, f.logs.stopTail = gen, true, cancel
	ref, arn := m.state.stage.ref, f.logGroupARN
	send := func(msg tea.Msg) {
		select {
		case ch <- msg:
		case <-ctx.Done():
		}
	}
	go func() {
		defer close(ch)
		defer cancel()
		var err error
		defer func() {
			if r := recover(); r != nil {
				err = recovered("the live tail of the logs", r)
			}
			send(tailMsg{gen: gen, ended: true, err: err, ch: ch})
		}()
		err = m.b.Tail(ctx, ref, arn, func(lines []cli.LogEvent) { send(tailMsg{gen: gen, lines: lines, ch: ch}) })
	}()
	return m, waitFor(ch)
}

// panicError is a panic in a goroutine of the interface, turned into the error of what it was
// doing.
type panicError struct {
	value any
}

func (e *panicError) Error() string {
	return fmt.Sprintf("a bug in koldstart stopped it (%v): run %s ui --debug and report what debug.log says", e.value, binary)
}

// recovered turns a panic in a goroutine the interface started into an error, logging its stack.
// Bubble Tea restores the terminal after a panic only in its own goroutines: one in a deploy or a
// tail would end the program with the terminal still raw and the stage's lock held.
func recovered(what string, value any) error {
	slog.Error("panic", "in", what, "value", fmt.Sprint(value), "stack", string(debug.Stack()))
	return &panicError{value: value}
}

// stopTail ends the live tail of the logs, if one runs.
func (m model) stopTail() model {
	l := &m.state.function.logs
	if l.stopTail != nil {
		l.stopTail()
	}
	l.tailing, l.stopTail = false, nil
	return m
}

func (m model) loadFunctionLive() (model, tea.Cmd) {
	gen := m.next()
	f := &m.state.function
	f.liveGen = gen
	ref, name := m.state.stage.ref, f.lambdaName
	ctx, cancel := m.withTimeout(loadTimeout)
	return m, func() tea.Msg {
		defer cancel()
		live, err := m.b.Function(ctx, ref, name)
		return functionLiveMsg{gen: gen, live: live, err: err}
	}
}

// loadMetrics reads the metrics of the stage's functions, by their names in lambda, unless a read
// of them is still out.
func (m model) loadMetrics() (model, tea.Cmd) {
	s := &m.state.stage
	names := s.lambdaNames()
	if len(names) == 0 || s.metricsGen != s.metricsAnswered {
		return m, nil
	}
	gen := m.next()
	s.metricsGen = gen
	ref := s.ref
	ctx, cancel := m.withTimeout(loadTimeout)
	return m, func() tea.Msg {
		defer cancel()
		metrics, err := m.b.Metrics(ctx, ref, names)
		return metricsLoadedMsg{gen: gen, metrics: metrics, err: err}
	}
}

// loadQueues reads how many messages each queue of the stage holds, by its id in the state,
// unless a read of them is still out.
func (m model) loadQueues() (model, tea.Cmd) {
	s := &m.state.stage
	queues := s.queueURLs()
	if len(queues) == 0 || s.queuesGen != s.queuesAnswered {
		return m, nil
	}
	gen := m.next()
	s.queuesGen = gen
	ref := s.ref
	ctx, cancel := m.withTimeout(loadTimeout)
	return m, func() tea.Msg {
		defer cancel()
		depths, errs := map[string]cli.QueueDepth{}, map[string]error{}
		for id, url := range queues {
			d, err := m.b.Queue(ctx, ref, url)
			if err != nil {
				errs[id] = err
				continue
			}
			depths[id] = d
		}
		return queuesLoadedMsg{gen: gen, depths: depths, errs: errs}
	}
}

// loadLive reads the resource on screen as cloud control sees it now.
func (m model) loadLive() (model, tea.Cmd) {
	gen := m.next()
	r := &m.state.resource
	r.liveGen = gen
	ref, typ, id := m.state.stage.ref, r.res.res.Type, r.res.res.PhysicalID
	ctx, cancel := m.withTimeout(loadTimeout)
	return m, func() tea.Msg {
		defer cancel()
		props, err := m.b.Live(ctx, ref, typ, id)
		return liveLoadedMsg{gen: gen, props: props, err: err}
	}
}

func (m model) invoke(event string, payload []byte, local bool) (model, tea.Cmd) {
	gen := m.next()
	f := &m.state.function
	f.invokeGen, f.invoking = gen, true
	r := f.results[event]
	r.running, r.local = true, local
	f.results[event] = r
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
		return invokedMsg{gen: gen, event: event, local: local, inv: inv, err: err, body: payloadLines(inv.Payload)}
	}
}

// loadPlan plans a deploy, of the stage or with function of its code, or a remove.
func (m model) loadPlan() (model, tea.Cmd) {
	gen := m.next()
	p := &m.state.plan
	p.gen = gen
	ref, function, remove, refresh := p.ref, p.function, p.remove, p.refresh
	ctx, cancel := m.withTimeout(planTimeout)
	p.cancel = cancel
	return m, func() tea.Msg {
		defer cancel()
		var plan *engine.Plan
		var err error
		if remove {
			plan, err = m.b.PlanRemove(ctx, ref)
		} else {
			plan, err = m.b.Plan(ctx, ref, function, refresh)
		}
		return planLoadedMsg{gen: gen, plan: plan, err: err}
	}
}

// planTimeout bounds building and planning, which bundles every function first.
const planTimeout = 10 * time.Minute

// apply applies the plan on screen in the background, sending its progress on a channel the
// returned command reads from. Quitting stops it and waits for it: once the interface is gone
// nothing reads the channel, so a send gives up when the context ends rather than hold the apply,
// and its lock, open.
func (m model) apply() tea.Cmd {
	p := m.state.plan
	ch := make(chan tea.Msg, 64)
	name := "the deploy of " + p.ref.String()
	if p.remove {
		name = "the remove of " + p.ref.String()
	}
	ctx := m.ctx
	send := func(msg tea.Msg) {
		select {
		case ch <- msg:
		case <-ctx.Done():
		}
	}
	m.ops.start(name)
	go func() {
		defer m.ops.done(name)
		defer close(ch)
		defer func() {
			if r := recover(); r != nil {
				send(appliedMsg{err: recovered(name, r)})
			}
		}()
		onEvent := func(ev engine.Event) { send(progressMsg{event: ev, ch: ch}) }
		var msg appliedMsg
		if p.remove {
			msg.plan, msg.applied, msg.gone, msg.err = m.b.Remove(ctx, p.ref, p.plan, onEvent)
		} else {
			msg.plan, msg.applied, msg.err = m.b.Deploy(ctx, p.ref, p.function, p.plan, onEvent)
		}
		send(msg)
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
