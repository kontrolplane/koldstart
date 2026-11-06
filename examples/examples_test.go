// Package examples holds example projects, each a module of its own, and checks they evaluate.
package examples

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kontrolplane/koldstart/internal/build"
	"github.com/kontrolplane/koldstart/internal/config"
	"github.com/kontrolplane/koldstart/internal/desired"
	"github.com/kontrolplane/koldstart/internal/pkl"
	schema "github.com/kontrolplane/koldstart/pkl"
)

// version is the koldstart whose schema package the examples' PklProjects name.
const version = "0.1.0"

// TestExamplesProject checks every example has a PklProject that names the schema package of
// version, and a koldstart.pkl that amends it as @koldstart/.
func TestExamplesProject(t *testing.T) {
	files, err := filepath.Glob("*/koldstart.pkl")
	if err != nil || len(files) == 0 {
		t.Fatalf("no examples: %v", err)
	}
	for _, file := range files {
		t.Run(filepath.Dir(file), func(t *testing.T) {
			project, err := os.ReadFile(filepath.Join(filepath.Dir(file), schema.Package))
			if err != nil {
				t.Fatal(err)
			}
			if dep, err := schema.ReadDependency(string(project)); err != nil || dep != schema.Release(version) {
				t.Errorf("PklProject names %+v, %v, want koldstart %s", dep, err, version)
			}
			source, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(string(source), "amends \"@koldstart/Project.pkl\"\n\nimport \"@koldstart/kdst.pkl\"\n") {
				t.Errorf("%s does not start with the amends and import of @koldstart/", file)
			}
		})
	}
}

// TestExamplesEvaluate checks every example evaluates against the schema in this tree, through
// schema.Local, which needs neither the released package nor its lock file, for dev and prod,
// passes the checks of a deploy, and plans.
func TestExamplesEvaluate(t *testing.T) {
	if os.Getenv("CI") == "" && os.Getenv(pkl.EnvBinary) == "" {
		if _, err := exec.LookPath("pkl"); err != nil {
			t.Skip("no pkl on the PATH")
		}
	}
	binary, err := pkl.Find(context.Background())
	if err != nil {
		t.Fatalf("finding pkl: %v", err)
	}
	files, err := filepath.Glob("*/koldstart.pkl")
	if err != nil || len(files) == 0 {
		t.Fatalf("no examples: %v", err)
	}
	for _, file := range files {
		for _, stage := range []string{"dev", "prod"} {
			t.Run(filepath.Dir(file)+"/"+stage, func(t *testing.T) {
				source, err := os.ReadFile(file)
				if err != nil {
					t.Fatal(err)
				}
				path, cleanup, err := schema.Local(source)
				if err != nil {
					t.Fatal(err)
				}
				defer cleanup()
				p, err := config.LoadIn(context.Background(), pkl.Evaluator{Binary: binary}, path, filepath.Dir(file), stage)
				if err != nil {
					t.Fatal(err)
				}
				archives := map[string]build.Archive{}
				for name := range p.Functions {
					archives[name] = build.Archive{Function: name, SHA256: "test"}
				}
				if _, err := desired.Build(p, desired.Target{Account: "123456789012", Region: p.Region, Bucket: "b", Archives: archives}); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}
