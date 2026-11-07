package tui

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"charm.land/bubbles/v2/cursor"
	tea "charm.land/bubbletea/v2"

	"github.com/kontrolplane/koldstart/internal/cli"
	"github.com/kontrolplane/koldstart/internal/state"
	"github.com/kontrolplane/koldstart/internal/tui/styles"
)

// leftoversBackend is the fake with no project to keep events in, and a resource that cannot be
// read from aws once liveErr is set.
type leftoversBackend struct {
	*fakeBackend
	noEvents bool
	liveErr  error
	queueErr error
}

func (b *leftoversBackend) EventsDir(function string) string {
	if b.noEvents {
		return ""
	}
	return b.fakeBackend.EventsDir(function)
}

func (b *leftoversBackend) Live(ctx context.Context, ref cli.StageRef, typ, id string) (map[string]any, error) {
	if b.liveErr != nil {
		return nil, b.liveErr
	}
	return b.fakeBackend.Live(ctx, ref, typ, id)
}

func (b *leftoversBackend) Queue(ctx context.Context, ref cli.StageRef, url string) (cli.QueueDepth, error) {
	if b.queueErr != nil {
		return cli.QueueDepth{}, b.queueErr
	}
	return b.fakeBackend.Queue(ctx, ref, url)
}

// leftoversStarted is the interface on b at 120 by 40, once its first loads are in.
func leftoversStarted(t *testing.T, b backend) model {
	t.Helper()
	m := newModel(context.Background(), b)
	m, _ = update(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	return settle(t, m, m.Init())
}

// leftoversBlink waits for the blink of a cursor among what cmd sends.
func leftoversBlink(t *testing.T, cmd tea.Cmd) tea.Msg {
	t.Helper()
	if cmd == nil {
		t.Fatal("focusing the input started no blink")
	}
	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()
	select {
	case msg := <-done:
		if batch, ok := msg.(tea.BatchMsg); ok {
			for _, c := range batch {
				if c == nil {
					continue
				}
				if blink := leftoversBlink(t, c); blink != nil {
					return blink
				}
			}
			return nil
		}
		if _, ok := msg.(cursor.BlinkMsg); ok {
			return msg
		}
	case <-time.After(2 * time.Second):
	}
	return nil
}

// TestUnreadableStage checks a stage whose state cannot be read says so in a word in its lock
// column, and its peek and enter show the error in full.
func TestUnreadableStage(t *testing.T) {
	long := "AccessDenied: User arn:aws:iam::123456789012:user/levi is not authorised to perform s3:GetObject"
	tests := []struct {
		name string
		keys []string
		want string
		not  string
	}{
		{name: "the row", want: "✗ unreadable", not: "AccessDenied"},
		{name: "the peek", want: "could not read the stage: AccessDenied"},
		{name: "enter", keys: []string{"enter"}, want: "is not authorised to perform s3:GetObject"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFake(t)
			f.app = ""
			m := started(t, f)
			m.state.overview.stages[0].Err = errors.New(long)
			m = m.updateOverviewTable()
			f.err = errors.New(long)
			m = pressKeys(t, m, tt.keys...)
			v := screen(m)
			if !strings.Contains(v, tt.want) {
				t.Errorf("want %q:\n%s", tt.want, v)
			}
			for line := range strings.SplitSeq(v, "\n") {
				if tt.not != "" && strings.Contains(line, tt.want) && strings.Contains(line, tt.not) {
					t.Errorf("want no %q beside %q: %s", tt.not, tt.want, line)
				}
			}
		})
	}
}

// TestBusy checks the spinner turns while older logs are read and while a resource is read from
// aws, as both draw it.
func TestBusy(t *testing.T) {
	tests := []struct {
		name string
		set  func(m *model)
		want bool
	}{
		{name: "nothing", set: func(*model) {}},
		{name: "older logs", set: func(m *model) { m.state.function.logs.loadingOlder = true }, want: true},
		{name: "a resource from aws", set: func(m *model) {
			m.page = resourcePage
			m.state.resource = resourceState{res: m.state.stage.resources[3], tab: liveTab}
		}, want: true},
		{name: "a resource applied", set: func(m *model) {
			m.page = resourcePage
			m.state.resource = resourceState{res: m.state.stage.resources[3]}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := started(t, newFake(t))
			tt.set(&m)
			if got := m.busy(); got != tt.want {
				t.Errorf("busy %v, want %v", got, tt.want)
			}
		})
	}
}

// TestInputsBlink checks the cursor of a focused input blinks, the blink reaching it through
// Update, which before took only keys.
func TestInputsBlink(t *testing.T) {
	tests := []struct {
		name string
		keys []string
		open string
	}{
		{name: "the filter of the stages", keys: []string{"q"}, open: "/"},
		{name: "the filter of a stage", open: "/"},
		{name: "the filter of the logs", keys: []string{"enter", "tab"}, open: "/"},
		{name: "the name of the stage to remove", open: "ctrl+d"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := pressKeys(t, started(t, newFake(t)), tt.keys...)
			m, cmd := update(t, m, press(tt.open))
			m = settle(t, m, cmd)
			if tt.open == "ctrl+d" {
				cmd = m.state.remove.confirm.input.Focus()
			}
			if !m.typing() {
				t.Fatal("no input has focus")
			}
			blink := leftoversBlink(t, cmd)
			if blink == nil {
				t.Fatal("no blink came")
			}
			before := m.render()
			m, next := update(t, m, blink)
			if m.render() == before {
				t.Error("the cursor did not blink")
			}
			if next == nil {
				t.Error("the blink scheduled no next one")
			}
		})
	}
}

// TestCancelRefreshPlan checks esc while r plans again goes back to the plan before, and the
// plan it stopped is dropped when it comes in.
func TestCancelRefreshPlan(t *testing.T) {
	tests := []struct {
		name string
		msg  func(gen int) tea.Msg
	}{
		{name: "stopped", msg: func(gen int) tea.Msg { return planLoadedMsg{gen: gen, err: context.Canceled} }},
		{name: "finished anyway", msg: func(gen int) tea.Msg { return planLoadedMsg{gen: gen, plan: newFake(t).plan} }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := pressKeys(t, started(t, newFake(t)), "d")
			shown := m.state.plan.plan
			m, _ = update(t, m, press("r"))
			gen := m.state.plan.gen
			if !m.loading || m.state.plan.plan != nil {
				t.Fatal("r is not planning again")
			}
			m, _ = update(t, m, press("esc"), tt.msg(gen))
			if m.page != planPage || m.loading || m.state.plan.plan != shown || m.state.plan.refresh || m.error != "" {
				t.Errorf("page %v, loading %v, error %q; want the plan before on screen", m.page, m.loading, m.error)
			}
		})
	}
}

// TestRefreshPlanFails checks a plan r asked for that fails says why over the plan before.
func TestRefreshPlanFails(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{name: "aws", err: errAWS},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFake(t)
			m := pressKeys(t, started(t, f), "d")
			shown := m.state.plan.plan
			f.err = tt.err
			m = pressKeys(t, m, "r")
			if m.page != planPage || m.state.plan.plan != shown || !strings.Contains(m.error, tt.err.Error()) {
				t.Errorf("page %v, error %q; want the plan before, and why", m.page, m.error)
			}
		})
	}
}

// TestEventsWithoutProject checks e without a project here creates nothing, and says why.
func TestEventsWithoutProject(t *testing.T) {
	tests := []struct {
		name     string
		noEvents bool
		want     string
	}{
		{name: "no project", noEvents: true, want: "events are kept in a project"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			t.Chdir(dir)
			b := &leftoversBackend{fakeBackend: newFake(t), noEvents: tt.noEvents}
			m := pressKeys(t, leftoversStarted(t, b), "enter", "tab", "tab", "tab")
			if v := screen(m); strings.Contains(v, "e creates") || strings.Contains(v, "e edits") {
				t.Errorf("the invoke tab offers e:\n%s", v)
			}
			m = pressKeys(t, m, "e")
			if !strings.Contains(m.status, tt.want) || m.statusTone != styles.ToneWarning {
				t.Errorf("status %q in tone %v, want a warning %q", m.status, m.statusTone, tt.want)
			}
			if _, err := os.Stat(filepath.Join(dir, ".koldstart")); err == nil {
				t.Error("e created .koldstart in this directory")
			}
		})
	}
}

// TestRunLocalNamesEvent checks the dialog before running against prod here names the event it
// sends.
func TestRunLocalNamesEvent(t *testing.T) {
	tests := []struct {
		name  string
		files []string
		want  string
	}{
		{name: "none kept", want: "with {}: no events are kept for it"},
		{name: "a file", files: []string{"order.json"}, want: "with order.json"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFake(t)
			dir := filepath.Join(f.events, "api")
			for _, name := range tt.files {
				if err := os.MkdirAll(dir, 0o750); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, name), []byte("{}"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			m, cmd := started(t, f).openStage(ordersProd)
			m = pressKeys(t, settle(t, m, cmd), "enter", "I")
			if v := screen(m); m.page != runLocalPage || !strings.Contains(v, tt.want) {
				t.Errorf("page %v; the dialog lacks %q:\n%s", m.page, tt.want, v)
			}
		})
	}
}

// TestTabKeys checks ← and → switch the tabs on every page with tabs, as tab and shift+tab do.
func TestTabKeys(t *testing.T) {
	tests := []struct {
		name string
		keys []string
		tab  func(m model) int
	}{
		{name: "stage", tab: func(m model) int { return int(m.state.stage.tab) }},
		{name: "function", keys: []string{"enter"}, tab: func(m model) int { return int(m.state.function.tab) }},
		{name: "resource", keys: []string{"tab", "j", "j", "j", "j", "j", "enter"}, tab: func(m model) int { return int(m.state.resource.tab) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := pressKeys(t, started(t, newFake(t)), tt.keys...)
			first := tt.tab(m)
			m = pressKeys(t, m, "l")
			if tt.tab(m) == first {
				t.Fatal("l did not switch the tab")
			}
			if m = pressKeys(t, m, "h"); tt.tab(m) != first {
				t.Errorf("h went to tab %d, want %d", tt.tab(m), first)
			}
		})
	}
}

// TestStaleMarkers checks data that could not be read again stays on screen, marked with when it
// was read and why the read after failed.
func TestStaleMarkers(t *testing.T) {
	tests := []struct {
		name string
		keys []string
		fail func(b *leftoversBackend)
		want []string
	}{
		{
			name: "the metrics of a stage",
			fail: func(b *leftoversBackend) { b.metricsErr = errAWS },
			want: []string{"1,740", "metrics as of", "could not read again:"},
		},
		{
			name: "the state of a stage",
			fail: func(b *leftoversBackend) { b.err = errAWS },
			want: []string{"orders-dev-api", "could not read again"},
		},
		{
			name: "what lambda says of a function",
			keys: []string{"enter"},
			fail: func(b *leftoversBackend) { b.err, b.metricsErr = errAWS, errAWS },
			want: []string{"active", "1,740", "as of", "could not read again:"},
		},
		{
			name: "a resource in aws",
			keys: []string{"tab", "j", "j", "j", "j", "j", "enter", "tab"},
			fail: func(b *leftoversBackend) { b.liveErr = errAWS },
			want: []string{`"BillingMode": "PAY_PER_REQUEST" → "PROVISIONED"`, "could not read again:"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := &leftoversBackend{fakeBackend: newFake(t)}
			b.function = cli.FunctionState{State: "Active"}
			b.live = map[string]any{"BillingMode": "PROVISIONED"}
			m := pressKeys(t, leftoversStarted(t, b), tt.keys...)
			if v := screen(m); strings.Contains(v, "could not read") {
				t.Fatalf("a good read says it failed:\n%s", v)
			}
			tt.fail(b)
			m = pressKeys(t, m, "r")
			v := screen(m)
			for _, want := range tt.want {
				if !strings.Contains(v, want) {
					t.Errorf("no %q:\n%s", want, v)
				}
			}
		})
	}
}

// TestQueueDepthUnreadable checks a queue whose depth cannot be read says so, on the resources
// tab and on its page, and keeps the depth of the read before.
func TestQueueDepthUnreadable(t *testing.T) {
	tests := []struct {
		name  string
		first bool
		keys  []string
		want  []string
	}{
		{name: "the tab, never read", keys: []string{"tab"}, want: []string{"could not read the depth"}},
		{name: "its page, never read", keys: []string{"tab", "j", "j", "j", "j", "j", "enter"}, want: []string{"could not read the depth: aws said no · r tries again"}},
		{name: "its page, read before", first: true, keys: []string{"tab", "j", "j", "j", "j", "j", "enter"}, want: []string{"12 queued", "as of", "could not read again:"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := &leftoversBackend{fakeBackend: newFake(t)}
			b.states[ordersDev].Resources["resource/jobs"] = &state.Resource{Type: "AWS::SQS::Queue", PhysicalID: "https://sqs/jobs"}
			if !tt.first {
				b.queueErr = errAWS
			}
			m := leftoversStarted(t, b)
			if tt.first {
				b.queueErr = errAWS
				m = pressKeys(t, m, "r")
			}
			v := screen(pressKeys(t, m, tt.keys...))
			for _, want := range tt.want {
				if !strings.Contains(v, want) {
					t.Errorf("no %q:\n%s", want, v)
				}
			}
		})
	}
}
