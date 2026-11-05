package cli

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	lambdatypes "github.com/aws/aws-sdk-go-v2/service/lambda/types"
	"github.com/spf13/cobra"

	"github.com/kontrolplane/koldstart/internal/build"
	"github.com/kontrolplane/koldstart/internal/desired"
	"github.com/kontrolplane/koldstart/internal/local"
	"github.com/kontrolplane/koldstart/internal/pkl"
	"github.com/kontrolplane/koldstart/internal/provider/awsp"
)

// localDir is where functions are built to run on this machine.
const localDir = ".koldstart/local"

// overridesFile holds the variables a stage's functions get when they run here instead of what
// the stage deployed: functions { ["api"] { ["TABLE_NAME"] = "orders-local" } }.
func overridesFile(stage string) string {
	return filepath.Join(".koldstart", "local."+stage+".pkl")
}

// runner builds and runs the stage's functions on this machine.
func (s *stage) runner() local.Runner {
	return local.Runner{Root: s.Root, Stage: s.Stage, Dir: filepath.Join(s.Root, localDir, s.Stage)}
}

// overrides reads the stage's overrides file, when there is one.
func (s *stage) overrides(ctx context.Context) (map[string]map[string]string, error) {
	path := filepath.Join(s.Root, overridesFile(s.Stage))
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	binary, err := pkl.Find(ctx)
	if err != nil {
		return nil, fmt.Errorf("finding pkl: %w", err)
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
	env, err := s.declaredEnvironment(name)
	if err != nil {
		return local.Function{}, err
	}
	vars, err := s.resolveVariables(ctx, name, env)
	if err != nil {
		return local.Function{}, err
	}
	credentials, err := s.awsVariables(ctx)
	if err != nil {
		return local.Function{}, err
	}
	maps.Copy(vars, credentials)
	lambdaName := desired.FunctionName(s.Project, name)
	return local.Function{
		Name: name, LambdaName: lambdaName, Config: s.Functions[name], Region: s.Region, Env: vars, Output: out,
		ARN: "arn:" + awsp.Partition(s.Region) + ":lambda:" + s.Region + ":" + s.sess.Account + ":function:" + lambdaName,
	}, nil
}

// declaredEnvironment is a function's environment as a deploy gives it, before it is resolved.
func (s *stage) declaredEnvironment(name string) (map[string]any, error) {
	archives := map[string]build.Archive{}
	for fn := range s.Functions {
		archives[fn] = build.Archive{Function: fn, SHA256: "local"}
	}
	nodes, err := s.nodes(archives)
	if err != nil {
		return nil, err
	}
	for _, n := range nodes {
		if n.ID == desired.FunctionID(name) {
			env, _ := n.Inputs["environment"].(map[string]any)
			return env, nil
		}
	}
	return nil, nil
}

// resolveVariables resolves a function's variables against what the stage deployed, each one
// the overrides file sets taken from there instead.
func (s *stage) resolveVariables(ctx context.Context, name string, env map[string]any) (map[string]string, error) {
	overrides, err := s.overrides(ctx)
	if err != nil {
		return nil, err
	}
	e := s.engine()
	vars := map[string]string{}
	for _, key := range slices.Sorted(maps.Keys(env)) {
		if _, set := overrides[name][key]; set {
			continue
		}
		v, err := e.Resolve(ctx, env[key], s.state)
		if err != nil {
			return nil, fmt.Errorf("%s: %w: deploy it, or set %s in %s", key, err, key, overridesFile(s.Stage))
		}
		vars[key] = fmt.Sprint(v)
	}
	maps.Copy(vars, overrides[name])
	return vars, nil
}

// awsVariables are the session's credentials, and the endpoint of an emulator when one is set,
// so the function reaches what koldstart reaches.
func (s *stage) awsVariables(ctx context.Context) (map[string]string, error) {
	creds, err := s.sess.cfg.Credentials.Retrieve(ctx)
	if err != nil {
		return nil, fmt.Errorf("reading the aws credentials for the function: %w", err)
	}
	vars := map[string]string{"AWS_ACCESS_KEY_ID": creds.AccessKeyID, "AWS_SECRET_ACCESS_KEY": creds.SecretAccessKey}
	if creds.SessionToken != "" {
		vars["AWS_SESSION_TOKEN"] = creds.SessionToken
	}
	for _, k := range []string{"AWS_ENDPOINT_URL", "AWS_ENDPOINT_URL_S3"} {
		if v := os.Getenv(k); v != "" {
			vars[k] = v
		}
	}
	return vars, nil
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
			if err := s.function(function); err != nil {
				return err
			}
			if !runLocal {
				return s.invokeRemote(ctx, cmd, function, payload)
			}
			if err := confirmProd(cmd, s, yes); err != nil {
				return err
			}
			return s.invokeLocal(ctx, cmd, function, payload)
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

// invokeRemote invokes the deployed function, printing the end of its log on stderr and what it
// returned on stdout.
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

// invokeLocal runs the function once on this machine, its output on stderr and what it returned
// on stdout.
func (s *stage) invokeLocal(ctx context.Context, cmd *cobra.Command, function string, payload []byte) error {
	f, err := s.localFunction(ctx, function, cmd.ErrOrStderr())
	if err != nil {
		return err
	}
	r, err := s.runner().Start(ctx, f)
	if err != nil {
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
