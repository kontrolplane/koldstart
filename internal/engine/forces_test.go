package engine

import (
	"context"
	"maps"
	"slices"
	"testing"
)

// TestReplaceForces checks a replace names the fields that need a new resource, the properties
// of a resource one by one, and not the fields that could change in place, and says whether the
// old one is retained.
func TestReplaceForces(t *testing.T) {
	tests := []struct {
		name     string
		replace  []string
		inputs   map[string]any
		retained bool
		want     []string
	}{
		{name: "one field of two", replace: []string{"key"}, inputs: map[string]any{"key": "pk", "memory": 512}, want: []string{"key"}},
		{name: "a property", replace: []string{"properties"}, inputs: map[string]any{"properties": map[string]any{"KeySchema": "pk", "BillingMode": "PROVISIONED"}}, want: []string{"properties.BillingMode", "properties.KeySchema"}},
		{name: "retained", replace: []string{"key"}, inputs: map[string]any{"key": "pk"}, retained: true, want: []string{"key"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)
			h.engine.AllowReplace = true
			node := Node{ID: "resource/orders", Type: fakeType, Name: "orders", Inputs: map[string]any{
				"key": "id", "memory": 256, "properties": map[string]any{"KeySchema": "id", "BillingMode": "PAY_PER_REQUEST"},
			}}
			if tt.retained {
				node.DeletionPolicy = PolicyRetain
			}
			if _, err := h.deploy([]Node{node}); err != nil {
				t.Fatal(err)
			}
			h.fake.ReplaceKeys = tt.replace
			node.Inputs = maps.Clone(node.Inputs)
			maps.Copy(node.Inputs, tt.inputs)
			st, _ := h.read()
			plan, err := h.engine.Plan(context.Background(), []Node{node}, st)
			if err != nil {
				t.Fatal(err)
			}
			c, _ := plan.Change("resource/orders")
			if c.Action != Replace || !slices.Equal(c.Forces, tt.want) || c.Retained != tt.retained {
				t.Errorf("%s forced by %v, retained %v; want a replace forced by %v, retained %v", c.Action, c.Forces, c.Retained, tt.want, tt.retained)
			}
		})
	}
}
