package desired

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/kontrolplane/koldstart/internal/engine"
	"github.com/kontrolplane/koldstart/internal/state"
)

// deployed is the state of a deploy of nodes, their refs resolved as the engine resolves them:
// every attribute of a node is arn:<its id>:<attribute>, but the orders table's, which are its own.
func deployed(t *testing.T, nodes []engine.Node) map[string]*state.Resource {
	t.Helper()
	table := map[string]string{"Arn": "arn:aws:dynamodb:eu-west-1:123456789012:table/orders-dev-orders", "TableName": "orders-dev-orders"}
	attr := func(node, attribute string) string {
		if node == "resource/orders" {
			return table[attribute]
		}
		return "arn:" + node + ":" + attribute
	}
	var resolve func(v any) any
	resolve = func(v any) any {
		switch v := v.(type) {
		case map[string]any:
			switch v["$koldstart"] {
			case "ref":
				return attr(v["node"].(string), v["attribute"].(string))
			case "join":
				var b strings.Builder
				for _, p := range v["parts"].([]any) {
					b.WriteString(resolve(p).(string))
				}
				return b.String()
			}
			out := map[string]any{}
			for k, x := range v {
				out[k] = resolve(x)
			}
			return out
		case []any:
			out := make([]any, len(v))
			for i, x := range v {
				out[i] = resolve(x)
			}
			return out
		}
		return v
	}
	out := map[string]*state.Resource{}
	for _, n := range nodes {
		id := n.ID
		data, err := json.Marshal(n.Inputs)
		if err != nil {
			t.Fatal(err)
		}
		var generic any
		if err := json.Unmarshal(data, &generic); err != nil {
			t.Fatal(err)
		}
		stored, err := json.Marshal(resolve(generic))
		if err != nil {
			t.Fatal(err)
		}
		r := &state.Resource{Type: n.Type, Inputs: stored, PhysicalID: id}
		if id == "resource/orders" {
			r.Type, r.PhysicalID, r.Attributes = "AWS::DynamoDB::Table", table["TableName"], table
		}
		out[id] = r
	}
	return out
}

// TestLinks checks the resources a deployed function links to are found by the variables link
// gives it, with the access its role's policy grants, and that permissions alone are no link.
func TestLinks(t *testing.T) {
	tests := []struct {
		name   string
		access string
		fn     string
		want   []Link
	}{
		{name: "read", access: "read", fn: "api", want: []Link{{ID: "resource/orders", Access: "read"}}},
		{name: "write", access: "write", fn: "api", want: []Link{{ID: "resource/orders", Access: "write"}}},
		{name: "readwrite", access: "readwrite", fn: "api", want: []Link{{ID: "resource/orders", Access: "readwrite"}}},
		{name: "permissions alone", access: "read", fn: "worker"},
		{name: "no such function", access: "read", fn: "missing"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := project(t)
			api := p.Functions["api"]
			api.Link = map[string]string{"orders": tt.access}
			p.Functions["api"] = api
			nodes, err := Build(p, target())
			if err != nil {
				t.Fatalf("Build: %v", err)
			}
			resources := deployed(t, nodes)
			if got := Links(resources, tt.fn); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Links(%s) = %v, want %v", tt.fn, got, tt.want)
			}
			if got := LinksByFunction(resources)[tt.fn]; !reflect.DeepEqual(got, tt.want) {
				t.Errorf("LinksByFunction()[%s] = %v, want %v", tt.fn, got, tt.want)
			}
		})
	}
}
