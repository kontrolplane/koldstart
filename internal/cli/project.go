package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/kontrolplane/koldstart/internal/config"
	"github.com/kontrolplane/koldstart/internal/pkl"
)

// project is a loaded configuration and the directory it lives in, which paths in it are
// relative to.
type project struct {
	*config.Project
	Root string
	Path string
}

// loadProject evaluates the configuration the global flags name, for their stage.
func loadProject(ctx context.Context, opts *Options) (*project, error) {
	path, err := filepath.Abs(opts.Config)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("no %s here: run init to write one, or pass --config", opts.Config)
		}
		return nil, err
	}
	binary, err := pkl.Find(ctx)
	if err != nil {
		return nil, fmt.Errorf("finding pkl: %w", err)
	}
	p, err := config.Load(ctx, pkl.Evaluator{Binary: binary}, path, opts.Stage)
	if err != nil {
		return nil, err
	}
	if opts.Region != "" {
		p.Region = opts.Region
	}
	return &project{Project: p, Root: filepath.Dir(path), Path: path}, nil
}
