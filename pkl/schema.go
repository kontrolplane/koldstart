// Package schema holds the pkl schema koldstart.pkl files amend, as this koldstart reads it, for
// checking a file without fetching the released package.
package schema

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Files are Project.pkl and kdst.pkl.
//
//go:embed Project.pkl kdst.pkl
var Files embed.FS

// The modules a koldstart.pkl amends and imports, by their file name in Files.
const (
	Project = "Project.pkl"
	Kdst    = "kdst.pkl"
)

// PackageURI is the uri of a module of the schema package released with koldstart version.
func PackageURI(version, module string) string {
	return "package://github.com/kontrolplane/koldstart/releases/download/v" + version + "/koldstart@" + version + "#/" + module
}

// Local writes source next to the schema built into koldstart, in a new directory, with the
// package uris of version pointed at the files there, so it evaluates without the released
// package. It returns the path of the file, and a function that removes the directory.
func Local(source []byte, version string) (path string, cleanup func(), err error) {
	dir, err := os.MkdirTemp("", "koldstart-schema-")
	if err != nil {
		return "", nil, fmt.Errorf("making a directory to evaluate in: %w", err)
	}
	cleanup = func() { _ = os.RemoveAll(dir) }
	text := string(source)
	for _, module := range []string{Project, Kdst} {
		data, err := fs.ReadFile(Files, module)
		if err != nil {
			cleanup()
			return "", nil, err
		}
		if err := os.WriteFile(filepath.Join(dir, module), data, 0o600); err != nil {
			cleanup()
			return "", nil, err
		}
		text = strings.ReplaceAll(text, `"`+PackageURI(version, module)+`"`, `"`+module+`"`)
	}
	path = filepath.Join(dir, "koldstart.pkl")
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		cleanup()
		return "", nil, err
	}
	return path, cleanup, nil
}
