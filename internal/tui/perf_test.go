package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"

	"github.com/kontrolplane/koldstart/internal/cli"
	"github.com/kontrolplane/koldstart/internal/engine"
	"github.com/kontrolplane/koldstart/internal/provider/awsp"
	"github.com/kontrolplane/koldstart/internal/state"
	"github.com/kontrolplane/koldstart/internal/tui/keys"
	"github.com/kontrolplane/koldstart/internal/tui/styles"
)

// The size of the big fixture: how many apps and stages of each, the functions and resources of
// the big stage, its outputs, the lines of the logs, the invocations among them, and the changes
// of a plan.
const (
	perfApps        = 40
	perfStagesEach  = 5
	perfFunctions   = 300
	perfResources   = 2000
	perfOutputs     = 40
	perfOutputBytes = 10 << 10
	perfInvocations = 1000
	perfLogLines    = 5000
	perfChanges     = 1500
	perfPayload     = 2 << 20
)

// perfBackend is the fake with every stage of the big fixture listed, not only the three of the
// small one.
type perfBackend struct {
	*fakeBackend
	refs []cli.StageRef
}

// Stages lists every stage of the big fixture.
func (p *perfBackend) Stages(context.Context) ([]cli.StageSummary, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]cli.StageSummary, 0, len(p.refs))
	for _, ref := range p.refs {
		st := p.states[ref]
		if st == nil {
			continue
		}
		out = append(out, cli.StageSummary{StageRef: ref, DeployedAt: st.DeployedAt, DeployedBy: st.DeployedBy, Resources: len(st.Resources), Lock: p.locks[ref]})
	}
	return out, p.err
}

// perfID is a request id, as lambda writes one, for the i-th invocation.
func perfID(i int) string {
	return fmt.Sprintf("%08x-0000-4000-8000-%012x", i, i)
}

// perfFake is a backend at the scale of a big account: 40 apps of 5 stages, orders/dev with 300
// functions among 2000 resources and outputs of 10 kb, 5000 lines of logs holding 1000
// invocations, a plan of 1500 changes and an invocation that returns 2 mb.
func perfFake(tb testing.TB) *perfBackend {
	tb.Helper()
	f := &fakeBackend{
		app: "orders", stage: "dev", events: tb.TempDir(),
		states: map[cli.StageRef]*state.State{},
		locks:  map[cli.StageRef]*state.LockInfo{},
	}
	p := &perfBackend{fakeBackend: f}
	for a := range perfApps {
		app := fmt.Sprintf("app-%02d", a)
		if a == 0 {
			app = "orders"
		}
		for s := range perfStagesEach {
			stage := []string{"dev", "prod", "staging", "qa", "perf"}[s]
			ref := cli.StageRef{Region: "eu-west-1", App: app, Stage: stage}
			p.refs = append(p.refs, ref)
			f.states[ref] = testState(ref)
			if a%7 == 3 {
				f.locks[ref] = &state.LockInfo{Who: "levi", Operation: "deploy", Where: "laptop", Since: time.Now()}
			}
		}
	}
	f.states[ordersDev] = perfState(ordersDev)
	f.logs = perfLogs(time.Now().Add(-20 * time.Minute))
	f.history = perfHistory()
	f.result = perfInvocation()
	f.plan = perfPlan()
	f.live = perfLive()
	f.function = cli.FunctionState{State: "Active", LastUpdate: "Successful", Memory: 1024, CodeSHA256: strings.Repeat("ab", 32)}
	dir := f.EventsDir("api")
	events := map[string][]byte{"big.json": f.result.Payload, "broken.json": append([]byte("{\n"), f.result.Payload...), "small.json": []byte(`{"id":1}`)}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		tb.Fatal(err)
	}
	for name, data := range events {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
			tb.Fatal(err)
		}
	}
	return p
}

// perfState is the big stage: 300 functions, each with its role and log group, a queue for every
// third, and resources to make 2000, with outputs of 10 kb, some on many lines.
func perfState(ref cli.StageRef) *state.State {
	st := testState(ref)
	for i := range perfFunctions - 1 {
		name := fmt.Sprintf("fn-%03d", i)
		physical := ref.App + "-" + ref.Stage + "-" + name
		in, _ := json.Marshal(map[string]any{
			"name": physical, "runtime": "nodejs22.x", "handler": "src/" + name + ".handler", "memory": 512, "timeout": 10,
			"role": "arn:aws:iam::123456789012:role/" + physical, "environment": map[string]any{"TABLE": "orders", "STAGE": ref.Stage},
			"code": map[string]any{"bucket": "b", "key": "k", "sha256": strings.Repeat("cd", 32)},
		})
		st.Resources["function/"+name] = &state.Resource{Type: awsp.TypeFunction, PhysicalID: physical, Inputs: in,
			Attributes: map[string]string{"Arn": "arn:aws:lambda:eu-west-1:123456789012:function:" + physical}}
		st.Resources["role/"+name] = &state.Resource{Type: awsp.TypeRole, PhysicalID: physical}
		st.Resources["loggroup/"+name] = &state.Resource{Type: awsp.TypeLogGroup, PhysicalID: "/aws/lambda/" + physical}
	}
	for i := 0; len(st.Resources) < perfResources; i++ {
		id := fmt.Sprintf("resource/queue-%04d", i)
		typ, physical := "AWS::SQS::Queue", "https://sqs.eu-west-1.amazonaws.com/123456789012/"+ref.App+"-queue-"+fmt.Sprint(i)
		if i%3 != 0 {
			id, typ, physical = fmt.Sprintf("resource/table-%04d", i), "AWS::DynamoDB::Table", fmt.Sprintf("%s-table-%04d", ref.App, i)
		}
		st.Resources[id] = &state.Resource{Type: typ, PhysicalID: physical, Inputs: json.RawMessage(`{"properties":{"BillingMode":"PAY_PER_REQUEST"}}`)}
	}
	value := strings.Repeat("https://example.com/a/very/long/path?", perfOutputBytes/37)
	for i := range perfOutputs {
		v := value
		if i%4 == 0 {
			v = strings.Repeat("a line of a certificate, 64 characters wide, or near enough...\n", perfOutputBytes/64)
		}
		st.Outputs[fmt.Sprintf("output%02d", i)] = v
	}
	big := map[string]any{}
	for i := range 400 {
		big[fmt.Sprintf("Key%03d", i)] = strings.Repeat("value ", 4)
	}
	big["Policy"] = strings.Repeat("x", perfOutputBytes)
	in, _ := json.Marshal(map[string]any{"properties": big})
	st.Resources["resource/table"].Inputs = in
	return st
}

// perfLogs are 1000 invocations, each a line of node, json or logfmt between its START and
// REPORT, every tenth long enough to wrap, and the newest with the rest of the 5000 lines.
func perfLogs(at time.Time) []cli.LogEvent {
	out := make([]cli.LogEvent, 0, perfLogLines)
	t := at
	line := func(stream, msg string) {
		t = t.Add(200 * time.Millisecond)
		out = append(out, cli.LogEvent{Time: t, Stream: stream, Message: msg})
	}
	body := func(i int, id string) string {
		msg := fmt.Sprintf("order %d placed", i)
		if i%10 == 0 {
			msg = strings.Repeat("a long message, with commas, ", 20)
		}
		switch i % 3 {
		case 0:
			return "2026-11-05T10:00:00.001Z\t" + id + "\tINFO\t" + `{"msg":"` + msg + `","order":{"id":1,"items":[1,2,3]}}`
		case 1:
			return `time=2026-11-05T10:00:00Z level=warn msg="` + msg + `" took=12ms`
		}
		return `{"level":"error","msg":"` + msg + `"}`
	}
	for i := range perfInvocations {
		id, stream := perfID(i), fmt.Sprintf("stream-%d", i%10)
		line(stream, "START RequestId: "+id+" Version: $LATEST")
		n := 1
		if i == perfInvocations-1 {
			n = perfLogLines - len(out) - 2
		}
		for j := range n {
			line(stream, body(i+j, id))
		}
		line(stream, "END RequestId: "+id)
		line(stream, "REPORT RequestId: "+id+"\tDuration: 12.50 ms\tBilled Duration: 13 ms\tMemory Size: 256 MB\tMax Memory Used: 70 MB")
	}
	return out
}

// perfHistory is 600 versions of the state, three a deploy.
func perfHistory() []state.ObjectVersion {
	out := make([]state.ObjectVersion, 600)
	now := time.Now()
	for i := range out {
		out[i] = state.ObjectVersion{ID: fmt.Sprintf("v%04d", 600-i), LastModified: now.Add(-time.Duration(i/3)*time.Hour - time.Duration(i%3)*time.Second), Latest: i == 0}
	}
	return out
}

// perfInvocation returns 2 mb of json, and a log of 300 lines.
func perfInvocation() cli.Invocation {
	var items []map[string]any
	size := 0
	for i := 0; size < perfPayload; i++ {
		item := map[string]any{"id": i, "name": fmt.Sprintf("item %d", i), "tags": []string{"a", "b", "c"}, "note": strings.Repeat("n", 40)}
		items = append(items, item)
		size += 110
	}
	payload, _ := json.Marshal(map[string]any{"statusCode": 200, "items": items})
	var logs strings.Builder
	for i := range 300 {
		fmt.Fprintf(&logs, "2026-11-05T10:00:00.001Z\t%s\tINFO\t{\"msg\":\"step %d\"}\n", perfID(0), i)
	}
	return cli.Invocation{Payload: payload, Logs: logs.String(), Duration: time.Second}
}

// perfPlan is 1500 changes: creates, updates of fields and properties, deletes and ones that
// change nothing, the first update with inputs of 10 kb.
func perfPlan() *engine.Plan {
	p := &engine.Plan{App: "orders", Stage: "dev"}
	for i := range perfChanges {
		id := fmt.Sprintf("resource/table-%04d", i)
		c := engine.Change{ID: id, Type: "AWS::DynamoDB::Table"}
		switch i % 5 {
		case 0, 1:
			c.Action = engine.Create
			c.After = map[string]any{"properties": map[string]any{"BillingMode": "PAY_PER_REQUEST"}}
		case 2:
			c.Action = engine.Update
			c.Fields = []string{"properties"}
			c.Before = map[string]any{"properties": map[string]any{"BillingMode": "PROVISIONED", "Tags": []any{"a"}}}
			c.After = map[string]any{"properties": map[string]any{"BillingMode": "PAY_PER_REQUEST", "Tags": []any{"a", "b"}}}
			if i%50 == 2 {
				c.Drift = []string{"BillingMode"}
			}
		case 3:
			c.Action = engine.Delete
		default:
			c.Action = engine.NoOp
		}
		p.Changes = append(p.Changes, c)
	}
	before, after := map[string]any{}, map[string]any{}
	for i := range 500 {
		k := fmt.Sprintf("Key%03d", i)
		before[k] = strings.Repeat("before ", 3)
		after[k] = strings.Repeat("before ", 3)
		if i%7 == 0 {
			after[k] = strings.Repeat("after ", 3)
		}
	}
	before["Policy"], after["Policy"] = strings.Repeat("p", perfOutputBytes), strings.Repeat("q", perfOutputBytes)
	p.Changes[2].Before, p.Changes[2].After = map[string]any{"properties": before}, map[string]any{"properties": after}
	return p
}

// perfLive is what cloud control says of the big table: 400 properties and one of 10 kb.
func perfLive() map[string]any {
	out := map[string]any{"BillingMode": "PROVISIONED"}
	for i := range 400 {
		out[fmt.Sprintf("Key%03d", i)] = strings.Repeat("value ", 4)
	}
	out["Policy"] = strings.Repeat("y", perfOutputBytes)
	return out
}

func perfPress(s string) tea.KeyPressMsg {
	codes := map[string]rune{"pgdown": tea.KeyPgDown, "pgup": tea.KeyPgUp, "up": tea.KeyUp, "down": tea.KeyDown, "home": tea.KeyHome, "end": tea.KeyEnd}
	if code, ok := codes[s]; ok {
		return tea.KeyPressMsg{Code: code}
	}
	return press(s)
}

func perfUpdate(m model, msg tea.Msg) (model, tea.Cmd) {
	next, cmd := m.Update(msg)
	return next.(model), cmd
}

// perfSettle runs a command and feeds what it returns back in, until nothing more comes, as
// settle does.
func perfSettle(m model, cmd tea.Cmd) model {
	for range 20 {
		msgs := run(cmd)
		if len(msgs) == 0 {
			return m
		}
		var cmds []tea.Cmd
		for _, msg := range msgs {
			var next tea.Cmd
			m, next = perfUpdate(m, msg)
			cmds = append(cmds, next)
		}
		cmd = tea.Batch(cmds...)
	}
	return m
}

// perfRun runs a command and the commands of a batch, as run does, waiting up to wait for each:
// an invocation that returns 2 mb takes longer than run waits under the race detector.
func perfRun(cmd tea.Cmd, wait time.Duration) []tea.Msg {
	if cmd == nil {
		return nil
	}
	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()
	var msg tea.Msg
	select {
	case msg = <-done:
	case <-time.After(wait):
		return nil
	}
	switch msg := msg.(type) {
	case nil, spinner.TickMsg, refreshTickMsg, statusClearMsg:
		return nil
	case tea.BatchMsg:
		var out []tea.Msg
		for _, c := range msg {
			out = append(out, perfRun(c, wait)...)
		}
		return out
	}
	return []tea.Msg{msg}
}

// perfInvoke presses i, waits for the invocation, and settles what it brings.
func perfInvoke(m model) model {
	m, cmd := perfUpdate(m, perfPress("i"))
	for _, msg := range perfRun(cmd, time.Minute) {
		var next tea.Cmd
		m, next = perfUpdate(m, msg)
		m = perfSettle(m, next)
	}
	return m
}

// perfKeys presses keys one by one, settling what each starts.
func perfKeys(m model, pressed ...string) model {
	for _, k := range pressed {
		var cmd tea.Cmd
		m, cmd = perfUpdate(m, perfPress(k))
		m = perfSettle(m, cmd)
	}
	return m
}

// perfPages opens every heavy page of the big fixture at w by h.
func perfPages(tb testing.TB, w, h int) map[string]model {
	tb.Helper()
	p := perfFake(tb)
	m := newModel(context.Background(), p)
	m, _ = perfUpdate(m, tea.WindowSizeMsg{Width: w, Height: h})
	m = perfSettle(m, m.Init())
	pages := map[string]model{}
	pages["stage functions"] = m
	m = perfKeys(m, "tab")
	pages["stage resources"] = m
	m = perfKeys(m, "tab")
	pages["stage outputs"] = m
	m = perfKeys(m, "tab")
	pages["stage history"] = m
	pages["version"] = perfKeys(m, "enter")
	res := perfKeys(m, "tab", "tab")
	for r, ok := res.state.stage.selectedRow(); ok && (r.res < 0 || res.state.stage.resources[r.res].id != "resource/table") && res.state.stage.tables[resourcesTab].cursor < len(res.state.stage.tree)-1; r, ok = res.state.stage.selectedRow() {
		res = perfKeys(res, "down")
	}
	res = perfKeys(res, "enter")
	pages["resource"] = res
	pages["resource in aws"] = perfKeys(res, "tab")
	fn := perfKeys(m, "tab", "enter")
	pages["function"] = fn
	logs := perfKeys(fn, "tab")
	pages["logs"] = logs
	inv := perfKeys(logs, "tab")
	pages["invocations"] = inv
	pages["invocation"] = perfKeys(inv, "enter")
	invoke := perfInvoke(perfKeys(inv, "tab"))
	pages["invoke"] = invoke
	pages["invoke event"] = perfKeys(invoke, "down")
	pages["invoke payload"] = perfKeys(invoke, "v")
	plan := perfKeys(fn, "d")
	pages["plan"] = plan
	pages["plan diff"] = perfKeys(plan, "down", "down", "enter")
	pages["overview"] = perfKeys(fn, "q", "q")
	return pages
}

// TestPerfPages checks the big fixture opens every page it means to, so the benchmarks measure
// what they say. A read of more than cli.MaxLogEvents lines drops the first millisecond, so one
// line of the logs is left out.
func TestPerfPages(t *testing.T) {
	if testing.Short() {
		t.Skip("the big fixture takes a while")
	}
	want := map[string]page{
		"overview": overviewPage, "stage functions": stagePage, "stage resources": stagePage, "stage outputs": stagePage,
		"stage history": stagePage, "version": versionPage, "resource": resourcePage, "resource in aws": resourcePage,
		"function": functionPage, "logs": functionPage, "invocations": functionPage, "invocation": invocationPage,
		"invoke": functionPage, "invoke event": functionPage, "invoke payload": functionPage, "plan": planPage, "plan diff": diffPage,
	}
	pages := perfPages(t, 200, 60)
	for name, p := range want {
		m, ok := pages[name]
		switch {
		case !ok:
			t.Errorf("no page %s", name)
		case m.page != p:
			t.Errorf("%s is on page %d, want %d", name, m.page, p)
		case m.loading || m.error != "":
			t.Errorf("%s is loading %v or failed: %s", name, m.loading, m.error)
		}
	}
	checks := []struct {
		name string
		got  int
		want int
	}{
		{"stages", len(pages["overview"].state.overview.shown), perfApps * perfStagesEach},
		{"functions", len(pages["stage functions"].state.stage.functions), perfFunctions},
		{"resources", len(pages["stage resources"].state.stage.resources), perfResources},
		{"log lines", len(pages["logs"].state.function.logs.lines), perfLogLines - 1},
		{"invocations", len(pages["invocations"].state.function.requests), perfInvocations},
		{"changes", len(pages["plan"].planChanges()), perfChanges * 4 / 5},
		{"lines of the invocation", len(pages["invocation"].state.invocation.req.lines), perfLogLines - 4*(perfInvocations-1)},
		{"bytes returned", len(pages["invoke"].state.function.results["big.json"].inv.Payload), len(perfInvocation().Payload)},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%d %s, want %d", c.got, c.name, c.want)
		}
	}
	if got := pages["plan diff"].state.diff.title; got != "resource/table-0002" {
		t.Errorf("the diff is of %s, want the change with inputs of 10 kb", got)
	}
	if got := pages["invoke event"].selectedEvent(); got != "broken.json" {
		t.Errorf("the event chosen is %s, want broken.json", got)
	}
	if p := pages["invoke payload"].state.function.pane; p.result || p.view.empty() || p.event != "big.json" {
		t.Errorf("the pane shows the result %v, or the payload of %q, want the payload of big.json", p.result, p.event)
	}
	if got := pages["resource"].state.resource.res.id; got != "resource/table" {
		t.Errorf("the resource is %s, want resource/table", got)
	}
}

// perfFresh is a page with every cache of what it drew emptied, so it draws everything anew.
func perfFresh(m model) model {
	m.state.function.logs.cache = newRowCache()
	m.state.invocation.cache = &invocationCache{}
	results := make(map[string]invocationResult, len(m.state.function.results))
	for k, r := range m.state.function.results {
		r.logs = &wrappedLog{}
		r.view.cache = &jsonCache{}
		results[k] = r
	}
	m.state.function.results = results
	m.state.function.pane.view.cache = &jsonCache{}
	m.state.diff.wrapped = &diffRows{}
	m.state.resource.applied.cache = &jsonCache{}
	m.state.resource.props.cache = &jsonCache{}
	return m
}

// TestPerfCachesFollowThePalette draws every page that keeps what it drew, changes the theme or
// the colours of the terminal, and checks each page then draws as it does with nothing kept, and,
// but without colour, where a page can draw as it did, otherwise than before.
func TestPerfCachesFollowThePalette(t *testing.T) {
	tests := []struct {
		name    string
		dark    bool
		profile colorprofile.Profile
	}{
		{name: "light", dark: false, profile: colorprofile.TrueColor},
		{name: "256 colours", dark: true, profile: colorprofile.ANSI256},
		{name: "16 colours", dark: false, profile: colorprofile.ANSI},
		{name: "no colour", dark: true, profile: colorprofile.NoTTY},
	}
	pages := layoutPages(t, 120, 40)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, name := range []string{"logs", "invocation", "invoke", "plan diff", "resource", "resource in aws"} {
				m := pages[name]
				before := m.render()
				themeUse(t, tt.dark, false, tt.profile)
				got, want := m.render(), perfFresh(m).render()
				switch {
				case got == before && tt.profile != colorprofile.NoTTY:
					t.Errorf("%s draws alike in both palettes, so the check checks nothing", name)
				case got != want:
					t.Errorf("%s draws what it kept from the palette before", name)
				}
				styles.Use(true)
				styles.UseProfile(colorprofile.TrueColor)
			}
		})
	}
}

// perfBenchPages are the pages the benchmarks draw, at 200 by 60, built once.
var perfBenchPages map[string]model

func perfBench(b *testing.B) map[string]model {
	b.Helper()
	if perfBenchPages == nil {
		perfBenchPages = perfPages(b, 200, 60)
	}
	return perfBenchPages
}

// perfPageNames are the pages benchmarked, in order.
var perfPageNames = []string{
	"overview", "stage functions", "stage resources", "stage outputs", "stage history", "version", "resource",
	"resource in aws", "function", "logs", "invocations", "invocation", "invoke", "invoke event", "invoke payload", "plan", "plan diff",
}

// BenchmarkView draws a frame of each heavy page at 200 by 60.
func BenchmarkView(b *testing.B) {
	pages := perfBench(b)
	for _, name := range perfPageNames {
		m := pages[name]
		b.Run(name, func(b *testing.B) {
			for b.Loop() {
				_ = m.View()
			}
		})
	}
}

// BenchmarkLongLines tints, marks and wraps lines of 64 kb that cut into many spans, which took
// time in the square of their length while a span grew by copying, or a cut walked every span.
func BenchmarkLongLines(b *testing.B) {
	lines := map[string]string{
		"json of brackets": "{" + strings.Repeat("[", 64<<10),
		"json of strings":  "{" + strings.Repeat(`"a",`, 16<<10) + "}",
		"logfmt":           strings.Repeat("a=b ", 16<<10),
		"node":             "2026-11-05T10:00:00.001Z\t1\tINFO\t" + strings.Repeat(`{"a":1}`, 8<<10),
	}
	for name, line := range lines {
		b.Run(name, func(b *testing.B) {
			for b.Loop() {
				_ = markedLogCells(line, "a", 120)
			}
		})
	}
}

// BenchmarkTail takes ten new lines from the live tail on the logs page, and draws the frame
// after them.
func BenchmarkTail(b *testing.B) {
	m := perfBench(b)["logs"]
	m.state.function.logs.tailGen = 1
	at := m.state.function.logs.lines[len(m.state.function.logs.lines)-1].Time
	lines := perfLogs(at)[:10]
	for b.Loop() {
		next, _ := perfUpdate(m, tailMsg{gen: 1, lines: lines})
		_ = next.View()
	}
}

// BenchmarkKeys takes a key on each heavy page and draws the frame after it: a row down, a page
// down, the end, opening the filter and typing into it.
func BenchmarkKeys(b *testing.B) {
	pages := perfBench(b)
	for _, name := range perfPageNames {
		m := pages[name]
		for _, k := range []string{"j", "pgdown", "g", "G", "/"} {
			b.Run(name+"/"+k, func(b *testing.B) {
				for b.Loop() {
					next, _ := perfUpdate(m, perfPress(k))
					_ = next.View()
				}
			})
		}
		if m.page == overviewPage || m.page == stagePage || m.state.function.tab == logsTab && m.page == functionPage {
			typing, _ := perfUpdate(m, perfPress("/"))
			b.Run(name+"/type", func(b *testing.B) {
				for b.Loop() {
					next, _ := perfUpdate(typing, perfPress("a"))
					_ = next.View()
				}
			})
		}
	}
}

// perfJSONLines is a document of ten thousand lines: an object of as many keys, some of them
// values long enough to wrap.
func perfJSONLines() []byte {
	doc := map[string]any{}
	for i := range 10_000 {
		v := any(i)
		switch i % 4 {
		case 1:
			v = fmt.Sprintf("arn:aws:lambda:eu-west-1:123456789012:function:orders-dev-%d", i)
		case 2:
			v = strings.Repeat("a value that wraps ", 12)
		}
		doc[fmt.Sprintf("Key%05d", i)] = v
	}
	data, _ := json.Marshal(doc)
	return data
}

// BenchmarkJSONView reads, draws and moves through a json view of 2 mb, an invocation's payload of
// some 19,000 items, and of 10,000 lines, at 200 by 60: reading it, a frame, a frame after a key,
// unfolding everything, and typing a search.
func BenchmarkJSONView(b *testing.B) {
	docs := map[string][]byte{"2 mb": perfInvocation().Payload, "10k lines": perfJSONLines()}
	for name, data := range docs {
		b.Run(name+"/read", func(b *testing.B) {
			for b.Loop() {
				v := newJSONViewBytes(data)
				_ = v.view(200, 60)
			}
		})
		v := newJSONViewBytes(data)
		_ = v.view(200, 60)
		b.Run(name+"/frame", func(b *testing.B) {
			for b.Loop() {
				_ = v.view(200, 60)
			}
		})
		for _, k := range []string{"j", "pgdown", "G"} {
			b.Run(name+"/"+k, func(b *testing.B) {
				for b.Loop() {
					next := v
					next.update(perfPress(k), keys.Keys, 200, 60)
					_ = next.view(200, 60)
				}
			})
		}
		b.Run(name+"/unfold all", func(b *testing.B) {
			for b.Loop() {
				open := v
				open.update(perfPress("z"), keys.Keys, 200, 60)
				_ = open.view(200, 60)
			}
		})
		open := v
		open.update(perfPress("z"), keys.Keys, 200, 60)
		_ = open.view(200, 60)
		b.Run(name+"/unfolded frame", func(b *testing.B) {
			for b.Loop() {
				next := open
				next.update(perfPress("G"), keys.Keys, 200, 60)
				_ = next.view(200, 60)
			}
		})
		typing := open
		typing.update(perfPress("/"), keys.Keys, 200, 60)
		b.Run(name+"/search", func(b *testing.B) {
			for b.Loop() {
				next := typing
				next.update(perfPress("q"), keys.Keys, 200, 60)
				_ = next.view(200, 60)
			}
		})
	}
}
