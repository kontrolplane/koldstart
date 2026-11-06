package cli

import (
	"context"
	"fmt"

	"github.com/kontrolplane/koldstart/internal/pkl"
)

// resolver resolves the project in a directory into its PklProject.deps.json.
type resolver func(ctx context.Context, dir string) error

// pklResolver resolves with the pkl koldstart uses, downloading it when there is none.
func pklResolver(name string) resolver {
	return func(ctx context.Context, dir string) error {
		binary, err := pkl.Find(ctx)
		if err != nil {
			return fmt.Errorf("finding pkl: %w", err)
		}
		return pkl.Evaluator{Binary: binary, Name: name}.Resolve(ctx, dir)
	}
}

// resolveWritten resolves the PklProject init or migrate wrote in dir, and returns why it could
// not as a line to print: the files are written either way, and a command that evaluates the
// configuration resolves it first.
func resolveWritten(ctx context.Context, resolve resolver, name, dir string) (bool, string) {
	if err := resolve(ctx, dir); err != nil {
		return false, fmt.Sprintf("%s is not resolved yet, and %s plan resolves it first: %v", pkl.ProjectFile, name, err)
	}
	return true, ""
}
