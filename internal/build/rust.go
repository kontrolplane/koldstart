package build

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/kontrolplane/koldstart/internal/config"
)

// cargoLambdaInstall is what a build for lambda says without cargo-lambda on the PATH.
const cargoLambdaInstall = "rust functions need cargo-lambda on the PATH to build for lambda: brew install cargo-lambda/tap/cargo-lambda, pip3 install cargo-lambda, or see https://www.cargo-lambda.info/guide/installation.html"

// rust builds the crate in the directory fn.Handler names into its bootstrap. For linux, as
// lambda runs it, that is cargo lambda build --release with --arm64 or --x86-64 for the target's
// architecture. For another os, this machine when the function runs here, it is cargo build
// --release, as cargo lambda builds for linux alone. The bootstrap is the crate's one binary, or
// the one named after the function or its directory.
//
//nolint:gosec // reads the binary cargo built of the user's own crate
func (b Builder) rust(ctx context.Context, name string, fn config.Function, target Target) (File, error) {
	cargo, err := exec.LookPath("cargo")
	if err != nil {
		return File{}, errors.New("rust functions need cargo on the PATH: https://rustup.rs/")
	}
	out, err := os.MkdirTemp("", "koldstart-rust-")
	if err != nil {
		return File{}, fmt.Errorf("making a directory to build in: %w", err)
	}
	defer func() { _ = os.RemoveAll(out) }()
	dir := filepath.Join(b.Root, filepath.FromSlash(fn.Handler))
	var binaries map[string]string
	if target.GOOS == Linux {
		binaries, err = cargoLambda(ctx, cargo, dir, out, target)
	} else {
		binaries, err = cargoBuild(ctx, cargo, dir)
	}
	if err != nil {
		return File{}, fmt.Errorf("building the crate in %s: %w", fn.Handler, err)
	}
	binary, err := pickBinary(binaries, name, path.Base(path.Clean(fn.Handler)))
	if err != nil {
		return File{}, fmt.Errorf("the crate in %s %w", fn.Handler, err)
	}
	data, err := os.ReadFile(binaries[binary])
	if err != nil {
		return File{}, fmt.Errorf("reading the built %s: %w", binary, err)
	}
	if err := runs("the binary "+binary+" cargo built", data, fn.Architecture, target); err != nil {
		return File{}, err
	}
	return File{Name: bootstrapFile, Data: data, Executable: true}, nil
}

// cargoLambda builds the crate in dir for lambda on target into out, and returns its binaries by
// name: cargo lambda writes each to <out>/<name>/bootstrap.
//
//nolint:gosec // cargo builds the user's own crate
func cargoLambda(ctx context.Context, cargo, dir, out string, target Target) (map[string]string, error) {
	if _, err := exec.LookPath("cargo-lambda"); err != nil {
		return nil, errors.New(cargoLambdaInstall)
	}
	cmd := exec.CommandContext(ctx, cargo, "lambda", "build", "--release", cargoArch(target), "--lambda-dir", out)
	cmd.Dir = dir
	if err := runCargo(cmd, "cargo lambda build"); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(out)
	if err != nil {
		return nil, err
	}
	binaries := map[string]string{}
	for _, e := range entries {
		if p := filepath.Join(out, e.Name(), bootstrapFile); e.IsDir() && regular(p) {
			binaries[e.Name()] = p
		}
	}
	return binaries, nil
}

// cargoArch is the flag of cargo lambda build for the target's architecture.
func cargoArch(target Target) string {
	if target.GOARCH == goAMD64 {
		return "--x86-64"
	}
	return "--arm64"
}

// cargoBuild builds the crate in dir for this machine, and returns its binaries by name, as
// cargo's json messages give them.
//
//nolint:gosec // cargo builds the user's own crate
func cargoBuild(ctx context.Context, cargo, dir string) (map[string]string, error) {
	cmd := exec.CommandContext(ctx, cargo, "build", "--release", "--message-format=json-render-diagnostics")
	cmd.Dir = dir
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	if err := runCargo(cmd, "cargo build"); err != nil {
		return nil, err
	}
	binaries := map[string]string{}
	scanner := bufio.NewScanner(&stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		var msg struct {
			Reason string `json:"reason"`
			Target struct {
				Name string   `json:"name"`
				Kind []string `json:"kind"`
			} `json:"target"`
			Executable string `json:"executable"`
		}
		if json.Unmarshal(scanner.Bytes(), &msg) != nil || msg.Reason != "compiler-artifact" {
			continue
		}
		if msg.Executable != "" && slices.Contains(msg.Target.Kind, "bin") {
			binaries[msg.Target.Name] = msg.Executable
		}
	}
	return binaries, scanner.Err()
}

// runCargo runs cmd, and returns what cargo wrote to stderr when it fails.
func runCargo(cmd *exec.Cmd, what string) error {
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return fmt.Errorf("%s:\n%s", what, msg)
		}
		return fmt.Errorf("%s: %w", what, err)
	}
	return nil
}

// pickBinary is the binary of a crate that is a function's bootstrap: its one binary, or the one
// called one of names. The error completes "the crate in <dir>".
func pickBinary(binaries map[string]string, names ...string) (string, error) {
	all := slices.Sorted(maps.Keys(binaries))
	switch len(all) {
	case 0:
		return "", errors.New("builds no binary: give it a src/main.rs")
	case 1:
		return all[0], nil
	}
	for _, name := range names {
		if _, ok := binaries[name]; ok {
			return name, nil
		}
	}
	return "", fmt.Errorf("builds %s: name the function's binary %s, or give each function a crate of its own", list(all), names[0])
}

// list joins items as a sentence does, with and before the last.
func list(items []string) string {
	if len(items) < 2 {
		return strings.Join(items, "")
	}
	return strings.Join(items[:len(items)-1], ", ") + " and " + items[len(items)-1]
}

// regular reports whether file is a regular file.
func regular(file string) bool {
	info, err := os.Stat(file)
	return err == nil && info.Mode().IsRegular()
}
