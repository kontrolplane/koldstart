package config

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
)

// DefaultNode and DefaultPython are the runtimes a function gets when its runtime is inferred
// from a node or a python file: nodejs22.x, the node kdst init and migrate write, and python3.13,
// the newest python the schema lists. A function that wants another sets runtime.
const (
	DefaultNode   = RuntimeNode22
	DefaultPython = RuntimePy313
)

// NodeExtensions are the extensions of a node handler's file, in the order they are tried.
var NodeExtensions = []string{".ts", ".mts", ".cts", ".tsx", ".js", ".mjs", ".cjs", ".jsx"}

// Infer fills in the runtime and handler a function without an image leaves out, from its name
// and the files in root, the project's directory:
//
//   - src/<name>.ts, or another of NodeExtensions: DefaultNode, src/<name>.handler
//   - src/<name>.py: DefaultPython, src/<name>.handler
//   - cmd/<name>/ with go files or a Cargo.toml: provided.al2023, ./cmd/<name>
//
// A runtime without a handler gets that runtime's handler. A handler without a runtime gets the
// runtime of what it names: provided.al2023 for ./…, a directory of go files or one with a
// Cargo.toml, otherwise node or python for the file that is there. A bootstrap without a runtime
// gets provided.al2023, and no handler. Finding none of them, or more than one, is an error that
// says what to set.
func (p *Project) Infer(root string) error {
	var errs []error
	for _, name := range slices.Sorted(maps.Keys(p.Functions)) {
		fn := p.Functions[name]
		if fn.IsImage() || (fn.Runtime != "" && fn.Handler != "") {
			continue
		}
		if err := infer(root, name, &fn); err != nil {
			errs = append(errs, fmt.Errorf("function %s: %w", name, err))
			continue
		}
		p.Functions[name] = fn
	}
	return errors.Join(errs...)
}

// candidate is a runtime a function could have, with the file or directory that says so.
type candidate struct {
	runtime string
	file    string
}

// infer fills in fn's runtime, its handler, or both, for a function called name.
func infer(root, name string, fn *Function) error {
	switch {
	case fn.Bootstrap != "":
		if fn.Runtime == "" {
			fn.Runtime = RuntimeProvided
		}
		return nil
	case fn.Runtime != "":
		fn.Handler = conventionalHandler(fn.Runtime, name)
		return nil
	case fn.Handler != "":
		runtime, err := handlerRuntime(root, fn.Handler)
		fn.Runtime = runtime
		return err
	}
	module := "src/" + name
	found := sources(root, module)
	if dir := "cmd/" + name; goPackage(root, dir) || crate(root, dir) {
		found = append(found, candidate{RuntimeProvided, dir + "/"})
	}
	switch len(found) {
	case 0:
		return fmt.Errorf("no %s.ts, %s.py or cmd/%s/ here: set runtime and handler", module, module, name)
	case 1:
		fn.Runtime = found[0].runtime
		fn.Handler = conventionalHandler(fn.Runtime, name)
		return nil
	}
	return ambiguous(found)
}

// conventionalHandler is the handler of a function called name on runtime, by the convention.
func conventionalHandler(runtime, name string) string {
	if runtime == RuntimeProvided {
		return "./cmd/" + name
	}
	return "src/" + name + ".handler"
}

// handlerRuntime is the runtime of what handler names in root.
func handlerRuntime(root, handler string) (string, error) {
	if strings.HasPrefix(handler, "./") || strings.HasPrefix(handler, "../") || goPackage(root, handler) || crate(root, handler) {
		return RuntimeProvided, nil
	}
	i := strings.LastIndex(handler, ".")
	if i <= 0 {
		return "", fmt.Errorf("handler %s names no file and function, such as src/api.handler, or directory to build, such as ./cmd/api: set runtime, or bootstrap for a binary built elsewhere", handler)
	}
	module := handler[:i]
	found := sources(root, module)
	switch len(found) {
	case 0:
		return "", fmt.Errorf("handler %s: no %s.ts or %s.py here: set runtime", handler, module, module)
	case 1:
		return found[0].runtime, nil
	}
	return "", fmt.Errorf("handler %s: %w", handler, ambiguous(found))
}

// ambiguous is the error of a function more than one runtime fits.
func ambiguous(found []candidate) error {
	files := make([]string, len(found))
	runtimes := make([]string, len(found))
	for i, c := range found {
		files[i] = c.file
		runtimes[i] = c.runtime
	}
	all := "both"
	if len(found) > 2 {
		all = "all"
	}
	return fmt.Errorf("%s are %s here: set runtime to %s", list(files, "and"), all, list(runtimes, "or"))
}

// list joins items as a sentence does, with word before the last.
func list(items []string, word string) string {
	if len(items) < 2 {
		return strings.Join(items, "")
	}
	return strings.Join(items[:len(items)-1], ", ") + " " + word + " " + items[len(items)-1]
}

// sources are the runtimes the files of module in root fit: node for one of NodeExtensions, and
// python for .py.
func sources(root, module string) []candidate {
	var found []candidate
	if file := nodeFile(root, module); file != "" {
		found = append(found, candidate{DefaultNode, file})
	}
	if file := module + ".py"; exists(root, file) {
		found = append(found, candidate{DefaultPython, file})
	}
	return found
}

// nodeFile is the first of module with one of NodeExtensions in root, or empty when there is none.
func nodeFile(root, module string) string {
	for _, ext := range NodeExtensions {
		if exists(root, module+ext) {
			return module + ext
		}
	}
	return ""
}

// exists reports whether root holds a regular file at the slash-separated name.
func exists(root, name string) bool {
	info, err := os.Stat(filepath.Join(root, filepath.FromSlash(name)))
	return err == nil && info.Mode().IsRegular()
}

// goPackage reports whether the slash-separated dir in root holds go files other than tests.
func goPackage(root, dir string) bool {
	entries, err := os.ReadDir(filepath.Join(root, filepath.FromSlash(path.Clean(dir))))
	if err != nil {
		return false
	}
	return slices.ContainsFunc(entries, func(e os.DirEntry) bool {
		return !e.IsDir() && strings.HasSuffix(e.Name(), ".go") && !strings.HasSuffix(e.Name(), "_test.go")
	})
}

// crate reports whether the slash-separated dir in root holds a Cargo.toml, a rust crate or a
// member of a workspace.
func crate(root, dir string) bool {
	return dir != "" && exists(root, path.Join(dir, "Cargo.toml"))
}
