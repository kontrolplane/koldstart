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
	"sync"
	"time"

	"github.com/kontrolplane/koldstart/internal/build"
	"github.com/kontrolplane/koldstart/internal/config"
)

//go:embed runtime.cjs
var nodeRuntime []byte

//go:embed runtime.py
var pythonRuntime []byte

// runtimeFile and pythonRuntimeFile are the names the runtime clients are written under, next to
// the function.
const (
	runtimeFile       = ".koldstart-runtime.cjs"
	pythonRuntimeFile = ".koldstart-runtime.py"
)

// commonJSPackage is the package.json written beside a node function's bundle. The bundle is
// commonjs in a .js file, as in lambda, where no package.json is beside it; here the project's own
// could be found above it instead, and with "type": "module" node would load the bundle as an es
// module and fail.
var commonJSPackage = []byte(`{"type": "commonjs"}` + "\n")

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

// Build builds f for this machine into its directory under Dir, replacing an earlier build. A
// container image is built into docker here, for this machine's architecture.
func (l Runner) Build(ctx context.Context, f Function) error {
	if f.Config.IsImage() {
		b := build.Builder{Root: l.Root, Stage: l.Stage}
		return b.BuildImage(ctx, f.Config, build.Target{GOOS: build.Linux, GOARCH: runtime.GOARCH}, localImage(f), false)
	}
	files, err := build.Builder{Root: l.Root, Stage: l.Stage}.Files(ctx, f.Name, f.Config, build.Target{GOOS: runtime.GOOS, GOARCH: runtime.GOARCH})
	if err != nil {
		return err
	}
	dir := l.dir(f)
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	switch {
	case f.Config.IsNode():
		files = append(files, build.File{Name: runtimeFile, Data: nodeRuntime}, build.File{Name: "package.json", Data: commonJSPackage})
	case f.Config.IsPython():
		files = append(files, build.File{Name: pythonRuntimeFile, Data: pythonRuntime})
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
// node on this machine is not the one the function runs on in lambda, and when the function has
// layers, which are not here.
//
//nolint:gosec // runs the user's own function
func (l Runner) Runtime(f Function) (*Runtime, error) {
	dir := l.dir(f)
	if note := layersMissing(len(f.Config.Layers)); note != "" {
		_, _ = fmt.Fprintln(f.Output, note)
	}
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
	case f.Config.IsProvided():
		command = func(addr string) *exec.Cmd {
			cmd := exec.Command(filepath.Join(dir, "bootstrap"))
			cmd.Dir, cmd.Env = dir, l.env(f, dir, addr)
			return cmd
		}
	case f.Config.IsPython():
		python, err := build.LocalPython(context.Background(), f.Config.Runtime)
		if err != nil {
			return nil, err
		}
		module, export, err := build.SplitHandler(f.Config.Handler)
		if err != nil {
			return nil, err
		}
		command = func(addr string) *exec.Cmd {
			cmd := exec.Command(python, filepath.Join(dir, pythonRuntimeFile), module, export)
			cmd.Dir, cmd.Env = dir, append(l.env(f, dir, addr), "PYTHONUNBUFFERED=1")
			return cmd
		}
	case f.Config.IsImage():
		return l.imageRuntime(f)
	default:
		return nil, fmt.Errorf("runtime %q cannot run here", f.Config.Runtime)
	}
	return &Runtime{
		Name: f.Name, ARN: f.ARN,
		Timeout: time.Duration(f.Config.Timeout) * time.Second,
		Command: command, Output: f.Output,
	}, nil
}

// localImage is the tag a function's image is built under to run here.
func localImage(f Function) string {
	return "koldstart-local/" + strings.ToLower(f.LambdaName) + ":latest"
}

// imageRuntime runs a container image function in docker, pointed at the runtime api here: the
// base images of lambda run their runtime client against AWS_LAMBDA_RUNTIME_API when it is set.
// On linux the container shares this machine's network; elsewhere it reaches it as
// host.docker.internal, and so does an emulator's endpoint on this machine.
//
//nolint:gosec // runs the user's own image
func (l Runner) imageRuntime(f Function) (*Runtime, error) {
	docker, err := exec.LookPath("docker")
	if err != nil {
		return nil, errors.New("container image functions need docker on the PATH to run here")
	}
	var mu sync.Mutex
	var container string
	command := func(addr string) *exec.Cmd {
		mu.Lock()
		defer mu.Unlock()
		container = "koldstart-" + strings.ReplaceAll(strings.ToLower(f.LambdaName), "_", "-") + "-" + strconv.FormatInt(time.Now().UnixNano(), 36)
		args := []string{"run", "--rm", "--name", container}
		if runtime.GOOS == build.Linux {
			args = append(args, "--network", "host")
		} else {
			addr = hostAddress(addr)
		}
		for _, kv := range l.imageEnv(f, addr) {
			args = append(args, "-e", kv)
		}
		img := f.Config.Image
		if img.WorkingDirectory != "" {
			args = append(args, "-w", img.WorkingDirectory)
		}
		var after []string
		if len(img.EntryPoint) > 0 {
			args = append(args, "--entrypoint", img.EntryPoint[0])
			after = append(after, img.EntryPoint[1:]...)
		}
		args = append(args, localImage(f))
		args = append(args, append(after, img.Command...)...)
		return exec.Command(docker, args...)
	}
	stop := func() {
		mu.Lock()
		name := container
		mu.Unlock()
		if name != "" {
			_ = exec.Command(docker, "rm", "-f", name).Run()
		}
	}
	return &Runtime{
		Name: f.Name, ARN: f.ARN,
		Timeout: time.Duration(f.Config.Timeout) * time.Second,
		Command: command, Stop: stop, Output: f.Output,
	}, nil
}

// hostAddress is how a container reaches an address of this machine: 127.0.0.1 and localhost as
// host.docker.internal.
func hostAddress(addr string) string {
	for _, local := range []string{"127.0.0.1", "localhost"} {
		addr = strings.ReplaceAll(addr, local, "host.docker.internal")
	}
	return addr
}

// imageEnv is what a container gets: the variables lambda sets but those of this machine and of
// a runtime the image brings itself, and the function's own.
func (l Runner) imageEnv(f Function, addr string) []string {
	all := l.env(f, "", addr)
	out := make([]string, 0, len(all))
	for _, kv := range all {
		key, value, _ := strings.Cut(kv, "=")
		switch key {
		case "PATH", "HOME", "LAMBDA_TASK_ROOT", "_HANDLER", "AWS_EXECUTION_ENV":
			continue
		case "AWS_ENDPOINT_URL", "AWS_ENDPOINT_URL_S3":
			if runtime.GOOS != build.Linux {
				value = hostAddress(value)
			}
		}
		out = append(out, key+"="+value)
	}
	return out
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
		"_HANDLER":                        f.Config.LambdaHandler(),
	}
	maps.Copy(vars, f.Env)
	out := make([]string, 0, len(vars))
	for _, k := range slices.Sorted(maps.Keys(vars)) {
		out = append(out, k+"="+vars[k])
	}
	return out
}

var nodeMajor = regexp.MustCompile(`^v(\d+)\.`)

// layersMissing warns that a function's layers are not added when it runs here, so what they put
// under /opt, packages or files, is missing; none for a function without layers.
func layersMissing(n int) string {
	switch n {
	case 0:
		return ""
	case 1:
		return "note: the function's layer is not added here: what it puts under /opt is missing"
	}
	return fmt.Sprintf("note: the function's %d layers are not added here: what they put under /opt is missing", n)
}

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
