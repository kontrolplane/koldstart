package cloudcontrol

import (
	"slices"
	"testing"
)

// TestDrift checks what counts as changed outside koldstart: a different value, an element
// gone from a list or an object; and what does not: what aws adds, the order of a list, a
// number aws returns as a string, and a property aws never returns.
func TestDrift(t *testing.T) {
	applied := map[string]any{
		"VisibilityTimeout": 30.0,
		"Tags":              []any{map[string]any{"Key": "team", "Value": "shop"}, map[string]any{"Key": "env", "Value": "dev"}},
		"RedrivePolicy":     map[string]any{"maxReceiveCount": 5.0},
		"Password":          "secret",
		"Arn":               "arn:x",
		"KmsKey":            "",
	}
	live := map[string]any{
		"VisibilityTimeout": "30",
		"Tags":              []any{map[string]any{"Key": "env", "Value": "dev"}, map[string]any{"Key": "team", "Value": "shop"}},
		"RedrivePolicy":     map[string]any{"maxReceiveCount": 5.0, "deadLetterTargetArn": "arn:dead"},
		"Arn":               "arn:y",
	}
	schema := &Schema{ReadOnly: []string{"Arn"}, WriteOnly: []string{"Password"}}
	if got := Drift(applied, live, schema); len(got) != 0 {
		t.Errorf("Drift of the same = %v", got)
	}

	live["VisibilityTimeout"] = 60.0
	live["Tags"] = []any{map[string]any{"Key": "env", "Value": "dev"}}
	delete(live, "RedrivePolicy")
	if got, want := Drift(applied, live, schema), []string{"RedrivePolicy", "Tags", "VisibilityTimeout"}; !slices.Equal(got, want) {
		t.Errorf("Drift = %v, want %v", got, want)
	}
	if got, want := Drift(applied, live, nil), []string{"Arn", "Tags", "VisibilityTimeout"}; !slices.Equal(got, want) {
		t.Errorf("Drift without a schema = %v, want %v", got, want)
	}
}
