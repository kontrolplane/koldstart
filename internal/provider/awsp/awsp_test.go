package awsp

import (
	"encoding/json"
	"testing"
)

// TestConfigChanged checks the configuration is told apart from the code and the concurrency,
// which have calls of their own.
func TestConfigChanged(t *testing.T) {
	base := FunctionInputs{Name: "f", Memory: 128, Code: Code{SHA256: "a"}}
	codeOnly := base
	codeOnly.Code.SHA256 = "b"
	codeOnly.Architecture = "x86_64"
	if configChanged(base, codeOnly) {
		t.Error("a code change counts as a configuration change")
	}
	memory := base
	memory.Memory = 256
	if !configChanged(base, memory) {
		t.Error("a memory change does not count")
	}
}

// TestReplaces checks only a new name needs a new resource.
func TestReplaces(t *testing.T) {
	a, _ := json.Marshal(FunctionInputs{Name: "a", Memory: 128})
	b, _ := json.Marshal(FunctionInputs{Name: "a", Memory: 256})
	c, _ := json.Marshal(FunctionInputs{Name: "c", Memory: 128})
	if (Function{}).Replaces(a, b) || !(Function{}).Replaces(a, c) {
		t.Error("Function.Replaces")
	}
	r1, _ := json.Marshal(RoleInputs{Name: "r", Policy: map[string]any{"a": 1}})
	r2, _ := json.Marshal(RoleInputs{Name: "r", Policy: map[string]any{"a": 2}})
	if (Role{}).Replaces(r1, r2) {
		t.Error("a policy change replaces the role")
	}
}

// TestPartition checks the partition of china and govcloud regions.
func TestPartition(t *testing.T) {
	for region, want := range map[string]string{"eu-west-1": "aws", "cn-north-1": "aws-cn", "us-gov-west-1": "aws-us-gov"} {
		if got := Partition(region); got != want {
			t.Errorf("Partition(%s) = %s, want %s", region, got, want)
		}
	}
}
