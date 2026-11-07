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

// TestFunctionSettings checks what a function sends for its tracing, logs, tags, dead letter
// target and file system, empty values to remove them, and that a function without them writes
// none, so its hash stays.
func TestFunctionSettings(t *testing.T) {
	plain := FunctionInputs{Name: "f", LogGroup: "/aws/lambda/f"}
	set := FunctionInputs{
		Name: "f", LogGroup: "/aws/lambda/f", Tracing: "Active", Tags: map[string]string{"tier": "web", "koldstart:app": "mine"},
		DeadLetter: "arn:aws:sqs:eu-west-1:1:dead", FileSystem: &FileSystemConfig{AccessPoint: "arn:fsap", MountPath: "/mnt/x"},
		LogFormat: "JSON", ApplicationLogLevel: "WARN", Async: &AsyncConfig{OnFailure: "arn:aws:sqs:eu-west-1:1:dead"},
	}
	if plain.tracing().Mode != "PassThrough" || set.tracing().Mode != "Active" {
		t.Errorf("tracing = %v and %v", plain.tracing().Mode, set.tracing().Mode)
	}
	if l := plain.logging(); l.LogFormat != "Text" || l.ApplicationLogLevel != "" {
		t.Errorf("text logging = %+v", l)
	}
	if l := set.logging(); l.LogFormat != "JSON" || l.ApplicationLogLevel != "WARN" || l.SystemLogLevel != "" {
		t.Errorf("json logging = %+v", l)
	}
	if *plain.deadLetter().TargetArn != "" || plain.createDeadLetter() != nil || *set.createDeadLetter().TargetArn != set.DeadLetter {
		t.Error("the dead letter target is not removed with an empty one, or sent on create")
	}
	if fs := plain.fileSystems(); fs == nil || len(fs) != 0 || plain.createFileSystems() != nil || len(set.fileSystems()) != 1 {
		t.Errorf("file systems = %v, %v", plain.fileSystems(), set.fileSystems())
	}
	ours := map[string]string{"koldstart:app": "orders"}
	if tags := set.tags(ours); tags["tier"] != "web" || tags["koldstart:app"] != "orders" {
		t.Errorf("tags = %v, want the function's with koldstart's over them", tags)
	}
	if !configChanged(plain, FunctionInputs{Name: "f", LogGroup: "/aws/lambda/f", Tracing: "Active"}) {
		t.Error("tracing does not count as a configuration change")
	}
	if configChanged(plain, FunctionInputs{Name: "f", LogGroup: "/aws/lambda/f", Tags: set.Tags, Async: set.Async}) {
		t.Error("tags and async settings count as a configuration change, which have calls of their own")
	}
	data, _ := json.Marshal(plain)
	for _, key := range []string{"tracing", "kmsKey", "tags", "deadLetter", "async", "fileSystem", "logFormat"} {
		if strings.Contains(string(data), `"`+key+`"`) {
			t.Errorf("a function without %s writes it: %s", key, data)
		}
	}
}

// TestMappingSettings checks a stream's failure settings go back to lambda's defaults when not
// set, a queue's limit is removed with an empty one, and a mapping is enabled unless it says not.
func TestMappingSettings(t *testing.T) {
	stream := MappingInputs{StartingPosition: "LATEST"}
	retries, age, bisect, parallel, window, dest := stream.streamSettings()
	if *retries != -1 || *age != -1 || *bisect || *parallel != 1 || *window != 0 || *dest.OnFailure.Destination != "" {
		t.Errorf("defaults = %d %d %v %d %d %q", *retries, *age, *bisect, *parallel, *window, *dest.OnFailure.Destination)
	}
	three := int32(3)
	set := MappingInputs{StartingPosition: "LATEST", MaximumRetryAttempts: &three, ParallelizationFactor: 4, OnFailure: "arn:aws:sqs:x"}
	retries, _, _, parallel, _, dest = set.streamSettings()
	if *retries != 3 || *parallel != 4 || *dest.OnFailure.Destination != "arn:aws:sqs:x" {
		t.Errorf("set = %d %d %q", *retries, *parallel, *dest.OnFailure.Destination)
	}
	queue := MappingInputs{}
	if queue.scaling(false) != nil || queue.scaling(true) == nil || queue.scaling(true).MaximumConcurrency != nil || stream.scaling(true) != nil {
		t.Error("a queue without a limit is not left to lambda, or a stream has one")
	}
	off := false
	if !*queue.enabled() || *(MappingInputs{Enabled: &off}).enabled() {
		t.Error("enabled")
	}
}

// TestRuleTarget checks the target of a rule carries its input path, template, retries and dead
// letter queue, and none of them when not set.
func TestRuleTarget(t *testing.T) {
	four := int32(4)
	in := RuleInputs{Function: "arn:f", InputTransformer: &InputTransformer{Paths: map[string]string{"id": "$.detail.id"}, Template: "<id>"},
		MaximumRetryAttempts: &four, DeadLetter: "arn:aws:sqs:x"}
	target := in.target()
	if target.InputTransformer == nil || *target.InputTransformer.InputTemplate != "<id>" || *target.RetryPolicy.MaximumRetryAttempts != 4 ||
		target.RetryPolicy.MaximumEventAgeInSeconds != nil || *target.DeadLetterConfig.Arn != "arn:aws:sqs:x" {
		t.Errorf("target = %+v", target)
	}
	if plain := (RuleInputs{Function: "arn:f"}).target(); plain.RetryPolicy != nil || plain.DeadLetterConfig != nil || plain.InputPath != nil {
		t.Errorf("a plain target = %+v", plain)
	}
}

// TestListenerConditions checks the header, query and source ip conditions of a listener rule,
// one condition for each header, in the order of their names.
func TestListenerConditions(t *testing.T) {
	in := ListenerRuleInputs{Paths: []string{"/x"}, Headers: map[string][]string{"b": {"2"}, "a": {"1"}},
		Query: map[string]string{"format": "json"}, SourceIPs: []string{"10.0.0.0/8"}}
	conditions := in.conditions()
	fields := make([]string, 0, len(conditions))
	for _, c := range conditions {
		field := *c.Field
		if c.HttpHeaderConfig != nil {
			field += ":" + *c.HttpHeaderConfig.HttpHeaderName
		}
		fields = append(fields, field)
	}
	if got := strings.Join(fields, ","); got != "path-pattern,http-header:a,http-header:b,query-string,source-ip" {
		t.Errorf("conditions = %s", got)
	}
}

// TestAccessLogs checks the stage logs to the log group's arn without the :* DescribeLogGroups
// ends it with, and an api without access logs sends none.
func TestAccessLogs(t *testing.T) {
	in := HTTPAPIInputs{AccessLogs: &AccessLogs{Destination: "arn:aws:logs:eu-west-1:1:log-group:/aws/http-api/x:*", Format: "$context.requestId"}}
	if got := *in.accessLogs().DestinationArn; got != "arn:aws:logs:eu-west-1:1:log-group:/aws/http-api/x" {
		t.Errorf("destination = %s", got)
	}
	if (HTTPAPIInputs{}).accessLogs() != nil {
		t.Error("an api without access logs sends some")
	}
}

// TestLevel checks lambda's default level of json logs is no drift for a function that sets none.
func TestLevel(t *testing.T) {
	tests := []struct {
		want, live, got string
	}{
		{want: "", live: "INFO", got: ""},
		{want: "", live: "WARN", got: "WARN"},
		{want: "INFO", live: "INFO", got: "INFO"},
	}
	for _, tt := range tests {
		if got := level(tt.want, tt.live); got != tt.got {
			t.Errorf("level(%q, %q) = %q, want %q", tt.want, tt.live, got, tt.got)
		}
	}
}
