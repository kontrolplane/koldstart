package cli

import (
	"slices"
	"testing"
)

// TestReadFailure checks the error a function returned is read with its trace a line at a time,
// node's repeated first line left out, and the frames of the runtime and of dependencies marked
// as internal, in node, python and go.
func TestReadFailure(t *testing.T) {
	tests := []struct {
		name     string
		payload  string
		ok       bool
		head     string
		lines    []string
		internal []bool
		timedOut bool
	}{
		{
			name:    "node",
			payload: `{"errorType":"TypeError","errorMessage":"x is undefined","stackTrace":["TypeError: x is undefined","    at handler (file:///var/task/index.mjs:2:9)","    at lib (/var/task/node_modules/pg/index.js:4:1)","    at Runtime.handleOnceNonStreaming (file:///var/runtime/index.mjs:1:1)","    at process.processTicksAndRejections (node:internal/process/task_queues:95:5)"]}`,
			ok:      true, head: "TypeError: x is undefined",
			lines:    []string{"at handler (file:///var/task/index.mjs:2:9)", "at lib (/var/task/node_modules/pg/index.js:4:1)", "at Runtime.handleOnceNonStreaming (file:///var/runtime/index.mjs:1:1)", "at process.processTicksAndRejections (node:internal/process/task_queues:95:5)"},
			internal: []bool{false, true, true, true},
		},
		{
			name:    "python",
			payload: `{"errorType":"ValueError","errorMessage":"boom","stackTrace":["  File \"/var/task/app.py\", line 2, in handler\n    raise ValueError(\"boom\")\n","  File \"/var/lang/lib/python3.12/json/__init__.py\", line 1, in loads\n    return x\n"]}`,
			ok:      true, head: "ValueError: boom",
			lines:    []string{`File "/var/task/app.py", line 2, in handler`, `  raise ValueError("boom")`, `File "/var/lang/lib/python3.12/json/__init__.py", line 1, in loads`, "  return x"},
			internal: []bool{false, false, true, true},
		},
		{
			name:    "go",
			payload: `{"errorType":"runtime.Error","errorMessage":"nil map","stackTrace":[{"path":"shop/main.go","line":12,"label":"handler"},{"path":"github.com/aws/aws-lambda-go@v1.47.0/lambda/handler.go","line":3,"label":"lambda.callHandler"},{"path":"runtime/panic.go","line":9,"label":"runtime.gopanic"}]}`,
			ok:      true, head: "runtime.Error: nil map",
			lines:    []string{"handler (shop/main.go:12)", "lambda.callHandler (github.com/aws/aws-lambda-go@v1.47.0/lambda/handler.go:3)", "runtime.gopanic (runtime/panic.go:9)"},
			internal: []bool{false, true, true},
		},
		{
			name:    "timeout",
			payload: `{"errorType":"Sandbox.Timedout","errorMessage":"RequestId: r Error: Task timed out after 3.00 seconds"}`,
			ok:      true, head: "Sandbox.Timedout: RequestId: r Error: Task timed out after 3.00 seconds", timedOut: true,
		},
		{name: "not an error", payload: `{"ok":false}`},
		{name: "not json", payload: `oops`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, ok := ReadFailure([]byte(tt.payload))
			if ok != tt.ok || f.Head() != tt.head || f.TimedOut() != tt.timedOut {
				t.Fatalf("ReadFailure = %q, %v, timed out %v; want %q, %v, %v", f.Head(), ok, f.TimedOut(), tt.head, tt.ok, tt.timedOut)
			}
			var lines []string
			var internal []bool
			for _, line := range f.Trace {
				lines, internal = append(lines, line.Text), append(internal, line.Internal)
			}
			if !slices.Equal(lines, tt.lines) || !slices.Equal(internal, tt.internal) {
				t.Errorf("trace %q internal %v, want %q %v", lines, internal, tt.lines, tt.internal)
			}
		})
	}
}

// TestInternalFrames checks the frames of the runtime and of dependencies are told from the
// function's own, in a line of node or python, the file of a go frame and its function.
func TestInternalFrames(t *testing.T) {
	tests := []struct {
		name  string
		check func(string) bool
		in    string
		want  bool
	}{
		{name: "node, its own", check: InternalLine, in: "at handler (/var/task/index.js:4:2)"},
		{name: "node, a dependency", check: InternalLine, in: "at f (/var/task/node_modules/pg/index.js:1:1)", want: true},
		{name: "node, its runtime", check: InternalLine, in: "at process (node:internal/process/task_queues:95:5)", want: true},
		{name: "node, its internals", check: InternalLine, in: "at Module._compile (internal/modules/cjs/loader.js:1:1)", want: true},
		{name: "node, lambda's runtime", check: InternalLine, in: "at Runtime.handle (file:///var/runtime/index.mjs:1:1)", want: true},
		{name: "python, its own", check: InternalLine, in: `File "/var/task/app.py", line 8, in handler`},
		{name: "python, a package", check: InternalLine, in: `File "/opt/python/site-packages/boto3/x.py", line 1`, want: true},
		{name: "python, frozen", check: InternalLine, in: `File "<frozen importlib._bootstrap>", line 1`, want: true},
		{name: "go file, its own", check: InternalGoPath, in: "/build/main.go:23 +0x1d"},
		{name: "go file, the module cache", check: InternalGoPath, in: "/root/go/pkg/mod/github.com/x/y.go:1", want: true},
		{name: "go file, a module trimmed", check: InternalGoPath, in: "github.com/aws/aws-lambda-go@v1.47.0/lambda/invoke_loop.go:1", want: true},
		{name: "go file, generated", check: InternalGoPath, in: "<autogenerated>:1", want: true},
		{name: "go call, its own", check: InternalGoCall, in: "main.handler(...)"},
		{name: "go call, the runtime", check: InternalGoCall, in: "runtime.gopanic(...)", want: true},
		{name: "go call, the library", check: InternalGoCall, in: "net/http.(*conn).serve(...)", want: true},
		{name: "go call, aws-lambda-go", check: InternalGoCall, in: "github.com/aws/aws-lambda-go/lambda.(*Function).Invoke(...)", want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.check(tt.in); got != tt.want {
				t.Errorf("%q: %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}
