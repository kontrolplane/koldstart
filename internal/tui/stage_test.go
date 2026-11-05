package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/kontrolplane/koldstart/internal/cli"
	"github.com/kontrolplane/koldstart/internal/state"
	"github.com/kontrolplane/koldstart/internal/tui/styles"
)

// TestStageCursorKept checks the cursor stays on its row when a refresh adds rows above it, on
// the resources and the history.
func TestStageCursorKept(t *testing.T) {
	tests := []struct {
		name   string
		keys   []string
		change func(f *fakeBackend)
		cursor func(m model) string
		want   string
	}{
		{
			name: "a resource",
			keys: []string{"tab", "G"},
			change: func(f *fakeBackend) {
				f.states[ordersDev].Resources["function/aaa"] = &state.Resource{Type: "koldstart::Lambda::Function", PhysicalID: "orders-dev-aaa"}
			},
			cursor: func(m model) string {
				s := m.state.stage
				return s.resources[s.tables[resourcesTab].cursor].id
			},
			want: "resource/table",
		},
		{
			name: "a deploy",
			keys: []string{"shift+tab", "j"},
			change: func(f *fakeBackend) {
				f.history = append([]state.ObjectVersion{{ID: "v3", LastModified: time.Now().Add(time.Hour), Latest: true}}, f.history...)
				f.history[1].Latest = false
			},
			cursor: func(m model) string { return m.stageCopyValue() },
			want:   "v1",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFake(t)
			m := pressKeys(t, started(t, f), tt.keys...)
			if got := tt.cursor(m); got != tt.want {
				t.Fatalf("before: cursor on %q, want %q", got, tt.want)
			}
			tt.change(f)
			m = pressKeys(t, m, "r")
			if got := tt.cursor(m); got != tt.want {
				t.Errorf("after a refresh: cursor on %q, want %q", got, tt.want)
			}
		})
	}
}

// TestOutputValue checks an output is drawn on one line, with how many lines more it has.
func TestOutputValue(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "one line", in: "https://api.example.com", want: "https://api.example.com"},
		{name: "empty", in: "", want: "empty"},
		{name: "a trailing newline", in: "abc\n", want: "abc"},
		{name: "three lines", in: "-----BEGIN-----\nMIIB\n-----END-----", want: "-----BEGIN-----  +2 lines"},
		{name: "windows lines", in: "a\r\nb\r\n", want: "a  +1 line"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := plainText(outputValue(tt.in)); got != tt.want {
				t.Errorf("outputValue(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// TestMetricCells checks the calls, errors and p99 of a function while they are read, where
// they cannot be, and in the units they are read in.
func TestMetricCells(t *testing.T) {
	series := func(v float64) []float64 { return []float64{0, v} }
	tests := []struct {
		name    string
		metrics map[string]cli.Metrics
		err     string
		want    string
		danger  bool
	}{
		{name: "reading", want: "… … …"},
		{name: "unreadable", err: "aws said no", want: "- - -"},
		{name: "no calls", metrics: map[string]cli.Metrics{"fn": {Invocations: series(0), Errors: series(0), Throttles: series(0), DurationP99: series(0)}}, want: "0 0 -"},
		{name: "under a millisecond", metrics: map[string]cli.Metrics{"fn": {Invocations: series(3), Errors: series(0), Throttles: series(0), DurationP99: series(0.4)}}, want: "3 0 <1ms"},
		{name: "errors and seconds", metrics: map[string]cli.Metrics{"fn": {Invocations: series(12000), Errors: series(4), Throttles: series(0), DurationP99: series(1234)}}, want: "12,000 4 1.2s", danger: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := stageState{metrics: tt.metrics, metricsErr: tt.err}
			calls, errs, p99 := s.metricCells("fn")
			if got := plainText(calls) + " " + plainText(errs) + " " + plainText(p99); got != tt.want {
				t.Errorf("cells %q, want %q", got, tt.want)
			}
			if danger := errs[0].Tone == styles.ToneDanger; danger != tt.danger {
				t.Errorf("errors in danger %v, want %v", danger, tt.danger)
			}
		})
	}
}

// TestStageMetricsUnavailable checks the functions tab says why it has no metrics, as on an
// emulator, and shows the rest.
func TestStageMetricsUnavailable(t *testing.T) {
	f := newFake(t)
	f.metricsErr = errAWS
	m := started(t, f)
	if v := screen(m); m.error != "" || !strings.Contains(v, "could not read the metrics: aws said no") || !strings.Contains(v, "orders-dev-api") {
		t.Errorf("error %q; screen:\n%s", m.error, v)
	}
	if v := screen(pressKeys(t, m, "tab")); strings.Contains(v, "metrics") {
		t.Errorf("the resources tab says there are no metrics:\n%s", v)
	}
}

// TestStageEmpty checks what a tab says without rows: when the filter hides them, and when the
// history could not be listed.
func TestStageEmpty(t *testing.T) {
	m := pressKeys(t, started(t, newFake(t)), "/")
	m = typeText(t, m, "nothing-like-this")
	m = pressKeys(t, m, "enter")
	for _, tab := range []string{"no function matches nothing-like-this", "no resource matches nothing-like-this", "no output matches nothing-like-this", "no deploy matches nothing-like-this"} {
		if v := screen(m); !strings.Contains(v, tab) {
			t.Errorf("no %q:\n%s", tab, v)
		}
		m = pressKeys(t, m, "tab")
	}

	f := newFake(t)
	m = started(t, f)
	f.err = errAWS
	m = pressKeys(t, m, "shift+tab")
	if m.error == "" {
		t.Fatal("a history that cannot be listed raised no error")
	}
	m = pressKeys(t, m, "esc")
	if v := screen(m); !strings.Contains(v, "could not list the history: aws said no · r tries again") {
		t.Errorf("the history tab:\n%s", v)
	}
	f.err = nil
	if m = pressKeys(t, m, "r"); len(m.state.stage.shownDeploys) != 2 {
		t.Errorf("r listed %d deploys, want 2", len(m.state.stage.shownDeploys))
	}
}

// TestStageUnreadable checks a stage whose state could not be read says so once the error is
// closed, and r reads it again.
func TestStageUnreadable(t *testing.T) {
	f := newFake(t)
	m := started(t, f)
	f.err = errAWS
	m, cmd := m.openStage(ordersProd)
	m = pressKeys(t, settle(t, m, cmd), "esc")
	if v := screen(m); !strings.Contains(v, "could not read the state of orders/prod: aws said no · r tries again") {
		t.Fatalf("the stage:\n%s", v)
	}
	f.err = nil
	if v := screen(pressKeys(t, m, "r")); !strings.Contains(v, "orders-prod-api") {
		t.Errorf("r did not read the stage again:\n%s", v)
	}
}

// TestHistoryFilter checks the filter narrows the deploys, and enter opens the one under the
// cursor, not the one at its place in the whole history.
func TestHistoryFilter(t *testing.T) {
	m := pressKeys(t, started(t, newFake(t)), "shift+tab", "/")
	m = typeText(t, m, "v1")
	m = pressKeys(t, m, "enter")
	if n := len(m.state.stage.shownDeploys); n != 1 {
		t.Fatalf("%d deploys match v1, want 1", n)
	}
	if m = pressKeys(t, m, "enter"); m.page != versionPage || m.state.version.version.ID != "v1" {
		t.Errorf("page %v on %q, want the version v1", m.page, m.state.version.version.ID)
	}
}

// TestResourceTreeOrphans checks what is left of a function a remove took, without the function
// itself, is not drawn under the group before it, and a queue shows its depth beside its note.
func TestResourceTreeOrphans(t *testing.T) {
	f := newFake(t)
	st := f.states[ordersDev]
	st.Retained = map[string]*state.Resource{"loggroup/old": {Type: "koldstart::Logs::LogGroup", PhysicalID: "/aws/lambda/old"}}
	st.Resources["resource/jobs"] = &state.Resource{Type: "AWS::SQS::Queue", PhysicalID: "https://sqs/jobs", DeletionPolicy: "retain"}
	m := pressKeys(t, started(t, f), "tab")
	for _, r := range m.state.stage.resources {
		if r.id == "loggroup/old" && r.depth != 0 {
			t.Errorf("loggroup/old at depth %d, want 0", r.depth)
		}
	}
	if v := screen(m); !strings.Contains(v, "retained · 12 queued") {
		t.Errorf("the queue's depth and note:\n%s", v)
	}
}
