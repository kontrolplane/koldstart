package build

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/kontrolplane/koldstart/internal/config"
)

// pythonSkipped are directories never put in a python function's zip.
var pythonSkipped = []string{"__pycache__", ".venv", "venv", "node_modules", ".git", ".koldstart", ".mypy_cache", ".pytest_cache"}

// pythonVersion is the version of python a runtime runs, as uv takes it: 3.12.
func pythonVersion(runtime string) string {
	return strings.TrimPrefix(runtime, "python")
}

// pythonPlatform is the platform uv installs wheels for, so a package with native code gets
// the build lambda runs: manylinux for the function's architecture. For this machine it is
// empty, and uv installs for the python here.
func pythonPlatform(target Target) string {
	if target.GOOS != Linux {
		return ""
	}
	if target.GOARCH == goAMD64 {
		return "x86_64-manylinux2014"
	}
	return "aarch64-manylinux2014"
}

// python builds a python function: the files bundle.include names, or the directory of the
// handler's module, and its dependencies, from requirements.txt, or pyproject.toml through uv,
// installed by uv for the platform the function runs on.
func (b Builder) python(ctx context.Context, fn config.Function, target Target) ([]File, error) {
	module, _, err := SplitHandler(fn.Handler)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(filepath.Join(b.Root, filepath.FromSlash(module)+".py")); err != nil {
		return nil, fmt.Errorf("handler %s: there is no %s.py", fn.Handler, module)
	}
	include := fn.Bundle.Include
	if len(include) == 0 {
		include = []string{module + ".py"}
		if top, _, ok := strings.Cut(module, "/"); ok {
			include = []string{top}
		}
	}
	files, err := b.pythonSources(include)
	if err != nil {
		return nil, err
	}
	deps, err := b.pythonDependencies(ctx, fn, target)
	if err != nil {
		return nil, err
	}
	return append(files, deps...), nil
}

// pythonSources reads the files and directories include names, relative to the project.
func (b Builder) pythonSources(include []string) ([]File, error) {
	var files []File
	for _, pattern := range include {
		matches, err := filepath.Glob(filepath.Join(b.Root, filepath.FromSlash(pattern)))
		if err != nil {
			return nil, fmt.Errorf("bundle.include %q: %w", pattern, err)
		}
		if len(matches) == 0 {
			return nil, fmt.Errorf("bundle.include %q names nothing in %s", pattern, b.Root)
		}
		for _, m := range matches {
			found, err := treeFiles(b.Root, m)
			if err != nil {
				return nil, err
			}
			files = append(files, found...)
		}
	}
	slices.SortFunc(files, func(a, c File) int { return strings.Compare(a.Name, c.Name) })
	return slices.CompactFunc(files, func(a, c File) bool { return a.Name == c.Name }), nil
}

// treeFiles reads a file, or the files under a directory, named relative to root, without
// caches, environments and compiled python.
//
//nolint:gosec // reads the project's own files
func treeFiles(root, path string) ([]File, error) {
	var files []File
	err := filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if p != path && slices.Contains(pythonSkipped, d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(p, ".pyc") || d.Name() == ".DS_Store" {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		files = append(files, File{Name: filepath.ToSlash(rel), Data: data, Executable: info.Mode()&0o111 != 0})
		return nil
	})
	return files, err
}

// pythonDependencies installs the function's dependencies with uv into a directory of its own,
// and returns what is in it. The scripts uv writes to bin, whose first line names a python of
// this machine, and the RECORD files that list them are left out, so the zip is the same on
// every machine.
//
//nolint:gosec // runs uv on the project's own requirements
func (b Builder) pythonDependencies(ctx context.Context, fn config.Function, target Target) ([]File, error) {
	requirements := filepath.Join(b.Root, "requirements.txt")
	_, hasRequirements := os.Stat(requirements)
	_, hasProject := os.Stat(filepath.Join(b.Root, "pyproject.toml"))
	if hasRequirements != nil && hasProject != nil {
		return nil, nil
	}
	uv, err := exec.LookPath("uv")
	if err != nil {
		return nil, errors.New("python functions need uv on the PATH to install their dependencies: https://docs.astral.sh/uv/")
	}
	dir, err := os.MkdirTemp("", "koldstart-python-")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	if hasRequirements != nil {
		requirements = filepath.Join(dir, "requirements.txt")
		args := []string{"export", "--no-dev", "--no-hashes", "--no-emit-project", "--quiet", "-o", requirements}
		if _, err := os.Stat(filepath.Join(b.Root, "uv.lock")); err == nil {
			args = append(args, "--frozen")
		}
		if err := run(ctx, b.Root, uv, args...); err != nil {
			return nil, fmt.Errorf("uv export of pyproject.toml: %w", err)
		}
	}
	depsDir := filepath.Join(dir, "deps")
	args := []string{"pip", "install", "--quiet", "--target", depsDir, "-r", requirements}
	if platform := pythonPlatform(target); platform != "" {
		args = append(args, "--python-version", pythonVersion(fn.Runtime), "--python-platform", platform, "--only-binary", ":all:")
	} else {
		local, err := LocalPython(ctx, fn.Runtime)
		if err != nil {
			return nil, err
		}
		args = append(args, "--python", local)
	}
	if err := run(ctx, b.Root, uv, args...); err != nil {
		return nil, fmt.Errorf("uv pip install: %w", err)
	}
	all, err := treeFiles(depsDir, depsDir)
	if err != nil {
		return nil, err
	}
	files := all[:0]
	for _, f := range all {
		if strings.HasPrefix(f.Name, "bin/") || strings.HasSuffix(f.Name, ".dist-info/RECORD") {
			continue
		}
		files = append(files, f)
	}
	return files, nil
}

// LocalPython is the python a function runs with on this machine: the version its runtime runs,
// as uv finds it, or installs it once when it is not here. Its dependencies are installed for it,
// so packages with native code import, as in lambda.
//
//nolint:gosec // runs uv to find or install a python
func LocalPython(ctx context.Context, runtime string) (string, error) {
	uv, err := exec.LookPath("uv")
	if err != nil {
		return "", errors.New("python functions need uv on the PATH to run here: https://docs.astral.sh/uv/")
	}
	version := pythonVersion(runtime)
	find := func() (string, error) {
		out, err := exec.CommandContext(ctx, uv, "python", "find", version).Output()
		return strings.TrimSpace(string(out)), err
	}
	if p, err := find(); err == nil && p != "" {
		return p, nil
	}
	if err := run(ctx, ".", uv, "python", "install", version); err != nil {
		return "", fmt.Errorf("installing python %s with uv: %w", version, err)
	}
	return find()
}

// run runs a command in dir, its output in the error when it fails.
//
//nolint:gosec // runs uv, with arguments koldstart puts together
func run(ctx context.Context, dir, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w\n%s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
