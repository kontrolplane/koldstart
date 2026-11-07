package tui

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/kontrolplane/koldstart/internal/cli"
	"github.com/kontrolplane/koldstart/internal/state"
	"github.com/kontrolplane/koldstart/internal/tui/keys"
	"github.com/kontrolplane/koldstart/internal/tui/styles"
)

// TestSparklineScaling checks a sparkline keeps a lone spike, marks a small point apart from
// none, and draws a short series at its own length.
func TestSparklineScaling(t *testing.T) {
	spike := make([]float64, 60)
	spike[59] = 40
	tests := []struct {
		name   string
		series []float64
		width  int
		want   string
	}{
		{name: "nothing", series: nil, width: 10, want: ""},
		{name: "no width", series: []float64{1, 2}, width: 0, want: ""},
		{name: "a lone spike", series: spike, width: 6, want: "     █"},
		{name: "small beside large", series: []float64{1, 0, 1000}, width: 3, want: "▁ █"},
		{name: "shorter than the width", series: []float64{2, 4}, width: 30, want: "▄█"},
		{name: "flat", series: []float64{3, 3, 3}, width: 3, want: "███"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sparkline(tt.series, tt.width); got != tt.want {
				t.Errorf("sparkline = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestCutMiddle checks a long arn keeps its start and the name at its end.
func TestCutMiddle(t *testing.T) {
	arn := "arn:aws:lambda:eu-west-1:123456789012:function:orders-dev-api"
	tests := []struct {
		name  string
		s     string
		width int
		want  string
	}{
		{name: "fits", s: "short", width: 10, want: "short"},
		{name: "an arn", s: arn, width: 25, want: "arn:aw…ion:orders-dev-api"},
		{name: "too narrow to cut", s: arn, width: 5, want: arn},
		{name: "wide runes", s: strings.Repeat("日本", 20), width: 21, want: "日本…本日本日本日本"},
		{name: "a wide rune cut", s: "a日本" + strings.Repeat("日本", 20) + "a", width: 22, want: "a日本…本日本日本日本a"},
		{name: "wide runes that fit", s: strings.Repeat("日本", 5), width: 20, want: strings.Repeat("日本", 5)},
		{name: "a megabyte", s: "https://" + strings.Repeat("a", 1<<20) + "/end", width: 25, want: "https:…aaaaaaaaaaaaaa/end"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := cutMiddle(tt.s, tt.width)
			if got != tt.want {
				t.Errorf("cutMiddle = %q, want %q", got, tt.want)
			}
			if tt.width >= 8 && ansi.StringWidth(got) > tt.width {
				t.Errorf("cutMiddle is %d wide, want at most %d", ansi.StringWidth(got), tt.width)
			}
		})
	}
}

// TestScrollBy checks a scroll stays between the top and the furthest it goes, so the key after
// G or a page past the end moves it at once.
func TestScrollBy(t *testing.T) {
	tests := []struct {
		name   string
		key    string
		offset int
		most   int
		want   int
	}{
		{name: "down", key: "j", offset: 3, most: 10, want: 4},
		{name: "down at the end", key: "j", offset: 10, most: 10, want: 10},
		{name: "up at the top", key: "k", offset: 0, most: 10, want: 0},
		{name: "bottom", key: "G", offset: 0, most: 10, want: 10},
		{name: "up from the bottom", key: "k", offset: 10, most: 10, want: 9},
		{name: "a page past the end", key: "pgdown", offset: 8, most: 10, want: 10},
		{name: "nothing to scroll", key: "G", offset: 0, most: 0, want: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg := press(tt.key)
			if tt.key == "pgdown" {
				msg = tea.KeyPressMsg{Code: tea.KeyPgDown}
			}
			if got := scrollBy(msg, keys.Keys, tt.offset, 5, tt.most); got != tt.want {
				t.Errorf("scrollBy = %d, want %d", got, tt.want)
			}
		})
	}
}

// TestScrollStopsAtTheEnd checks G on a page that scrolls goes to its last line and no further,
// so k after it moves at once: a diff, an invocation, and a deploy that changed no resource, which
// has no change to choose. The resource page moves a cursor through its json instead.
func TestScrollStopsAtTheEnd(t *testing.T) {
	tests := []struct {
		name   string
		open   func(t *testing.T) model
		scroll func(m model) int
		lines  func(m model) int
	}{
		{
			name: "diff",
			open: func(t *testing.T) model {
				after := map[string]any{}
				for i := range 100 {
					after[strings.Repeat("k", i%7+1)+string(rune('a'+i%26))+string(rune('a'+i/26))] = i
				}
				m, _ := started(t, newFake(t)).openDiff("function/api", "update", map[string]any{}, after)
				return m
			},
			scroll: func(m model) int { return m.state.diff.scroll },
			lines:  func(m model) int { return m.diffLines().len() },
		},
		{
			name: "invocation",
			open: func(t *testing.T) model {
				at := time.Now().Add(-time.Minute)
				id := "66666666-6666-6666-6666-666666666666"
				lines := make([]cli.LogEvent, 0, 81)
				lines = append(lines, cli.LogEvent{Time: at, Stream: "a", Message: "START RequestId: " + id + " Version: $LATEST"})
				for i := range 80 {
					lines = append(lines, cli.LogEvent{Time: at.Add(time.Duration(i+1) * time.Millisecond), Stream: "a", Message: "2026-11-05T10:00:00.001Z\t" + id + "\tINFO\tstep"})
				}
				m, _ := onLogs(t, lines)
				return pressKeys(t, m, "tab", "enter")
			},
			scroll: func(m model) int { return m.state.invocation.scroll },
			lines:  func(m model) int { return m.invocationLines().len() },
		},
		{
			name: "deploy without resource changes",
			open: func(t *testing.T) model {
				before, after := testState(ordersDev), testState(ordersDev)
				for i := range 60 {
					after.Outputs["output"+strings.Repeat("x", i)] = "v"
				}
				return openNewestDeploy(t, before, after)
			},
			scroll: func(m model) int { return m.state.version.scroll },
			lines:  func(m model) int { return m.versionLines().lines.len() },
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := pressKeys(t, tt.open(t), "G", "j", "j")
			end := tt.lines(m) - contentHeight
			if end <= 0 || tt.scroll(m) != end {
				t.Fatalf("G: scroll %d, want the end at %d", tt.scroll(m), end)
			}
			if m = pressKeys(t, m, "k"); tt.scroll(m) != end-1 {
				t.Errorf("k at the end: scroll %d, want %d", tt.scroll(m), end-1)
			}
		})
	}
}

// TestWrapped checks text wraps to the width, and is cut to an ellipsis on the last line a limit
// allows, but not without one.
func TestWrapped(t *testing.T) {
	long := "could not read it: the role cannot be assumed by lambda, check its trust policy"
	tests := []struct {
		name  string
		limit int
		want  int
	}{
		{name: "no limit", limit: 0, want: 4},
		{name: "a limit", limit: 2, want: 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lines := wrapped(long, styles.ToneBody, 24, tt.limit)
			if len(lines) != tt.want {
				t.Fatalf("%d lines, want %d: %q", len(lines), tt.want, lines)
			}
			for _, l := range lines {
				if ansi.StringWidth(l) > 24 {
					t.Errorf("%q is wider than 24", ansi.Strip(l))
				}
			}
			if last := ansi.Strip(lines[len(lines)-1]); (tt.limit > 0) != strings.HasSuffix(last, "…") {
				t.Errorf("the last line is %q", last)
			}
		})
	}
}

// TestEnvValue checks values that look like secrets are hidden, a url's password too, and a
// secret koldstart reads is shown by where it is.
func TestEnvValue(t *testing.T) {
	tests := []struct {
		name  string
		key   string
		value any
		want  string
	}{
		{name: "plain", key: "TABLE", value: "orders", want: "orders"},
		{name: "empty", key: "TABLE", value: "", want: `""`},
		{name: "a token by its name", key: "GITHUB_TOKEN", value: "ghp_abc", want: hidden},
		{name: "an api key in camel case", key: "stripeApiKey", value: "sk_live_1", want: hidden},
		{name: "a password", key: "DB_PASSWORD", value: "hunter2", want: hidden},
		{name: "a private key by its value", key: "CERT", value: "-----BEGIN PRIVATE KEY-----", want: hidden},
		{name: "a url's password", key: "DATABASE_URL", value: "postgres://app:hunter2@db:5432/orders", want: "postgres://app:" + hidden + "@db:5432/orders"},
		{name: "a url without a password", key: "API_URL", value: "https://user@example.com/x", want: "https://user@example.com/x"},
		{name: "a secret koldstart reads", key: "KEY", value: map[string]any{"$koldstart": "secret", "source": "ssm", "name": "/orders/key"}, want: "ssm:/orders/key"},
		{name: "a number", key: "PORT", value: 8080.0, want: "8080"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := envValue(tt.key, tt.value).Text; got != tt.want {
				t.Errorf("envValue = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestOverviewStates checks what the overview says when lambda and its metrics cannot be read,
// as on an emulator, when nothing invoked the function, and when it was changed outside
// koldstart.
func TestOverviewStates(t *testing.T) {
	tests := []struct {
		name       string
		function   cli.FunctionState
		metricsErr error
		liveErr    error
		want       []string
		notWant    []string
	}{
		{
			name:       "an emulator",
			metricsErr: errAWS,
			liveErr:    errAWS,
			want:       []string{"could not read the metrics: aws said no", "could not read it from lambda: aws said"},
			notWant:    []string{"changed outside koldstart", "✓ what koldstart deployed", "p99"},
		},
		{
			name:     "deployed as it is",
			function: cli.FunctionState{State: "Active", LastUpdate: "Successful", LastModified: time.Now(), CodeSHA256: strings.Repeat("ab", 32), CodeSize: 42 << 20, Memory: 1024, Timeout: 6, Runtime: "nodejs22.x"},
			want:     []string{"✓ what koldstart deployed · 42 mb", "nodejs22.x · 1,024 mb", "✓ active"},
			notWant:  []string{"changed outside koldstart"},
		},
		{
			name:     "changed outside koldstart",
			function: cli.FunctionState{State: "Active", CodeSHA256: "ff", Memory: 512, Timeout: 30, Runtime: "nodejs20.x"},
			want:     []string{"▲ changed outside koldstart: code, memory 1,024 → 512 mb, timeout 6s → 30s, runtime nodejs22.x → nodejs20.x"},
		},
		{
			name:     "nothing known of the code",
			function: cli.FunctionState{State: "Active"},
			want:     []string{"modified  -", "code  -"},
			notWant:  []string{"✓ what koldstart deployed", "0 b"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFake(t)
			f.function, f.metricsErr = tt.function, tt.metricsErr
			m := pressKeys(t, started(t, f), "enter")
			if tt.liveErr != nil {
				m.state.function.live = nil
				m, _ = update(t, m, functionLiveMsg{gen: m.state.function.liveGen, err: tt.liveErr})
			}
			v := screen(m)
			for _, want := range tt.want {
				if !strings.Contains(v, want) {
					t.Errorf("the overview has no %q:\n%s", want, v)
				}
			}
			for _, not := range tt.notWant {
				if strings.Contains(v, not) {
					t.Errorf("the overview has %q:\n%s", not, v)
				}
			}
		})
	}
}

// TestOverviewNotInvoked checks a function without invocations in the last hour says so once,
// instead of a row of nothing for each metric.
func TestOverviewNotInvoked(t *testing.T) {
	m := pressKeys(t, started(t, newFake(t)), "enter")
	mt := m.state.stage.metrics["orders-dev-api"]
	for _, s := range [][]float64{mt.Invocations, mt.Errors, mt.Throttles, mt.Duration, mt.DurationP99, mt.Concurrency} {
		clear(s)
	}
	v := screen(m)
	if !strings.Contains(v, "not invoked in the last hour") || strings.Contains(v, "throttles") {
		t.Errorf("the overview of a function nothing invoked:\n%s", v)
	}
}

// fdOpened is the urls the overview opened in the browser in a test; no test opens a browser.
var fdOpened []string

func init() {
	openURL = func(url string) error {
		fdOpened = append(fdOpened, url)
		return nil
	}
}

// fdFull is a fake of a function with a url, triggers of each kind, a role with a policy, a
// secret, a url with a password and an output in its environment, and a vpc.
func fdFull(t *testing.T) *fakeBackend {
	t.Helper()
	f := newFake(t)
	f.function = cli.FunctionState{State: "Active", LastUpdate: "Successful", LastModified: time.Now(), CodeSHA256: strings.Repeat("ab", 32), Memory: 1024, Timeout: 6, Runtime: "nodejs22.x"}
	st := f.states[ordersDev]
	fn := st.Resources["function/api"]
	var in map[string]any
	if err := json.Unmarshal(fn.Inputs, &in); err != nil {
		t.Fatal(err)
	}
	env := in["environment"].(map[string]any)
	env["STRIPE_SECRET"] = "sk_live_123"
	env["DATABASE_URL"] = "postgres://app:hunter2@db:5432/orders"
	env["BILLING_URL"] = map[string]any{"$koldstart": "output", "app": "billing", "stage": "prod", "output": "url"}
	in["vpc"] = map[string]any{"subnetIds": []string{"subnet-1", "subnet-2"}, "securityGroupIds": []string{"sg-1"}}
	fn.Inputs, _ = json.Marshal(in)
	st.Resources["role/api"].Inputs, _ = json.Marshal(map[string]any{
		"name": "orders-dev-api", "managedPolicies": []string{"arn:aws:iam::aws:policy/service-role/AWSLambdaVPCAccessExecutionRole"},
		"policy": map[string]any{"Version": "2012-10-17", "Statement": []any{
			map[string]any{"Effect": "Allow", "Action": []any{"dynamodb:GetItem", "dynamodb:PutItem"}, "Resource": []any{"arn:aws:dynamodb:eu-west-1:123456789012:table/orders-dev", "arn:aws:dynamodb:eu-west-1:123456789012:table/orders-dev/index/*"}},
			map[string]any{"Effect": "Deny", "Action": "s3:*", "Resource": "*"},
		}},
	})
	st.Resources["url/api"] = &state.Resource{PhysicalID: "u", Attributes: map[string]string{"FunctionUrl": "https://abc.lambda-url.eu-west-1.on.aws/"}}
	st.Resources["integration/api"] = &state.Resource{PhysicalID: "i"}
	st.Resources["route/GET /orders"] = &state.Resource{PhysicalID: "r", Dependencies: []string{"httpapi", "integration/api"}}
	st.Resources["schedule/api/0"] = &state.Resource{PhysicalID: "s", Inputs: json.RawMessage(`{"expression":"rate(5 minutes)","timezone":"Europe/Amsterdam"}`)}
	st.Resources["mapping/api/jobs"] = &state.Resource{PhysicalID: "m", Inputs: json.RawMessage(`{"source":"arn:aws:sqs:eu-west-1:123456789012:orders-dev-jobs"}`)}
	return f
}

// fdCursorTo moves the overview's cursor down to the first row whose text holds label.
func fdCursorTo(t *testing.T, m model, label string) model {
	t.Helper()
	m = pressKeys(t, m, "g")
	for range m.functionOverview().rowCount() {
		if strings.Contains(fdRowText(m), label) {
			return m
		}
		m = pressKeys(t, m, "j")
	}
	t.Fatalf("no row of the overview holds %q", label)
	return m
}

// fdRowText is the text of the row under the overview's cursor, its lines joined.
func fdRowText(m model) string {
	o := m.functionOverview()
	col, r := o.locate(m.state.function.cursor)
	if col < 0 {
		return ""
	}
	c := o.cols[col]
	var lines []string
	for i, of := range c.rowOf {
		if of == r {
			lines = append(lines, strings.TrimSpace(ansi.Strip(c.lines[i])))
		}
	}
	return strings.Join(lines, " ")
}

// TestOverviewEnvironmentAndTriggers checks the environment is sorted and counted, secrets hidden
// and refs shown by what they stand for, and what invokes the function listed by its kind and
// what it is, its url first.
func TestOverviewEnvironmentAndTriggers(t *testing.T) {
	v := screen(pressKeys(t, started(t, fdFull(t)), "enter"))
	for _, want := range []string{
		"environment 5", "BILLING_URL  output billing/prod.url", "DATABASE_URL  postgres://app:" + hidden + "@db:5432/orders",
		"KEY  ssm:/orders/key", "STRIPE_SECRET  " + hidden, "TABLE  orders",
		"invoked by 4", "url  https://abc.lambda-url.eu-west-1.on.aws/", "route  GET /orders",
		"schedule  rate(5 minutes) Europe/Amsterdam", "queue  orders-dev-jobs",
	} {
		if !strings.Contains(v, want) {
			t.Errorf("the overview has no %q:\n%s", want, v)
		}
	}
	if strings.Contains(v, "sk_live_123") || strings.Contains(v, "hunter2") || strings.Contains(v, "nothing: invoke it") {
		t.Errorf("a secret is shown, or the function is said to have no triggers:\n%s", v)
	}
	if i, j := strings.Index(v, "BILLING_URL"), strings.Index(v, "TABLE "); i < 0 || j < i {
		t.Errorf("the environment is not sorted:\n%s", v)
	}
}

// TestOverviewPermissions checks the role is named, with its managed policies and what its
// policy allows, each service named once, and a role that is not koldstart's says why there is
// no more.
func TestOverviewPermissions(t *testing.T) {
	tests := []struct {
		name    string
		noRole  bool
		want    []string
		notWant []string
	}{
		{
			name:    "koldstart's role",
			want:    []string{"permissions", "role  api", "managed  AWSLambdaVPCAccessExecutionRole", "allows  dynamodb GetItem, PutItem on table/orders-dev +1"},
			notWant: []string{"s3", "not koldstart's role"},
		},
		{
			name:   "a role of its own",
			noRole: true,
			want:   []string{"role  api", "not koldstart's role: its policies are not shown"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := fdFull(t)
			if tt.noRole {
				delete(f.states[ordersDev].Resources, "role/api")
			}
			m := pressKeys(t, started(t, f), "enter")
			m, _ = update(t, m, tea.WindowSizeMsg{Width: 100, Height: 80})
			v := screen(m)
			for _, want := range tt.want {
				if !strings.Contains(v, want) {
					t.Errorf("the overview has no %q:\n%s", want, v)
				}
			}
			for _, not := range tt.notWant {
				if strings.Contains(v, not) {
					t.Errorf("the overview has %q:\n%s", not, v)
				}
			}
		})
	}
}

// TestOverviewCopy checks c copies the whole value of the row under the cursor, however it is
// cut on screen, and the name of a variable whose value is hidden, so no secret reaches the
// clipboard or the status line.
func TestOverviewCopy(t *testing.T) {
	tests := []struct {
		row  string
		want string
	}{
		{row: "arn", want: "arn:aws:lambda:eu-west-1:123456789012:function:orders-dev-api"},
		{row: "code", want: strings.Repeat("ab", 32)},
		{row: "log group", want: "/aws/lambda/orders-dev-api"},
		{row: "subnets", want: "subnet-1, subnet-2"},
		{row: "role", want: "arn:aws:iam::123456789012:role/api"},
		{row: "allows", want: "dynamodb:GetItem, dynamodb:PutItem on arn:aws:dynamodb:eu-west-1:123456789012:table/orders-dev, arn:aws:dynamodb:eu-west-1:123456789012:table/orders-dev/index/*"},
		{row: "url  https", want: "https://abc.lambda-url.eu-west-1.on.aws/"},
		{row: "queue", want: "arn:aws:sqs:eu-west-1:123456789012:orders-dev-jobs"},
		{row: "schedule", want: "rate(5 minutes)"},
		{row: "TABLE", want: "orders"},
		{row: "STRIPE_SECRET", want: "STRIPE_SECRET"},
		{row: "DATABASE_URL", want: "postgres://app:" + hidden + "@db:5432/orders"},
		{row: "KEY ", want: "ssm:/orders/key"},
	}
	for _, tt := range tests {
		t.Run(tt.row, func(t *testing.T) {
			m := pressKeys(t, started(t, fdFull(t)), "enter")
			m = fdCursorTo(t, m, tt.row)
			if got := m.functionCopyValue(); got != tt.want {
				t.Errorf("c on %q copies %q, want %q", fdRowText(m), got, tt.want)
			}
			m = pressKeys(t, m, "c")
			if !strings.HasPrefix(m.status, "copied ") || strings.Contains(m.status, "sk_live") || strings.Contains(m.status, "hunter2") {
				t.Errorf("the status after c is %q", m.status)
			}
		})
	}
}

// TestOverviewOpen checks enter on a row opens what it names: the log group, the role and a
// trigger on the resource page, which goes back to the overview as it was left, and the url in
// the browser; on any other row it does nothing.
func TestOverviewOpen(t *testing.T) {
	tests := []struct {
		row      string
		resource string
		url      string
	}{
		{row: "log group", resource: "loggroup/api"},
		{row: "role", resource: "role/api"},
		{row: "schedule", resource: "schedule/api/0"},
		{row: "route", resource: "route/GET /orders"},
		{row: "url  https", url: "https://abc.lambda-url.eu-west-1.on.aws/"},
		{row: "arn"},
	}
	for _, tt := range tests {
		t.Run(tt.row, func(t *testing.T) {
			fdOpened = nil
			m := fdCursorTo(t, pressKeys(t, started(t, fdFull(t)), "enter"), tt.row)
			left := navSnapshot(m)
			opened := pressKeys(t, m, "enter")
			switch {
			case tt.resource != "":
				if opened.page != resourcePage || opened.state.resource.res.id != tt.resource {
					t.Fatalf("enter on %q: page %v, resource %q, want %q", tt.row, opened.page, opened.state.resource.res.id, tt.resource)
				}
				if top := navTop(opened); !strings.Contains(top, "api › overview › "+strings.Replace(tt.resource, "/", " ", 1)) {
					t.Errorf("the breadcrumb is %q", top)
				}
				back := pressKeys(t, opened, "q")
				if got := navSnapshot(back); got != left {
					t.Errorf("q went back to\n%s\nwant\n%s", got, left)
				}
			case tt.url != "":
				if opened.page != functionPage || len(fdOpened) != 1 || fdOpened[0] != tt.url || !strings.HasPrefix(opened.status, "opened ") {
					t.Errorf("enter on the url opened %q, page %v, status %q", fdOpened, opened.page, opened.status)
				}
			default:
				if opened.page != functionPage || len(fdOpened) != 0 {
					t.Errorf("enter on %q: page %v, opened %q", tt.row, opened.page, fdOpened)
				}
			}
		})
	}
}

// TestFunctionOverviewHints checks the footer offers enter only on a row that opens something,
// and says what it opens.
func TestFunctionOverviewHints(t *testing.T) {
	tests := []struct {
		row  string
		want string
	}{
		{row: "arn", want: ""},
		{row: "log group", want: "enter open"},
		{row: "url  https", want: "enter open in the browser"},
	}
	for _, tt := range tests {
		t.Run(tt.row, func(t *testing.T) {
			m := fdCursorTo(t, pressKeys(t, started(t, fdFull(t)), "enter"), tt.row)
			got := ""
			for _, h := range m.shortHelp() {
				if h[0] == "enter" {
					got = h[0] + " " + h[1]
				}
			}
			if got != tt.want {
				t.Errorf("the hint for enter is %q, want %q", got, tt.want)
			}
		})
	}
}

// TestOverviewCursor checks the cursor walks every row of the overview and keeps it in sight, the
// end of the page in sight on the last, the frame saying which lines are, and moves back up one
// row at a time.
func TestOverviewCursor(t *testing.T) {
	m := pressKeys(t, started(t, fdFull(t)), "enter")
	m, _ = update(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	if v := screen(m); !strings.Contains(v, "lines 1–") || strings.Contains(v, "invoked by") {
		t.Fatalf("at 80×24 the overview should be cut, with a hint:\n%s", v)
	}
	o := m.functionOverview()
	for i := range o.rowCount() + 5 {
		row := fdRowText(m)
		if v := screen(m); row == "" || !strings.Contains(v, row) {
			t.Fatalf("row %d, %q, is not in sight:\n%s", i, row, v)
		}
		m = pressKeys(t, m, "j")
	}
	if last := o.rowCount() - 1; m.state.function.cursor != last {
		t.Fatalf("cursor %d after walking down, want the last row, %d", m.state.function.cursor, last)
	}
	total := formatInt(o.lines())
	if foot := navFoot(m); !strings.Contains(foot, "–"+total+" of "+total) {
		t.Errorf("at the last row the foot should show the end: %q", foot)
	}
	if m = pressKeys(t, m, "k"); m.state.function.cursor != o.rowCount()-2 {
		t.Errorf("one row up: cursor %d, want %d", m.state.function.cursor, o.rowCount()-2)
	}
	if m = pressKeys(t, m, "g"); m.state.function.cursor != 0 || !strings.Contains(navFoot(m), "lines 1–") {
		t.Errorf("g: cursor %d, foot %q", m.state.function.cursor, navFoot(m))
	}
	m, _ = update(t, m, tea.WindowSizeMsg{Width: 200, Height: 60})
	if v := screen(m); strings.Contains(v, "lines ") {
		t.Errorf("with room for it all there should be no hint:\n%s", v)
	}
}

// TestOverviewColumns checks the overview is one column below overviewTwoColumns, its sections
// from the configuration down to the permissions, and two from there, the last hour beside the
// configuration.
func TestOverviewColumns(t *testing.T) {
	tests := []struct {
		width int
		cols  int
	}{
		{width: 80, cols: 1},
		{width: overviewTwoColumns + chromeWidth - 1, cols: 1},
		{width: overviewTwoColumns + chromeWidth, cols: 2},
		{width: 200, cols: 2},
	}
	for _, tt := range tests {
		t.Run(formatInt(tt.width), func(t *testing.T) {
			m := pressKeys(t, started(t, fdFull(t)), "enter")
			m, _ = update(t, m, tea.WindowSizeMsg{Width: tt.width, Height: 80})
			o := m.functionOverview()
			if len(o.cols) != tt.cols {
				t.Fatalf("%d columns, want %d", len(o.cols), tt.cols)
			}
			first := ansi.Strip(strings.Join(o.cols[0].lines, "\n"))
			order := []string{"configuration", "in lambda now", "last hour", "invoked by", "environment", "permissions"}
			if tt.cols == 2 {
				order = []string{"configuration", "in lambda now", "permissions"}
				if right := ansi.Strip(o.cols[1].lines[0]); !strings.HasPrefix(right, "last hour") {
					t.Errorf("the right column starts with %q", right)
				}
			}
			at := -1
			for _, section := range order {
				i := strings.Index(first, section+" ")
				if i <= at {
					t.Errorf("%q is not after the section before it:\n%s", section, first)
				}
				at = i
			}
		})
	}
}

// TestOverviewHead checks the frame's border says the facts of the function on its page, as far as
// lambda's state is known, and that it is not deployed or no longer in the state, which the
// breadcrumb names, so the overview has no head of its own.
func TestOverviewHead(t *testing.T) {
	tests := []struct {
		name string
		prep func(m model) model
		want string
	}{
		{name: "deployed", prep: func(m model) model { return m }, want: "deployed 10m ago · ✓ active ─╮"},
		{name: "reading lambda", prep: func(m model) model { m.state.function.live = nil; return m }, want: "· deployed 10m ago ─╮"},
		{name: "failed", prep: func(m model) model { m.state.function.live.State = "Failed"; return m }, want: "deployed 10m ago · ✗ failed"},
		{name: "pending", prep: func(m model) model { m.state.function.live.State = "Pending"; return m }, want: "· ● pending"},
		{name: "gone", prep: func(m model) model { m.state.function.gone = true; return m }, want: "▲ no longer in the stage's state"},
		{name: "not deployed", prep: func(m model) model { m.state.function.lambdaName = ""; return m }, want: "not deployed yet · d plans a deploy of its code"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := tt.prep(pressKeys(t, started(t, fdFull(t)), "enter"))
			meta, _ := m.frameMeta()
			v := ansi.Strip(meta) + "\n" + screen(m)
			if !strings.Contains(v, tt.want) {
				t.Errorf("want %q in the frame's border or the page:\n%s", tt.want, v)
			}
			if head := m.functionOverview().head; len(head) != 1 || strings.TrimSpace(ansi.Strip(head[0])) != "" {
				t.Errorf("the head on the page is %q, want the facts in the border alone", head)
			}
		})
	}
}

// TestSparkLines checks each metric's sparkline lines up with the others, with what it comes to,
// its range and its last point beside it as far as they fit, the hour under them, a metric of
// zeros a dotted baseline, and one without points saying so.
func TestSparkLines(t *testing.T) {
	ramp := []float64{0, 0, 1, 2, 4, 8}
	hour := make([]float64, 60)
	hour[30], hour[59] = 3, 6
	series := func(points ...[]float64) []metricSeries {
		out := make([]metricSeries, len(points))
		for i, p := range points {
			out[i] = metricSeries{label: "m" + formatInt(i), points: p, tone: styles.ToneAccent, total: text("15", styles.ToneBody), format: func(v float64) string { return formatInt(int(v)) }, unit: "/min"}
		}
		return out
	}
	tests := []struct {
		name   string
		series []metricSeries
		width  int
		want   []string
	}{
		{name: "room for all of it", series: series(ramp), width: 60, want: []string{"m0  ··▁▂▄█  15  0–8/min  last 8"}},
		{name: "no room for the last point", series: series(ramp), width: 36, want: []string{"m0  ··▁▂▄█  15  0–8/min"}},
		{name: "no room for the figures", series: series(ramp), width: 30, want: []string{"m0  ··▁▂▄█  15"}},
		{name: "an hour", series: series(hour), width: 120, want: []string{"m0  " + strings.Repeat("·", 30) + "▄" + strings.Repeat("·", 28) + "█  15  0–6/min  last 6", "1h ago now"}},
		{name: "a sparse hour in less room", series: series(hour), width: 50, want: []string{"m0  ·······▄······█  15  0–6/min  last 6", "1h ago now"}},
		{name: "a sparse hour in little room", series: series(hour), width: 27, want: []string{"m0  ····▄···█  15", "-1h now"}},
		{name: "zeros", series: series(ramp, make([]float64, 6)), width: 60, want: []string{"m1  ······  15"}},
		{name: "no points", series: series(ramp, nil), width: 60, want: []string{"m1  no data"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lines := sparkLines(tt.series, tt.width)
			var got []string
			for _, l := range lines {
				if w := ansi.StringWidth(l); w > tt.width {
					t.Errorf("%q is %d wide, more than %d", ansi.Strip(l), w, tt.width)
				}
				got = append(got, strings.Join(strings.Fields(ansi.Strip(l)), " "))
			}
			for _, want := range tt.want {
				if !slices.Contains(got, strings.Join(strings.Fields(want), " ")) {
					t.Errorf("no line %q in\n%s", want, strings.Join(got, "\n"))
				}
			}
		})
	}
}

// TestBetween checks a range writes a unit both ends share once.
func TestBetween(t *testing.T) {
	tests := []struct {
		low, high, want string
	}{
		{low: "0", high: "60", want: "0–60"},
		{low: "40ms", high: "240ms", want: "40–240ms"},
		{low: "900ms", high: "1.2s", want: "900ms–1.2s"},
		{low: "1.5s", high: "2.0s", want: "1.5–2.0s"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			if got := between(tt.low, tt.high); got != tt.want {
				t.Errorf("between(%q, %q) = %q, want %q", tt.low, tt.high, got, tt.want)
			}
		})
	}
}

// TestInvokerOf checks each kind of trigger is named by what it is, and the rest of its id where
// its inputs say nothing.
func TestInvokerOf(t *testing.T) {
	tests := []struct {
		kind, rest string
		in         map[string]any
		want       invoker
	}{
		{kind: "schedule", rest: "0", in: map[string]any{"expression": "cron(0 3 * * ? *)"}, want: invoker{kind: "schedule", what: "cron(0 3 * * ? *)", copy: "cron(0 3 * * ? *)"}},
		{kind: "schedule", rest: "0", want: invoker{kind: "schedule", what: "0", copy: "0"}},
		{kind: "mapping", rest: "jobs", in: map[string]any{"source": "arn:aws:sqs:eu-west-1:1:jobs"}, want: invoker{kind: "queue", what: "jobs", copy: "arn:aws:sqs:eu-west-1:1:jobs"}},
		{kind: "mapping", rest: "s", in: map[string]any{"source": "arn:aws:kinesis:eu-west-1:1:stream/clicks"}, want: invoker{kind: "stream", what: "clicks", copy: "arn:aws:kinesis:eu-west-1:1:stream/clicks"}},
		{kind: "mapping", rest: "t", in: map[string]any{"source": "arn:aws:dynamodb:eu-west-1:1:table/orders/stream/2026"}, want: invoker{kind: "table stream", what: "2026", copy: "arn:aws:dynamodb:eu-west-1:1:table/orders/stream/2026"}},
		{kind: "subscription", rest: "t", in: map[string]any{"topic": "arn:aws:sns:eu-west-1:1:orders"}, want: invoker{kind: "topic", what: "orders", copy: "arn:aws:sns:eu-west-1:1:orders"}},
		{kind: "rule", rest: "0", in: map[string]any{"pattern": `{"source":["orders"]}`}, want: invoker{kind: "event rule", what: `{"source":["orders"]}`, copy: `{"source":["orders"]}`}},
		{kind: "lbrule", rest: "0", in: map[string]any{"paths": []any{"/a", "/b"}}, want: invoker{kind: "alb", what: "/a, /b", copy: "/a, /b"}},
		{kind: "wsroute", rest: "$connect", want: invoker{kind: "websocket", what: "$connect", copy: "$connect"}},
		{kind: "notification", rest: "uploads", want: invoker{kind: "bucket", what: "uploads", copy: "uploads"}},
	}
	for _, tt := range tests {
		t.Run(tt.want.kind+" "+tt.want.what, func(t *testing.T) {
			got := invokerOf("id", tt.kind, tt.rest, tt.in)
			tt.want.id = "id"
			if got != tt.want {
				t.Errorf("invokerOf = %+v, want %+v", got, tt.want)
			}
			if w := ansi.StringWidth(got.kind); w > labelWidth-2 {
				t.Errorf("the kind %q is wider than a label", got.kind)
			}
		})
	}
}

// TestActionsText checks actions are written with each service named once, in the order they
// come.
func TestActionsText(t *testing.T) {
	tests := []struct {
		actions []string
		want    string
	}{
		{actions: []string{"sqs:SendMessage", "sqs:GetQueueUrl"}, want: "sqs SendMessage, GetQueueUrl"},
		{actions: []string{"s3:GetObject", "dynamodb:Query", "s3:PutObject"}, want: "s3 GetObject, PutObject; dynamodb Query"},
		{actions: []string{"*"}, want: "*"},
		{actions: nil, want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			if got := actionsText(tt.actions); got != tt.want {
				t.Errorf("actionsText = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestOverviewLayout draws the overview with long values, a vpc, a drift notice and an error at
// terminal sizes from the minimum up, with the cursor on every row, and checks it stays inside
// the terminal.
func TestOverviewLayout(t *testing.T) {
	long := strings.Repeat("very-long-value-", 10)
	f := fdFull(t)
	st := f.states[ordersDev]
	fn := st.Resources["function/api"]
	var in map[string]any
	if err := json.Unmarshal(fn.Inputs, &in); err != nil {
		t.Fatal(err)
	}
	in["environment"].(map[string]any)[long] = long + "日本語🚀"
	in["role"] = "arn:aws:iam::123456789012:role/" + long
	in["layers"] = []string{"arn:aws:lambda:eu-west-1:123456789012:layer:" + long + ":3"}
	in["vpc"] = map[string]any{"subnetIds": []string{"subnet-" + long, "subnet-2"}, "securityGroupIds": []string{"sg-1"}}
	fn.Inputs, _ = json.Marshal(in)
	st.Resources["schedule/api/"+long] = &state.Resource{PhysicalID: "s"}
	f.function = cli.FunctionState{State: "Failed", StateReason: long, CodeSHA256: "ff", Memory: 128, Timeout: 900, Runtime: long}
	opened := pressKeys(t, started(t, f), "enter")
	for _, size := range [][2]int{{78, 22}, {100, 30}, {overviewTwoColumns + chromeWidth, 30}, {120, 40}, {200, 60}} {
		w, h := size[0], size[1]
		m, _ := update(t, opened, tea.WindowSizeMsg{Width: w, Height: h})
		for range m.functionOverview().rowCount() + 1 {
			lines := strings.Split(m.render(), "\n")
			if len(lines) > h {
				t.Errorf("%dx%d: %d lines", w, h, len(lines))
			}
			for i, line := range lines {
				if lw := ansi.StringWidth(line); lw > w {
					t.Errorf("%dx%d: line %d is %d wide", w, h, i, lw)
					break
				}
			}
			m = pressKeys(t, m, "j")
		}
	}
}

// TestOverviewStale checks a read of lambda or of the metrics that fails after one that worked
// keeps what that one read on screen, under a note of when it was.
func TestOverviewStale(t *testing.T) {
	tests := []struct {
		name string
		fail func(t *testing.T, m model) model
		want []string
	}{
		{
			name: "lambda",
			fail: func(t *testing.T, m model) model {
				m, _ = update(t, m, functionLiveMsg{gen: m.state.function.liveGen, err: errAWS})
				return m
			},
			want: []string{"as of ", "could not read again: aws said no", "state  ✓ active"},
		},
		{
			name: "metrics",
			fail: func(t *testing.T, m model) model {
				m, _ = update(t, m, metricsLoadedMsg{gen: m.state.stage.metricsGen, err: errAWS})
				return m
			},
			want: []string{"as of ", "could not read again: aws said no", "1,740"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := pressKeys(t, started(t, fdFull(t)), "enter")
			m, _ = update(t, m, tea.WindowSizeMsg{Width: 100, Height: 80})
			v := screen(tt.fail(t, m))
			for _, want := range tt.want {
				if !strings.Contains(v, want) {
					t.Errorf("the overview has no %q:\n%s", want, v)
				}
			}
		})
	}
}
