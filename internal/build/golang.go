package build

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/kontrolplane/koldstart/internal/config"
)

// goFlags make the binary depend on the code alone: no paths of the machine, no vcs stamp that
// changes with every commit, no build id. The same code and go version give the same bytes on a
// laptop and in ci, so the hash says whether the code changed.
var goFlags = []string{"-trimpath", "-buildvcs=false", "-tags", "lambda.norpc", "-ldflags", "-s -w -buildid="}

// golang cross-compiles the package in fn.Handler into a binary named bootstrap, which the
// provided.al2023 runtime runs.
func (b Builder) golang(ctx context.Context, fn config.Function, target Target) (File, error) {
	goBin, err := exec.LookPath("go")
	if err != nil {
		return File{}, errors.New("go functions need go on the PATH: https://go.dev/dl/")
	}
	tmp, err := os.MkdirTemp("", "koldstart-go-")
	if err != nil {
		return File{}, err
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	out := filepath.Join(tmp, "bootstrap")

	args := append([]string{"build"}, goFlags...)
	args = append(args, "-o", out, fn.Handler)
	cmd := exec.CommandContext(ctx, goBin, args...) //nolint:gosec // the handler is the user's package
	cmd.Dir = b.Root
	cmd.Env = append(os.Environ(), "GOOS="+target.GOOS, "GOARCH="+target.GOARCH, "CGO_ENABLED=0")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return File{}, fmt.Errorf("go build %s:\n%s", fn.Handler, msg)
		}
		return File{}, fmt.Errorf("go build %s: %w", fn.Handler, err)
	}
	data, err := os.ReadFile(out) //nolint:gosec // our own temporary file
	if err != nil {
		return File{}, err
	}
	return File{Name: "bootstrap", Data: data, Executable: true}, nil
}
