package local

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/kontrolplane/koldstart/internal/build"
	"github.com/kontrolplane/koldstart/internal/config"
)

//go:embed runtime.cjs
var nodeRuntime []byte

// runtimeFile is the name the node runtime client is written under, next to the bundle.
const runtimeFile = ".koldstart-runtime.cjs"

// Function is a function to run on this machine.
type Function struct {
	// Name is its name in the configuration, LambdaName its name in lambda.
	Name       string
	LambdaName string
	ARN        string
	Config     config.Function
	Region     string
	// Env are its environment variables, resolved against the deployed stage, and the aws
	// credentials it runs with.
	Env map[string]string
	// Output receives what it writes.
	Output io.Writer
}

// Runner runs functions of the project in Root, built under Dir.
type Runner struct {
	Root  string
	Stage string
	Dir   string
}

// Build builds f for this machine into its directory under Dir, replacing an earlier build.
func (l Runner) Build(ctx context.Context, f Function) error {
	files, err := build.Builder{Root: l.Root, Stage: l.Stage}.Files(ctx, f.Name, f.Config, build.Target{GOOS: runtime.GOOS, GOARCH: runtime.GOARCH})
	if err != nil {
		return err
	}
	dir := l.dir(f)
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	if f.Config.IsNode() {
		files = append(files, build.File{Name: runtimeFile, Data: nodeRuntime})
	}
	for _, file := range files {
		p := filepath.Join(dir, filepath.FromSlash(file.Name))
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			return err
		}
		mode := os.FileMode(0o600)
		if file.Executable {
			mode = 0o700
		}
		if err := os.WriteFile(p, file.Data, mode); err != nil {
			return fmt.Errorf("writing %s: %w", p, err)
		}
	}
	return nil
}

func (l Runner) dir(f Function) string {
	return filepath.Join(l.Dir, f.Name)
}

// Runtime returns the runtime api and process for f, built with Build first. It warns when the
// node on this machine is not the one the function runs on in lambda.
//
//nolint:gosec // runs the user's own function
func (l Runner) Runtime(f Function) (*Runtime, error) {
	dir := l.dir(f)
	var command func(addr string) *exec.Cmd
	switch {
	case f.Config.IsNode():
		node, err := exec.LookPath("node")
		if err != nil {
			return nil, errors.New("node functions need node on the PATH to run here: https://nodejs.org/")
		}
		if warning := nodeMismatch(node, f.Config.Runtime); warning != "" {
			_, _ = fmt.Fprintln(f.Output, warning)
		}
		module, export, err := build.SplitHandler(f.Config.Handler)
		if err != nil {
			return nil, err
		}
		bundle := filepath.Join(dir, filepath.FromSlash(module)+".js")
		command = func(addr string) *exec.Cmd {
			cmd := exec.Command(node, filepath.Join(dir, runtimeFile), bundle, export)
			cmd.Dir, cmd.Env = dir, l.env(f, dir, addr)
			return cmd
		}
	case f.Config.IsGo():
		command = func(addr string) *exec.Cmd {
			cmd := exec.Command(filepath.Join(dir, "bootstrap"))
			cmd.Dir, cmd.Env = dir, l.env(f, dir, addr)
			return cmd
		}
	default:
		return nil, fmt.Errorf("runtime %q cannot run here", f.Config.Runtime)
	}
	return &Runtime{
		Name: f.Name, ARN: f.ARN,
		Timeout: time.Duration(f.Config.Timeout) * time.Second,
		Command: command, Output: f.Output,
	}, nil
}

// Start builds f, and starts its runtime api, ready to invoke.
func (l Runner) Start(ctx context.Context, f Function) (*Runtime, error) {
	if err := l.Build(ctx, f); err != nil {
		return nil, err
	}
	r, err := l.Runtime(f)
	if err != nil {
		return nil, err
	}
	return r, r.Start()
}

// env is what the process gets: what lambda sets, the function's own variables and the
// credentials, and from this machine only PATH and HOME, so it runs as it would in lambda.
func (l Runner) env(f Function, dir, addr string) []string {
	vars := map[string]string{
		"PATH":                            os.Getenv("PATH"),
		"HOME":                            os.Getenv("HOME"),
		"TZ":                              ":UTC",
		"AWS_LAMBDA_RUNTIME_API":          addr,
		"AWS_LAMBDA_FUNCTION_NAME":        f.LambdaName,
		"AWS_LAMBDA_FUNCTION_MEMORY_SIZE": strconv.Itoa(f.Config.Memory),
		"AWS_LAMBDA_FUNCTION_VERSION":     "$LATEST",
		"AWS_LAMBDA_LOG_GROUP_NAME":       "/aws/lambda/" + f.LambdaName,
		"AWS_LAMBDA_LOG_STREAM_NAME":      localName,
		"AWS_EXECUTION_ENV":               "AWS_Lambda_" + f.Config.Runtime,
		"AWS_REGION":                      f.Region,
		"AWS_DEFAULT_REGION":              f.Region,
		"LAMBDA_TASK_ROOT":                dir,
		"_HANDLER":                        f.Config.Handler,
	}
	maps.Copy(vars, f.Env)
	out := make([]string, 0, len(vars))
	for _, k := range slices.Sorted(maps.Keys(vars)) {
		out = append(out, k+"="+vars[k])
	}
	return out
}

var nodeMajor = regexp.MustCompile(`^v(\d+)\.`)

// nodeMismatch warns when the node at path is another major version than the runtime runs.
//
//nolint:gosec // the node on the PATH
func nodeMismatch(path, runtimeName string) string {
	out, err := exec.Command(path, "--version").Output()
	if err != nil {
		return ""
	}
	m := nodeMajor.FindStringSubmatch(strings.TrimSpace(string(out)))
	want := strings.TrimSuffix(strings.TrimPrefix(runtimeName, "nodejs"), ".x")
	if m == nil || m[1] == want {
		return ""
	}
	return fmt.Sprintf("note: node here is %s, the function runs on node %s in lambda", strings.TrimSpace(string(out)), want)
}
