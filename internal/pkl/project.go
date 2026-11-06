package pkl

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// The files of a pkl project: the one that declares its dependencies, and the lock file pkl
// resolves them into, which is committed with it.
const (
	ProjectFile = "PklProject"
	DepsFile    = "PklProject.deps.json"
)

// Dependency is the name a project gives the schema package, which a koldstart.pkl imports as
// @koldstart/.
const Dependency = "koldstart"

// FindProject returns the directory of the nearest PklProject at or above the directory of path,
// or "" when there is none.
func FindProject(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("finding %s: %w", path, err)
	}
	for dir := filepath.Dir(abs); ; dir = filepath.Dir(dir) {
		if _, err := os.Stat(filepath.Join(dir, ProjectFile)); err == nil {
			return dir, nil
		} else if !errors.Is(err, fs.ErrNotExist) {
			return "", fmt.Errorf("looking for a %s above %s: %w", ProjectFile, path, err)
		}
		if parent := filepath.Dir(dir); parent == dir {
			return "", nil
		}
	}
}

// Stale reports whether the project in dir needs resolving: its PklProject.deps.json is missing,
// or older than its PklProject.
func Stale(dir string) (bool, error) {
	project, err := os.Stat(filepath.Join(dir, ProjectFile))
	if err != nil {
		return false, fmt.Errorf("reading %s: %w", filepath.Join(dir, ProjectFile), err)
	}
	deps, err := os.Stat(filepath.Join(dir, DepsFile))
	if errors.Is(err, fs.ErrNotExist) {
		return true, nil
	} else if err != nil {
		return false, fmt.Errorf("reading %s: %w", filepath.Join(dir, DepsFile), err)
	}
	return deps.ModTime().Before(project.ModTime()), nil
}

// Resolve runs pkl project resolve on the project in dir, which writes its PklProject.deps.json,
// downloading the metadata of the packages it depends on.
//
//nolint:gosec // runs our pkl on the user's project
func (e Evaluator) Resolve(ctx context.Context, dir string) error {
	cmd := exec.CommandContext(ctx, e.Binary, "project", "resolve", dir)
	var stderr bytes.Buffer
	cmd.Stdout = io.Discard
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && stderr.Len() > 0 {
			return &ResolveError{Project: filepath.Join(dir, ProjectFile), Message: report(stderr.String(), filepath.Join(dir, ProjectFile)), name: e.name()}
		}
		return fmt.Errorf("running %s on %s: %w\ncheck it runs, or set %s to another pkl binary", e.Binary, dir, err, EnvBinary)
	}
	return nil
}

// resolveStale resolves the project in dir when it is stale, saying so on e.Notes.
func (e Evaluator) resolveStale(ctx context.Context, dir string) error {
	stale, err := Stale(dir)
	if err != nil || !stale {
		return err
	}
	if e.Notes != nil {
		_, _ = fmt.Fprintf(e.Notes, "resolving the dependencies of %s into %s\n", shown(filepath.Join(dir, ProjectFile)), DepsFile)
	}
	return e.Resolve(ctx, dir)
}

// name is the name of the command, for errors that say what to run.
func (e Evaluator) name() string {
	return cmp.Or(e.Name, "koldstart")
}

// shown is path relative to the working directory, or as it is when it cannot be.
func shown(path string) string {
	wd, err := os.Getwd()
	if err != nil {
		return path
	}
	if rel, err := filepath.Rel(wd, path); err == nil && !strings.HasPrefix(rel, "..") {
		return rel
	}
	return path
}

// ResolveError is a project whose dependencies pkl could not resolve; Message is pkl's report.
type ResolveError struct {
	Project string
	Message string
	// name is the command's name, for what to run.
	name string
}

// notFound finds the package pkl was refused, and the version in its uri.
var notFound = regexp.MustCompile("status code `404` when making request `GET ([^`]*@([^`/@]+))`")

func (e *ResolveError) Error() string {
	shownProject := shown(e.Project)
	if m := notFound.FindStringSubmatch(e.Message); m != nil {
		return fmt.Sprintf("resolving %s: there is no schema package %s at %s, which may not be released: set the version in its %s uri to a release of koldstart, or run %s upgrade to use this koldstart's\n\n%s",
			shownProject, m[2], m[1], Dependency, cmp.Or(e.name, "koldstart"), e.Message)
	}
	if offline(e.Message) {
		return fmt.Sprintf("resolving %s: the packages it depends on did not download: check this machine is online, and run it again\n\n%s", shownProject, e.Message)
	}
	return fmt.Sprintf("resolving %s: fix it where pkl points\n\n%s", shownProject, e.Message)
}

// offline reports whether pkl's report is of a request that did not reach its host.
func offline(message string) bool {
	for _, marker := range []string{"Error connecting to host", "UnknownHostException", "Exception when making request"} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}
