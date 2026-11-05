package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/kontrolplane/koldstart/internal/engine"
)

// TestDescribeChange checks short values show before and after, and the rest say what happened.
func TestDescribeChange(t *testing.T) {
	tests := []struct {
		before, after any
		want          string
	}{
		{before: 256.0, after: 1024.0, want: "256 → 1024"},
		{before: "a", after: "b", want: `"a" → "b"`},
		{before: nil, after: map[string]any{"a": 1.0}, want: "added"},
		{before: "a", after: nil, want: "removed"},
		{before: "a", after: map[string]any{"$koldstart": "unknown"}, want: "known after apply"},
		{before: "a", after: map[string]any{"$koldstart": "secret", "name": "x"}, want: "(a secret)"},
		{before: strings.Repeat("a", 100), after: "b", want: "changed"},
		{before: map[string]any{"sha256": "0123456789abcdef"}, after: map[string]any{"sha256": "fedcba9876543210"}, want: "0123456789ab → fedcba987654"},
	}
	for _, tt := range tests {
		if got := describeChange(tt.before, tt.after); got != tt.want {
			t.Errorf("describeChange(%v, %v) = %q, want %q", tt.before, tt.after, got, tt.want)
		}
	}
}

// TestFieldChanges checks the properties of a resource are listed one by one.
func TestFieldChanges(t *testing.T) {
	c := engine.Change{Fields: []string{"properties"},
		Before: map[string]any{"properties": map[string]any{"VisibilityTimeout": 30.0, "QueueName": "q"}},
		After:  map[string]any{"properties": map[string]any{"VisibilityTimeout": 120.0, "QueueName": "q", "DelaySeconds": 5.0}},
	}
	got := strings.Join(FieldChanges(c), "; ")
	if got != "properties.DelaySeconds: added; properties.VisibilityTimeout: 30 → 120" {
		t.Errorf("FieldChanges = %q", got)
	}
}

// TestPrintPlan checks each action is marked, updates list their fields, and the summary counts.
func TestPrintPlan(t *testing.T) {
	plan := &engine.Plan{Changes: []engine.Change{
		{ID: "role/api", Action: engine.NoOp},
		{ID: "function/api", Action: engine.Update, Fields: []string{"memory"},
			Before: map[string]any{"memory": 256.0}, After: map[string]any{"memory": 1024.0}},
		{ID: "url/api", Action: engine.Create},
		{ID: "resource/orders", Action: engine.Keep},
	}}
	var out bytes.Buffer
	printPlanTo(&out, "orders", "dev", "eu-west-1", "123456789012", plan)
	got := out.String()
	for _, want := range []string{"~   function/api", "memory: 256 → 1024", "+   url/api", "=   resource/orders", "1 to create, 1 to update, 1 to leave in place"} {
		if !strings.Contains(got, want) {
			t.Errorf("the plan lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "role/api") {
		t.Errorf("a node without changes is listed:\n%s", got)
	}
}

// TestPrintRefreshedPlan checks a plan with refresh says what was deleted and changed outside
// koldstart.
func TestPrintRefreshedPlan(t *testing.T) {
	plan := &engine.Plan{Refreshed: true, Changes: []engine.Change{
		{ID: "loggroup/api", Action: engine.Create, Gone: true},
		{ID: "function/api", Action: engine.Update, Drift: []string{"environment", "memory"}},
	}}
	var out bytes.Buffer
	printPlanTo(&out, "orders", "dev", "eu-west-1", "123456789012", plan)
	got := out.String()
	for _, want := range []string{"+   loggroup/api  (deleted outside koldstart)", "~   function/api\n        changed outside koldstart: environment, memory", "1 to create, 1 to update"} {
		if !strings.Contains(got, want) {
			t.Errorf("the plan lacks %q:\n%s", want, got)
		}
	}
}

// TestPrintPlanColumns checks the ids line up in one column, a resource shows its type after
// it, and nothing is coloured when the output is not a terminal.
func TestPrintPlanColumns(t *testing.T) {
	plan := &engine.Plan{Changes: []engine.Change{
		{ID: "function/api", Type: "koldstart::Function", Action: engine.Create},
		{ID: "resource/orders", Type: "AWS::DynamoDB::Table", Action: engine.Replace, Resume: true},
		{ID: "url/api", Type: "koldstart::Url", Action: engine.Delete},
	}}
	var out bytes.Buffer
	printPlanTo(&out, "orders", "dev", "eu-west-1", "123456789012", plan)
	want := "plan for orders/dev in eu-west-1, account 123456789012\n\n" +
		"  +   function/api\n" +
		"  -/+ resource/orders  AWS::DynamoDB::Table  (finishing what a deploy started)\n" +
		"  -   url/api\n" +
		"\n1 to create, 1 to replace, 1 to delete\n"
	if got := out.String(); got != want {
		t.Errorf("the plan is\n%s\nwant\n%s", got, want)
	}
}

// TestPrintPlanNoChanges checks a plan without changes says what it compared, with refresh and
// without.
func TestPrintPlanNoChanges(t *testing.T) {
	tests := []struct {
		name string
		plan *engine.Plan
		want string
	}{
		{name: "from the state", plan: &engine.Plan{Changes: []engine.Change{{ID: "role/api", Action: engine.NoOp}}},
			want: "  no changes: what the state says is deployed matches the configuration\n"},
		{name: "refreshed", plan: &engine.Plan{Refreshed: true},
			want: "  no changes: what is in aws matches the configuration\n"},
	}
	for _, tt := range tests {
		var out bytes.Buffer
		printPlanTo(&out, "orders", "dev", "eu-west-1", "123456789012", tt.plan)
		if got := out.String(); !strings.HasSuffix(got, "\n\n"+tt.want) {
			t.Errorf("%s: the plan is\n%s\nwant it to end with\n%s", tt.name, got, tt.want)
		}
	}
}
