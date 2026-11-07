package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/kontrolplane/koldstart/internal/engine"
)

// TestPlanOpensInputs checks enter on a create with nothing deployed before it shows its inputs
// in the json viewer, secrets by what they stand for, and the rest open their diff: an update,
// and a create of what was deleted outside koldstart, which has what was applied to diff against.
func TestPlanOpensInputs(t *testing.T) {
	create := engine.Change{ID: "resource/table", Type: "AWS::DynamoDB::Table", Action: engine.Create,
		After: map[string]any{"properties": map[string]any{"TableName": "orders", "Key": pdSecret}}}
	gone := create
	gone.Gone, gone.Before = true, map[string]any{"properties": map[string]any{"TableName": "orders"}}
	update := engine.Change{ID: "resource/table", Type: "AWS::DynamoDB::Table", Action: engine.Update, Fields: []string{"properties"},
		Before: map[string]any{"properties": map[string]any{"TableName": "a"}}, After: map[string]any{"properties": map[string]any{"TableName": "b"}}}
	tests := []struct {
		name   string
		change engine.Change
		page   page
		want   []string
	}{
		{name: "a create", change: create, page: inputsPage, want: []string{
			"create dynamodb table · its inputs, with nothing deployed to diff against", `"TableName": "orders"`, "(secret ssm:/orders/api-key)", "stages › app orders › stage dev › deploy › resource/table",
		}},
		{name: "created again", change: gone, page: diffPage},
		{name: "an update", change: update, page: diffPage},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := pressKeys(t, pdPlanned(t, tt.change), "enter")
			if m.page != tt.page {
				t.Fatalf("enter on %s opens page %v, want %v", tt.change.ID, m.page, tt.page)
			}
			v := screen(m)
			for _, w := range tt.want {
				if !strings.Contains(v, w) {
					t.Errorf("the page does not say %q:\n%s", w, v)
				}
			}
		})
	}
}

// TestInputsKeys checks the inputs take the keys of the json viewer, which folds, searches and
// says where the cursor is, and q and esc go back to the plan as it was.
func TestInputsKeys(t *testing.T) {
	create := engine.Change{ID: "resource/table", Type: "AWS::DynamoDB::Table", Action: engine.Create,
		After: map[string]any{"properties": map[string]any{"TableName": "orders", "BillingMode": "PAY_PER_REQUEST"}}}
	plan := pdPlanned(t, create)
	m := pressKeys(t, plan, "enter")
	if m.state.json(m.page) == nil {
		t.Fatal("the inputs page has no json view for the frame to show")
	}
	if folded := pressKeys(t, m, "space"); strings.Contains(ansi.Strip(folded.inputsView()), "TableName") {
		t.Errorf("space did not fold the inputs:\n%s", ansi.Strip(folded.inputsView()))
	}
	if found := pressKeys(t, m, "/"); found.pageFilter() == nil || !found.pageFilter().active {
		t.Error("/ does not search the inputs")
	}
	for _, back := range []string{"q", "esc"} {
		if got := pressKeys(t, m, back); got.page != planPage || got.state.plan.cursor != plan.state.plan.cursor {
			t.Errorf("%s goes to page %v, cursor %d; want the plan, cursor %d", back, got.page, got.state.plan.cursor, plan.state.plan.cursor)
		}
	}
}
