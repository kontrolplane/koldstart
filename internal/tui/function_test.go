package tui

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/kontrolplane/koldstart/internal/cli"
	"github.com/kontrolplane/koldstart/internal/state"
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
			want:       []string{"could not read the metrics: aws said no", "could not read it from lambda: aws said no"},
			notWant:    []string{"changed outside koldstart", "✓ what koldstart deployed", "p99"},
		},
		{
			name:     "deployed as it is",
			function: cli.FunctionState{State: "Active", LastUpdate: "Successful", LastModified: time.Now(), CodeSHA256: strings.Repeat("ab", 32), CodeSize: 42 << 20, Memory: 1024, Timeout: 6, Runtime: "nodejs22.x"},
			want:     []string{"✓ what koldstart deployed · 42 mb", "1,024 mb", "timeout  6s"},
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

// TestOverviewEnvironmentAndTriggers checks the environment's names line up with secrets hidden,
// and what invokes the function is listed by kind, its url first.
func TestOverviewEnvironmentAndTriggers(t *testing.T) {
	f := newFake(t)
	st := f.states[ordersDev]
	fn := st.Resources["function/api"]
	var in map[string]any
	if err := json.Unmarshal(fn.Inputs, &in); err != nil {
		t.Fatal(err)
	}
	in["environment"].(map[string]any)["STRIPE_SECRET"] = "sk_live_123"
	fn.Inputs, _ = json.Marshal(in)
	st.Resources["url/api"] = &state.Resource{PhysicalID: "u", Attributes: map[string]string{"FunctionUrl": "https://abc.lambda-url.eu-west-1.on.aws/"}}
	st.Resources["integration/api"] = &state.Resource{PhysicalID: "i"}
	st.Resources["route/GET /orders"] = &state.Resource{PhysicalID: "r", Dependencies: []string{"httpapi", "integration/api"}}
	st.Resources["schedule/api/nightly"] = &state.Resource{PhysicalID: "s"}

	v := screen(pressKeys(t, started(t, f), "enter"))
	for _, want := range []string{
		"KEY           = ssm:/orders/key", "STRIPE_SECRET = " + hidden, "TABLE         = orders",
		"url       https://abc.lambda-url.eu-west-1.on.aws/", "route     GET /orders", "schedule  nightly",
	} {
		if !strings.Contains(v, want) {
			t.Errorf("the overview has no %q:\n%s", want, v)
		}
	}
	if strings.Contains(v, "sk_live_123") || strings.Contains(v, "nothing: invoke it") {
		t.Errorf("a secret is shown, or the function is said to have no triggers:\n%s", v)
	}
}

// TestOverviewScroll checks the overview scrolls no further than its longer column, so one key
// back up moves it, and the tab bar says there is more.
func TestOverviewScroll(t *testing.T) {
	m := pressKeys(t, started(t, newFake(t)), "enter")
	m, _ = update(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	if v := screen(m); !strings.Contains(v, "↓ more") || strings.Contains(v, "last update") {
		t.Fatalf("at 80×24 the overview should be cut, with a hint:\n%s", v)
	}
	for range 50 {
		m = pressKeys(t, m, "j")
	}
	end := m.functionOverview().maxScroll()
	if m.state.function.scroll != end {
		t.Fatalf("scroll %d after 50 lines down, want the end at %d", m.state.function.scroll, end)
	}
	if v := screen(m); !strings.Contains(v, "↑ more") || !strings.Contains(v, "last update") {
		t.Errorf("at the end the hint should point up, and what lambda says be in sight:\n%s", v)
	}
	if m = pressKeys(t, m, "k"); m.state.function.scroll != end-1 {
		t.Errorf("one line up: scroll %d, want %d", m.state.function.scroll, end-1)
	}
	m, _ = update(t, m, tea.WindowSizeMsg{Width: 200, Height: 60})
	if v := screen(m); strings.Contains(v, "more") {
		t.Errorf("with room for it all there should be no hint:\n%s", v)
	}
}

// TestOverviewLayout draws the overview with long values, a vpc, a drift notice and an error at
// terminal sizes from the minimum up, and checks it stays inside the terminal.
func TestOverviewLayout(t *testing.T) {
	long := strings.Repeat("very-long-value-", 10)
	f := newFake(t)
	st := f.states[ordersDev]
	fn := st.Resources["function/api"]
	var in map[string]any
	if err := json.Unmarshal(fn.Inputs, &in); err != nil {
		t.Fatal(err)
	}
	in["environment"].(map[string]any)[long] = long + "日本語🚀"
	in["role"] = "arn:aws:iam::123456789012:role/" + long
	in["vpc"] = map[string]any{"subnetIds": []string{"subnet-" + long, "subnet-2"}, "securityGroupIds": []string{"sg-1"}}
	fn.Inputs, _ = json.Marshal(in)
	st.Resources["schedule/api/"+long] = &state.Resource{PhysicalID: "s"}
	f.function = cli.FunctionState{State: "Failed", StateReason: long, CodeSHA256: "ff", Memory: 128, Timeout: 900, Runtime: long}
	for _, size := range [][2]int{{78, 22}, {80, 24}, {100, 30}, {200, 60}} {
		w, h := size[0], size[1]
		m := pressKeys(t, started(t, f), "enter")
		m, _ = update(t, m, tea.WindowSizeMsg{Width: w, Height: h})
		for _, k := range []string{"j", "G"} {
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
			m = pressKeys(t, m, k)
		}
	}
}
