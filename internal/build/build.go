// Package build turns a function's code into the zip lambda runs: node bundled with esbuild, go
// cross-compiled to a bootstrap binary, rust built into one with cargo lambda, an executable
// built elsewhere zipped as one, python with its dependencies installed by uv; and a container
// image function into its image.
package build

import (
	"context"
	"crypto/sha256"
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
	// SHA256 is the hex sha256 of the zip, which names it in the state bucket, or of what a
	// container image is built from, which tags it.
	SHA256 string
	// Image says the function is a container image, which Path and Size say nothing of.
	Image bool
}

// Target is the platform a function is built for.
type Target struct {
	GOOS, GOARCH string
}

// Linux is the os lambda runs functions on, and containers run.
const Linux = "linux"

// goARM64 and goAMD64 are the architectures lambda runs functions on, as go names them.
const (
	goARM64 = "arm64"
	goAMD64 = "amd64"
)

// bootstrapFile is the file of the zip the provided.al2023 runtime runs.
const bootstrapFile = "bootstrap"

// LambdaTarget returns the platform lambda runs a function of the architecture on.
func LambdaTarget(architecture string) Target {
	if architecture == config.ArchX8664 {
		return Target{GOOS: Linux, GOARCH: goAMD64}
	}
	return Target{GOOS: Linux, GOARCH: goARM64}
}

// Builder builds the functions of the project in Root.
type Builder struct {
	// Root is the project directory, absolute.
	Root  string
	Stage string
}

// Build builds the function name for lambda and writes its zip under Dir. For a container image it
// hashes what the image is built from; BuildImage builds it.
func (b Builder) Build(ctx context.Context, name string, fn config.Function) (Archive, error) {
	if fn.IsImage() {
		sha, err := b.imageHash(fn, LambdaTarget(fn.Architecture))
		if err != nil {
			return Archive{}, fmt.Errorf("building %s: %w", name, err)
		}
		return Archive{Function: name, SHA256: sha, Image: true}, nil
	}
	files, err := b.Files(ctx, name, fn, LambdaTarget(fn.Architecture))
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
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return Archive{}, fmt.Errorf("writing %s: %w", path, err)
	}
	return Archive{
		Function: name,
		Path:     path,
		Size:     int64(len(data)),
		SHA256:   hex.EncodeToString(sum[:]),
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
	case fn.IsProvided():
		file, err := b.provided(ctx, name, fn, target)
		if err != nil {
			return nil, fmt.Errorf("building %s: %w", name, err)
		}
		return []File{file}, nil
	case fn.IsPython():
		files, err := b.python(ctx, fn, target)
		if err != nil {
			return nil, fmt.Errorf("building %s: %w", name, err)
		}
		return files, nil
	default:
		return nil, fmt.Errorf("building %s: runtime %q is not supported", name, fn.Runtime)
	}
}
