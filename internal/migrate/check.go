package migrate

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/kontrolplane/koldstart/internal/config"
	schema "github.com/kontrolplane/koldstart/pkl"
)

// Check evaluates a converted koldstart.pkl for stage, against the schema built into koldstart,
// so it needs no released package. It returns pkl's report when the file does not evaluate; what
// a deploy would refuse in a file that does comes back as todos.
func Check(ctx context.Context, ev config.Evaluator, source, version, stage string) ([]Todo, error) {
	dir, err := os.MkdirTemp("", "koldstart-migrate-")
	if err != nil {
		return nil, fmt.Errorf("making a directory to check in: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	for _, module := range []string{schema.Project, schema.Kdst} {
		data, err := fs.ReadFile(schema.Files, module)
		if err != nil {
			return nil, err
		}
		if err := os.WriteFile(filepath.Join(dir, module), data, 0o600); err != nil {
			return nil, err
		}
		source = strings.ReplaceAll(source, quote(schema.PackageURI(version, module)), quote(module))
	}
	path := filepath.Join(dir, "koldstart.pkl")
	if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
		return nil, err
	}
	if stage == "" || strings.Contains(stage, "${") {
		stage = "dev"
	}
	_, err = config.Load(ctx, ev, path, stage)
	checking := "checking " + path + ": "
	switch {
	case err == nil:
		return nil, nil
	case strings.HasPrefix(err.Error(), checking):
		var todos []Todo
		for line := range strings.SplitSeq(strings.TrimPrefix(err.Error(), checking), "\n") {
			todos = append(todos, Todo{Where: "koldstart.pkl", What: line})
		}
		return todos, nil
	default:
		return nil, fmt.Errorf("the converted koldstart.pkl does not evaluate, which is a bug in kdst migrate: %w", err)
	}
}
