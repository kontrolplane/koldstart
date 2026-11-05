package cli

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kontrolplane/koldstart/internal/migrate"
)

// TestPrintMigration checks migrate says where it wrote, what it left, and the plan to run next:
// in the file's directory, with its name and the stage serverless.yml deployed to.
func TestPrintMigration(t *testing.T) {
	todos := []migrate.Todo{{Where: "functions.api.layers", What: "layers has no equivalent in koldstart yet"}}
	tests := []struct {
		name  string
		out   string
		stage string
		todos []migrate.Todo
		want  []string
		not   []string
	}{
		{name: "here, all converted", out: "koldstart.pkl", want: []string{"wrote koldstart.pkl, with everything converted", "next:\n  kdst plan\n"}, not: []string{"cd ", "--config", "TODO"}},
		{name: "beside serverless.yml", out: filepath.Join("svc", "koldstart.pkl"), todos: todos, want: []string{"left to you:\n\n  functions.api.layers: layers has no equivalent", "the TODO comments in " + filepath.Join("svc", "koldstart.pkl"), "  cd svc\n  kdst plan\n"}},
		{name: "another name", out: "other.pkl", want: []string{"  kdst plan --config other.pkl\n"}},
		{name: "a stage", out: "koldstart.pkl", stage: "prod", want: []string{"  kdst plan --stage prod\n"}},
		{name: "dev", out: "koldstart.pkl", stage: "dev", not: []string{"--stage"}},
		{name: "a variable stage", out: "koldstart.pkl", stage: "${opt:stage}", not: []string{"--stage"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			printMigration(&out, nameShort, tt.out, tt.stage, tt.todos)
			for _, w := range tt.want {
				if !strings.Contains(out.String(), w) {
					t.Errorf("printed no %q:\n%s", w, out.String())
				}
			}
			for _, n := range tt.not {
				if strings.Contains(out.String(), n) {
					t.Errorf("printed %q:\n%s", n, out.String())
				}
			}
		})
	}
}
