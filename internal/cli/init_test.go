package cli

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kontrolplane/koldstart/internal/pkl"
	"github.com/kontrolplane/koldstart/internal/scaffold"
)

// TestInit checks init writes the template into the directory it is given, names the app after
// it, and says the app, its region and what to run next. As a dev build, its PklProject depends
// on the schema extracted into the cache, and is resolved when there is a pkl, or says it is not
// when there is none, with the files written.
func TestInit(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		files    []string
		app      string
		started  string
		next     string
		failWith string
	}{
		{name: "node", args: []string{"orders"}, files: []string{"koldstart.pkl", "src/api.ts", ".gitignore"}, app: `app = "orders"`, started: "started orders, which deploys to eu-west-1", next: "kdst deploy"},
		{name: "go", args: []string{"orders", "-t", "go", "--app", "shop"}, files: []string{"koldstart.pkl", "cmd/api/main.go", "go.mod"}, app: `app = "shop"`, started: "started shop, which deploys to eu-west-1", next: "go mod tidy"},
		{name: "python", args: []string{"orders", "-t", "python"}, files: []string{"koldstart.pkl", "src/api.py", "requirements.txt"}, app: `app = "orders"`, started: "started orders", next: "kdst plan"},
		{name: "unknown template", args: []string{"orders", "-t", "ruby"}, failWith: `-t "ruby": want node, go, python or rust`},
		{name: "no app name", args: []string{"日本"}, failWith: "pass --app"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			t.Chdir(dir)
			cache := t.TempDir()
			t.Setenv("XDG_CACHE_HOME", cache)
			pklInstalled := withoutDownload(t)
			root := Root(nameShort, nil)
			var out bytes.Buffer
			root.SetOut(&out)
			root.SetArgs(append([]string{"init", "--region", "eu-west-1"}, tt.args...))
			err := root.Execute()
			if tt.failWith != "" {
				if err == nil || !strings.Contains(err.Error(), tt.failWith) {
					t.Fatalf("err = %v, want one saying %q", err, tt.failWith)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			for _, f := range tt.files {
				if _, err := os.Stat(filepath.Join(dir, tt.args[0], f)); err != nil {
					t.Errorf("no %s: %v", f, err)
				}
			}
			data, _ := os.ReadFile(filepath.Join(dir, tt.args[0], "koldstart.pkl"))
			if !strings.Contains(string(data), tt.app) || !strings.Contains(string(data), `region = "eu-west-1"`) {
				t.Errorf("koldstart.pkl:\n%s", data)
			}
			if !strings.Contains(out.String(), tt.started) || !strings.Contains(out.String(), tt.next) || !strings.Contains(out.String(), "cd orders") {
				t.Errorf("printed:\n%s", out.String())
			}
			project, _ := os.ReadFile(filepath.Join(dir, tt.args[0], "PklProject"))
			if want := `["koldstart"] = import("` + filepath.Join(cache, "koldstart", "schema"); !strings.Contains(string(project), want) {
				t.Errorf("PklProject of a dev build:\n%s\nwant it to hold %s", project, want)
			}
			_, err = os.Stat(filepath.Join(dir, tt.args[0], "PklProject.deps.json"))
			switch {
			case pklInstalled && (err != nil || !strings.Contains(out.String(), "wrote PklProject.deps.json")):
				t.Errorf("not resolved: %v\n%s", err, out.String())
			case !pklInstalled && !strings.Contains(out.String(), "PklProject is not resolved yet, and kdst plan resolves it first"):
				t.Errorf("printed no note of the PklProject it could not resolve:\n%s", out.String())
			}
		})
	}
}

// withoutDownload keeps a test from downloading pkl: it reports whether there is a pkl to run, and
// when there is none, names one that does not exist in KOLDSTART_PKL, so what needs it fails.
func withoutDownload(t *testing.T) bool {
	t.Helper()
	if os.Getenv(pkl.EnvBinary) != "" {
		return true
	}
	if _, err := exec.LookPath("pkl"); err == nil {
		return true
	}
	t.Setenv(pkl.EnvBinary, filepath.Join(t.TempDir(), "no-pkl"))
	return false
}

// TestNextSteps checks what init says to run next lines up what each command does in one column,
// the go steps included, and leaves cd without one.
func TestNextSteps(t *testing.T) {
	tests := []struct {
		name     string
		binary   string
		dir      string
		template string
		written  []string
		want     []string
	}{
		{
			name: "node here", binary: "kdst", dir: ".", template: "node",
			want: []string{
				"kdst plan      see what a deploy creates",
				"kdst deploy    deploy it, and print the url of GET /hello",
			},
		},
		{
			name: "go in a directory", binary: "kdst", dir: "orders", template: "go", written: []string{"go.mod"},
			want: []string{
				"cd orders",
				"go mod tidy    fetch aws-lambda-go, which the handler imports",
				"kdst plan      see what a deploy creates",
				"kdst deploy    deploy it, and print the url of GET /hello",
			},
		},
		{
			name: "go with a go.mod of its own", binary: "koldstart", dir: ".", template: "go",
			want: []string{
				"go get github.com/aws/aws-lambda-go    add it to go.mod, as the handler imports it",
				"koldstart plan                         see what a deploy creates",
				"koldstart deploy                       deploy it, and print the url of GET /hello",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := strings.Join(nextSteps(tt.binary, tt.dir, tt.template, scaffold.Result{Written: tt.written}), "\n")
			if want := strings.Join(tt.want, "\n"); got != want {
				t.Errorf("nextSteps =\n%s\nwant\n%s", got, want)
			}
		})
	}
}
