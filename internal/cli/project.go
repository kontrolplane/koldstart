package cli

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
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

// shownPath is the configuration's path as errors show it: relative to the working directory.
func (p *project) shownPath() string {
	return relative(p.Path)
}

// relative returns path relative to the working directory, or as it is when it cannot be.
func relative(path string) string {
	wd, err := os.Getwd()
	if err != nil {
		return path
	}
	rel, err := filepath.Rel(wd, path)
	if err != nil {
		return path
	}
	return rel
}

// loadProject evaluates the configuration the global flags name, for their stage.
func loadProject(ctx context.Context, opts *Options) (*project, error) {
	path, err := filepath.Abs(opts.Config)
	if err != nil {
		return nil, fmt.Errorf("finding %s: %w", opts.Config, err)
	}
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("no %s here: run %s init to start a project, or pass --config", opts.Config, opts.binary())
	} else if err != nil {
		return nil, fmt.Errorf("reading %s: %w", opts.Config, err)
	}
	binary, err := pkl.Find(ctx)
	if err != nil {
		return nil, fmt.Errorf("finding pkl: %w", err)
	}
	p, err := config.Load(ctx, pkl.Evaluator{Binary: binary}, relative(path), opts.Stage)
	if err != nil {
		return nil, err
	}
	if opts.Region != "" {
		p.Region = opts.Region
	}
	return &project{Project: p, Root: filepath.Dir(path), Path: path}, nil
}
