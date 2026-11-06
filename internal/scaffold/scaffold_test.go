package scaffold

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/kontrolplane/koldstart/internal/build"
	"github.com/kontrolplane/koldstart/internal/config"
	"github.com/kontrolplane/koldstart/internal/desired"
	"github.com/kontrolplane/koldstart/internal/engine"
	"github.com/kontrolplane/koldstart/internal/pkl"
	schema "github.com/kontrolplane/koldstart/pkl"
)

func evaluator(t *testing.T) pkl.Evaluator {
	t.Helper()
	if os.Getenv("CI") == "" && os.Getenv(pkl.EnvBinary) == "" {
		if _, err := exec.LookPath("pkl"); err != nil {
			t.Skip("no pkl on the PATH")
		}
	}
	binary, err := pkl.Find(context.Background())
	if err != nil {
		t.Fatalf("finding pkl: %v", err)
	}
	return pkl.Evaluator{Binary: binary}
}

// TestTemplatesEvaluate checks what each template writes evaluates against the schema, passes
// the checks of a deploy, and plans a function with its route, and that its commented secret
// evaluates once uncommented, under the app and stage.
func TestTemplatesEvaluate(t *testing.T) {
	ev := evaluator(t)
	tests := []struct {
		template string
		files    []string
		runtime  string
	}{
		{template: Node, files: []string{"koldstart.pkl", "src/api.ts"}, runtime: config.RuntimeNode22},
		{template: Go, files: []string{"cmd/api/main.go", "go.mod", "koldstart.pkl"}, runtime: config.RuntimeGo},
		{template: Python, files: []string{"koldstart.pkl", "requirements.txt", "src/api.py"}, runtime: config.RuntimePy313},
	}
	for _, tt := range tests {
		t.Run(tt.template, func(t *testing.T) {
			dir := t.TempDir()
			res, err := Write(Options{Dir: dir, App: "orders", Region: "eu-west-1", Template: tt.template, Version: "0.1.0"})
			if err != nil {
				t.Fatal(err)
			}
			slices.Sort(res.Written)
			if !slices.Equal(res.Written, tt.files) {
				t.Errorf("wrote %v, want %v", res.Written, tt.files)
			}
			source, err := os.ReadFile(filepath.Join(dir, Config))
			if err != nil {
				t.Fatal(err)
			}
			path, cleanup, err := schema.Local(source, "0.1.0")
			if err != nil {
				t.Fatal(err)
			}
			defer cleanup()
			p, err := config.Load(context.Background(), ev, path, "dev")
			if err != nil {
				t.Fatalf("the %s template does not evaluate: %v", tt.template, err)
			}
			if p.App != "orders" || p.Region != "eu-west-1" || p.Functions["api"].Runtime != tt.runtime {
				t.Fatalf("evaluated to %+v", p)
			}
			nodes, err := desired.Build(p, desired.Target{Account: "123456789012", Region: "eu-west-1", Bucket: "b",
				Archives: map[string]build.Archive{"api": {Function: "api", SHA256: "abc"}}})
			if err != nil {
				t.Fatal(err)
			}
			if !slices.ContainsFunc(nodes, func(n engine.Node) bool { return n.ID == "route/GET /hello" }) {
				t.Error("no route for GET /hello")
			}

			uncommented := strings.Replace(string(source), "// environment {", "environment {", 1)
			if uncommented == string(source) {
				t.Fatal("no commented environment with a secret")
			}
			path, cleanup, err = schema.Local([]byte(uncommented), "0.1.0")
			if err != nil {
				t.Fatal(err)
			}
			defer cleanup()
			p, err = config.Load(context.Background(), ev, path, "dev")
			if err != nil {
				t.Fatalf("the %s template's secret does not evaluate: %v", tt.template, err)
			}
			if s := p.Functions["api"].Environment["API_KEY"].Secret; s == nil || s.Name != "/orders/dev/api-key" {
				t.Errorf("API_KEY = %+v, want /orders/dev/api-key", s)
			}
		})
	}
}

// TestWriteKeeps checks files that are there are kept, koldstart.pkl is refused without force,
// and .gitignore gets the lines it lacks once.
func TestWriteKeeps(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "src"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "src", "api.ts"), []byte("mine"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("node_modules\n.koldstart/local\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	o := Options{Dir: dir, App: "orders", Region: "eu-west-1", Template: Node, Version: "0.1.0"}
	res, err := Write(o)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(res.Kept, []string{"src/api.ts"}) || !slices.Equal(res.Gitignore, []string{".koldstart/build/"}) {
		t.Errorf("kept %v, added %v to .gitignore", res.Kept, res.Gitignore)
	}
	if data, _ := os.ReadFile(filepath.Join(dir, "src", "api.ts")); string(data) != "mine" {
		t.Error("the handler that was there was written over")
	}
	if _, err := Write(o); err == nil || !strings.Contains(err.Error(), "--force") {
		t.Errorf("a second Write: %v, want it refused", err)
	}
	o.Force = true
	res, err = Write(o)
	if err != nil || len(res.Gitignore) != 0 {
		t.Fatalf("Write with force: %v, added %v to .gitignore again", err, res.Gitignore)
	}
	data, _ := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if want := "node_modules\n.koldstart/local\n\n# koldstart\n.koldstart/build/\n"; string(data) != want {
		t.Errorf(".gitignore:\n%s\nwant:\n%s", data, want)
	}
}

// TestAppName checks a directory's name becomes an app name, or none.
func TestAppName(t *testing.T) {
	tests := []struct {
		dir  string
		want string
	}{
		{dir: "/src/orders", want: "orders"},
		{dir: "/src/Order_Service", want: "order-service"},
		{dir: "/src/2024-shop", want: "shop"},
		{dir: "/src/" + strings.Repeat("a", 30), want: strings.Repeat("a", 24)},
		{dir: "/src/日本", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.dir, func(t *testing.T) {
			if got := AppName(tt.dir); got != tt.want {
				t.Errorf("AppName = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestWriteRefuses checks an unknown template and an app name aws would refuse are refused, each
// saying what to pass instead.
func TestWriteRefuses(t *testing.T) {
	tests := []struct {
		name string
		opts Options
		want string
	}{
		{name: "template", opts: Options{Dir: t.TempDir(), App: "orders", Template: "ruby"}, want: `no template "ruby": want node, go or python`},
		{name: "app", opts: Options{Dir: t.TempDir(), App: "Orders", Template: Node}, want: "pass --app with lowercase letters"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := Write(tt.opts); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("Write = %v, want an error with %q", err, tt.want)
			}
		})
	}
}
