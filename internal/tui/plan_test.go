package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/kontrolplane/koldstart/internal/cli"
	"github.com/kontrolplane/koldstart/internal/engine"
)

// planned is the interface on the plan of a deploy of orders/dev, with changes of every action.
func planned(t *testing.T) model {
	t.Helper()
	f := newFake(t)
	f.plan.Changes = append(f.plan.Changes,
		engine.Change{ID: "function/worker", Action: engine.Replace, Fields: []string{"name"}, Before: map[string]any{"name": "a"}, After: map[string]any{"name": "b"}},
		engine.Change{ID: "queue/old", Action: engine.Delete},
		engine.Change{ID: "resource/bucket", Action: engine.Keep, Before: map[string]any{"name": "bucket"}},
	)
	m := pressKeys(t, started(t, f), "d")
	if m.page != planPage || m.state.plan.plan == nil {
		t.Fatalf("after d: page %v, plan %v", m.page, m.state.plan.plan)
	}
	return m
}

// applying is m with its plan applying, the events given already in, without a goroutine.
func applying(t *testing.T, m model, events ...engine.Event) model {
	t.Helper()
	p := &m.state.plan
	p.applying, p.progress, p.startedAt = true, map[string]nodeProgress{}, time.Now()
	for _, ev := range events {
		m, _ = update(t, m, progressMsg{event: ev})
	}
	return m
}

// lineOf is the line of the plan on screen that names id.
func lineOf(m model, id string) string {
	for line := range strings.SplitSeq(screen(m), "\n") {
		if strings.Contains(line, " "+id+" ") {
			return line
		}
	}
	return ""
}

// TestPlanSummary checks the summary counts the changes of each action in the order plan writes
// them, and says when there are none.
func TestPlanSummary(t *testing.T) {
	tests := []struct {
		name    string
		actions []engine.Action
		want    string
	}{
		{name: "none", actions: []engine.Action{engine.NoOp}, want: "no changes"},
		{name: "one of each", actions: []engine.Action{engine.Delete, engine.Create, engine.Replace, engine.Update}, want: "1 to create, 1 to update, 1 to replace, 1 to delete"},
		{name: "kept and adopted", actions: []engine.Action{engine.Keep, engine.Keep, engine.Adopt}, want: "2 to leave in place, 1 to take over again"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan := &engine.Plan{}
			for i, a := range tt.actions {
				plan.Changes = append(plan.Changes, engine.Change{ID: fmt.Sprint(i), Action: a})
			}
			var b strings.Builder
			for _, s := range planSummary(plan) {
				b.WriteString(s.Text)
			}
			if got := b.String(); got != tt.want {
				t.Errorf("planSummary = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestApplyProgress checks each change says how far it got: waiting, in flight with what it is
// doing, done with how long it took, failed, and once the apply ended, stopped or not started, as
// the head counts them.
func TestApplyProgress(t *testing.T) {
	tests := []struct {
		name   string
		events []engine.Event
		ended  bool
		end    error
		id     string
		want   string
	}{
		{name: "waiting", id: "queue/old", want: "waiting"},
		{name: "in flight", events: []engine.Event{{ID: "function/api", Action: engine.Update}}, id: "function/api", want: "updating 0s"},
		{name: "done", events: []engine.Event{{ID: "resource/table", Action: engine.Create, Done: true, Duration: 2 * time.Second}}, id: "resource/table", want: "✓ created in 2s"},
		{name: "done as the engine did it", events: []engine.Event{{ID: "function/api", Action: engine.Replace, Done: true, Duration: time.Second}}, id: "function/api", want: "✓ replaced in 1s"},
		{name: "failed", events: []engine.Event{{ID: "function/worker", Action: engine.Replace}, {ID: "function/worker", Action: engine.Replace, Done: true, Err: errAWS, Duration: time.Second}}, id: "function/worker", want: "✗ failed after 1s"},
		{name: "failed before starting", events: []engine.Event{{ID: "function/worker", Action: engine.Replace, Done: true, Err: errAWS}}, id: "function/worker", want: "✗ failed before starting"},
		{name: "stopped by a failure", events: []engine.Event{{ID: "function/api", Action: engine.Update}}, ended: true, end: errAWS, id: "function/api", want: "✗ stopped"},
		{name: "not started after a failure", ended: true, end: errAWS, id: "queue/old", want: "not started"},
		{name: "nothing to do after all", ended: true, id: "queue/old", want: "unchanged"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := applying(t, planned(t), tt.events...)
			if tt.ended {
				m, _ = update(t, m, appliedMsg{plan: m.state.plan.plan, applied: true, err: tt.end})
				m.error = ""
			}
			if line := lineOf(m, tt.id); !strings.HasSuffix(strings.TrimRight(line, " │"), tt.want) {
				t.Errorf("%s: %q, want it to end in %q", tt.id, line, tt.want)
			}
		})
	}
}

// TestApplyEnds checks the head says what an apply did once it ended, in the words deploy and
// remove end with: what was created and replaced, what failed and what was stopped.
func TestApplyEnds(t *testing.T) {
	created := engine.Event{ID: "resource/table", Action: engine.Create, Done: true, Duration: time.Second}
	tests := []struct {
		name   string
		events []engine.Event
		end    error
		want   string
	}{
		{name: "deployed", events: []engine.Event{created, {ID: "function/worker", Action: engine.Replace, Done: true}}, want: "· 1 created, 1 replaced"},
		{name: "failed", events: []engine.Event{created, {ID: "function/api", Action: engine.Update}, {ID: "function/worker", Action: engine.Replace, Done: true, Err: errAWS}}, end: errAWS, want: "· 1 created, 1 failed, 1 stopped"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := applying(t, planned(t), tt.events...)
			m, _ = update(t, m, appliedMsg{plan: m.state.plan.plan, applied: true, err: tt.end})
			m.error = ""
			if v := screen(m); !strings.Contains(v, tt.want) {
				t.Errorf("the head does not say %q:\n%s", tt.want, v)
			}
		})
	}
}

// TestApplyFailsPartWay checks a failed apply shows the node's error under it, how far it got, and
// how to finish it, both in the dialog and on the page once the dialog is closed.
func TestApplyFailsPartWay(t *testing.T) {
	err := errors.New("function/worker: InvalidParameterValueException: the role cannot be assumed by lambda")
	m := applying(t, planned(t),
		engine.Event{ID: "resource/table", Action: engine.Create, Done: true, Duration: time.Second},
		engine.Event{ID: "function/worker", Action: engine.Replace, Done: true, Err: err, Duration: time.Second},
	)
	m, _ = update(t, m, appliedMsg{err: fmt.Errorf("%w\nwhat was done is in the state: deploy again to finish", err)})
	if !strings.Contains(m.error, "the deploy of orders/dev failed") || !strings.Contains(m.error, "d plans what is left") {
		t.Errorf("error %q, want what failed and how to finish it", m.error)
	}
	if n := strings.Count(m.error, "what was done is in the state"); n != 1 || strings.Contains(m.error, "deploy again") {
		t.Errorf("error %q says %d times what was done is in the state, want once in the keys here", m.error, n)
	}
	m = pressKeys(t, m, "esc")
	if m.error != "" || m.page != planPage {
		t.Fatalf("esc on the error: error %q, page %v; want the plan", m.error, m.page)
	}
	v := screen(m)
	for _, want := range []string{"failed after", "1 created, 1 failed", "InvalidParameterValueException: the role cannot be assumed by lambda", "d plans what is left"} {
		if !strings.Contains(v, want) {
			t.Errorf("the plan does not say %q:\n%s", want, v)
		}
	}
	if strings.Contains(v, "function/worker: InvalidParameterValueException") {
		t.Error("the node's error repeats its id under it")
	}
	if m = pressKeys(t, m, "ctrl+s"); m.state.plan.applying {
		t.Error("ctrl+s applied a plan that failed, without planning again")
	}
}

// TestPlanWhatIsLeft checks the key a failed apply names, d after a deploy and ctrl+d after a
// remove, works on the plan's page as the footer says: it plans again what is left, and going
// back still goes to where the first plan was asked from.
func TestPlanWhatIsLeft(t *testing.T) {
	tests := []struct {
		name string
		open string
		key  string
		hint string
		page page
	}{
		{name: "a deploy", open: "d", key: "d", hint: "d plan what is left", page: planPage},
		{name: "a remove", open: "ctrl+d", key: "ctrl+d", hint: "ctrl+d remove what is left", page: removePage},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFake(t)
			m := pressKeys(t, started(t, f), tt.open)
			from := m.state.plan.from
			if m.page == removePage {
				m.page = planPage
			}
			first := m.planChanges()[0]
			m = applying(t, m, engine.Event{ID: first.ID, Action: first.Action, Done: true, Err: errAWS})
			m, _ = update(t, m, appliedMsg{plan: m.state.plan.plan, applied: true, err: errAWS})
			m.error = ""
			if v := screen(m); !strings.Contains(v, tt.hint) {
				t.Errorf("the footer does not say %q:\n%s", tt.hint, v)
			}
			m = pressKeys(t, m, tt.key)
			if m.page != tt.page || m.state.plan.failed || m.state.plan.from != from {
				t.Errorf("after %s: page %v, failed %v, from %v; want page %v from %v", tt.key, m.page, m.state.plan.failed, m.state.plan.from, tt.page, from)
			}
		})
	}
}

// TestApplyingKeys checks q and esc while an apply runs keep the plan on screen and do not quit,
// and the footer says ctrl+c stops it.
func TestApplyingKeys(t *testing.T) {
	for _, k := range []string{"q", "esc"} {
		t.Run(k, func(t *testing.T) {
			m := applying(t, planned(t))
			m, cmd := update(t, m, press(k))
			if quits(cmd) || m.page != planPage || !strings.Contains(m.status, "ctrl+c to stop it") {
				t.Errorf("%s while applying: quits %v, page %v, status %q", k, quits(cmd), m.page, m.status)
			}
			if !strings.Contains(screen(m), "ctrl+c stop and quit") {
				t.Error("the footer does not say ctrl+c stops the apply")
			}
		})
	}
}

// blockingBackend is a fake whose deploy sends more events than the channel holds, and returns
// only once they are all sent, as an engine with many nodes does.
type blockingBackend struct {
	*fakeBackend
}

func (b blockingBackend) Deploy(_ context.Context, _ cli.StageRef, _ string, shown *engine.Plan, onEvent func(engine.Event)) (*engine.Plan, bool, error) {
	for i := range 500 {
		onEvent(engine.Event{ID: fmt.Sprint(i), Action: engine.Create})
	}
	return shown, true, nil
}

// TestQuitStopsApply checks an apply whose progress nobody reads any more, once the interface has
// quit, still ends when its context does, so Run's wait returns and the lock is released.
func TestQuitStopsApply(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	f := newFake(t)
	m := newModel(ctx, blockingBackend{f})
	m, _ = update(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	m.state.plan = planState{ref: ordersDev, plan: f.plan}
	m, _ = m.startApply()
	if names := m.ops.names(); len(names) != 1 {
		t.Fatalf("operations running: %v, want the deploy", names)
	}
	cancel()
	done := make(chan struct{})
	go func() {
		m.ops.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the apply did not end once its context was cancelled")
	}
}

// liveSignalWriter collects what is written, and closes wrote on the first write.
type liveSignalWriter struct {
	mu    sync.Mutex
	b     strings.Builder
	once  sync.Once
	wrote chan struct{}
}

func (w *liveSignalWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.once.Do(func() { close(w.wrote) })
	return w.b.Write(p)
}

// TestStopSaysWhatItWaitsFor checks quitting with a deploy running says it is stopping the
// deploy before it waits for it, as the screen is gone by then, and that it stopped after.
func TestStopSaysWhatItWaitsFor(t *testing.T) {
	ops := &operations{running: map[string]bool{}}
	w := &liveSignalWriter{wrote: make(chan struct{})}
	ctx, cancel := context.WithCancel(t.Context())
	ops.start("the deploy of orders/dev")
	finished := make(chan bool, 1)
	go func() {
		defer ops.done("the deploy of orders/dev")
		<-ctx.Done()
		select {
		case <-w.wrote:
			finished <- true
		case <-time.After(5 * time.Second):
			finished <- false
		}
	}()
	ops.stop(w, cancel)
	if !<-finished {
		t.Error("nothing was said before waiting for the deploy")
	}
	got := w.b.String()
	stopping, stopped := strings.Index(got, "stopping the deploy of orders/dev"), strings.Index(got, "stopped the deploy of orders/dev")
	if stopping < 0 || stopped < stopping {
		t.Errorf("stop said:\n%s", got)
	}
}

// livePanicBackend is a fake whose deploy and live tail panic, as a bug in a provider would.
type livePanicBackend struct {
	*fakeBackend
}

func (b livePanicBackend) Deploy(_ context.Context, _ cli.StageRef, _ string, _ *engine.Plan, onEvent func(engine.Event)) (*engine.Plan, bool, error) {
	onEvent(engine.Event{ID: "function/api", Action: engine.Update})
	panic("boom")
}

func (b livePanicBackend) Tail(context.Context, cli.StageRef, string, func([]cli.LogEvent)) error {
	panic("boom")
}

// TestApplyPanics checks a panic in a deploy ends the deploy with an error on screen, rather
// than the program with the terminal left raw, and the operation is no longer running.
func TestApplyPanics(t *testing.T) {
	f := newFake(t)
	m := newModel(t.Context(), livePanicBackend{f})
	m, _ = update(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	m = settle(t, m, m.Init())
	m = pressKeys(t, m, "d", "ctrl+s")
	m.ops.Wait()
	if m.state.plan.applying || !strings.Contains(m.error, "failed: a bug in koldstart stopped it (boom)") {
		t.Errorf("after the panic: applying %v, error %q", m.state.plan.applying, m.error)
	}
	if names := m.ops.names(); len(names) != 0 {
		t.Errorf("operations still running: %v", names)
	}
}

// TestTailPanics checks a panic in the live tail ends it with an error in the footer, and the
// logs are read on each refresh instead.
func TestTailPanics(t *testing.T) {
	f := newFake(t)
	m := newModel(t.Context(), livePanicBackend{f})
	m, _ = update(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	m = settle(t, m, m.Init())
	m = pressKeys(t, m, "enter")
	l := m.state.function.logs
	if m.page != functionPage || l.tailing || !l.noTail || !strings.Contains(m.status, "a bug in koldstart stopped it (boom)") {
		t.Errorf("after the panic: page %v, tailing %v, noTail %v, status %q", m.page, l.tailing, l.noTail, m.status)
	}
}

// TestLeavePlanning checks esc while a plan is made goes back, stops the plan, and never quits.
func TestLeavePlanning(t *testing.T) {
	m := started(t, newFake(t))
	m, _ = m.planDeploy(ordersDev, "")
	stopped := false
	m.state.plan.cancel = func() { stopped = true }
	if !strings.Contains(screen(m), "esc cancel") {
		t.Error("the footer does not say esc cancels the plan")
	}
	m, cmd := update(t, m, press("esc"))
	if quits(cmd) || m.page != stagePage || !stopped {
		t.Errorf("esc while planning: quits %v, page %v, stopped %v", quits(cmd), m.page, stopped)
	}
}

// TestNothingToDeploy checks a plan with no changes says what it compared, as the commands say,
// with no changes said once, in the head, offers to compare with aws when it did not, and offers
// nothing to apply.
func TestNothingToDeploy(t *testing.T) {
	tests := []struct {
		name      string
		keys      []string
		refreshed bool
		want      string
		offer     bool
	}{
		{name: "the stage", keys: []string{"d"}, want: "what the state says is deployed matches the configuration", offer: true},
		{name: "read from aws", keys: []string{"d"}, refreshed: true, want: "what is in aws matches the configuration"},
		{name: "a function's code", keys: []string{"enter", "d"}, want: "api is up to date: its code is the code deployed to orders/dev"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFake(t)
			f.plan = &engine.Plan{Refreshed: tt.refreshed, Changes: []engine.Change{{ID: "role/api", Action: engine.NoOp}}}
			m := pressKeys(t, started(t, f), tt.keys...)
			v := screen(m)
			if !strings.Contains(v, tt.want) || strings.Contains(v, "ctrl+s") {
				t.Errorf("a plan without changes does not say %q:\n%s", tt.want, v)
			}
			if n := strings.Count(v, "no changes"); n != 1 {
				t.Errorf("says no changes %d times, want once:\n%s", n, v)
			}
			if offers := strings.Contains(v, "r reads every resource"); offers != tt.offer {
				t.Errorf("offers to read aws %v, want %v:\n%s", offers, tt.offer, v)
			}
			if m = pressKeys(t, m, "ctrl+s"); len(f.deploys) != 0 || !strings.Contains(m.status, "no changes") {
				t.Errorf("ctrl+s: %d deploys, status %q", len(f.deploys), m.status)
			}
		})
	}
}

// TestPlanHeadStays checks the title and summary stay in sight when a long plan scrolls to its
// end.
func TestPlanHeadStays(t *testing.T) {
	f := newFake(t)
	for i := range 80 {
		f.plan.Changes = append(f.plan.Changes, engine.Change{ID: fmt.Sprintf("resource/q%02d", i), Action: engine.Create})
	}
	m := pressKeys(t, started(t, f), "d", "G")
	v := screen(m)
	if !strings.Contains(v, "deploy of orders/dev") || !strings.Contains(v, "81 to create") || !strings.Contains(v, "resource/q79") {
		t.Errorf("at the end of a long plan:\n%s", v)
	}
	for i, line := range strings.Split(m.planView(), "\n") {
		if w := ansi.StringWidth(line); w > contentWidth {
			t.Errorf("line %d is %d wide, the content %d", i, w, contentWidth)
		}
	}
}

// TestPlanDiffDetail checks the diff of a change says what it does to what type, the type named
// as the plan and the inputs of a create name it.
func TestPlanDiffDetail(t *testing.T) {
	tests := []struct {
		name   string
		change engine.Change
		want   string
	}{
		{name: "a function", change: engine.Change{ID: "function/api", Action: engine.Update, Type: "koldstart::Lambda::Function", Before: map[string]any{"memory": 1.0}, After: map[string]any{"memory": 2.0}}, want: "update lambda function"},
		{name: "a queue left in place", change: engine.Change{ID: "resource/q", Action: engine.Keep, Type: "AWS::SQS::Queue", Before: map[string]any{"name": "q"}}, want: "leave in place sqs queue"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := pressKeys(t, pdPlanned(t, tt.change), "enter")
			if m.page != diffPage || m.state.diff.detail != tt.want {
				t.Errorf("page %v with %q, want the diff with %q", m.page, m.state.diff.detail, tt.want)
			}
		})
	}
}

// TestKeepDiff checks the diff of a resource left in place shows its inputs kept, not removed.
func TestKeepDiff(t *testing.T) {
	m := pressKeys(t, pdChoose(t, planned(t), "resource/bucket"), "enter")
	if m.page != diffPage || m.state.diff.title != "resource/bucket" {
		t.Fatalf("page %v on %q, want the diff of resource/bucket", m.page, m.state.diff.title)
	}
	for _, l := range m.state.diff.lines {
		if l.op != ' ' {
			t.Errorf("a line of what is left in place is marked %q: %q", l.op, l.text)
		}
	}
}
