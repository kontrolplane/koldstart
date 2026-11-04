// Package build turns a function's code into the zip lambda runs: node bundled with esbuild, go
// cross-compiled to a bootstrap binary.
package build

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"

	"github.com/kontrolplane/koldstart/internal/config"
)

// Dir is where builds go, inside the project: .koldstart/build/<stage>/<function>.zip.
const Dir = ".koldstart/build"

// Archive is a function's built zip.
type Archive struct {
	Function string
	Path     string
	Size     int64
	// SHA256 is the hex sha256 of the zip, which names it in the state bucket.
	SHA256 string
	// CodeSHA256 is the same hash as lambda reports it, in base64.
	CodeSHA256 string
}

// Target is the platform a function is built for.
type Target struct {
	GOOS, GOARCH string
}

// Lambda returns the target lambda runs a function with architecture on.
func Lambda(architecture string) Target {
	if architecture == config.ArchX8664 {
		return Target{GOOS: "linux", GOARCH: "amd64"}
	}
	return Target{GOOS: "linux", GOARCH: "arm64"}
}

// Builder builds the functions of the project in Root.
type Builder struct {
	Root  string // the project directory, absolute
	Stage string
}

// Build builds the function name for lambda and writes its zip under Dir.
func (b Builder) Build(ctx context.Context, name string, fn config.Function) (Archive, error) {
	files, err := b.Files(ctx, name, fn, Lambda(fn.Architecture))
	if err != nil {
		return Archive{}, err
	}
	data, err := Zip(files)
	if err != nil {
		return Archive{}, fmt.Errorf("packaging %s: %w", name, err)
	}
	sum := sha256.Sum256(data)
	path := filepath.Join(b.Root, Dir, b.Stage, name+".zip")
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return Archive{}, fmt.Errorf("creating %s: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil { //nolint:gosec // a zip anyone may read
		return Archive{}, fmt.Errorf("writing %s: %w", path, err)
	}
	return Archive{
		Function:   name,
		Path:       path,
		Size:       int64(len(data)),
		SHA256:     hex.EncodeToString(sum[:]),
		CodeSHA256: base64.StdEncoding.EncodeToString(sum[:]),
	}, nil
}

// Files builds the function name for target and returns what goes in its zip.
func (b Builder) Files(ctx context.Context, name string, fn config.Function, target Target) ([]File, error) {
	switch {
	case fn.IsNode():
		files, err := b.node(ctx, fn)
		if err != nil {
			return nil, fmt.Errorf("building %s: %w", name, err)
		}
		return files, nil
	case fn.IsGo():
		file, err := b.golang(ctx, fn, target)
		if err != nil {
			return nil, fmt.Errorf("building %s: %w", name, err)
		}
		return []File{file}, nil
	default:
		return nil, fmt.Errorf("building %s: runtime %q is not supported", name, fn.Runtime)
	}
}
