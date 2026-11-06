package pkl

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakePkl writes a pkl that logs its arguments to the file $FAKE_PKL_LOG names, one run a line.
// project resolve writes the deps.json of the directory it is given, or prints resolveErr and
// fails when there is one; eval prints a project.
func fakePkl(t *testing.T, resolveErr string) (binary, log string) {
	t.Helper()
	dir := t.TempDir()
	log = filepath.Join(dir, "log")
	t.Setenv("FAKE_PKL_LOG", log)
	script := `#!/bin/sh
echo "$@" >> "$FAKE_PKL_LOG"
if [ "$1" = project ]; then
  if [ -n "$FAKE_PKL_RESOLVE_ERR" ]; then echo "$FAKE_PKL_RESOLVE_ERR" >&2; exit 1; fi
  echo '{}' > "$3/PklProject.deps.json"
  exit 0
fi
echo '{"schemaVersion":1}'
`
	t.Setenv("FAKE_PKL_RESOLVE_ERR", resolveErr)
	binary = filepath.Join(dir, "pkl")
	if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return binary, log
}

// writeFiles writes files under dir, by their slash-separated paths.
func writeFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// TestFindProject checks the nearest PklProject at or above a file's directory is found, and none
// when there is none, or only a directory of its name.
func TestFindProject(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, map[string]string{
		"app/PklProject":                    "",
		"app/koldstart.pkl":                 "",
		"app/services/orders/koldstart.pkl": "",
		"other/koldstart.pkl":               "",
		"named/PklProject/keep":             "",
		"named/koldstart.pkl":               "",
	})
	tests := []struct {
		name string
		path string
		want string
	}{
		{name: "beside", path: "app/koldstart.pkl", want: "app"},
		{name: "above", path: "app/services/orders/koldstart.pkl", want: "app"},
		{name: "none", path: "other/koldstart.pkl", want: ""},
		{name: "a directory of the name", path: "named/koldstart.pkl", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := FindProject(filepath.Join(root, filepath.FromSlash(tt.path)))
			if err != nil {
				t.Fatal(err)
			}
			want := ""
			if tt.want != "" {
				want = filepath.Join(root, tt.want)
			}
			if got != want {
				t.Errorf("FindProject = %q, want %q", got, want)
			}
		})
	}
}

// TestStale checks a project needs resolving without a deps.json, or with one older than its
// PklProject, and not with a newer one.
func TestStale(t *testing.T) {
	now := time.Now()
	tests := []struct {
		name string
		deps *time.Time
		want bool
	}{
		{name: "no deps.json", want: true},
		{name: "older", deps: new(now.Add(-time.Hour)), want: true},
		{name: "newer", deps: new(now.Add(time.Hour)), want: false},
		{name: "as old", deps: new(now), want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			writeFiles(t, dir, map[string]string{ProjectFile: ""})
			if err := os.Chtimes(filepath.Join(dir, ProjectFile), now, now); err != nil {
				t.Fatal(err)
			}
			if tt.deps != nil {
				writeFiles(t, dir, map[string]string{DepsFile: "{}"})
				if err := os.Chtimes(filepath.Join(dir, DepsFile), *tt.deps, *tt.deps); err != nil {
					t.Fatal(err)
				}
			}
			got, err := Stale(dir)
			if err != nil || got != tt.want {
				t.Errorf("Stale = %v, %v, want %v", got, err, tt.want)
			}
		})
	}
}

// TestEvalInProject checks a module under a PklProject is evaluated in that project, resolved
// first with a note when it is stale and not when it is not, and one under none without a
// project, whatever directory pkl runs in, as a file that names the schema by its full package
// uri is.
func TestEvalInProject(t *testing.T) {
	tests := []struct {
		name      string
		files     map[string]string
		path      string
		wantRuns  []string
		wantNotes string
	}{
		{
			name:      "unresolved",
			files:     map[string]string{"app/PklProject": "", "app/koldstart.pkl": ""},
			path:      "app/koldstart.pkl",
			wantRuns:  []string{"project resolve {root}/app", "eval --format json --project-dir {root}/app --property stage=dev {root}/app/koldstart.pkl"},
			wantNotes: "resolving the dependencies of {root}/app/PklProject into PklProject.deps.json\n",
		},
		{
			name:     "above",
			files:    map[string]string{"app/PklProject": "", "app/PklProject.deps.json": "{}", "app/svc/koldstart.pkl": ""},
			path:     "app/svc/koldstart.pkl",
			wantRuns: []string{"eval --format json --project-dir {root}/app --property stage=dev {root}/app/svc/koldstart.pkl"},
		},
		{
			name:     "no project",
			files:    map[string]string{"app/koldstart.pkl": `amends "package://github.com/kontrolplane/koldstart/releases/download/v0.1.0/koldstart@0.1.0#/Project.pkl"`},
			path:     "app/koldstart.pkl",
			wantRuns: []string{"eval --format json --no-project --property stage=dev {root}/app/koldstart.pkl"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			binary, log := fakePkl(t, "")
			root, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			writeFiles(t, root, tt.files)
			if deps := filepath.Join(root, "app", DepsFile); tt.files["app/"+DepsFile] != "" {
				later := time.Now().Add(time.Minute)
				if err := os.Chtimes(deps, later, later); err != nil {
					t.Fatal(err)
				}
			}
			var notes bytes.Buffer
			ev := Evaluator{Binary: binary, Notes: &notes}
			if _, err := ev.EvalJSON(context.Background(), filepath.Join(root, filepath.FromSlash(tt.path)), map[string]string{"stage": "dev"}); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(log)
			if err != nil {
				t.Fatal(err)
			}
			want := strings.ReplaceAll(strings.Join(tt.wantRuns, "\n")+"\n", "{root}", root)
			if string(data) != want {
				t.Errorf("ran\n%s\nwant\n%s", data, want)
			}
			if want := strings.ReplaceAll(tt.wantNotes, "{root}", root); notes.String() != want {
				t.Errorf("notes = %q, want %q", notes.String(), want)
			}
		})
	}
}

// TestResolveErrorSays checks a project that does not resolve says the version may not be
// released and how to change it when its package is not found, to check the machine is online
// when it did not download, and to fix it otherwise.
func TestResolveErrorSays(t *testing.T) {
	tests := []struct {
		name    string
		message string
		want    []string
	}{
		{
			name:    "not found",
			message: "Received unexpected status code `404` when making request `GET https://github.com/kontrolplane/koldstart/releases/download/v9.9.9/koldstart@9.9.9`.",
			want:    []string{"there is no schema package 9.9.9 at https://github.com/kontrolplane/koldstart/releases/download/v9.9.9/koldstart@9.9.9, which may not be released", "set the version in its koldstart uri to a release of koldstart, or run kdst upgrade"},
		},
		{
			name:    "offline",
			message: "Exception when making request `GET https://github.com/kontrolplane/koldstart/releases/download/v0.1.0/koldstart@0.1.0`:\nError connecting to host `github.com`.",
			want:    []string{"did not download: check this machine is online"},
		},
		{name: "wrong", message: "Missing `}` delimiter.", want: []string{"fix it where pkl points\n\nMissing"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			binary, _ := fakePkl(t, tt.message)
			dir := t.TempDir()
			writeFiles(t, dir, map[string]string{ProjectFile: ""})
			err := Evaluator{Binary: binary, Name: "kdst"}.Resolve(context.Background(), dir)
			if _, ok := errors.AsType[*ResolveError](err); !ok {
				t.Fatalf("Resolve = %v, want a ResolveError", err)
			}
			for _, w := range tt.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("Resolve = %v\nwant it to hold %q", err, w)
				}
			}
		})
	}
}

// TestEvalErrorDependency checks a module that imports @koldstart/ without a PklProject, or in a
// project without a koldstart dependency, says to run upgrade.
func TestEvalErrorDependency(t *testing.T) {
	tests := []struct {
		name    string
		message string
		project string
		want    string
	}{
		{
			name:    "no project",
			message: "Cannot resolve dependency because there is no project found.\n\n1 | amends \"@koldstart/Project.pkl\"",
			want:    "koldstart.pkl imports @koldstart/, which needs a PklProject beside it or above it that names the schema package: run kdst upgrade to write one",
		},
		{
			name:    "no dependency",
			message: "Cannot find a dependency named `koldstart`, because it is not declared in the current project.",
			project: "app",
			want:    "koldstart.pkl imports @koldstart/, but " + filepath.Join("app", "PklProject") + " has no koldstart dependency: run kdst upgrade to add it",
		},
		{
			name:    "another dependency",
			message: "Cannot find a dependency named `other`, because it is not declared in the current project.",
			project: "app",
			want:    "fix it where pkl points",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := &EvalError{Path: "koldstart.pkl", Message: tt.message, Project: tt.project, name: "kdst"}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("Error() = %q, want it to hold %q", err.Error(), tt.want)
			}
		})
	}
}

// TestEvalLocalDependency evaluates a module that imports @koldstart/ in a project whose
// koldstart dependency is the schema in pkl/ of this tree, as a local dependency, resolved for
// real and offline, also through a link to its directory, and the two errors of a module without
// one.
func TestEvalLocalDependency(t *testing.T) {
	if _, err := exec.LookPath("pkl"); err != nil && os.Getenv(EnvBinary) == "" && os.Getenv("CI") == "" {
		t.Skip("no pkl on the PATH")
	}
	binary, err := Find(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	schema, err := filepath.Abs(filepath.Join("..", "..", "pkl", ProjectFile))
	if err != nil {
		t.Fatal(err)
	}
	module := "amends \"@koldstart/Project.pkl\"\n\nimport \"@koldstart/kdst.pkl\"\n\napp = \"shop\"\noutputs { [\"stage\"] = kdst.stage }\n"
	project := "amends \"pkl:Project\"\n\ndependencies {\n  [\"koldstart\"] = import(\"" + schema + "\")\n}\n"
	tests := []struct {
		name    string
		files   map[string]string
		linked  bool
		wantOut string
		wantErr string
	}{
		{name: "local", files: map[string]string{"PklProject": project, "koldstart.pkl": module}, wantOut: `"stage": "prod"`},
		{name: "through a link", files: map[string]string{"PklProject": project, "koldstart.pkl": module}, linked: true, wantOut: `"stage": "prod"`},
		{name: "no project", files: map[string]string{"koldstart.pkl": module}, wantErr: "needs a PklProject beside it or above it"},
		{name: "no dependency", files: map[string]string{"PklProject": "amends \"pkl:Project\"\n", "koldstart.pkl": module}, wantErr: "has no koldstart dependency: run koldstart upgrade to add it"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			writeFiles(t, dir, tt.files)
			path := filepath.Join(dir, "koldstart.pkl")
			if tt.linked {
				link := filepath.Join(t.TempDir(), "link")
				if err := os.Symlink(dir, link); err != nil {
					t.Fatal(err)
				}
				path = filepath.Join(link, "koldstart.pkl")
			}
			out, err := Evaluator{Binary: binary}.EvalJSON(context.Background(), path, map[string]string{"stage": "prod"})
			switch {
			case tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)):
				t.Errorf("EvalJSON = %v, want an error with %q", err, tt.wantErr)
			case tt.wantErr == "" && err != nil:
				t.Fatal(err)
			case tt.wantErr == "" && !strings.Contains(string(out), tt.wantOut):
				t.Errorf("EvalJSON =\n%s\nwant it to hold %s", out, tt.wantOut)
			}
			if _, statErr := os.Stat(filepath.Join(dir, DepsFile)); tt.wantErr == "" && statErr != nil {
				t.Errorf("not resolved: %v", statErr)
			}
		})
	}
}
