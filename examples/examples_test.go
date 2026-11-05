// Package examples holds example projects, each a module of its own, and checks they evaluate.
package examples

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/kontrolplane/koldstart/internal/build"
	"github.com/kontrolplane/koldstart/internal/config"
	"github.com/kontrolplane/koldstart/internal/desired"
	"github.com/kontrolplane/koldstart/internal/pkl"
	schema "github.com/kontrolplane/koldstart/pkl"
)

// version is the koldstart whose schema package the examples amend.
const version = "0.1.0"

// TestExamplesEvaluate checks every example evaluates against the schema in this tree, for dev
// and prod, passes the checks of a deploy, and plans.
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
				path, cleanup, err := schema.Local(source, version)
				if err != nil {
					t.Fatal(err)
				}
				defer cleanup()
				p, err := config.Load(context.Background(), pkl.Evaluator{Binary: binary}, path, stage)
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
