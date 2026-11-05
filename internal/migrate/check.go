package migrate

import (
	"context"
	"fmt"
	"strings"

	"github.com/kontrolplane/koldstart/internal/config"
	schema "github.com/kontrolplane/koldstart/pkl"
)

// Check evaluates a converted koldstart.pkl for stage, against the schema built into koldstart,
// so it needs no released package. It returns pkl's report when the file does not evaluate; what
// a deploy would refuse in a file that does comes back as todos.
func Check(ctx context.Context, ev config.Evaluator, source, version, stage string) ([]Todo, error) {
	path, cleanup, err := schema.Local([]byte(source), version)
	if err != nil {
		return nil, err
	}
	defer cleanup()
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
