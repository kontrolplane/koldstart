// Package pkl finds or downloads the pkl binary, and evaluates configuration with it.
package pkl

import (
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Version is the pkl koldstart downloads when none is installed.
const Version = "0.32.2"

// minimum is the oldest pkl on the PATH koldstart uses; an older one is ignored.
var minimum = [3]int{0, 30, 0}

// EnvBinary names a pkl binary to use instead of finding or downloading one.
const EnvBinary = "KOLDSTART_PKL"

// downloadURL is where the release binaries of Version are.
var downloadURL = "https://github.com/apple/pkl/releases/download/" + Version + "/"

// checksums are the sha256 of the release binaries of Version, as github lists them. They are
// pinned here, not fetched, so a changed release cannot pass for the real one.
var checksums = map[string]string{
	"pkl-macos-aarch64":      "466952f0b30c93dfa36393a5b3695159917ce4291e3c936a18a0a240cfbe98e9",
	"pkl-macos-amd64":        "3c4c914a75210d3f3292740b4a8d447f9f832e81b60720bec57221e884717eee",
	"pkl-linux-aarch64":      "e60744e17d4ddc0257a35260998b5f98387b5d6a5e868317cccfbebe8d962ae3",
	"pkl-linux-amd64":        "874c75484c79add77fb2346ea4ab2e8875eea35fb707894bf5457a2df5d03def",
	"pkl-alpine-linux-amd64": "b64816272feb4d2d3010587a62875d53ff75a1081c13d5f3436fefdd537b0543",
}

// asset returns the name of the release binary for goos and goarch, musl telling a linux with
// musl libc, such as alpine, from one with glibc.
func asset(goos, goarch string, musl bool) (string, error) {
	const amd64, linux = "amd64", "linux"
	arch := map[string]string{amd64: amd64, "arm64": "aarch64"}[goarch]
	switch {
	case arch == "":
		return "", fmt.Errorf("pkl has no build for %s/%s: install pkl and set %s", goos, goarch, EnvBinary)
	case goos == "darwin":
		return "pkl-macos-" + arch, nil
	case goos == linux && musl && arch == amd64:
		return "pkl-alpine-linux-amd64", nil
	case goos == linux && musl:
		return "", fmt.Errorf("pkl has no build for musl on %s: install pkl and set %s", goarch, EnvBinary)
	case goos == linux:
		return "pkl-linux-" + arch, nil
	}
	return "", fmt.Errorf("pkl has no build for %s/%s: install pkl and set %s", goos, goarch, EnvBinary)
}

// isMusl reports whether this linux runs musl libc.
func isMusl() bool {
	matches, _ := filepath.Glob("/lib/ld-musl-*")
	return len(matches) > 0
}

// CacheDir is where koldstart keeps what it downloads or extracts: $XDG_CACHE_HOME/koldstart, or
// ~/.cache/koldstart.
func CacheDir() (string, error) {
	if dir := os.Getenv("XDG_CACHE_HOME"); dir != "" {
		return filepath.Join(dir, "koldstart"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("finding the home directory for the cache: %w", err)
	}
	return filepath.Join(home, ".cache", "koldstart"), nil
}

// Find returns the pkl binary to use: $KOLDSTART_PKL, else a pkl on the PATH that is new enough,
// else the pinned download in the cache, downloading it first when it is not there yet.
func Find(ctx context.Context) (string, error) {
	if path, err := Installed(ctx); err != nil || path != "" {
		return path, err
	}
	dir, err := CacheDir()
	if err != nil {
		return "", err
	}
	name, err := asset(runtime.GOOS, runtime.GOARCH, runtime.GOOS == "linux" && isMusl())
	if err != nil {
		return "", err
	}
	return download(ctx, http.DefaultClient, downloadURL, dir, name)
}

// Installed returns the pkl binary Find would use without downloading one: $KOLDSTART_PKL, else
// a pkl on the PATH that is new enough, else the pinned download already in the cache. It
// returns "" when there is none of them.
//
//nolint:gosec // the user names the binary in $KOLDSTART_PKL
func Installed(ctx context.Context) (string, error) {
	if path := os.Getenv(EnvBinary); path != "" {
		if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
			return "", fmt.Errorf("%s is %s, which does not exist: set it to a pkl binary, or unset it", EnvBinary, path)
		} else if err != nil {
			return "", fmt.Errorf("%s is %s: %w", EnvBinary, path, err)
		}
		return path, nil
	}
	if path, err := exec.LookPath("pkl"); err == nil {
		if v, err := binaryVersion(ctx, path); err == nil && !older(v, minimum) {
			return path, nil
		}
	}
	dir, err := CacheDir()
	if err != nil {
		return "", err
	}
	if path := cachedBinary(dir); fileExists(path) {
		return path, nil
	}
	return "", nil
}

// cachedBinary is where the pinned pkl is kept in the cache dir.
func cachedBinary(dir string) string {
	return filepath.Join(dir, "pkl", Version, "pkl")
}

// fileExists reports whether path is a file that can be read.
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// Download fetches the pinned pkl built for glibc on goos and goarch into dir, checked against
// its pinned checksum, and returns its path. The image build uses it for the platform it builds
// for.
func Download(ctx context.Context, dir, goos, goarch string) (string, error) {
	name, err := asset(goos, goarch, false)
	if err != nil {
		return "", err
	}
	return download(ctx, http.DefaultClient, downloadURL, dir, name)
}

// installHint says how to give koldstart a pkl when it cannot download one.
func installHint() string {
	return fmt.Sprintf("install pkl %d.%d or newer on the PATH, or set %s to a pkl binary", minimum[0], minimum[1], EnvBinary)
}

// download fetches the release binary name from base into dir, unless it is there already, and
// checks it against its pinned checksum before it can be run.
//
//nolint:gosec // the binary has to be executable
func download(ctx context.Context, client *http.Client, base, dir, name string) (string, error) {
	want, ok := checksums[name]
	if !ok {
		return "", fmt.Errorf("no checksum for %s", name)
	}
	path := cachedBinary(dir)
	if fileExists(path) {
		return path, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return "", fmt.Errorf("creating the cache for pkl: %w: set XDG_CACHE_HOME to a directory you can write to", err)
	}

	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+name, nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("downloading pkl %s: %w\ncheck this machine is online, or %s", Version, err, installHint())
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("downloading pkl %s from %s: %s\n%s", Version, base+name, resp.Status, installHint())
	}

	tmp, err := os.CreateTemp(filepath.Dir(path), "pkl-*")
	if err != nil {
		return "", fmt.Errorf("creating the cache for pkl: %w", err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	hash := sha256.New()
	if _, err := io.Copy(io.MultiWriter(tmp, hash), resp.Body); err != nil {
		_ = tmp.Close()
		return "", fmt.Errorf("downloading pkl %s: %w\ncheck this machine is online, or %s", Version, err, installHint())
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if got := hex.EncodeToString(hash.Sum(nil)); got != want {
		return "", fmt.Errorf("pkl %s from %s has sha256 %s, want %s: not using it\ntry again, or %s", Version, base+name, got, want, installHint())
	}
	if err := os.Chmod(tmp.Name(), 0o755); err != nil {
		return "", err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return "", fmt.Errorf("moving pkl into the cache: %w", err)
	}
	return path, nil
}

var versionPattern = regexp.MustCompile(`Pkl (\d+)\.(\d+)\.(\d+)`)

// binaryVersion runs pkl --version, which prints a line such as "Pkl 0.32.2 (macOS 15.0, native)".
func binaryVersion(ctx context.Context, path string) ([3]int, error) {
	out, err := exec.CommandContext(ctx, path, "--version").Output()
	if err != nil {
		return [3]int{}, err
	}
	return parseVersion(string(out))
}

func parseVersion(s string) ([3]int, error) {
	m := versionPattern.FindStringSubmatch(s)
	if m == nil {
		return [3]int{}, fmt.Errorf("no pkl version in %q", strings.TrimSpace(s))
	}
	var v [3]int
	for i := range v {
		v[i], _ = strconv.Atoi(m[i+1])
	}
	return v, nil
}

func older(v, than [3]int) bool {
	for i := range v {
		if v[i] != than[i] {
			return v[i] < than[i]
		}
	}
	return false
}

// Evaluator evaluates pkl modules with one binary.
type Evaluator struct {
	Binary string
	// Notes is where a note goes when a project is resolved before it is evaluated; nil writes
	// none.
	Notes io.Writer
	// Name is the name of the command, for errors that say what to run; koldstart when empty.
	Name string
}

// EvalJSON evaluates the module at path with the external properties props, and returns its
// output as json. A module in a project, under a PklProject at or above its directory, is
// evaluated in that project, which is resolved first when its PklProject.deps.json is missing or
// older than its PklProject. A module in none is evaluated without one, whatever directory pkl
// runs in.
//
//nolint:gosec // runs our pkl on the user's module
func (e Evaluator) EvalJSON(ctx context.Context, path string, props map[string]string) ([]byte, error) {
	module := realPath(path)
	project, err := FindProject(module)
	if err != nil {
		return nil, err
	}
	args := make([]string, 0, 6+2*len(props))
	args = append(args, "eval", "--format", "json")
	if project == "" {
		args = append(args, "--no-project")
	} else {
		if err := e.resolveStale(ctx, project); err != nil {
			return nil, err
		}
		args = append(args, "--project-dir", project)
	}
	for _, k := range slices.Sorted(maps.Keys(props)) {
		args = append(args, "--property", k+"="+props[k])
	}
	args = append(args, module)
	cmd := exec.CommandContext(ctx, e.Binary, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && stderr.Len() > 0 {
			return nil, &EvalError{Path: path, Message: report(stderr.String(), path), Project: project, name: e.name()}
		}
		return nil, fmt.Errorf("running %s on %s: %w\ncheck it runs, or set %s to another pkl binary", e.Binary, path, err, EnvBinary)
	}
	return stdout.Bytes(), nil
}

// EvalError is a module that pkl could not evaluate; Message is pkl's own report, which points
// at the line.
type EvalError struct {
	Path    string
	Message string
	// Project is the directory of the project it was evaluated in, "" for none.
	Project string
	// name is the command's name, for what to run.
	name string
}

func (e *EvalError) Error() string {
	dependency := "@" + Dependency + "/"
	switch {
	case strings.Contains(e.Message, "Cannot resolve dependency because there is no project found") && strings.Contains(e.Message, dependency):
		return fmt.Sprintf("%s imports %s, which needs a %s beside it or above it that names the schema package: run %s upgrade to write one\n\n%s", e.Path, dependency, ProjectFile, cmp.Or(e.name, "koldstart"), e.Message)
	case strings.Contains(e.Message, "Cannot find a dependency named `"+Dependency+"`"):
		return fmt.Sprintf("%s imports %s, but %s has no %s dependency: run %s upgrade to add it\n\n%s", e.Path, dependency, shown(filepath.Join(e.Project, ProjectFile)), Dependency, cmp.Or(e.name, "koldstart"), e.Message)
	}
	if e.downloadFailed() {
		return fmt.Sprintf("pkl cannot evaluate %s: a package it uses did not download; check this machine is online, and that the version it names exists\n\n%s", e.Path, e.Message)
	}
	return fmt.Sprintf("pkl cannot evaluate %s: fix it where pkl points\n\n%s", e.Path, e.Message)
}

// downloadFailed reports whether pkl failed to fetch a package or module, rather than finding
// the module wrong.
func (e *EvalError) downloadFailed() bool {
	for _, marker := range []string{"when making request", "Error connecting to host", "UnknownHostException"} {
		if strings.Contains(e.Message, marker) {
			return true
		}
	}
	return false
}

// stdlibFrame marks a frame of pkl's report in pkl's own standard library, which says nothing of
// where the module is wrong.
const stdlibFrame = "(https://github.com/apple/pkl/blob/"

// report tidies pkl's report of a module it could not evaluate: without its "Pkl Error" banner
// or the frames in pkl's standard library, and with the files of the module's directory named
// as path names the module, not as absolute file uris.
func report(stderr, path string) string {
	lines := strings.Split(strings.TrimSpace(stderr), "\n")
	if len(lines) > 0 && strings.Contains(lines[0], "Pkl Error") {
		lines = lines[1:]
	}
	blocks := strings.Split(strings.Join(lines, "\n"), "\n\n")
	kept := blocks[:0]
	for _, block := range blocks {
		if !strings.Contains(block, stdlibFrame) {
			kept = append(kept, block)
		}
	}
	out := strings.TrimSpace(strings.Join(kept, "\n\n"))
	if abs, err := filepath.Abs(path); err == nil {
		dir := filepath.Dir(path)
		shown := ""
		if dir != "." {
			shown = dir + string(filepath.Separator)
		}
		for _, d := range []string{filepath.Dir(realPath(path)), filepath.Dir(abs)} {
			out = strings.ReplaceAll(out, "file://"+d+string(filepath.Separator), shown)
		}
	}
	return out
}

// realPath is path made absolute with the links in its directory followed, as pkl sees it: a
// module and its project must agree, or pkl finds the module outside the project. It is path
// made absolute when the links cannot be followed.
func realPath(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		return path
	}
	dir, err := filepath.EvalSymlinks(filepath.Dir(abs))
	if err != nil {
		return abs
	}
	return filepath.Join(dir, filepath.Base(abs))
}
