package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/kontrolplane/koldstart/internal/cli"
	"github.com/kontrolplane/koldstart/internal/engine"
	"github.com/kontrolplane/koldstart/internal/state"
)

// requestLogs are two invocations of a node function on one instance, the second failing, and a
// cold start on another, as lambda writes them.
func requestLogs(at time.Time) []cli.LogEvent {
	line := func(d time.Duration, stream, msg string) cli.LogEvent {
		return cli.LogEvent{Time: at.Add(d), Stream: stream, Message: msg}
	}
	return []cli.LogEvent{
		line(0, "a", "START RequestId: 11111111-1111-1111-1111-111111111111 Version: $LATEST"),
		line(1*time.Millisecond, "a", "2026-11-05T10:00:00.001Z\t11111111-1111-1111-1111-111111111111\tINFO\torder placed"),
		line(2*time.Millisecond, "a", "END RequestId: 11111111-1111-1111-1111-111111111111"),
		line(3*time.Millisecond, "a", "REPORT RequestId: 11111111-1111-1111-1111-111111111111\tDuration: 12.50 ms\tBilled Duration: 13 ms\tMemory Size: 256 MB\tMax Memory Used: 70 MB"),
		line(time.Second, "a", "START RequestId: 22222222-2222-2222-2222-222222222222 Version: $LATEST"),
		line(time.Second+time.Millisecond, "a", `2026-11-05T10:00:01.001Z	22222222-2222-2222-2222-222222222222	ERROR	{"errorType":"Error","errorMessage":"no order 9"}`),
		line(time.Second+2*time.Millisecond, "a", "END RequestId: 22222222-2222-2222-2222-222222222222"),
		line(time.Second+3*time.Millisecond, "a", "REPORT RequestId: 22222222-2222-2222-2222-222222222222\tDuration: 5.00 ms\tBilled Duration: 5 ms\tMemory Size: 256 MB\tMax Memory Used: 71 MB"),
		line(2*time.Second, "b", "START RequestId: 33333333-3333-3333-3333-333333333333 Version: $LATEST"),
		line(2*time.Second+time.Millisecond, "b", `{"time":"2026-11-05T10:00:02Z","level":"INFO","msg":"stored an order"}`),
		line(2*time.Second+2*time.Millisecond, "b", "END RequestId: 33333333-3333-3333-3333-333333333333"),
		line(2*time.Second+3*time.Millisecond, "b", "REPORT RequestId: 33333333-3333-3333-3333-333333333333\tDuration: 30.00 ms\tBilled Duration: 31 ms\tMemory Size: 256 MB\tMax Memory Used: 80 MB\tInit Duration: 150.20 ms"),
	}
}

// TestLineIDs checks the request a line starts, reports or names is read from lambda's START and
// REPORT and a line of node, and nothing from a line like them that is not one.
func TestLineIDs(t *testing.T) {
	const id = "11111111-1111-1111-1111-111111111111"
	tests := []struct {
		name                string
		line                string
		start, report, node string
	}{
		{name: "start", line: "START RequestId: " + id + " Version: $LATEST", start: id},
		{name: "report", line: "REPORT RequestId: " + id + "\tDuration: 1.00 ms", report: id},
		{name: "node", line: "2026-11-05T10:00:00.001Z\t" + id + "\tINFO\thi", node: id},
		{name: "start without an id", line: "START RequestId:  x"},
		{name: "node without a tab after the id", line: "2026-11-05T10:00:00.001Z\t" + id + " INFO hi"},
		{name: "node with a short id", line: "2026-11-05T10:00:00.001Z\t1111\tINFO\thi"},
		{name: "node with words before the tab", line: "a b\t" + id + "\tINFO\thi"},
		{name: "node with an id of capitals", line: "t\t" + strings.ToUpper("aaaaaaaa-1111-1111-1111-111111111111") + "\tINFO\thi"},
		{name: "plain", line: "order placed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			start, report, node := lineIDs(tt.line)
			if start != tt.start || report != tt.report || node != tt.node {
				t.Errorf("lineIDs = %q %q %q, want %q %q %q", start, report, node, tt.start, tt.report, tt.node)
			}
		})
	}
}

// TestReportItems checks the numbers of a REPORT and an INIT_REPORT line are read by their label,
// whether tabs or spaces separate them.
func TestReportItems(t *testing.T) {
	tests := []struct {
		name string
		line string
		want map[string]float64
	}{
		{
			name: "tabs",
			line: "REPORT RequestId: r\tDuration: 12.50 ms\tBilled Duration: 13 ms\tMemory Size: 256 MB\tMax Memory Used: 70 MB\tInit Duration: 150.20 ms\tStatus: timeout",
			want: map[string]float64{"Duration": 12.5, "Billed Duration": 13, "Memory Size": 256, "Max Memory Used": 70, "Init Duration": 150.2},
		},
		{
			name: "spaces",
			line: "REPORT RequestId: r Duration: 12.50 ms Billed Duration: 13 ms Memory Size: 256 MB Max Memory Used: 70 MB",
			want: map[string]float64{"Duration": 12.5, "Billed Duration": 13, "Memory Size": 256, "Max Memory Used": 70},
		},
		{
			name: "init report",
			line: "INIT_REPORT Init Duration: 10005.31 ms\tPhase: init\tStatus: timeout",
			want: map[string]float64{"Init Duration": 10005.31},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := map[string]float64{}
			for label, v := range reportItems(tt.line) {
				got[label] = v
			}
			if fmt.Sprint(got) != fmt.Sprint(tt.want) {
				t.Errorf("reportItems = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestRequests checks the invocations are read out of the log, newest first, each with its
// lines, how long it took, whether it was a cold start, and whether it failed.
func TestRequests(t *testing.T) {
	got := requests(requestLogs(time.Now()))
	if len(got) != 3 {
		t.Fatalf("%d requests, want 3", len(got))
	}
	cold, failed, ok := got[0], got[1], got[2]
	tests := []struct {
		name string
		r    request
		id   string
		ms   float64
		init float64
		fail bool
		n    int
	}{
		{name: "cold start", r: cold, id: "33333333", ms: 30, init: 150.2, n: 4},
		{name: "failed", r: failed, id: "22222222", ms: 5, fail: true, n: 4},
		{name: "ok", r: ok, id: "11111111", ms: 12.5, n: 4},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := tt.r
			if !strings.HasPrefix(r.id, tt.id) || r.duration != tt.ms || r.init != tt.init || r.failed != tt.fail || len(r.lines) != tt.n || !r.reported {
				t.Errorf("got %+v", r)
			}
		})
	}
}

// TestSparkline checks a series is drawn scaled to its largest point, an empty period blank.
func TestSparkline(t *testing.T) {
	if got := sparkline([]float64{0, 1, 2, 4, 8}, 5); got != " ▁▂▄█" {
		t.Errorf("sparkline = %q", got)
	}
	if got := sparkline(make([]float64, 60), 10); got != strings.Repeat(" ", 10) {
		t.Errorf("sparkline of nothing = %q", got)
	}
}

// TestResourceTree checks the resources tab puts each node under the function or resource it
// belongs to, with a queue's depth.
func TestResourceTree(t *testing.T) {
	f := newFake(t)
	st := f.states[ordersDev]
	st.Resources["resource/jobs"] = &state.Resource{Type: "AWS::SQS::Queue", PhysicalID: "https://sqs/jobs"}
	st.Resources["httpapi"] = &state.Resource{Type: "koldstart::ApiGatewayV2::Api", PhysicalID: "abc"}
	st.Resources["integration/api"] = &state.Resource{Type: "koldstart::ApiGatewayV2::Integration", PhysicalID: "i"}
	st.Resources["route/GET /orders"] = &state.Resource{Type: "koldstart::ApiGatewayV2::Route", PhysicalID: "r", Dependencies: []string{"httpapi", "integration/api"}}
	m := pressKeys(t, started(t, f), "tab")
	got := make([]string, 0, len(m.state.stage.resources))
	for _, r := range m.state.stage.resources {
		got = append(got, strings.Repeat("  ", r.depth)+r.id)
	}
	want := []string{
		"function/api", "  integration/api", "  loggroup/api", "  role/api", "  route/GET /orders",
		"resource/jobs", "resource/table", "httpapi",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("tree:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if v := screen(m); !strings.Contains(v, "└─ route/GET /orders") || !strings.Contains(v, "12 queued · 3 in flight") {
		t.Errorf("the tree or the queue's depth is not drawn:\n%s", v)
	}
}

// TestFunctionOverview checks the overview shows the last hour's metrics, what lambda says of the
// function now, and code changed outside koldstart; and the stage's functions their totals.
func TestFunctionOverview(t *testing.T) {
	f := newFake(t)
	f.function = cli.FunctionState{State: "Active", LastUpdate: "Successful", LastModified: time.Now(), CodeSize: 4 << 20, CodeSHA256: "ff", Memory: 2048, Timeout: 6, Runtime: "nodejs22.x"}
	m := started(t, f)
	if v := screen(m); !strings.Contains(v, "last hour") || !strings.Contains(v, "1,740") {
		t.Errorf("the functions tab does not show the calls of the last hour:\n%s", v)
	}
	m = pressKeys(t, m, "enter")
	v := screen(m)
	for _, want := range []string{"last hour", "invocations", "█", "active", "changed outside koldstart", "memory 1,024 → 2,048 mb", "4.0 mb"} {
		if !strings.Contains(v, want) {
			t.Errorf("the overview has no %q:\n%s", want, v)
		}
	}
}

// TestMetricsUnavailable checks a region without metrics, as an emulator, says so once and
// raises nothing.
func TestMetricsUnavailable(t *testing.T) {
	f := newFake(t)
	f.metricsErr = errAWS
	m := pressKeys(t, started(t, f), "enter")
	if m.error != "" || !strings.Contains(screen(m), "could not read the metrics: aws said no") {
		t.Errorf("error %q; screen:\n%s", m.error, screen(m))
	}
}

// TestLogsTail checks a live tail adds its lines, and that without one the logs are read again
// on every refresh.
func TestLogsTail(t *testing.T) {
	f := newFake(t)
	f.tail = []cli.LogEvent{{Time: time.Now(), Message: "from the tail"}}
	m := pressKeys(t, started(t, f), "enter", "tab")
	if !m.state.function.logs.tailing || !strings.Contains(screen(m), "from the tail") || !strings.Contains(screen(m), "live tail") {
		t.Errorf("the tail's line is not shown:\n%s", screen(m))
	}
	m = m.stopTail()

	f = newFake(t)
	m = pressKeys(t, started(t, f), "enter", "tab")
	if !m.state.function.logs.noTail || m.state.function.logs.tailing {
		t.Fatal("without a live tail, it should fall back to reading again")
	}
	f.logs = append(f.logs, cli.LogEvent{Time: time.Now().Add(time.Second), Message: "read on refresh"})
	m, cmd := m.refresh()
	m = settle(t, m, cmd)
	if !strings.Contains(screen(m), "read on refresh") {
		t.Errorf("a refresh did not add the new line:\n%s", screen(m))
	}
}

// TestLogsFilterAndOlder checks / keeps the lines that match, and ↑ on the first line reads the
// hour before.
func TestLogsFilterAndOlder(t *testing.T) {
	f := newFake(t)
	f.logs = requestLogs(time.Now().Add(-time.Minute))
	f.older = []cli.LogEvent{{Time: time.Now().Add(-2 * time.Hour), Message: "from the hour before"}}
	m := pressKeys(t, started(t, f), "enter", "tab", "/")
	m = typeText(t, m, "order")
	m = pressKeys(t, m, "enter")
	if v := screen(m); strings.Contains(v, "START") || !strings.Contains(v, "order placed") || !strings.Contains(v, "no order 9") || !strings.Contains(v, "stored an order") {
		t.Fatalf("the lines that have order:\n%s", v)
	}
	m = pressKeys(t, m, "esc", "g", "k")
	if f.olderReads != 1 || !strings.Contains(screen(m), "from the hour before") || m.state.function.logs.follow {
		t.Errorf("%d reads of older lines, follow %v:\n%s", f.olderReads, m.state.function.logs.follow, screen(m))
	}
}

// TestInvocations checks the invocations tab lists them from the logs, and enter shows one with
// its lines.
func TestInvocations(t *testing.T) {
	f := newFake(t)
	f.logs = requestLogs(time.Now().Add(-time.Minute))
	m := pressKeys(t, started(t, f), "enter", "tab", "tab")
	v := screen(m)
	if !strings.Contains(v, "✗ failed") || !strings.Contains(v, "150ms") {
		t.Errorf("the invocations tab:\n%s", v)
	}
	m = pressKeys(t, m, "j", "enter")
	v = screen(m)
	if m.page != invocationPage || !strings.Contains(v, "no order 9") || strings.Contains(v, "order placed") {
		t.Errorf("the failed invocation:\n%s", v)
	}
	if m = pressKeys(t, m, "q"); m.page != functionPage {
		t.Errorf("q goes back to the function, not %v", m.page)
	}
}

// TestPlanDiff checks enter on a change of a plan shows its full diff, and q goes back to the
// plan, where the second change is past the heading of its group.
func TestPlanDiff(t *testing.T) {
	m := pressKeys(t, started(t, newFake(t)), "d", "enter")
	v := screen(m)
	if m.page != diffPage || !strings.Contains(v, `-   "memory": 128`) || !strings.Contains(v, `+   "memory": 256`) {
		t.Fatalf("page %v:\n%s", m.page, v)
	}
	if m = pressKeys(t, m, "q"); m.page != planPage {
		t.Errorf("q goes back to the plan, not %v", m.page)
	}
	m = pressKeys(t, m, "j", "j", "enter")
	if !strings.Contains(screen(m), "resource/table") {
		t.Errorf("the second change:\n%s", screen(m))
	}
}

// TestDiffOpensOnTheChange checks a diff longer than the terminal opens with its first change in
// sight, as the change of a function's timeout sat below the fold of an 80 by 24 terminal.
func TestDiffOpensOnTheChange(t *testing.T) {
	before, after := map[string]any{}, map[string]any{}
	for i := range 40 {
		before[fmt.Sprintf("key%02d", i)], after[fmt.Sprintf("key%02d", i)] = i, i
	}
	after["key30"] = "changed"
	for _, size := range [][2]int{{80, 24}, {120, 40}, {200, 60}} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			m := started(t, newFake(t))
			m, _ = update(t, m, tea.WindowSizeMsg{Width: size[0], Height: size[1]})
			m, _ = m.openDiff("function/api", "update", before, after)
			v := screen(m)
			if !strings.Contains(v, `-   "key30": 30`) || !strings.Contains(v, `+   "key30": "changed"`) || !strings.Contains(v, `"key29": 29`) {
				t.Errorf("the change is not in sight:\n%s", v)
			}
		})
	}
	m := started(t, newFake(t))
	after = map[string]any{"key00": "changed"}
	if m, _ = m.openDiff("function/api", "update", map[string]any{"key00": 0}, after); m.state.diff.scroll != 0 {
		t.Errorf("a change in sight from the top scrolled to %d", m.state.diff.scroll)
	}
}

// TestVersionDiff checks enter on a resource a version changed shows what it changed.
func TestVersionDiff(t *testing.T) {
	m := pressKeys(t, started(t, newFake(t)), "shift+tab", "enter", "enter")
	if v := screen(m); m.page != diffPage || !strings.Contains(v, `+   "properties"`) {
		t.Errorf("page %v:\n%s", m.page, v)
	}
}

// TestResourceLive checks the resource's in aws tab shows what cloud control says, and what
// changed outside koldstart.
func TestResourceLive(t *testing.T) {
	f := newFake(t)
	f.live = map[string]any{"BillingMode": "PROVISIONED", "Arn": "arn:aws:dynamodb:table"}
	m := pressKeys(t, started(t, f), "tab", "G", "enter")
	if m.page != resourcePage || m.state.resource.res.id != "resource/table" {
		t.Fatalf("page %v on %s", m.page, m.state.resource.res.id)
	}
	m = pressKeys(t, m, "tab")
	if v := screen(m); !strings.Contains(v, "changed outside koldstart: BillingMode") {
		t.Errorf("the in aws tab:\n%s", v)
	}
	f.live = nil
	m = pressKeys(t, m, "r")
	if v := screen(m); !strings.Contains(v, "cloud control cannot read") {
		t.Errorf("a type cloud control cannot read:\n%s", v)
	}
}

// TestPresentable checks a secret in a diff is shown by what it stands for, never as a marker
// with more than the reference.
func TestPresentable(t *testing.T) {
	lines := jsonLines(map[string]any{"KEY": engine.Ref("resource/x", "Arn")})
	if !strings.Contains(strings.Join(lines, "\n"), `"KEY": (ref resource/x.Arn)`) {
		t.Errorf("jsonLines = %q", lines)
	}
}

// TestDeploys checks the versions a deploy writes close together count as one deploy.
func TestDeploys(t *testing.T) {
	at := time.Now()
	v := func(id string, ago time.Duration) state.ObjectVersion {
		return state.ObjectVersion{ID: id, LastModified: at.Add(-ago)}
	}
	got := deploys([]state.ObjectVersion{v("c", 0), v("b", time.Second), v("a", 2*time.Second), v("z", time.Hour), v("y", time.Hour+time.Second), v("x", 3*time.Hour)})
	sizes := make([]int, 0, len(got))
	for _, d := range got {
		sizes = append(sizes, len(d))
	}
	if len(sizes) != 3 || sizes[0] != 3 || sizes[1] != 2 || sizes[2] != 1 {
		t.Errorf("deploys of %v writes, want 3, 2 and 1", sizes)
	}
}
