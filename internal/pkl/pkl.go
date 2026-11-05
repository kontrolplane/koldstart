// Package pkl finds or downloads the pkl binary, and evaluates configuration with it.
package pkl

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
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

// cacheDir is where downloaded binaries go: $XDG_CACHE_HOME/koldstart, or ~/.cache/koldstart.
func cacheDir() (string, error) {
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
//
//nolint:gosec // the user names the binary in $KOLDSTART_PKL
func Find(ctx context.Context) (string, error) {
	if path := os.Getenv(EnvBinary); path != "" {
		if _, err := os.Stat(path); err != nil {
			return "", fmt.Errorf("%s=%s: %w", EnvBinary, path, err)
		}
		return path, nil
	}
	if path, err := exec.LookPath("pkl"); err == nil {
		if v, err := binaryVersion(ctx, path); err == nil && !older(v, minimum) {
			return path, nil
		}
	}
	dir, err := cacheDir()
	if err != nil {
		return "", err
	}
	name, err := asset(runtime.GOOS, runtime.GOARCH, runtime.GOOS == "linux" && isMusl())
	if err != nil {
		return "", err
	}
	return download(ctx, http.DefaultClient, downloadURL, dir, name)
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

// download fetches the release binary name from base into dir, unless it is there already, and
// checks it against its pinned checksum before it can be run.
//
//nolint:gosec // the binary has to be executable
func download(ctx context.Context, client *http.Client, base, dir, name string) (string, error) {
	want, ok := checksums[name]
	if !ok {
		return "", fmt.Errorf("no checksum for %s", name)
	}
	path := filepath.Join(dir, "pkl", Version, "pkl")
	if _, err := os.Stat(path); err == nil {
		return path, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return "", fmt.Errorf("creating the cache for pkl: %w", err)
	}

	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+name, nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("downloading pkl %s: %w", Version, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("downloading pkl %s from %s: %s", Version, base+name, resp.Status)
	}

	tmp, err := os.CreateTemp(filepath.Dir(path), "pkl-*")
	if err != nil {
		return "", fmt.Errorf("creating the cache for pkl: %w", err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	hash := sha256.New()
	if _, err := io.Copy(io.MultiWriter(tmp, hash), resp.Body); err != nil {
		_ = tmp.Close()
		return "", fmt.Errorf("downloading pkl %s: %w", Version, err)
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if got := hex.EncodeToString(hash.Sum(nil)); got != want {
		return "", fmt.Errorf("pkl %s from %s has sha256 %s, want %s: not using it", Version, base+name, got, want)
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
}

// EvalJSON evaluates the module at path with the external properties props, and returns its
// output as json.
//
//nolint:gosec // runs our pkl on the user's module
func (e Evaluator) EvalJSON(ctx context.Context, path string, props map[string]string) ([]byte, error) {
	args := make([]string, 0, 4+2*len(props))
	args = append(args, "eval", "--format", "json")
	for _, k := range slices.Sorted(maps.Keys(props)) {
		args = append(args, "--property", k+"="+props[k])
	}
	args = append(args, path)
	cmd := exec.CommandContext(ctx, e.Binary, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && stderr.Len() > 0 {
			return nil, &EvalError{Path: path, Message: strings.TrimSpace(stderr.String())}
		}
		return nil, fmt.Errorf("running pkl on %s: %w", path, err)
	}
	return stdout.Bytes(), nil
}

// EvalError is a module that pkl could not evaluate; Message is pkl's own report, which points
// at the line.
type EvalError struct {
	Path    string
	Message string
}

func (e *EvalError) Error() string {
	return fmt.Sprintf("evaluating %s:\n%s", e.Path, e.Message)
}
