package awsp

import (
	"encoding/json"
	"strings"
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

// TestImageInputs checks a container image function has no runtime, handler or layers, and its
// image as its code, and a zip function its layers, an empty list to remove them.
func TestImageInputs(t *testing.T) {
	zip := FunctionInputs{Runtime: "python3.12", Handler: "src/app.handler", Code: Code{Bucket: "b", Key: "k", SHA256: "a"}}
	img := FunctionInputs{Code: Code{ImageURI: "r/x:t", SHA256: "a"}, ImageConfig: &ImageConfig{Command: []string{"app.handler"}}}
	if zip.packageType() != "Zip" || zip.runtime() != "python3.12" || *zip.handler() != "src/app.handler" || zip.code().ImageUri != nil {
		t.Errorf("a zip function: %v %v %v", zip.packageType(), zip.runtime(), zip.code())
	}
	if l := zip.layers(); l == nil || len(l) != 0 {
		t.Errorf("a zip function without layers sends %v, want an empty list", l)
	}
	if img.packageType() != "Image" || img.runtime() != "" || img.handler() != nil || *img.code().ImageUri != "r/x:t" || img.layers() != nil {
		t.Errorf("an image function: %v %q %v %v", img.packageType(), img.runtime(), img.handler(), img.layers())
	}
	if c := img.imageConfig(); len(c.Command) != 1 || zip.imageConfig() != nil {
		t.Errorf("image config: %v, %v", c, zip.imageConfig())
	}
	a, _ := json.Marshal(zip)
	b, _ := json.Marshal(img)
	if !(Function{}).Replaces(a, b) {
		t.Error("a zip function turned into an image is not replaced")
	}
	if strings.Contains(string(a), "layers") || strings.Contains(string(a), "imageConfig") || strings.Contains(string(a), "imageUri") {
		t.Errorf("a function without them writes them: %s", a)
	}
}
