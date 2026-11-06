package tui

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kontrolplane/koldstart/internal/cli"
	"github.com/kontrolplane/koldstart/internal/provider/awsp"
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
				r, _ := m.state.stage.selectedResource()
				return r.id
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

// TestOutputValue checks an output is drawn on one line with a hint of what more it holds: json
// compacted, with its keys or items, and text with how many lines more it has.
func TestOutputValue(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
		hint string
	}{
		{name: "one line", in: "https://api.example.com", want: "https://api.example.com"},
		{name: "empty", in: "", want: "empty"},
		{name: "a trailing newline", in: "abc\n", want: "abc"},
		{name: "three lines", in: "-----BEGIN-----\nMIIB\n-----END-----", want: "-----BEGIN-----", hint: "+2 lines"},
		{name: "windows lines", in: "a\r\nb\r\n", want: "a", hint: "+1 line"},
		{name: "an object", in: `{"host": "db", "port": 5432, "tags": {"a": 1}, "on": true}`, want: `{"host":"db","port":5432,"tags":{"a":1},"on":true}`, hint: "{…} 4 keys"},
		{name: "an object on lines", in: "{\n  \"a\": [1, 2]\n}\n", want: `{"a":[1,2]}`, hint: "{…} 1 key"},
		{name: "a list", in: `["a", "b", "c"]`, want: `["a","b","c"]`, hint: "[…] 3 items"},
		{name: "an empty list", in: `[]`, want: `[]`, hint: "[…] 0 items"},
		{name: "broken json", in: `{"a":`, want: `{"a":`},
		{name: "a json string", in: `"quoted"`, want: `"quoted"`},
		{name: "a number", in: `42`, want: `42`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			value, hint := outputValue(tt.in)
			if got := plainText(value); got != tt.want {
				t.Errorf("value %q, want %q", got, tt.want)
			}
			if got := plainText(hint); got != tt.hint {
				t.Errorf("hint %q, want %q", got, tt.hint)
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

// sdFunctionNames are the names the fixture of a stage gives its first functions.
var sdFunctionNames = []string{"api", "checkout", "orders-worker", "invoice-render", "nightly-report", "webhooks", "search-indexer", "image-resize"}

// sdARN is an arn of the fixture's account and region.
func sdARN(service, rest string) string {
	return "arn:aws:" + service + ":eu-west-1:123456789012:" + rest
}

// sdStage is a fake with orders/dev as a real stage holds it: functions with their roles, log
// groups, urls, routes and schedules, tables, queues, buckets and topics to make 200 resources,
// one of them pending and one kept by a remove, outputs that are urls, arns, json and a
// certificate, 50 deploys in its history, some unfinished, and a lock.
func sdStage(t *testing.T, functions int) *fakeBackend {
	t.Helper()
	f := newFake(t)
	st := testState(ordersDev)
	for _, id := range []string{"function/api", "role/api", "loggroup/api"} {
		delete(st.Resources, id)
	}
	f.metrics = map[string]cli.Metrics{}
	for i := range functions {
		name := fmt.Sprintf("fn-%02d", i)
		if i < len(sdFunctionNames) {
			name = sdFunctionNames[i]
		}
		sdFunction(st, f.metrics, i, name)
	}
	st.Resources["httpapi"] = &state.Resource{Type: awsp.TypeHTTPAPI, PhysicalID: "a1b2c3d4e5"}
	st.Resources["domain"] = &state.Resource{Type: awsp.TypeDomain, PhysicalID: "api.orders.example.com"}
	st.Resources["authorizer/jwt"] = &state.Resource{Type: awsp.TypeAuthorizer, PhysicalID: "auth01"}
	for i := 0; len(st.Resources) < 200; i++ {
		switch i % 4 {
		case 0:
			st.Resources[fmt.Sprintf("resource/table-%02d", i)] = &state.Resource{Type: "AWS::DynamoDB::Table", PhysicalID: fmt.Sprintf("orders-dev-table-%02d", i)}
		case 1:
			st.Resources[fmt.Sprintf("resource/jobs-%02d", i)] = &state.Resource{Type: "AWS::SQS::Queue", PhysicalID: fmt.Sprintf("https://sqs.eu-west-1.amazonaws.com/123456789012/orders-dev-jobs-%02d", i)}
		case 2:
			bucket := fmt.Sprintf("orders-dev-uploads-%02d-123456789012-eu-west-1", i)
			st.Resources[fmt.Sprintf("resource/uploads-%02d", i)] = &state.Resource{Type: "AWS::S3::Bucket", PhysicalID: bucket}
			st.Resources[fmt.Sprintf("notification/uploads-%02d", i)] = &state.Resource{Type: awsp.TypeNotification, PhysicalID: bucket}
		default:
			st.Resources[fmt.Sprintf("resource/events-%02d", i)] = &state.Resource{Type: "AWS::SNS::Topic", PhysicalID: sdARN("sns", fmt.Sprintf("orders-dev-events-%02d", i))}
		}
	}
	st.Resources["resource/table"].Pending = &state.Pending{Operation: "update", StartedAt: time.Now().Add(-3 * time.Hour)}
	st.Retained = map[string]*state.Resource{"resource/archive": {Type: "AWS::S3::Bucket", PhysicalID: "orders-dev-archive-123456789012-eu-west-1"}}
	st.Outputs = map[string]string{
		"apiUrl":       "https://a1b2c3d4e5.execute-api.eu-west-1.amazonaws.com",
		"tableArn":     sdARN("dynamodb", "table/orders-dev-table-00"),
		"workflowArn":  sdARN("states", "stateMachine:orders-dev-checkout-fulfilment-workflow-with-a-long-name"),
		"queueUrls":    `["https://sqs.eu-west-1.amazonaws.com/123456789012/orders-dev-jobs-01","https://sqs.eu-west-1.amazonaws.com/123456789012/orders-dev-jobs-05"]`,
		"database":     `{"host":"orders.cluster-c9akciq32.eu-west-1.rds.amazonaws.com","port":5432,"name":"orders","tags":{"team":"payments"}}`,
		"certificate":  "-----BEGIN CERTIFICATE-----\nMIIBszCCAVmgAwIBAgIUQ\n-----END CERTIFICATE-----",
		"dashboardUrl": "https://eu-west-1.console.aws.amazon.com/cloudwatch/home?region=eu-west-1#dashboards/dashboard/orders-dev-overview?start=PT3H&end=null",
		"emptyNote":    "",
	}
	st.DeployedAt, st.DeployedBy, st.Serial = time.Now().Add(-3*time.Hour), "levi", 212
	f.states[ordersDev] = st
	f.locks[ordersDev] = &state.LockInfo{Who: "ana", Where: "ana-mbp", Operation: "deploy", Since: time.Now().Add(-4 * time.Minute)}
	sdHistory(f, st, 50)
	return f
}

// sdFunction adds the i-th function of the fixture, its role and log group, a url to every
// third, a route to every second and a schedule to every fifth, and its metrics: every fourth
// not invoked, and every fourth from the second failing in the last ten minutes.
func sdFunction(st *state.State, metrics map[string]cli.Metrics, i int, name string) {
	physical := "orders-dev-" + name
	in, _ := json.Marshal(map[string]any{
		"name": physical, "runtime": []string{"nodejs22.x", "provided.al2023", "python3.13"}[i%3], "handler": "src/" + name + ".handler",
		"memory": []int{128, 512, 1024, 3008}[i%4], "timeout": []int{6, 30, 900}[i%3],
		"code": map[string]any{"bucket": "b", "key": "k", "sha256": fmt.Sprintf("%x", sha256.Sum256([]byte(name)))},
	})
	st.Resources["function/"+name] = &state.Resource{Type: awsp.TypeFunction, PhysicalID: physical, Inputs: in, Attributes: map[string]string{"Arn": sdARN("lambda", "function:"+physical)}}
	st.Resources["role/"+name] = &state.Resource{Type: awsp.TypeRole, PhysicalID: physical}
	st.Resources["loggroup/"+name] = &state.Resource{Type: awsp.TypeLogGroup, PhysicalID: "/aws/lambda/" + physical}
	if i%3 == 0 {
		st.Resources["url/"+name] = &state.Resource{Type: awsp.TypeURL, PhysicalID: sdARN("lambda", "function:"+physical), Attributes: map[string]string{"FunctionUrl": "https://" + name + ".lambda-url.eu-west-1.on.aws/"}}
	}
	if i%2 == 0 {
		st.Resources["integration/"+name] = &state.Resource{Type: awsp.TypeIntegration, PhysicalID: fmt.Sprintf("int%04d", i)}
		st.Resources["route/GET /"+name] = &state.Resource{Type: awsp.TypeRoute, PhysicalID: fmt.Sprintf("rt%04d", i), Dependencies: []string{"httpapi", "integration/" + name}}
		st.Resources["permission/"+name+"/httpapi"] = &state.Resource{Type: awsp.TypePermission, PhysicalID: physical + "-httpapi"}
	}
	if i%5 == 4 {
		st.Resources["schedule/"+name+"/nightly"] = &state.Resource{Type: awsp.TypeSchedule, PhysicalID: physical + "-nightly"}
	}
	calls, errs := make([]float64, 60), make([]float64, 60)
	for j := range calls {
		calls[j] = float64((j*(i+3))%17) * float64(i%4)
		if i%4 == 1 && j > 50 {
			errs[j] = 2
		}
	}
	metrics[physical] = cli.Metrics{Invocations: calls, Errors: errs, Throttles: make([]float64, 60), DurationP99: calls, Concurrency: calls}
}

// sdHistory gives the fake n deploys of st, five hours apart and newest first, each of one to
// four writes. Every second deploy adds three resources, every fourth removes one the deploy
// before added, and each changes a table; the newest, and every sixth from the third, left a
// table pending.
func sdHistory(f *fakeBackend, st *state.State, n int) {
	ids := slices.Sorted(maps.Keys(st.Resources))
	f.history, f.versions = nil, map[string]*state.State{}
	for k := range n {
		at := st.DeployedAt.Add(-time.Duration(k) * 5 * time.Hour)
		writes := 1 + k%4
		for w := range writes {
			f.history = append(f.history, state.ObjectVersion{ID: fmt.Sprintf("d%02d-%d", k, writes-1-w), LastModified: at.Add(-time.Duration(w) * 20 * time.Second), Latest: k == 0 && w == 0})
		}
		v := st.Clone()
		for _, id := range ids[max(0, len(ids)-3*((k+1)/2)):] {
			delete(v.Resources, id)
		}
		if k%4 == 1 {
			v.Resources[fmt.Sprintf("resource/gone-%02d", k)] = &state.Resource{Type: "AWS::SQS::Queue", PhysicalID: "gone"}
		}
		if r := v.Resources["resource/table"]; r != nil && k > 0 {
			r.Pending = nil
		}
		v.Resources["resource/table-00"] = &state.Resource{Type: "AWS::DynamoDB::Table", PhysicalID: "orders-dev-table-00", Inputs: json.RawMessage(fmt.Sprintf(`{"properties":{"v":%d}}`, k))}
		if k%6 == 2 {
			v.Resources["resource/table-00"].Pending = &state.Pending{Operation: "update", StartedAt: at}
		}
		v.DeployedBy = []string{"levi", "ana", "ci@github-actions"}[k%3]
		f.versions[fmt.Sprintf("d%02d-%d", k, writes-1)] = v
	}
}
