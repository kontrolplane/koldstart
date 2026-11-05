package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/kontrolplane/koldstart/internal/cli"
	"github.com/kontrolplane/koldstart/internal/engine"
	"github.com/kontrolplane/koldstart/internal/state"
)

// TestOpensOnTheProjectStage checks the interface starts on the stage of the project here, and
// q goes back to the overview of every stage.
func TestOpensOnTheProjectStage(t *testing.T) {
	m := started(t, newFake(t))
	if m.page != stagePage || m.state.stage.ref != ordersDev {
		t.Fatalf("page %v on %v, want the stage orders/dev", m.page, m.state.stage.ref)
	}
	if !strings.Contains(screen(m), "orders-dev-api") {
		t.Error("the stage does not list its function")
	}
	m = pressKeys(t, m, "q")
	if m.page != overviewPage || len(m.state.overview.shown) != 3 {
		t.Fatalf("after q: page %v with %d stages, want the overview of 3", m.page, len(m.state.overview.shown))
	}
}

// TestOverviewWithoutProject checks that without a project the overview stays open, and that the
// project's stage is listed and opened before its first deploy.
func TestOverviewWithoutProject(t *testing.T) {
	tests := []struct {
		name   string
		app    string
		stage  string
		page   page
		stages int
	}{
		{name: "no project", page: overviewPage, stages: 3},
		{name: "a stage not deployed yet", app: "orders", stage: "pr-12", page: stagePage, stages: 4},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFake(t)
			f.app, f.stage = tt.app, tt.stage
			m := started(t, f)
			if m.page != tt.page || len(m.state.overview.shown) != tt.stages {
				t.Fatalf("page %v with %d stages, want %v with %d", m.page, len(m.state.overview.shown), tt.page, tt.stages)
			}
		})
	}
}

// TestKeys checks q goes back and quits only on the overview, esc never quits, ctrl+c always
// does, and letters typed into a filter are not shortcuts.
func TestKeys(t *testing.T) {
	tests := []struct {
		name  string
		keys  []string
		last  string
		quits bool
		page  page
	}{
		{name: "q on the overview", keys: []string{"q"}, last: "q", quits: true, page: overviewPage},
		{name: "esc on the overview", keys: []string{"q"}, last: "esc", page: overviewPage},
		{name: "q on a stage", last: "q", page: overviewPage},
		{name: "esc on a stage", last: "esc", page: overviewPage},
		{name: "ctrl+c on a function", keys: []string{"enter"}, last: "ctrl+c", quits: true, page: functionPage},
		{name: "q in a filter", keys: []string{"q", "/"}, last: "q", page: overviewPage},
		{name: "esc on a function", keys: []string{"enter"}, last: "esc", page: stagePage},
		{name: "q on a resource", keys: []string{"tab", "j", "enter"}, last: "q", page: stagePage},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := pressKeys(t, started(t, newFake(t)), tt.keys...)
			m, cmd := update(t, m, press(tt.last))
			if got := quits(cmd); got != tt.quits {
				t.Errorf("quits = %v, want %v", got, tt.quits)
			}
			if m.page != tt.page {
				t.Errorf("page = %v, want %v", m.page, tt.page)
			}
		})
	}
}

// TestFilter checks the filter narrows the overview, and esc clears it.
func TestFilter(t *testing.T) {
	m := pressKeys(t, started(t, newFake(t)), "q", "/")
	m = typeText(t, m, "bill")
	if n := len(m.state.overview.shown); n != 1 {
		t.Fatalf("filtered to %d stages, want 1", n)
	}
	m = pressKeys(t, m, "enter")
	if m.typing() || m.state.overview.filter.text != "bill" {
		t.Fatalf("after enter: typing %v, filter %q", m.typing(), m.state.overview.filter.text)
	}
	m = pressKeys(t, m, "esc")
	if n := len(m.state.overview.shown); n != 3 {
		t.Fatalf("after esc %d stages, want 3", n)
	}
}

// TestStaleLoadsAreDropped checks a load that was overtaken by a newer one does not reach the
// screen.
func TestStaleLoadsAreDropped(t *testing.T) {
	m := pressKeys(t, started(t, newFake(t)), "q")
	stale := m.state.overview.gen - 1
	m, _ = update(t, m, stagesLoadedMsg{gen: stale, stages: nil})
	if len(m.state.overview.shown) != 3 {
		t.Fatalf("a stale load replaced the stages: %d left", len(m.state.overview.shown))
	}
	m, _ = update(t, m, stagesLoadedMsg{gen: m.state.overview.gen, err: errAWS})
	if len(m.state.overview.shown) != 3 || !strings.Contains(m.status, "aws said no") || m.error != "" {
		t.Fatalf("a failed refresh: %d stages, status %q, error %q; want the stages kept and the error in the footer", len(m.state.overview.shown), m.status, m.error)
	}
}

// TestDeploy checks d plans, ctrl+s applies the plan shown with each node's progress, and a
// plan that changed since is shown again instead of applied.
func TestDeploy(t *testing.T) {
	tests := []struct {
		name    string
		replan  bool
		deploys int
		status  string
	}{
		{name: "applied", deploys: 1, status: "deployed orders/dev"},
		{name: "changed since", replan: true, deploys: 2, status: "deployed orders/dev"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFake(t)
			if tt.replan {
				f.replan = &engine.Plan{App: "orders", Stage: "dev", Changes: []engine.Change{{ID: "function/api", Action: engine.Update, Fields: []string{"code"}}}}
			}
			m := pressKeys(t, started(t, f), "d")
			if m.page != planPage || m.state.plan.plan == nil {
				t.Fatalf("after d: page %v, plan %v", m.page, m.state.plan.plan)
			}
			if v := screen(m); !strings.Contains(v, "resource/table") || strings.Contains(v, "role/api") {
				t.Error("the plan does not show the changes, or shows what does not change")
			}
			m = pressKeys(t, m, "ctrl+s")
			if tt.replan {
				if !strings.Contains(m.status, "changed since") || m.state.plan.plan.Changes[0].Fields[0] != "code" {
					t.Fatalf("a plan that changed: status %q, want the new plan shown", m.status)
				}
				m = pressKeys(t, m, "ctrl+s")
			}
			if len(f.deploys) != tt.deploys || m.status != tt.status || !m.state.plan.done {
				t.Fatalf("%d deploys, status %q, done %v", len(f.deploys), m.status, m.state.plan.done)
			}
			if !strings.Contains(screen(m), "✓") {
				t.Error("the plan does not show what was applied")
			}
			m = pressKeys(t, m, "ctrl+s")
			if len(f.deploys) != tt.deploys {
				t.Error("ctrl+s applied a plan that was applied already")
			}
			if m = pressKeys(t, m, "q"); m.page != stagePage {
				t.Errorf("q after the deploy: page %v, want the stage", m.page)
			}
		})
	}
}

// TestDeployOnlyTheProject checks a stage of another app than the project here is not planned.
func TestDeployOnlyTheProject(t *testing.T) {
	m := pressKeys(t, started(t, newFake(t)), "q")
	m = pressKeys(t, m, "enter")
	if m.state.stage.ref != billingDev {
		t.Fatalf("opened %v, want billing/dev", m.state.stage.ref)
	}
	m = pressKeys(t, m, "d")
	if m.page != stagePage || !strings.Contains(m.status, "only the project here") {
		t.Fatalf("d on another app: page %v, status %q", m.page, m.status)
	}
}

// TestRemove checks ctrl+d shows the plan of the remove, asks for the stage's name, and removes
// only once it is typed.
func TestRemove(t *testing.T) {
	f := newFake(t)
	m := pressKeys(t, started(t, f), "ctrl+d")
	if m.page != removePage || !strings.Contains(screen(m), "type dev to confirm") {
		t.Fatalf("ctrl+d: page %v, want the typed confirmation", m.page)
	}
	m = pressKeys(t, m, "enter")
	m = typeText(t, m, "prod")
	m = pressKeys(t, m, "enter")
	if f.removes != 0 {
		t.Fatal("removed without the stage's name")
	}
	for range 4 {
		m = pressKeys(t, m, "backspace")
	}
	m = typeText(t, m, "dev")
	m = pressKeys(t, m, "enter")
	if f.removes != 1 || m.page != overviewPage || m.status != "removed orders/dev" {
		t.Fatalf("%d removes, page %v, status %q", f.removes, m.page, m.status)
	}
}

// TestUnlock checks ctrl+x shows who holds the lock, n keeps it and y releases it.
func TestUnlock(t *testing.T) {
	f := newFake(t)
	f.locks[ordersDev] = &state.LockInfo{Who: "ci", Where: "github", Operation: "deploy", Since: time.Now().Add(-time.Hour)}
	m := pressKeys(t, started(t, f), "ctrl+x")
	if m.page != unlockPage || !strings.Contains(screen(m), "held by") {
		t.Fatalf("ctrl+x: page %v", m.page)
	}
	m = pressKeys(t, m, "n")
	if m.page != stagePage || len(f.unlocks) != 0 {
		t.Fatalf("n: page %v, %d unlocks", m.page, len(f.unlocks))
	}
	m = pressKeys(t, m, "ctrl+x", "y")
	if len(f.unlocks) != 1 || m.state.stage.lock != nil || !strings.Contains(m.status, "released") {
		t.Fatalf("y: %d unlocks, lock %v, status %q", len(f.unlocks), m.state.stage.lock, m.status)
	}
}

// TestInvoke checks i invokes the function with the event under the cursor and shows what it
// returned, and e's events are listed from the events directory.
func TestInvoke(t *testing.T) {
	f := newFake(t)
	dir := filepath.Join(f.events, "api")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "get-order.json"), []byte(`{"id": 1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	m := pressKeys(t, started(t, f), "enter", "tab", "tab", "tab")
	if m.page != functionPage || m.state.function.tab != invokeTab {
		t.Fatalf("page %v tab %v, want the invoke tab of the function", m.page, m.state.function.tab)
	}
	m = pressKeys(t, m, "i")
	if len(f.invokes) != 1 || f.invokes[0] != "orders-dev-api" || f.payloads[0] != `{"id": 1}` {
		t.Fatalf("invokes %v with %v", f.invokes, f.payloads)
	}
	if v := screen(m); !strings.Contains(v, `"statusCode": 200`) || !strings.Contains(v, "END RequestId") {
		t.Error("the result does not show what the function returned and its log")
	}
}

// TestRunLocal checks shift+i runs the function here, asking first against prod.
func TestRunLocal(t *testing.T) {
	tests := []struct {
		name  string
		stage cli.StageRef
		keys  []string
		local int
	}{
		{name: "dev runs at once", stage: ordersDev, keys: []string{"I"}, local: 1},
		{name: "prod asks, no", stage: ordersProd, keys: []string{"I", "n"}},
		{name: "prod asks, yes", stage: ordersProd, keys: []string{"I", "y"}, local: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFake(t)
			m := started(t, f)
			m, cmd := m.openStage(tt.stage)
			m = settle(t, m, cmd)
			m = pressKeys(t, m, "enter")
			m = pressKeys(t, m, tt.keys...)
			if f.local != tt.local || m.page != functionPage {
				t.Fatalf("%d runs here, page %v; want %d on the function", f.local, m.page, tt.local)
			}
		})
	}
}

// TestLogs checks the logs tab shows the lines, f stops following, and scrolling up does too.
func TestLogs(t *testing.T) {
	m := pressKeys(t, started(t, newFake(t)), "enter", "tab")
	if !strings.Contains(screen(m), "order placed") || !m.state.function.logs.follow {
		t.Fatal("the logs tab does not show the logs, or does not follow them")
	}
	m = pressKeys(t, m, "f")
	if m.state.function.logs.follow {
		t.Error("f did not stop following")
	}
	m = pressKeys(t, m, "f", "k")
	if m.state.function.logs.follow {
		t.Error("scrolling up did not stop following")
	}
}

// TestHistory checks the history tab lists the versions, and a version shows what it changed.
func TestHistory(t *testing.T) {
	m := pressKeys(t, started(t, newFake(t)), "shift+tab")
	if m.state.stage.tab != historyTab || len(m.state.stage.history) != 2 {
		t.Fatalf("tab %v with %d versions", m.state.stage.tab, len(m.state.stage.history))
	}
	m = pressKeys(t, m, "enter")
	if v := screen(m); m.page != versionPage || !strings.Contains(v, "+ resource/table") {
		t.Fatalf("page %v; the version does not show the table it added", m.page)
	}
}

// TestStateChanges checks the changes between two versions name what was added, removed and
// changed.
func TestStateChanges(t *testing.T) {
	before, after := testState(ordersDev), testState(ordersDev)
	delete(after.Resources, "role/api")
	after.Resources["resource/queue"] = &state.Resource{PhysicalID: "jobs"}
	after.Resources["resource/table"].Inputs = []byte(`{"properties":{"BillingMode":"PROVISIONED"}}`)
	changes := stateChanges(before, after)
	got := make([]string, 0, len(changes))
	for _, c := range changes {
		var b strings.Builder
		for _, s := range c.line {
			b.WriteString(s.Text)
		}
		got = append(got, b.String())
	}
	want := []string{"+ resource/queue  jobs", "~ resource/table  properties", "- role/api  orders-dev-api"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("changes:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// layoutPages opens every page, with long and wide values in the state.
func layoutPages(t *testing.T, w, h int) map[string]model {
	t.Helper()
	f := newFake(t)
	long := strings.Repeat("very-long-name-", 12)
	st := f.states[ordersDev]
	st.Resources["resource/"+long] = &state.Resource{Type: "AWS::SQS::Queue::" + long, PhysicalID: "https://sqs/" + long + "日本語🚀"}
	st.Outputs[long] = strings.Repeat("https://example.com/", 20)
	f.logs = append(f.logs, cli.LogEvent{Time: time.Now(), Message: strings.Repeat("a long line 漢字 ", 60)})
	f.locks[ordersDev] = &state.LockInfo{Who: long, Where: long, Operation: "deploy", Since: time.Now()}
	f.logs = append(requestLogs(time.Now().Add(-time.Minute)), f.logs...)
	f.live = map[string]any{"BillingMode": "PROVISIONED", long: strings.Repeat("v", 300)}
	f.function = cli.FunctionState{State: "Failed", StateReason: long, LastUpdate: "Failed", LastUpdateReason: long, CodeSHA256: "ff", Memory: 9999}

	m := newModel(t.Context(), f)
	m, _ = update(t, m, tea.WindowSizeMsg{Width: w, Height: h})
	pages := map[string]model{"loading": m}
	m = settle(t, m, m.Init())
	pages["stage"] = m
	for _, k := range []string{"tab", "tab", "tab"} {
		m = pressKeys(t, m, k)
		pages["stage "+stageTabs[m.state.stage.tab]] = m
	}
	m = pressKeys(t, m, "enter")
	pages["version"] = m
	m = pressKeys(t, m, "q", "tab", "tab", "enter")
	pages["resource"] = m
	pages["resource in aws"] = pressKeys(t, m, "tab")
	m = pressKeys(t, m, "q", "tab", "enter")
	pages["function"] = m
	m = pressKeys(t, m, "tab")
	pages["logs"] = m
	inv := pressKeys(t, m, "tab")
	pages["invocations"] = inv
	pages["invocation"] = pressKeys(t, inv, "enter")
	m = pressKeys(t, m, "tab", "i")
	pages["invoke"] = m
	p := pressKeys(t, m, "q", "d")
	pages["plan"] = p
	pages["plan applied"] = pressKeys(t, p, "ctrl+s")
	pages["plan diff"] = pressKeys(t, p, "enter")
	pages["unlock"] = pressKeys(t, m, "q", "ctrl+x")
	pages["remove"] = pressKeys(t, m, "q", "ctrl+d")
	o := pressKeys(t, m, "q", "q")
	pages["overview"] = o
	o.showHelp = true
	pages["help"] = o
	o.showHelp, o.error = false, strings.Repeat("an error with a long description ", 30)
	pages["error"] = o
	o.error = ""
	o, _ = o.setStatus(strings.Repeat("a long status ", 30), 0)
	pages["status"] = o
	return pages
}

// TestLayout draws every page at terminal sizes from below the minimum to large, and checks no
// line is wider than the terminal and there are no more lines than it is high.
func TestLayout(t *testing.T) {
	sizes := [][2]int{{20, 5}, {60, 20}, {78, 22}, {80, 24}, {100, 30}, {120, 40}, {200, 60}}
	for _, size := range sizes {
		w, h := size[0], size[1]
		for name, m := range layoutPages(t, w, h) {
			lines := strings.Split(m.render(), "\n")
			if len(lines) > h {
				t.Errorf("%dx%d %s: %d lines", w, h, name, len(lines))
			}
			for i, line := range lines {
				if lw := ansi.StringWidth(line); lw > w {
					t.Errorf("%dx%d %s: line %d is %d wide", w, h, name, i, lw)
					break
				}
			}
		}
	}
}

// TestEscapesNeverReachTheTerminal puts escape sequences in every string that comes from aws or
// the state, and checks none reaches the view unescaped.
func TestEscapesNeverReachTheTerminal(t *testing.T) {
	evil := "x\x1b]8;;https://evil.example/\x07y\x1b[2J\x9b"
	f := newFake(t)
	f.states = map[cli.StageRef]*state.State{ordersDev: testState(ordersDev)}
	st := f.states[ordersDev]
	st.DeployedBy = evil
	st.Resources["resource/"+evil] = &state.Resource{Type: evil, PhysicalID: evil, Attributes: map[string]string{evil: evil}}
	st.Resources["function/api"].Attributes["Arn"] = evil
	st.Outputs[evil] = evil
	f.locks[ordersDev] = &state.LockInfo{Who: evil, Where: evil, Operation: evil, Since: time.Now()}
	f.logs = []cli.LogEvent{{Time: time.Now(), Message: evil}}
	f.result = cli.Invocation{Payload: []byte(evil), Logs: evil, Error: evil}
	f.plan = &engine.Plan{Changes: []engine.Change{{ID: evil, Action: engine.Update, Fields: []string{evil}, Before: map[string]any{evil: evil}, After: map[string]any{evil: "y"}}}}
	f.live = map[string]any{evil: evil}
	f.function = cli.FunctionState{State: evil, StateReason: evil, LastUpdate: evil, LastUpdateReason: evil, Runtime: evil}
	f.logs = []cli.LogEvent{
		{Time: time.Now(), Stream: evil, Message: "START RequestId: " + evil},
		{Time: time.Now(), Stream: evil, Message: "2026-11-05T10:00:00.001Z\t" + evil + "\tERROR\t" + evil},
		{Time: time.Now(), Stream: evil, Message: `{"msg":"` + evil + `","level":"error"}`},
		{Time: time.Now(), Stream: evil, Message: "msg=" + evil + " level=error"},
	}

	check := func(name string, m model) {
		t.Helper()
		v := m.render()
		if strings.Contains(v, "\x1b]") || strings.Contains(v, "\x07") || strings.Contains(v, "\x1b[2J") || strings.ContainsRune(v, '\u009b') {
			t.Errorf("%s: an escape sequence reached the view", name)
		}
	}
	m := started(t, f)
	check("stage", m)
	for _, k := range []string{"tab", "tab"} {
		m = pressKeys(t, m, k)
		check("stage "+stageTabs[m.state.stage.tab], m)
	}
	r := pressKeys(t, m, "tab", "tab", "enter")
	check("resource", r)
	fn := pressKeys(t, m, "tab", "tab", "enter")
	check("function", fn)
	check("overview", fn)
	check("logs", pressKeys(t, fn, "tab"))
	inv := pressKeys(t, fn, "tab", "tab")
	check("invocations", inv)
	check("invocation", pressKeys(t, inv, "enter"))
	check("resource in aws", pressKeys(t, r, "tab"))
	check("plan diff", pressKeys(t, m, "d", "enter"))
	check("invoke", pressKeys(t, fn, "tab", "tab", "i"))
	check("unlock", pressKeys(t, m, "ctrl+x"))
	check("plan", pressKeys(t, m, "d"))
	o := pressKeys(t, m, "q")
	check("overview", o)
	o.error = evil
	check("error", o)
	o.error = ""
	o, _ = o.setStatus(evil, 0)
	check("status", o)
}
