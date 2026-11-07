package engine

import (
	"slices"
	"testing"
)

// TestAfterDeletes checks a node with AfterDeletes, and what depends on it, is applied after the
// nodes gone from the configuration are deleted, as a deployment of a rest api must be, and the
// rest before them.
func TestAfterDeletes(t *testing.T) {
	h := newHarness(t)
	route := func(path string) Node {
		return Node{ID: "restroute/GET " + path, Type: fakeType, Name: path, Inputs: map[string]any{"path": path}}
	}
	stage := func(shape string, routes ...string) []Node {
		return []Node{
			{ID: "reststage", Type: fakeType, Name: "stage", Inputs: map[string]any{"deployment": shape}, After: routes, AfterDeletes: true},
			{ID: "restusageplan", Type: fakeType, Name: "plan", Inputs: map[string]any{"stage": Ref("reststage", "Name")}},
		}
	}
	first := append([]Node{route("/a"), route("/b")}, stage("1", "restroute/GET /a", "restroute/GET /b")...)
	if _, err := h.deploy(first); err != nil {
		t.Fatal(err)
	}
	h.fake.ResetCalls()
	second := append([]Node{route("/a"), route("/c")}, stage("2", "restroute/GET /a", "restroute/GET /c")...)
	if _, err := h.deploy(second); err != nil {
		t.Fatal(err)
	}
	calls := h.fake.Calls()
	tests := []struct {
		before, after string
	}{
		{before: "create:restroute/GET /c", after: "delete:restroute/GET /b"},
		{before: "delete:restroute/GET /b", after: "update:reststage"},
	}
	for _, tt := range tests {
		b, a := slices.Index(calls, tt.before), slices.Index(calls, tt.after)
		if b < 0 || a < 0 || b > a {
			t.Errorf("%s does not come before %s: %v", tt.before, tt.after, calls)
		}
	}
	if last := lastIDs(second); !last["reststage"] || !last["restusageplan"] || last["restroute/GET /a"] {
		t.Errorf("lastIDs = %v, want the stage and the usage plan that refers to it", last)
	}
}
