// Package scaffold writes a new project: a koldstart.pkl with one function behind an http route,
// its handler in typescript or go, and the entries koldstart needs in .gitignore.
package scaffold

import (
	"bytes"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"text/template"

	schema "github.com/kontrolplane/koldstart/pkl"
)

// The templates init writes from.
const (
	Node = "node"
	Go   = "go"
)

// Templates are the names of the templates, in the order help lists them.
var Templates = []string{Node, Go}

// files are the templates, a file ending in .tmpl filled in and written without the suffix.
//
//go:embed all:templates
var files embed.FS

// Ignored are the lines .gitignore gets: where koldstart builds functions, for aws and to run here.
var Ignored = []string{".koldstart/build/", ".koldstart/local/"}

// Config is the file every template writes, which init refuses to write over without force.
const Config = "koldstart.pkl"

// Options say what to write, and where.
type Options struct {
	Dir      string
	App      string
	Region   string
	Template string
	// Version is the koldstart whose schema package the configuration amends.
	Version string
	// Force writes over a koldstart.pkl that is there.
	Force bool
}

// Result is what was written, and what was there already and left as it was, by their paths in
// the project.
type Result struct {
	Written []string
	Kept    []string
	// Gitignore lists the lines added to .gitignore.
	Gitignore []string
}

var appPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,23}$`)

// AppName makes an app name from a directory's name: lowercase, with dashes for what an app name
// cannot hold, and at most 24 long. It is "" when nothing usable is left.
func AppName(dir string) string {
	name := strings.ToLower(filepath.Base(dir))
	name = regexp.MustCompile(`[^a-z0-9]+`).ReplaceAllString(name, "-")
	name = strings.TrimLeft(name, "-0123456789")
	if len(name) > 24 {
		name = name[:24]
	}
	name = strings.TrimRight(name, "-")
	if !appPattern.MatchString(name) {
		return ""
	}
	return name
}

// Write writes the template into the directory. Files that are there already are kept, but for
// koldstart.pkl, which is refused unless o.Force is set.
func Write(o Options) (Result, error) {
	if !slices.Contains(Templates, o.Template) {
		return Result{}, fmt.Errorf("no template %q: want %s", o.Template, strings.Join(Templates, " or "))
	}
	if !appPattern.MatchString(o.App) {
		return Result{}, fmt.Errorf("app %q: lowercase letters, digits and dashes, at most 24, starting with a letter", o.App)
	}
	if _, err := os.Stat(filepath.Join(o.Dir, Config)); err == nil && !o.Force {
		return Result{}, fmt.Errorf("%s is there already: pass --force to write over it", Config)
	}
	vars := map[string]string{
		"App":     o.App,
		"Region":  o.Region,
		"Project": schema.PackageURI(o.Version, schema.Project),
		"Kdst":    schema.PackageURI(o.Version, schema.Kdst),
	}
	root := "templates/" + o.Template
	var res Result
	err := fs.WalkDir(files, root, func(name string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel := strings.TrimSuffix(strings.TrimPrefix(name, root+"/"), ".tmpl")
		data, err := render(name, vars)
		if err != nil {
			return err
		}
		written, err := writeFile(filepath.Join(o.Dir, filepath.FromSlash(rel)), data, rel == Config)
		if err != nil {
			return err
		}
		if written {
			res.Written = append(res.Written, rel)
		} else {
			res.Kept = append(res.Kept, rel)
		}
		return nil
	})
	if err != nil {
		return Result{}, err
	}
	res.Gitignore, err = ignore(filepath.Join(o.Dir, ".gitignore"))
	return res, err
}

// render fills in a template, or returns a file that is not one as it is.
func render(name string, vars map[string]string) ([]byte, error) {
	data, err := fs.ReadFile(files, name)
	if err != nil {
		return nil, err
	}
	if !strings.HasSuffix(name, ".tmpl") {
		return data, nil
	}
	t, err := template.New(name).Option("missingkey=error").Parse(string(data))
	if err != nil {
		return nil, err
	}
	var b bytes.Buffer
	if err := t.Execute(&b, vars); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// writeFile writes a file unless it is there already, or always with overwrite, and reports
// whether it did.
//
//nolint:gosec // the files of a project, which everyone reads
func writeFile(path string, data []byte, overwrite bool) (bool, error) {
	if _, err := os.Stat(path); err == nil && !overwrite {
		return false, nil
	} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return false, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return false, err
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return false, fmt.Errorf("writing %s: %w", path, err)
	}
	return true, nil
}

// ignore adds the lines of Ignored that .gitignore does not have yet, and returns them.
//
//nolint:gosec // reads and writes the project's .gitignore, which everyone reads
func ignore(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	have := map[string]bool{}
	for line := range strings.SplitSeq(string(data), "\n") {
		have[strings.TrimSpace(line)] = true
	}
	var missing []string
	for _, line := range Ignored {
		if !have[line] && !have[strings.TrimSuffix(line, "/")] {
			missing = append(missing, line)
		}
	}
	if len(missing) == 0 {
		return nil, nil
	}
	var b strings.Builder
	b.Write(data)
	if len(data) > 0 && !bytes.HasSuffix(data, []byte("\n")) {
		b.WriteString("\n")
	}
	if len(data) > 0 {
		b.WriteString("\n")
	}
	b.WriteString("# koldstart\n" + strings.Join(missing, "\n") + "\n")
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		return nil, fmt.Errorf("writing %s: %w", path, err)
	}
	return missing, nil
}
