package cli

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kontrolplane/koldstart/internal/migrate"
	"github.com/kontrolplane/koldstart/internal/pkl"
	schema "github.com/kontrolplane/koldstart/pkl"
)

// TestMigrateWritesProject runs migrate as a dev build, and checks it writes a PklProject that
// depends on the schema extracted into the cache and resolves it, adds the dependency to a
// PklProject without one, and keeps one that has one.
func TestMigrateWritesProject(t *testing.T) {
	if _, err := exec.LookPath("pkl"); err != nil && os.Getenv(pkl.EnvBinary) == "" && os.Getenv("CI") == "" {
		t.Skip("no pkl on the PATH")
	}
	elsewhere, err := schema.Extract(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	kept := elsewhere.PklProject()
	tests := []struct {
		name      string
		existing  string
		wantLocal bool
		want      string
		printed   string
	}{
		{name: "none", wantLocal: true, printed: "wrote PklProject, with the schema of koldstart dev ("},
		{name: "without the dependency", existing: "amends \"pkl:Project\"\n", wantLocal: true, printed: "wrote PklProject"},
		{name: "with it", existing: kept, want: kept, printed: "kept PklProject, which names the schema already"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			cache := t.TempDir()
			t.Setenv("XDG_CACHE_HOME", cache)
			files := map[string]string{"serverless.yml": "service: orders\nprovider:\n  name: aws\n  runtime: nodejs22.x\nfunctions:\n  api:\n    handler: src/api.handler\n"}
			if tt.existing != "" {
				files[pkl.ProjectFile] = tt.existing
			}
			for name, content := range files {
				if err := os.WriteFile(name, []byte(content), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			root := Root(nameShort, nil)
			var out bytes.Buffer
			root.SetOut(&out)
			root.SetArgs([]string{"migrate"})
			if err := root.Execute(); err != nil {
				t.Fatalf("migrate: %v\n%s", err, out.String())
			}
			got, _ := os.ReadFile(pkl.ProjectFile)
			dep, err := schema.ReadDependency(string(got))
			switch {
			case err != nil:
				t.Errorf("PklProject:\n%s\n%v", got, err)
			case tt.wantLocal && (!dep.Local() || !strings.HasPrefix(dep.Dir, filepath.Join(cache, "koldstart", "schema"))):
				t.Errorf("PklProject depends on %+v, want the schema in the cache", dep)
			case !tt.wantLocal && string(got) != tt.want:
				t.Errorf("PklProject =\n%s\nwant it kept as\n%s", got, tt.want)
			}
			if !strings.Contains(out.String(), tt.printed) {
				t.Errorf("printed no %q:\n%s", tt.printed, out.String())
			}
			if _, err := os.Stat(pkl.DepsFile); err != nil || !strings.Contains(out.String(), "resolved it into PklProject.deps.json") {
				t.Errorf("not resolved: %v\n%s", err, out.String())
			}
		})
	}
}

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
