// Package schema holds the pkl schema a koldstart.pkl amends, as this koldstart reads it, and the
// koldstart dependency a project's PklProject declares on it.
package schema

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/kontrolplane/koldstart/internal/pkl"
	"github.com/kontrolplane/koldstart/internal/version"
)

// Files are Project.pkl and kdst.pkl, and the PklProject that makes them a package.
//
//go:embed Project.pkl kdst.pkl PklProject
var Files embed.FS

// The modules a koldstart.pkl amends and imports, and the package's PklProject, by their file
// name in Files.
const (
	Project = "Project.pkl"
	Kdst    = "kdst.pkl"
	Package = pkl.ProjectFile
)

// modules are the files of Files, in the order they are hashed.
var modules = []string{Project, Kdst, Package}

// releases is where a release of koldstart publishes its schema package, before its version.
const releases = "package://github.com/kontrolplane/koldstart/releases/download/v"

// PackageURI is the uri of a module of the schema package released with koldstart v, as a
// koldstart.pkl without a PklProject names it.
func PackageURI(v, module string) string {
	return Release(v).URI() + "#/" + module
}

// Dependency is the koldstart dependency of a project: the schema package of a release, or a
// local one, the schema a dev build embeds, extracted into a directory.
type Dependency struct {
	// Version is the release, such as 0.1.0, and "" for a local dependency.
	Version string
	// Dir is the directory of a local dependency's PklProject, as the project's PklProject names
	// it.
	Dir string
}

// Release is the schema package released with koldstart v.
func Release(v string) Dependency {
	return Dependency{Version: v}
}

// Local reports whether d is a local dependency rather than a release.
func (d Dependency) Local() bool {
	return d.Version == ""
}

// URI is the package uri of a release.
func (d Dependency) URI() string {
	return releases + d.Version + "/koldstart@" + d.Version
}

// String is the version of a release, or "dev" and the directory of a local dependency.
func (d Dependency) String() string {
	if d.Local() {
		return "dev (" + d.Dir + ")"
	}
	return d.Version
}

// Name is d by its version, or dev for the schema of a build, without the directory String gives.
func (d Dependency) Name() string {
	if d.Local() {
		return "dev"
	}
	return d.Version
}

// Entry is d as a line of a PklProject's dependencies.
func (d Dependency) Entry() string {
	if d.Local() {
		return fmt.Sprintf("[%q] = import(%q)", pkl.Dependency, filepath.ToSlash(filepath.Join(d.Dir, Package)))
	}
	return fmt.Sprintf("[%q] { uri = %q }", pkl.Dependency, d.URI())
}

// PklProject is the PklProject of a new project that depends on d.
func (d Dependency) PklProject() string {
	return "amends \"pkl:Project\"\n\ndependencies {\n  " + d.Entry() + "\n}\n"
}

// releasePattern is the version of a release of koldstart, with or without its v: 0.1.0, or a
// prerelease such as 0.2.0-rc.1. A dev build's version, such as dev, a commit, the 0.1.1-next of
// a snapshot, the v0.1.0-3-gabc1234 of git describe or a go pseudo-version, is none.
var releasePattern = regexp.MustCompile(`^v?(\d+\.\d+\.\d+(?:-(?:alpha|beta|rc)(?:\.\d+)?)?)$`)

// Current is the dependency kdst init, migrate and upgrade write: the schema package of this
// koldstart's release. A dev build has no release to point at, so it is the schema the build
// embeds, extracted into the cache koldstart downloads pkl into, under a directory named by a
// hash of its files.
func Current() (Dependency, error) {
	if v, ok := ReleaseVersion(version.Current().Version); ok {
		return Release(v), nil
	}
	cache, err := pkl.CacheDir()
	if err != nil {
		return Dependency{}, err
	}
	return Extract(filepath.Join(cache, "schema"))
}

// ReleaseVersion returns the version of the schema package released with the koldstart of
// version v, and false for a dev build, which has none.
func ReleaseVersion(v string) (string, bool) {
	m := releasePattern.FindStringSubmatch(v)
	if m == nil {
		return "", false
	}
	return m[1], true
}

// Hash is a hash of the embedded schema, which names the directory it is extracted into.
func Hash() (string, error) {
	h := sha256.New()
	for _, name := range modules {
		data, err := fs.ReadFile(Files, name)
		if err != nil {
			return "", err
		}
		h.Write([]byte(name + "\x00"))
		h.Write(data)
		h.Write([]byte("\x00"))
	}
	return hex.EncodeToString(h.Sum(nil))[:16], nil
}

// Extract writes the embedded schema, a package with its PklProject, into a directory of parent
// named by its hash, unless it is there already, and returns it as a local dependency.
func Extract(parent string) (Dependency, error) {
	hash, err := Hash()
	if err != nil {
		return Dependency{}, err
	}
	dir := filepath.Join(parent, hash)
	if _, err := os.Stat(filepath.Join(dir, Package)); err == nil {
		return Dependency{Dir: dir}, nil
	}
	if err := os.MkdirAll(parent, 0o750); err != nil {
		return Dependency{}, fmt.Errorf("creating %s for the schema: %w: set XDG_CACHE_HOME to a directory you can write to", parent, err)
	}
	tmp, err := os.MkdirTemp(parent, hash+"-*")
	if err != nil {
		return Dependency{}, fmt.Errorf("extracting the schema into %s: %w", parent, err)
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	if err := extractTo(tmp); err != nil {
		return Dependency{}, err
	}
	if err := os.Rename(tmp, dir); err != nil {
		if _, statErr := os.Stat(filepath.Join(dir, Package)); statErr == nil {
			return Dependency{Dir: dir}, nil
		}
		return Dependency{}, fmt.Errorf("extracting the schema into %s: %w", dir, err)
	}
	return Dependency{Dir: dir}, nil
}

// extractTo writes the files of the embedded schema into dir.
func extractTo(dir string) error {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("extracting the schema into %s: %w", dir, err)
	}
	for _, name := range modules {
		data, err := fs.ReadFile(Files, name)
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
			return fmt.Errorf("extracting the schema into %s: %w", dir, err)
		}
	}
	return nil
}

// fullURI is a module of the released schema package named by its full uri, as a koldstart.pkl
// without a PklProject does.
var fullURI = regexp.MustCompile(`"package://github\.com/kontrolplane/koldstart/releases/download/v[^/"]+/koldstart@[^#"]+#/([A-Za-z]+\.pkl)"`)

// Local writes source as the koldstart.pkl of a project in a new directory, whose koldstart
// dependency is the schema built into koldstart, extracted beside it, so it evaluates offline and
// without the released package. A full package uri of the schema, as a file without a PklProject
// has, is pointed at that dependency too. It returns the path of the file, and a function that
// removes the directory.
func Local(source []byte) (path string, cleanup func(), err error) {
	dir, err := os.MkdirTemp("", "koldstart-schema-")
	if err != nil {
		return "", nil, fmt.Errorf("making a directory to evaluate in: %w", err)
	}
	cleanup = func() { _ = os.RemoveAll(dir) }
	if err := extractTo(filepath.Join(dir, "schema")); err != nil {
		cleanup()
		return "", nil, err
	}
	project := filepath.Join(dir, "project")
	if err := os.MkdirAll(project, 0o750); err != nil {
		cleanup()
		return "", nil, err
	}
	text := Dependency{Dir: "../schema"}.PklProject()
	if err := os.WriteFile(filepath.Join(project, pkl.ProjectFile), []byte(text), 0o600); err != nil {
		cleanup()
		return "", nil, err
	}
	path = filepath.Join(project, "koldstart.pkl")
	source = fullURI.ReplaceAll(source, []byte(`"@`+pkl.Dependency+`/$1"`))
	if err := os.WriteFile(path, source, 0o600); err != nil {
		cleanup()
		return "", nil, err
	}
	return path, cleanup, nil
}

// entryPattern finds the koldstart dependency in a PklProject: a package by its uri, or a local
// one by the import of its PklProject.
var entryPattern = regexp.MustCompile(`\["koldstart"\]\s*(?:\{\s*uri\s*=\s*"([^"]*)"\s*\}|=\s*import\("([^"]*)"\))`)

// dependenciesPattern is the start of a PklProject's dependencies.
var dependenciesPattern = regexp.MustCompile(`(?m)^dependencies\s*\{[ \t]*\n?`)

// ErrNoDependency is a PklProject without a koldstart dependency.
var ErrNoDependency = errors.New("no koldstart dependency")

// ReadDependency returns the koldstart dependency a PklProject declares, ErrNoDependency when it
// declares none, or an error when it declares one koldstart cannot read.
func ReadDependency(text string) (Dependency, error) {
	m := entryPattern.FindStringSubmatchIndex(text)
	if m == nil {
		if strings.Contains(text, `["koldstart"]`) {
			return Dependency{}, errors.New(`the koldstart dependency is neither ["koldstart"] { uri = "package://…" } nor ["koldstart"] = import("…/PklProject")`)
		}
		return Dependency{}, ErrNoDependency
	}
	if m[2] >= 0 {
		uri := text[m[2]:m[3]]
		at := strings.LastIndex(uri, "@")
		if at < 0 {
			return Dependency{}, fmt.Errorf("the koldstart dependency %s has no version after an @", uri)
		}
		return Release(uri[at+1:]), nil
	}
	return Dependency{Dir: strings.TrimSuffix(strings.TrimSuffix(text[m[4]:m[5]], Package), "/")}, nil
}

// SetDependency returns a PklProject with its koldstart dependency set to d: in place of the one
// it declares, else added to its dependencies, else in dependencies of its own.
func SetDependency(text string, d Dependency) (string, error) {
	if _, err := ReadDependency(text); err != nil && !errors.Is(err, ErrNoDependency) {
		return "", err
	}
	if m := entryPattern.FindStringIndex(text); m != nil {
		return text[:m[0]] + d.Entry() + text[m[1]:], nil
	}
	if m := dependenciesPattern.FindStringIndex(text); m != nil {
		return text[:m[1]] + "  " + d.Entry() + "\n" + text[m[1]:], nil
	}
	if text != "" && !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	return text + "\ndependencies {\n  " + d.Entry() + "\n}\n", nil
}

// WriteProject makes the PklProject in dir depend on d: it writes one when there is none, adds d
// to one without a koldstart dependency, and keeps one with a koldstart dependency as it is. It
// reports whether it wrote the file.
//
//nolint:gosec // the PklProject of a project, which everyone reads
func WriteProject(dir string, d Dependency) (bool, error) {
	path := filepath.Join(dir, Package)
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		if err := os.WriteFile(path, []byte(d.PklProject()), 0o644); err != nil {
			return false, fmt.Errorf("writing %s: %w", path, err)
		}
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("reading %s: %w", path, err)
	}
	_, err = ReadDependency(string(data))
	switch {
	case err == nil:
		return false, nil
	case !errors.Is(err, ErrNoDependency):
		return false, fmt.Errorf("reading %s: %w", path, err)
	}
	text, err := SetDependency(string(data), d)
	if err != nil {
		return false, fmt.Errorf("reading %s: %w", path, err)
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		return false, fmt.Errorf("writing %s: %w", path, err)
	}
	return true, nil
}

// Compare compares two release versions, -1 when a is older than b, 0 when they are the same and
// 1 when a is newer. A prerelease is older than its release.
func Compare(a, b string) int {
	core := func(v string) ([3]int, string) {
		main, pre, _ := strings.Cut(v, "-")
		var n [3]int
		for i, part := range strings.SplitN(main, ".", 3) {
			n[i], _ = strconv.Atoi(part)
		}
		return n, pre
	}
	an, ap := core(a)
	bn, bp := core(b)
	for i := range an {
		if an[i] != bn[i] {
			if an[i] < bn[i] {
				return -1
			}
			return 1
		}
	}
	switch {
	case ap == bp:
		return 0
	case ap == "":
		return 1
	case bp == "":
		return -1
	}
	return comparePre(ap, bp)
}

// comparePre compares two prereleases, such as rc.1 and rc.2, field by field: numbers as numbers
// and words as words.
func comparePre(a, b string) int {
	af, bf := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(af) && i < len(bf); i++ {
		an, aErr := strconv.Atoi(af[i])
		bn, bErr := strconv.Atoi(bf[i])
		switch {
		case aErr == nil && bErr == nil && an != bn:
			if an < bn {
				return -1
			}
			return 1
		case af[i] != bf[i]:
			return strings.Compare(af[i], bf[i])
		}
	}
	switch {
	case len(af) < len(bf):
		return -1
	case len(af) > len(bf):
		return 1
	}
	return 0
}
