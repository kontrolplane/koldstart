package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestInit checks init writes the template into the directory it is given, names the app after
// it, and says the app, its region and what to run next.
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
		{name: "unknown template", args: []string{"orders", "-t", "ruby"}, failWith: "no template"},
		{name: "no app name", args: []string{"日本"}, failWith: "pass --app"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			t.Chdir(dir)
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
		})
	}
}
