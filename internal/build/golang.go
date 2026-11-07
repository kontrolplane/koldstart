package build

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
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
//
//nolint:gosec // go builds the user's own package
func (b Builder) golang(ctx context.Context, fn config.Function, target Target) (File, error) {
	goBin, err := exec.LookPath("go")
	if err != nil {
		return File{}, errors.New("go functions need go on the PATH: https://go.dev/dl/")
	}
	tmp, err := os.MkdirTemp("", "koldstart-go-")
	if err != nil {
		return File{}, fmt.Errorf("making a directory to build in: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	out := filepath.Join(tmp, bootstrapFile)

	args := append([]string{"build"}, goFlags...)
	args = append(args, "-o", out, fn.Handler)
	cmd := exec.CommandContext(ctx, goBin, args...)
	cmd.Dir = b.Root
	cmd.Env = append(os.Environ(), "GOOS="+target.GOOS, "GOARCH="+target.GOARCH, "CGO_ENABLED=0")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		switch {
		case strings.Contains(msg, "go.mod file not found"):
			return File{}, fmt.Errorf("go build %s:\n%s\nthere is no go module: run go mod init <module path> in %s", fn.Handler, msg, shownDir(b.Root))
		case msg != "":
			return File{}, fmt.Errorf("go build %s:\n%s", fn.Handler, msg)
		}
		return File{}, fmt.Errorf("go build %s: %w", fn.Handler, err)
	}
	data, err := fs.ReadFile(os.DirFS(tmp), bootstrapFile)
	if err != nil {
		return File{}, fmt.Errorf("reading the built %s: %w", fn.Handler, err)
	}
	return File{Name: bootstrapFile, Data: data, Executable: true}, nil
}
