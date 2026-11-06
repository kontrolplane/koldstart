package build

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/evanw/esbuild/pkg/api"

	"github.com/kontrolplane/koldstart/internal/config"
)

// SplitHandler splits a node handler, src/api.handler, into the module, src/api, and the export,
// handler.
func SplitHandler(handler string) (module, export string, err error) {
	i := strings.LastIndex(handler, ".")
	if i <= 0 || i == len(handler)-1 {
		return "", "", fmt.Errorf("handler %q: want the file and its export, such as src/api.handler", handler)
	}
	return handler[:i], handler[i+1:], nil
}

// Entry finds the file of a node handler in root.
func Entry(root, handler string) (string, error) {
	module, _, err := SplitHandler(handler)
	if err != nil {
		return "", err
	}
	for _, ext := range config.NodeExtensions {
		file := filepath.Join(root, filepath.FromSlash(module)+ext)
		if _, err := os.Stat(file); err == nil {
			return file, nil
		}
	}
	return "", fmt.Errorf("handler %q: no %s with any of %s", handler, module, strings.Join(config.NodeExtensions, ", "))
}

// npmCPU is the architecture lambda runs a function on, as npm's --cpu names it.
func npmCPU(architecture string) string {
	if architecture == config.ArchX8664 {
		return "x64"
	}
	return "arm64"
}

// nodeVersion is the node a runtime runs, for esbuild's target.
func nodeVersion(runtime string) string {
	if runtime == config.RuntimeNode24 {
		return "24"
	}
	return "22"
}

// node bundles the handler with esbuild into one commonjs file, named after the handler's module
// so the handler stays as configured. Packages in fn.Bundle.External are left out of the bundle
// and copied from node_modules instead, with what they depend on.
func (b Builder) node(ctx context.Context, fn config.Function) ([]File, error) {
	entry, err := Entry(b.Root, fn.Handler)
	if err != nil {
		return nil, err
	}
	if fn.Typecheck {
		if err := typecheck(ctx, b.Root); err != nil {
			return nil, err
		}
	}
	module, _, _ := SplitHandler(fn.Handler)
	outfile := filepath.Join(b.Root, filepath.FromSlash(module)+".js")

	sourcemap := api.SourceMapNone
	if fn.Bundle.Sourcemap {
		sourcemap = api.SourceMapExternal
	}
	result := api.Build(api.BuildOptions{
		EntryPoints:   []string{entry},
		Outfile:       outfile,
		AbsWorkingDir: b.Root,
		Bundle:        true,
		Platform:      api.PlatformNode,
		Format:        api.FormatCommonJS,
		Engines:       []api.Engine{{Name: api.EngineNode, Version: nodeVersion(fn.Runtime)}},
		External:      fn.Bundle.External,
		Sourcemap:     sourcemap,
		LogLevel:      api.LogLevelSilent,
		Write:         false,
	})
	if len(result.Errors) > 0 {
		err := buildErrors(result.Errors)
		if hint := installHint(b.Root, unresolvedPackages(result.Errors)); hint != "" {
			return nil, fmt.Errorf("%w\n%s", err, hint)
		}
		return nil, err
	}

	var files []File
	for _, out := range result.OutputFiles {
		rel, err := filepath.Rel(b.Root, out.Path)
		if err != nil {
			return nil, err
		}
		files = append(files, File{Name: filepath.ToSlash(rel), Data: out.Contents})
	}
	modules, err := nodeModules(b.Root, fn.Bundle.External, npmCPU(fn.Architecture))
	if err != nil {
		return nil, err
	}
	return append(files, modules...), nil
}

// buildErrors turns esbuild's errors into one error, each with its file and line.
func buildErrors(messages []api.Message) error {
	errs := make([]error, 0, len(messages))
	for _, m := range messages {
		if m.Location != nil {
			errs = append(errs, fmt.Errorf("%s:%d:%d: %s", m.Location.File, m.Location.Line, m.Location.Column+1, m.Text))
		} else {
			errs = append(errs, errors.New(m.Text))
		}
	}
	return errors.Join(errs...)
}

// unresolvedPackages are the packages esbuild could not resolve, by name, such as lodash or
// @aws-sdk/client-s3: the bare imports of its errors, not the relative ones.
func unresolvedPackages(messages []api.Message) []string {
	var names []string
	for _, m := range messages {
		imported, ok := strings.CutPrefix(m.Text, `Could not resolve "`)
		if !ok {
			continue
		}
		imported = strings.TrimSuffix(imported, `"`)
		if imported == "" || strings.HasPrefix(imported, ".") || strings.HasPrefix(imported, "/") || strings.Contains(imported, ":") {
			continue
		}
		parts := strings.SplitN(imported, "/", 3)
		name := parts[0]
		if strings.HasPrefix(name, "@") && len(parts) > 1 {
			name += "/" + parts[1]
		}
		names = append(names, name)
	}
	slices.Sort(names)
	return slices.Compact(names)
}

// installHint says how to install the packages esbuild could not resolve: npm install when
// node_modules is missing or they are in package.json, npm install <package> when they are
// not. It is "" without packages.
func installHint(root string, packages []string) string {
	if len(packages) == 0 {
		return ""
	}
	dir := shownDir(root)
	if _, err := os.Stat(filepath.Join(root, "node_modules")); err != nil {
		return fmt.Sprintf("there is no node_modules in %s: run npm install, or npm ci, there", dir)
	}
	declared := declaredPackages(root)
	var missing, undeclared []string
	for _, name := range packages {
		if declared[name] {
			missing = append(missing, name)
		} else {
			undeclared = append(undeclared, name)
		}
	}
	var hints []string
	if len(missing) > 0 {
		hints = append(hints, fmt.Sprintf("%s in package.json but not installed: run npm install, or npm ci, in %s", isAre(missing), dir))
	}
	if len(undeclared) > 0 {
		hints = append(hints, fmt.Sprintf("%s not in package.json: add %s with npm install %s in %s", isAre(undeclared), itThem(undeclared), strings.Join(undeclared, " "), dir))
	}
	return strings.Join(hints, "\n")
}

// declaredPackages are the dependencies and dev dependencies in the package.json in root.
func declaredPackages(root string) map[string]bool {
	var pkg struct {
		Dependencies    map[string]string `json:"dependencies"`
		DevDependencies map[string]string `json:"devDependencies"`
	}
	data, err := fs.ReadFile(os.DirFS(root), "package.json")
	if err != nil {
		return nil
	}
	if err := json.Unmarshal(data, &pkg); err != nil {
		return nil
	}
	declared := map[string]bool{}
	for name := range pkg.Dependencies {
		declared[name] = true
	}
	for name := range pkg.DevDependencies {
		declared[name] = true
	}
	return declared
}

// isAre names packages with is or are: "lodash is", "lodash and zod are".
func isAre(names []string) string {
	if len(names) == 1 {
		return names[0] + " is"
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1] + " are"
}

// itThem is it for one package and them for more.
func itThem(names []string) string {
	if len(names) == 1 {
		return "it"
	}
	return "them"
}

// shownDir is a directory as an error shows it: relative to the working directory when it is
// inside it, this directory when it is the working directory, absolute otherwise.
func shownDir(dir string) string {
	wd, err := os.Getwd()
	if err != nil {
		return dir
	}
	rel, err := filepath.Rel(wd, dir)
	switch {
	case err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)):
		return dir
	case rel == ".":
		return "this directory"
	}
	return rel
}

// typecheck runs tsc over the project, as asked for with typecheck = true.
//
//nolint:gosec // the project's own tsc
func typecheck(ctx context.Context, root string) error {
	tsc := filepath.Join(root, "node_modules", ".bin", "tsc")
	if _, err := os.Stat(tsc); err != nil {
		found, err := exec.LookPath("tsc")
		if err != nil {
			return errors.New("typecheck = true needs typescript: npm install --save-dev typescript")
		}
		tsc = found
	}
	cmd := exec.CommandContext(ctx, tsc, "--noEmit")
	cmd.Dir = root
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		if msg := strings.TrimSpace(out.String()); msg != "" {
			return fmt.Errorf("tsc --noEmit:\n%s", msg)
		}
		return fmt.Errorf("tsc --noEmit: %w", err)
	}
	return nil
}

// nodeModules returns the files of the packages names and of everything they depend on, from
// the node_modules of root, as npm and yarn lay it out. cpu is the architecture they are for, as
// npm names it: arm64 or x64.
func nodeModules(root string, names []string, cpu string) ([]File, error) {
	var files []File
	seen := map[string]bool{}
	queue := slices.Clone(names)
	for len(queue) > 0 {
		name := queue[0]
		queue = queue[1:]
		if seen[name] {
			continue
		}
		seen[name] = true
		pkgDir, err := filepath.EvalSymlinks(filepath.Join(root, "node_modules", filepath.FromSlash(name)))
		if err != nil {
			return nil, fmt.Errorf("%s is not in node_modules: install it for lambda in %s, with npm install --os=linux --cpu=%s %s", name, shownDir(root), cpu, name)
		}
		deps, err := dependencies(root, name, pkgDir)
		if err != nil {
			return nil, err
		}
		queue = append(queue, deps...)
		pkgFiles, err := packageFiles(name, pkgDir)
		if err != nil {
			return nil, fmt.Errorf("copying %s: %w", name, err)
		}
		files = append(files, pkgFiles...)
	}
	return files, nil
}

// dependencies are what the package in pkgDir depends on: all its dependencies, and the optional
// ones that are installed.
func dependencies(root, name, pkgDir string) ([]string, error) {
	var pkg struct {
		Dependencies         map[string]string `json:"dependencies"`
		OptionalDependencies map[string]string `json:"optionalDependencies"`
	}
	data, err := fs.ReadFile(os.DirFS(pkgDir), "package.json")
	if err != nil {
		return nil, fmt.Errorf("reading the package.json of %s: %w", name, err)
	}
	if err := json.Unmarshal(data, &pkg); err != nil {
		return nil, fmt.Errorf("reading the package.json of %s: %w", name, err)
	}
	deps := slices.Sorted(maps.Keys(pkg.Dependencies))
	for _, dep := range slices.Sorted(maps.Keys(pkg.OptionalDependencies)) {
		if _, err := os.Stat(filepath.Join(root, "node_modules", filepath.FromSlash(dep))); err == nil {
			deps = append(deps, dep)
		}
	}
	return deps, nil
}

// packageFiles are the files of the package in pkgDir, under node_modules/<name> in the zip.
func packageFiles(name, pkgDir string) ([]File, error) {
	var files []File
	fsys := os.DirFS(pkgDir)
	err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		content, err := fs.ReadFile(fsys, p)
		if err != nil {
			return err
		}
		files = append(files, File{
			Name:       path.Join("node_modules", name, p),
			Data:       content,
			Executable: info.Mode().Perm()&0o111 != 0,
		})
		return nil
	})
	return files, err
}
