package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestInfer checks a function's runtime and handler are inferred from its name and the files of
// the project, that what is set is kept, and that finding none or more than one runtime says
// what to set.
func TestInfer(t *testing.T) {
	tests := []struct {
		name    string
		files   []string
		fn      Function
		runtime string
		handler string
		err     string
	}{
		{name: "typescript", files: []string{"src/api.ts"}, runtime: DefaultNode, handler: "src/api.handler"},
		{name: "javascript module", files: []string{"src/api.mjs"}, runtime: DefaultNode, handler: "src/api.handler"},
		{name: "typescript beside its javascript", files: []string{"src/api.ts", "src/api.js"}, runtime: DefaultNode, handler: "src/api.handler"},
		{name: "python", files: []string{"src/api.py"}, runtime: DefaultPython, handler: "src/api.handler"},
		{name: "go", files: []string{"cmd/api/main.go"}, runtime: RuntimeGo, handler: "./cmd/api"},
		{name: "runtime set", files: []string{"src/api.ts"}, fn: Function{Runtime: RuntimeNode24}, runtime: RuntimeNode24, handler: "src/api.handler"},
		{name: "go runtime set", fn: Function{Runtime: RuntimeGo}, runtime: RuntimeGo, handler: "./cmd/api"},
		{name: "handler of a node file", files: []string{"lib/orders.ts"}, fn: Function{Handler: "lib/orders.main"}, runtime: DefaultNode, handler: "lib/orders.main"},
		{name: "handler of a python file", files: []string{"app/main.py"}, fn: Function{Handler: "app/main.handler"}, runtime: DefaultPython, handler: "app/main.handler"},
		{name: "handler of a go package", fn: Function{Handler: "./services/api"}, runtime: RuntimeGo, handler: "./services/api"},
		{name: "handler of a go directory", files: []string{"services/api/main.go"}, fn: Function{Handler: "services/api"}, runtime: RuntimeGo, handler: "services/api"},
		{name: "both set", fn: Function{Runtime: RuntimePy312, Handler: "app.main"}, runtime: RuntimePy312, handler: "app.main"},
		{name: "image", files: []string{"src/api.ts"}, fn: Function{Image: &Image{Dockerfile: "Dockerfile"}}},
		{name: "nothing", files: []string{"src/other.ts"}, err: "function api: no src/api.ts, src/api.py or cmd/api/ here: set runtime and handler"},
		{name: "go directory without go files", files: []string{"cmd/api/README.md", "cmd/api/main_test.go"}, err: "no src/api.ts, src/api.py or cmd/api/ here"},
		{name: "node and python", files: []string{"src/api.ts", "src/api.py"}, err: "function api: src/api.ts and src/api.py are both here: set runtime to nodejs22.x or python3.13"},
		{name: "every runtime", files: []string{"src/api.js", "src/api.py", "cmd/api/main.go"}, err: "src/api.js, src/api.py and cmd/api/ are all here: set runtime to nodejs22.x, python3.13 or provided.al2023"},
		{name: "handler of no file", fn: Function{Handler: "src/gone.handler"}, err: "function api: handler src/gone.handler: no src/gone.ts or src/gone.py here: set runtime"},
		{name: "handler of node and python", files: []string{"src/app.ts", "src/app.py"}, fn: Function{Handler: "src/app.handler"}, err: "handler src/app.handler: src/app.ts and src/app.py are both here: set runtime to nodejs22.x or python3.13"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			for _, f := range tt.files {
				file := filepath.Join(root, filepath.FromSlash(f))
				if err := os.MkdirAll(filepath.Dir(file), 0o750); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(file, nil, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			p := &Project{Functions: map[string]Function{"api": tt.fn}}
			err := p.Infer(root)
			if tt.err != "" {
				if err == nil || !strings.Contains(err.Error(), tt.err) {
					t.Fatalf("Infer = %v, want an error with %q", err, tt.err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Infer: %v", err)
			}
			got := p.Functions["api"]
			if got.Runtime != tt.runtime || got.Handler != tt.handler {
				t.Errorf("runtime %q, handler %q, want %q, %q", got.Runtime, got.Handler, tt.runtime, tt.handler)
			}
		})
	}
}
