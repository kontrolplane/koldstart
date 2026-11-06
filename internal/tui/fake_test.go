package tui

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/kontrolplane/koldstart/internal/cli"
	"github.com/kontrolplane/koldstart/internal/engine"
	"github.com/kontrolplane/koldstart/internal/provider/awsp"
	"github.com/kontrolplane/koldstart/internal/state"
)

// fakeBackend is a backend of states in memory. It records the calls that change something.
type fakeBackend struct {
	mu      sync.Mutex
	app     string
	stage   string
	events  string
	states  map[cli.StageRef]*state.State
	locks   map[cli.StageRef]*state.LockInfo
	history []state.ObjectVersion
	// versions are the states of the history by their version, where a test wants others than
	// testState's; versionReads counts the reads of one.
	versions     map[string]*state.State
	versionReads int
	// metrics are what Metrics returns where a test wants others than the series it makes up.
	metrics map[string]cli.Metrics
	logs    []cli.LogEvent
	older   []cli.LogEvent
	tail    []cli.LogEvent
	result  cli.Invocation
	live    map[string]any
	// function is what lambda says of a function, metricsErr the error metrics fail with.
	function   cli.FunctionState
	metricsErr error
	olderReads int
	plan       *engine.Plan
	// replan is the plan Deploy and Remove find instead of the one shown, once.
	replan *engine.Plan
	err    error

	deploys []*engine.Plan
	// refreshes are the refresh of each plan asked for.
	refreshes []bool
	removes   int
	unlocks   []cli.StageRef
	invokes   []string
	payloads  []string
	local     int
}

var (
	ordersDev  = cli.StageRef{Region: "eu-west-1", App: "orders", Stage: "dev"}
	ordersProd = cli.StageRef{Region: "eu-west-1", App: "orders", Stage: "prod"}
	billingDev = cli.StageRef{Region: "us-east-1", App: "billing", Stage: "dev"}
)

func newFake(t *testing.T) *fakeBackend {
	t.Helper()
	return &fakeBackend{
		app: "orders", stage: "dev", events: t.TempDir(),
		states: map[cli.StageRef]*state.State{
			ordersDev:  testState(ordersDev),
			ordersProd: testState(ordersProd),
			billingDev: testState(billingDev),
		},
		locks: map[cli.StageRef]*state.LockInfo{},
		history: []state.ObjectVersion{
			{ID: "v2", LastModified: time.Now(), Latest: true},
			{ID: "v1", LastModified: time.Now().Add(-time.Hour)},
		},
		logs:   []cli.LogEvent{{Time: time.Now(), Message: "START RequestId: 1"}, {Time: time.Now(), Message: `{"level":"info","msg":"order placed"}`}},
		result: cli.Invocation{Payload: []byte(`{"statusCode":200}`), Logs: "END RequestId: 1\n", Duration: 120 * time.Millisecond},
		plan: &engine.Plan{App: "orders", Stage: "dev", Changes: []engine.Change{
			{ID: "function/api", Action: engine.Update, Fields: []string{"memory"}, Before: map[string]any{"memory": 128.0}, After: map[string]any{"memory": 256.0}},
			{ID: "resource/table", Action: engine.Create},
			{ID: "role/api", Action: engine.NoOp},
		}},
	}
}

// testState is a deployed stage with a function, its role and log group, a table and an output.
func testState(ref cli.StageRef) *state.State {
	st := state.New(ref.App, ref.Stage, ref.Region, "123456789012")
	st.DeployedAt, st.DeployedBy, st.Serial = time.Now().Add(-10*time.Minute), "levi", 7
	in, _ := json.Marshal(map[string]any{
		"name": ref.App + "-" + ref.Stage + "-api", "runtime": "nodejs22.x", "handler": "src/api.handler", "memory": 1024, "timeout": 6,
		"architecture": "arm64", "ephemeralStorage": 512, "role": "arn:aws:iam::123456789012:role/api",
		"environment": map[string]any{"TABLE": "orders", "KEY": map[string]any{"$koldstart": "secret", "source": "ssm", "name": "/orders/key"}},
		"code":        map[string]any{"bucket": "b", "key": "k", "sha256": strings.Repeat("ab", 32)},
	})
	st.Resources["function/api"] = &state.Resource{Type: awsp.TypeFunction, PhysicalID: ref.App + "-" + ref.Stage + "-api", Inputs: in,
		Attributes: map[string]string{"Arn": "arn:aws:lambda:eu-west-1:123456789012:function:" + ref.App + "-" + ref.Stage + "-api"}}
	st.Resources["loggroup/api"] = &state.Resource{Type: awsp.TypeLogGroup, PhysicalID: "/aws/lambda/" + ref.App + "-" + ref.Stage + "-api",
		Attributes: map[string]string{"Arn": "arn:aws:logs:eu-west-1:123456789012:log-group:/aws/lambda/" + ref.App + "-" + ref.Stage + "-api:*"}}
	st.Resources["role/api"] = &state.Resource{Type: awsp.TypeRole, PhysicalID: ref.App + "-" + ref.Stage + "-api"}
	st.Resources["resource/table"] = &state.Resource{Type: "AWS::DynamoDB::Table", PhysicalID: "orders-" + ref.Stage, DeletionPolicy: "retain",
		Inputs: json.RawMessage(`{"properties":{"BillingMode":"PAY_PER_REQUEST"}}`), Attributes: map[string]string{"Arn": "arn:aws:dynamodb:table"}}
	st.Outputs = map[string]string{"httpApiUrl": "https://api.example.com"}
	return st
}

func (f *fakeBackend) Identity() cli.Identity {
	return cli.Identity{Profile: "sandbox", Account: "123456789012", Caller: "arn:aws:iam::123456789012:user/levi", Region: "eu-west-1", Bucket: "koldstart-state-123456789012-eu-west-1"}
}

func (f *fakeBackend) Project() (string, string, error) { return f.app, f.stage, nil }

func (f *fakeBackend) Local(ref cli.StageRef) bool { return f.app != "" && ref.App == f.app }

func (f *fakeBackend) EventsDir(function string) string { return f.events + "/" + function }

func (f *fakeBackend) Stages(context.Context) ([]cli.StageSummary, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []cli.StageSummary
	for _, ref := range []cli.StageRef{billingDev, ordersDev, ordersProd} {
		st := f.states[ref]
		if st == nil {
			continue
		}
		out = append(out, cli.StageSummary{StageRef: ref, DeployedAt: st.DeployedAt, DeployedBy: st.DeployedBy, Resources: len(st.Resources), Lock: f.locks[ref]})
	}
	return out, f.err
}

func (f *fakeBackend) Stage(_ context.Context, ref cli.StageRef) (*state.State, *state.LockInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.states[ref], f.locks[ref], f.err
}

func (f *fakeBackend) History(context.Context, cli.StageRef) ([]state.ObjectVersion, error) {
	return f.history, f.err
}

func (f *fakeBackend) Version(_ context.Context, ref cli.StageRef, id string) (*state.State, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.versionReads++
	if st, ok := f.versions[id]; ok {
		return st, f.err
	}
	st := testState(ref)
	if id == "v1" {
		delete(st.Resources, "resource/table")
	}
	return st, f.err
}

func (f *fakeBackend) LogsBetween(_ context.Context, _ cli.StageRef, _ string, since, until time.Time, _ string) ([]cli.LogEvent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !until.IsZero() {
		f.olderReads++
		return f.older, f.err
	}
	var out []cli.LogEvent
	for _, e := range f.logs {
		if !e.Time.Before(since) {
			out = append(out, e)
		}
	}
	return out, f.err
}

func (f *fakeBackend) Tail(ctx context.Context, _ cli.StageRef, _ string, fn func([]cli.LogEvent)) error {
	if f.tail == nil {
		return cli.ErrNoLiveTail
	}
	fn(f.tail)
	<-ctx.Done()
	return nil
}

func (f *fakeBackend) Live(_ context.Context, _ cli.StageRef, typ, _ string) (map[string]any, error) {
	if f.live == nil {
		return nil, cli.ErrNotLive
	}
	return f.live, nil
}

func (f *fakeBackend) Function(context.Context, cli.StageRef, string) (cli.FunctionState, error) {
	return f.function, f.err
}

func (f *fakeBackend) Queue(context.Context, cli.StageRef, string) (cli.QueueDepth, error) {
	return cli.QueueDepth{Visible: 12, InFlight: 3}, nil
}

func (f *fakeBackend) Metrics(_ context.Context, _ cli.StageRef, names []string) (map[string]cli.Metrics, error) {
	if f.metricsErr != nil {
		return nil, f.metricsErr
	}
	if f.metrics != nil {
		return f.metrics, nil
	}
	out := map[string]cli.Metrics{}
	for _, name := range names {
		series := func(v float64) []float64 {
			s := make([]float64, 60)
			for i := range s {
				s[i] = v * float64(i%7)
			}
			return s
		}
		out[name] = cli.Metrics{Invocations: series(10), Errors: series(0.5), Throttles: series(0), Duration: series(12), DurationP99: series(40), Concurrency: series(1)}
	}
	return out, nil
}

func (f *fakeBackend) Invoke(_ context.Context, _ cli.StageRef, function string, payload []byte) (cli.Invocation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.invokes = append(f.invokes, function)
	f.payloads = append(f.payloads, string(payload))
	return f.result, f.err
}

func (f *fakeBackend) InvokeLocal(_ context.Context, _ cli.StageRef, function string, payload []byte) (cli.Invocation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.local++
	f.payloads = append(f.payloads, string(payload))
	return f.result, f.err
}

func (f *fakeBackend) Plan(_ context.Context, _ cli.StageRef, _ string, refresh bool) (*engine.Plan, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.refreshes = append(f.refreshes, refresh)
	return f.plan, f.err
}

func (f *fakeBackend) Deploy(_ context.Context, _ cli.StageRef, _ string, shown *engine.Plan, onEvent func(engine.Event)) (*engine.Plan, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deploys = append(f.deploys, shown)
	if f.replan != nil {
		plan := f.replan
		f.replan = nil
		return plan, false, nil
	}
	for _, c := range shown.Changes {
		if c.Action != engine.NoOp {
			onEvent(engine.Event{ID: c.ID, Action: c.Action})
			onEvent(engine.Event{ID: c.ID, Action: c.Action, Done: true, Duration: time.Second})
		}
	}
	return shown, true, f.err
}

func (f *fakeBackend) PlanRemove(_ context.Context, ref cli.StageRef) (*engine.Plan, error) {
	return &engine.Plan{App: ref.App, Stage: ref.Stage, Changes: []engine.Change{
		{ID: "function/api", Action: engine.Delete}, {ID: "resource/table", Action: engine.Keep},
	}}, f.err
}

func (f *fakeBackend) Remove(_ context.Context, ref cli.StageRef, shown *engine.Plan, onEvent func(engine.Event)) (*engine.Plan, bool, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removes++
	onEvent(engine.Event{ID: "function/api", Action: engine.Delete, Done: true})
	delete(f.states, ref)
	return shown, true, true, f.err
}

func (f *fakeBackend) Unlock(_ context.Context, ref cli.StageRef) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.unlocks = append(f.unlocks, ref)
	delete(f.locks, ref)
	return nil
}

var errAWS = errors.New("aws said no")

// newTestModel is the interface on a fake, at 120 by 40, before anything is loaded.
func newTestModel(t *testing.T, f *fakeBackend) model {
	t.Helper()
	m := newModel(context.Background(), f)
	m, _ = update(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	return m
}

// started is the interface once its first loads are in.
func started(t *testing.T, f *fakeBackend) model {
	t.Helper()
	m := newTestModel(t, f)
	return settle(t, m, m.Init())
}

func press(s string) tea.KeyPressMsg {
	switch s {
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "tab":
		return tea.KeyPressMsg{Code: tea.KeyTab}
	case "shift+tab":
		return tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift}
	case "backspace":
		return tea.KeyPressMsg{Code: tea.KeyBackspace}
	}
	if ctrl, ok := strings.CutPrefix(s, "ctrl+"); ok {
		return tea.KeyPressMsg{Code: rune(ctrl[0]), Mod: tea.ModCtrl}
	}
	return tea.KeyPressMsg{Code: rune(s[0]), Text: s}
}

func update(t *testing.T, m model, msgs ...tea.Msg) (model, tea.Cmd) {
	t.Helper()
	var cmd tea.Cmd
	for _, msg := range msgs {
		var next tea.Model
		next, cmd = m.Update(msg)
		m = next.(model)
	}
	return m, cmd
}

// pressKeys presses keys one by one, settling what each starts.
func pressKeys(t *testing.T, m model, pressed ...string) model {
	t.Helper()
	for _, k := range pressed {
		var cmd tea.Cmd
		m, cmd = update(t, m, press(k))
		m = settle(t, m, cmd)
	}
	return m
}

func typeText(t *testing.T, m model, s string) model {
	t.Helper()
	for _, r := range s {
		m, _ = update(t, m, tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	return m
}

// settle runs a command and feeds what it returns back in, until nothing more comes. Timers,
// the spinner and the refresh, are left out, as they would wait.
func settle(t *testing.T, m model, cmd tea.Cmd) model {
	t.Helper()
	for range 20 {
		msgs := run(cmd)
		if len(msgs) == 0 {
			return m
		}
		var cmds []tea.Cmd
		for _, msg := range msgs {
			var next tea.Cmd
			m, next = update(t, m, msg)
			cmds = append(cmds, next)
		}
		cmd = tea.Batch(cmds...)
	}
	return m
}

// run runs a command and the commands of a batch, returning their messages. A command that
// does not answer at once is a timer, and is dropped.
func run(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()
	var msg tea.Msg
	select {
	case msg = <-done:
	case <-time.After(50 * time.Millisecond):
		return nil
	}
	switch msg := msg.(type) {
	case nil, spinner.TickMsg, refreshTickMsg, statusClearMsg:
		return nil
	case tea.BatchMsg:
		var out []tea.Msg
		for _, c := range msg {
			out = append(out, run(c)...)
		}
		return out
	}
	return []tea.Msg{msg}
}

// screen is the text on screen, without its styles.
func screen(m model) string {
	return ansi.Strip(m.render())
}

// quits reports whether a command quits the program.
func quits(cmd tea.Cmd) bool {
	if cmd == nil {
		return false
	}
	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()
	select {
	case msg := <-done:
		if _, ok := msg.(tea.QuitMsg); ok {
			return true
		}
		if batch, ok := msg.(tea.BatchMsg); ok {
			return slices.ContainsFunc(batch, quits)
		}
	case <-time.After(50 * time.Millisecond):
	}
	return false
}
