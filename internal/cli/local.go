package cli

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	lambdatypes "github.com/aws/aws-sdk-go-v2/service/lambda/types"
	"github.com/spf13/cobra"

	"github.com/kontrolplane/koldstart/internal/build"
	"github.com/kontrolplane/koldstart/internal/config"
	"github.com/kontrolplane/koldstart/internal/desired"
	"github.com/kontrolplane/koldstart/internal/local"
	"github.com/kontrolplane/koldstart/internal/pkl"
)

// localDir is where functions are built to run on this machine.
const localDir = ".koldstart/local"

// overridesFile holds the variables a stage's functions get when they run here instead of what
// the stage deployed: functions { ["api"] { ["TABLE_NAME"] = "orders-local" } }.
func overridesFile(stage string) string {
	return filepath.Join(".koldstart", "local."+stage+".pkl")
}

// overrides reads the stage's overrides file, when there is one.
func (s *stage) overrides(ctx context.Context) (map[string]map[string]string, error) {
	path := filepath.Join(s.Root, overridesFile(s.Stage))
	if _, err := os.Stat(path); err != nil {
		return nil, nil
	}
	binary, err := pkl.Find(ctx)
	if err != nil {
		return nil, err
	}
	out, err := pkl.Evaluator{Binary: binary}.EvalJSON(ctx, path, nil)
	if err != nil {
		return nil, err
	}
	var file struct {
		Functions map[string]map[string]string `json:"functions"`
	}
	if err := json.Unmarshal(out, &file); err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	return file.Functions, nil
}

// localFunction is a function as it runs here: its variables resolved against the deployed
// stage, overridden by the stage's overrides file, and the session's credentials, so it talks to
// what the stage deployed.
func (s *stage) localFunction(ctx context.Context, name string, out io.Writer) (local.Function, error) {
	fn := s.Functions[name]
	archives := map[string]build.Archive{}
	for other := range s.Functions {
		archives[other] = build.Archive{Function: other, SHA256: "local"}
	}
	nodes, err := s.nodes(archives)
	if err != nil {
		return local.Function{}, err
	}
	var env map[string]any
	for _, n := range nodes {
		if n.ID == desired.FunctionID(name) {
			env, _ = n.Inputs["environment"].(map[string]any)
		}
	}
	overrides, err := s.overrides(ctx)
	if err != nil {
		return local.Function{}, err
	}
	e := s.engine()
	vars := map[string]string{}
	for _, key := range slices.Sorted(maps.Keys(env)) {
		if v, ok := overrides[name][key]; ok {
			vars[key] = v
			continue
		}
		v, err := e.Resolve(ctx, env[key], s.state)
		if err != nil {
			return local.Function{}, fmt.Errorf("%s: %w: deploy it, or set %s in %s", key, err, key, overridesFile(s.Stage))
		}
		vars[key] = fmt.Sprint(v)
	}
	maps.Copy(vars, overrides[name])
	creds, err := s.sess.cfg.Credentials.Retrieve(ctx)
	if err != nil {
		return local.Function{}, fmt.Errorf("reading the aws credentials for the function: %w", err)
	}
	vars["AWS_ACCESS_KEY_ID"], vars["AWS_SECRET_ACCESS_KEY"] = creds.AccessKeyID, creds.SecretAccessKey
	if creds.SessionToken != "" {
		vars["AWS_SESSION_TOKEN"] = creds.SessionToken
	}
	for _, k := range []string{"AWS_ENDPOINT_URL", "AWS_ENDPOINT_URL_S3"} {
		if v := os.Getenv(k); v != "" {
			vars[k] = v
		}
	}
	fnName := desired.FunctionName(s.Project, name)
	return local.Function{
		Name: name, FunctionName: fnName, Config: fn, Region: s.Region, Env: vars, Output: out,
		ARN: "arn:aws:lambda:" + s.Region + ":" + s.sess.Account + ":function:" + fnName,
	}, nil
}

// prefixed writes each line of a function's output with its name before it, one line at a time.
type prefixed struct {
	mu     *sync.Mutex
	w      io.Writer
	prefix string
	buf    bytes.Buffer
}

func (p *prefixed) Write(b []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.buf.Write(b)
	for {
		line, err := p.buf.ReadString('\n')
		if err != nil {
			p.buf.WriteString(line)
			return len(b), nil
		}
		_, _ = fmt.Fprint(p.w, p.prefix+line)
	}
}

// eventFrom reads the event to invoke with: --data, --path, or {}.
//
//nolint:gosec // reads the event file the user names
func eventFrom(data, path string) ([]byte, error) {
	switch {
	case data != "" && path != "":
		return nil, errors.New("pass --data or --path, not both")
	case path != "":
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("reading the event: %w", err)
		}
		return b, nil
	case data != "":
		return []byte(data), nil
	default:
		return []byte("{}"), nil
	}
}

// confirmProd asks before code on this machine works with what prod deployed.
func confirmProd(cmd *cobra.Command, s *stage, yes bool) error {
	if s.Stage != "prod" {
		return nil
	}
	return confirm(cmd, yes, "this runs on this machine against what prod deployed, its data included. go on?")
}

func invokeCommand(name string, opts *Options) *cobra.Command {
	var function, data, path string
	var runLocal, yes bool
	cmd := &cobra.Command{
		Use:   "invoke",
		Short: "invoke a function with an event, in aws or with --local on this machine",
		Long: `invoke runs a function with the event from --data or --path, {} without either, and prints what it
returns. In aws, the end of its log follows. With --local, it is built and run here against the
lambda runtime api, with the variables the stage deployed and your credentials, so it reads and
writes what the stage deployed; .koldstart/local.<stage>.pkl overrides variables.`,
		Example: fmt.Sprintf(`  %[1]s invoke -f api --path events/get-order.json
  %[1]s invoke -f worker --local --data '{"job": 1}'`, name),
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			payload, err := eventFrom(data, path)
			if err != nil {
				return err
			}
			s, err := openStage(ctx, opts, false)
			if err != nil {
				return err
			}
			if _, ok := s.Functions[function]; !ok {
				return fmt.Errorf("no function %q in %s", function, s.Path)
			}
			if runLocal {
				if err := confirmProd(cmd, s, yes); err != nil {
					return err
				}
				return s.invokeLocal(ctx, cmd, function, payload)
			}
			return s.invokeRemote(ctx, cmd, function, payload)
		},
	}
	cmd.Flags().StringVarP(&function, "function", "f", "", "the function to invoke")
	cmd.Flags().StringVarP(&data, "data", "d", "", "the event, as json")
	cmd.Flags().StringVarP(&path, "path", "p", "", "a file with the event")
	cmd.Flags().BoolVar(&runLocal, "local", false, "run the function on this machine")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "run against prod without asking")
	_ = cmd.MarkFlagRequired("function")
	return cmd
}

func (s *stage) invokeRemote(ctx context.Context, cmd *cobra.Command, function string, payload []byte) error {
	out, err := s.sess.Lambda.Invoke(ctx, &lambda.InvokeInput{
		FunctionName: aws.String(desired.FunctionName(s.Project, function)), Payload: payload, LogType: lambdatypes.LogTypeTail,
	})
	if err != nil {
		return fmt.Errorf("invoking %s: %w", function, err)
	}
	if logs, err := base64.StdEncoding.DecodeString(aws.ToString(out.LogResult)); err == nil && len(logs) > 0 {
		if !bytes.HasSuffix(logs, []byte("\n")) {
			logs = append(logs, '\n')
		}
		_, _ = cmd.ErrOrStderr().Write(logs)
	}
	_, _ = fmt.Fprintln(cmd.OutOrStdout(), string(out.Payload))
	if out.FunctionError != nil {
		return fmt.Errorf("%s failed: %s", function, aws.ToString(out.FunctionError))
	}
	return nil
}

func (s *stage) invokeLocal(ctx context.Context, cmd *cobra.Command, function string, payload []byte) error {
	f, err := s.localFunction(ctx, function, cmd.ErrOrStderr())
	if err != nil {
		return err
	}
	l := local.Local{Root: s.Root, Stage: s.Stage, Dir: filepath.Join(s.Root, localDir, s.Stage)}
	if err := l.Build(ctx, f); err != nil {
		return err
	}
	r, err := l.Runtime(f)
	if err != nil {
		return err
	}
	if err := r.Start(); err != nil {
		return err
	}
	defer func() { _ = r.Close() }()
	result, err := r.Invoke(ctx, payload)
	if len(result) > 0 {
		_, _ = fmt.Fprintln(cmd.OutOrStdout(), string(result))
	}
	if err != nil {
		return fmt.Errorf("%s failed: %w", function, err)
	}
	return nil
}

// runtimes are the functions offline serves, each a warm process, rebuilt on a change.
type runtimes struct {
	mu   sync.Mutex
	byFn map[string]*local.Runtime
}

func (r *runtimes) Invoke(ctx context.Context, function string, payload []byte) ([]byte, error) {
	r.mu.Lock()
	rt := r.byFn[function]
	r.mu.Unlock()
	if rt == nil {
		return nil, fmt.Errorf("%s is not running", function)
	}
	return rt.Invoke(ctx, payload)
}

func offlineCommand(name string, opts *Options) *cobra.Command {
	var port int
	var yes bool
	cmd := &cobra.Command{
		Use:   "offline",
		Short: "serve the http routes on this machine",
		Long: `offline serves every http route of the stage on 127.0.0.1, each request an invocation of its
function on this machine, as invoke --local runs it. A change to a file of the project rebuilds the
functions and the next request runs the new code.`,
		Example: fmt.Sprintf("  %[1]s offline\n  %[1]s offline --port 8080", name),
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			s, err := openStage(ctx, opts, false)
			if err != nil {
				return err
			}
			if err := confirmProd(cmd, s, yes); err != nil {
				return err
			}
			var routes []local.Route
			for _, fn := range slices.Sorted(maps.Keys(s.Functions)) {
				for _, e := range s.Functions[fn].Events {
					if e.Type == config.EventHTTPRoute {
						routes = append(routes, local.Route{Method: strings.ToUpper(e.Method), Path: e.Path, Function: fn})
					}
				}
			}
			if len(routes) == 0 {
				return errors.New("no function has an http route to serve")
			}
			return s.serve(ctx, cmd, routes, port)
		},
	}
	cmd.Flags().IntVar(&port, "port", 3000, "the port to listen on")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "run against prod without asking")
	return cmd
}

func (s *stage) serve(ctx context.Context, cmd *cobra.Command, routes []local.Route, port int) error {
	l := local.Local{Root: s.Root, Stage: s.Stage, Dir: filepath.Join(s.Root, localDir, s.Stage)}
	var mu sync.Mutex
	rts := &runtimes{byFn: map[string]*local.Runtime{}}
	var functions []local.Function
	seen := map[string]bool{}
	for _, r := range routes {
		if seen[r.Function] {
			continue
		}
		seen[r.Function] = true
		f, err := s.localFunction(ctx, r.Function, &prefixed{mu: &mu, w: cmd.ErrOrStderr(), prefix: "[" + r.Function + "] "})
		if err != nil {
			return err
		}
		if err := l.Build(ctx, f); err != nil {
			return err
		}
		rt, err := l.Runtime(f)
		if err != nil {
			return err
		}
		if err := rt.Start(); err != nil {
			return err
		}
		defer func() { _ = rt.Close() }()
		rts.byFn[r.Function] = rt
		functions = append(functions, f)
	}

	listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return fmt.Errorf("listening on port %d: %w", port, err)
	}
	out := cmd.OutOrStdout()
	_, _ = fmt.Fprintf(out, "serving %s/%s on http://%s\n", s.App, s.Stage, listener.Addr())
	for _, r := range routes {
		_, _ = fmt.Fprintf(out, "  %-28s %s\n", r.Key(), r.Function)
	}
	srv := &http.Server{Handler: &local.Offline{Routes: routes, Invoker: rts, Log: cmd.ErrOrStderr()}, ReadHeaderTimeout: 10 * time.Second}
	go local.Watch(ctx, s.Root, 500*time.Millisecond, func() {
		for _, f := range functions {
			if err := l.Build(ctx, f); err != nil {
				_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "rebuilding %s: %v\n", f.Name, err)
				continue
			}
			rts.byFn[f.Name].Restart()
		}
		_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "rebuilt")
	})
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()
	if err := srv.Serve(listener); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
