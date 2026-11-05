package cli

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kontrolplane/koldstart/internal/migrate"
)

// TestPrintMigration checks migrate says where it wrote, how many todos it left and what they are,
// the ones to fix first, how many the file marks, and the plan to run next: in the file's
// directory, with its name and the stage serverless.yml deployed to.
func TestPrintMigration(t *testing.T) {
	layers := migrate.Todo{Kind: migrate.Add, Where: "functions.api.layers", What: "build what the layers hold into the binary", Marked: true}
	todos := []migrate.Todo{layers}
	mixed := []migrate.Todo{
		{Kind: migrate.Drop, Where: "plugins.serverless-esbuild", What: "koldstart bundles typescript with esbuild itself"},
		layers,
		{Kind: migrate.Fix, Where: "koldstart.pkl", What: "a deploy refuses it"},
	}
	tests := []struct {
		name  string
		out   string
		stage string
		todos []migrate.Todo
		want  []string
		not   []string
	}{
		{name: "here, all converted", out: "koldstart.pkl", want: []string{"wrote koldstart.pkl, with everything converted", "next:\n  kdst plan\n"}, not: []string{"cd ", "--config", "TODO"}},
		{
			name:  "beside serverless.yml",
			out:   filepath.Join("svc", "koldstart.pkl"),
			todos: todos,
			want: []string{
				"with 1 thing left to you\n\n" + migrate.Add.String() + ":\n  functions.api.layers: build what",
				"a TODO comment in " + filepath.Join("svc", "koldstart.pkl") + " marks where each goes\n",
				"  cd svc\n  kdst plan\n",
			},
		},
		{
			name:  "by kind",
			out:   "koldstart.pkl",
			todos: mixed,
			want: []string{
				"with 3 things left to you\n\n" + migrate.Fix.String() + ":\n  koldstart.pkl: a deploy refuses it\n\n" + migrate.Add.String() + ":\n",
				"\n\n" + migrate.Drop.String() + ":\n  plugins.serverless-esbuild:",
				"TODO comments in koldstart.pkl mark where 1 of them go\n",
			},
		},
		{
			name:  "none marked",
			out:   "koldstart.pkl",
			todos: mixed[:1],
			not:   []string{"TODO"},
		},
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
