package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/kontrolplane/koldstart/internal/build"
	"github.com/kontrolplane/koldstart/internal/desired"
	"github.com/kontrolplane/koldstart/internal/engine"
	"github.com/kontrolplane/koldstart/internal/state"
)

// stage is a loaded configuration in its aws session, with its state.
type stage struct {
	*project
	sess    *session
	backend *state.S3
	exists  bool // whether the state bucket exists
	state   *state.State
	etag    string
}

// openStage loads the configuration, connects to aws, and reads the state. With create, the
// state bucket is created when it does not exist yet.
func openStage(ctx context.Context, opts *Options, create bool) (*stage, error) {
	p, err := loadProject(ctx, opts)
	if err != nil {
		return nil, err
	}
	sess, err := openSession(ctx, opts, p.Region)
	if err != nil {
		return nil, err
	}
	p.Region = sess.Region
	b, exists, err := sess.backend(ctx, create)
	if err != nil {
		return nil, err
	}
	s := &stage{project: p, sess: sess, backend: b, exists: exists}
	return s, s.read(ctx)
}

func (s *stage) read(ctx context.Context) error {
	if s.exists {
		st, etag, err := s.backend.Read(ctx, s.App, s.Stage)
		if err != nil {
			return err
		}
		s.state, s.etag = st, etag
	}
	if s.state == nil {
		s.state, s.etag = state.New(s.App, s.Stage, s.Region, s.sess.Account), ""
	}
	return nil
}

// lock takes the stage's lock, and returns the function that releases it.
func (s *stage) lock(ctx context.Context, operation string) (func(), error) {
	if err := s.backend.Lock(ctx, s.App, s.Stage, lockInfo(operation)); err != nil {
		return nil, err
	}
	// The lock is released even when ctx was cancelled by ctrl+c.
	return func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = s.backend.Unlock(ctx, s.App, s.Stage)
	}, s.read(ctx)
}

// buildAll builds the functions named, or all of them, and uploads their zips when upload is set.
func (s *stage) buildAll(ctx context.Context, names []string, upload bool) (map[string]build.Archive, error) {
	if names == nil {
		names = slices.Sorted(maps.Keys(s.Functions))
	}
	b := build.Builder{Root: s.Root, Stage: s.Stage}
	archives := map[string]build.Archive{}
	for _, name := range names {
		arc, err := b.Build(ctx, name, s.Functions[name])
		if err != nil {
			return nil, err
		}
		archives[name] = arc
		if !upload {
			continue
		}
		data, err := os.ReadFile(arc.Path)
		if err != nil {
			return nil, err
		}
		if _, _, err := s.backend.PutZip(ctx, s.App, s.Stage, arc.SHA256, data); err != nil {
			return nil, err
		}
	}
	return archives, nil
}

func (s *stage) nodes(archives map[string]build.Archive) ([]engine.Node, error) {
	return desired.Build(s.Project, desired.Target{
		Account: s.sess.Account, Region: s.Region, Bucket: state.BucketName(s.sess.Account, s.Region), Archives: archives,
	})
}

// confirm asks a yes or no question, refusing without a terminal unless yes is set.
func confirm(cmd *cobra.Command, yes bool, question string) error {
	if yes {
		return nil
	}
	if !isTerminal(os.Stdin) {
		return errors.New("no terminal to ask on: pass --yes")
	}
	_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "%s [y/N] ", question)
	answer, _ := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
	if a := strings.ToLower(strings.TrimSpace(answer)); a != "y" && a != "yes" {
		return errors.New("cancelled")
	}
	return nil
}

func isTerminal(f *os.File) bool {
	info, err := f.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

func planCommand(name string, opts *Options) *cobra.Command {
	var allowReplace bool
	cmd := &cobra.Command{
		Use:   "plan",
		Short: "show what a deploy would create, change and delete",
		Long: `plan builds every function and compares the configuration with the state, without changing
anything in aws. It reads secrets, to tell whether they changed.`,
		Example: fmt.Sprintf("  %[1]s plan\n  %[1]s plan --stage prod", name),
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			s, err := openStage(ctx, opts, false)
			if err != nil {
				return err
			}
			archives, err := s.buildAll(ctx, nil, false)
			if err != nil {
				return err
			}
			nodes, err := s.nodes(archives)
			if err != nil {
				return err
			}
			e := s.sess.engine(s.Tags)
			e.AllowReplace = allowReplace
			plan, err := e.Plan(ctx, nodes, s.state)
			if err != nil {
				return err
			}
			printPlan(cmd.OutOrStdout(), s, plan)
			return nil
		},
	}
	cmd.Flags().BoolVar(&allowReplace, "allow-replace", false, "plan replacing retained resources, which leaves the old ones in place")
	return cmd
}

func deployCommand(name string, opts *Options) *cobra.Command {
	var yes, allowReplace bool
	cmd := &cobra.Command{
		Use:   "deploy",
		Short: "build, upload and apply the plan",
		Long: `deploy builds every function, uploads the zips, plans, asks, and applies the plan. Every step
is written to the state as it is made: a deploy that fails or is stopped is finished by the next.`,
		Example: fmt.Sprintf("  %[1]s deploy\n  %[1]s deploy --stage prod --yes", name),
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			s, err := openStage(ctx, opts, true)
			if err != nil {
				return err
			}
			unlock, err := s.lock(ctx, "deploy")
			if err != nil {
				return err
			}
			defer unlock()
			archives, err := s.buildAll(ctx, nil, true)
			if err != nil {
				return err
			}
			nodes, err := s.nodes(archives)
			if err != nil {
				return err
			}
			e := s.sess.engine(s.Tags)
			e.AllowReplace = allowReplace
			plan, err := e.Plan(ctx, nodes, s.state)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			printPlan(out, s, plan)
			if plan.HasChanges() {
				if err := confirm(cmd, yes, "apply this plan?"); err != nil {
					return err
				}
			}
			return s.apply(ctx, cmd, e, plan, nodes)
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "apply without asking")
	cmd.Flags().BoolVar(&allowReplace, "allow-replace", false, "replace retained resources, which leaves the old ones in place")
	cmd.AddCommand(deployFunctionCommand(name, opts))
	return cmd
}

// apply applies the plan with progress on stderr, then records and prints the outputs.
func (s *stage) apply(ctx context.Context, cmd *cobra.Command, e *engine.Engine, plan *engine.Plan, nodes []engine.Node) error {
	store := engine.NewStore(s.backend, s.state, s.etag, lockInfo("deploy").Who)
	e.OnEvent = progress(cmd.ErrOrStderr())
	if plan.HasChanges() {
		if err := e.Apply(ctx, plan, nodes, store); err != nil {
			return fmt.Errorf("%w\nwhat was done is in the state: deploy again to finish", err)
		}
	}
	outputs, err := s.outputs(store.Snapshot())
	if err != nil {
		return err
	}
	if err := store.Update(ctx, func(st *state.State) { st.Outputs = outputs }); err != nil {
		return err
	}
	printOutputs(cmd.OutOrStdout(), outputs)
	return nil
}

// outputs resolves the configuration's outputs against the state.
func (s *stage) outputs(st *state.State) (map[string]string, error) {
	out := map[string]string{}
	for _, name := range slices.Sorted(maps.Keys(s.Outputs)) {
		v := s.Outputs[name]
		switch {
		case v.Ref != nil:
			r := st.Resources[s.nodeID(v.Ref.Resource)]
			if r == nil {
				return nil, fmt.Errorf("outputs[%s]: %s does not exist", name, v.Ref.Resource)
			}
			out[name] = r.Attributes[v.Ref.Attribute]
		case v.Secret != nil || v.Output != nil:
			return nil, fmt.Errorf("outputs[%s]: an output is a string or a ref()", name)
		default:
			out[name] = v.Literal
		}
	}
	if url := s.functionURLs(st); url != nil {
		maps.Copy(out, url)
	}
	return out, nil
}

func (s *stage) nodeID(name string) string {
	if _, ok := s.Functions[name]; ok {
		return desired.FunctionID(name)
	}
	return desired.ResourceID(name)
}

// functionURLs are the urls of the functions that have one, as outputs.
func (s *stage) functionURLs(st *state.State) map[string]string {
	out := map[string]string{}
	for name := range s.Functions {
		if r := st.Resources[desired.URLID(name)]; r != nil {
			out[name+"Url"] = r.Attributes["FunctionUrl"]
		}
	}
	return out
}

func deployFunctionCommand(name string, opts *Options) *cobra.Command {
	var function string
	cmd := &cobra.Command{
		Use:   "function",
		Short: "deploy only the code of one function",
		Long: `deploy function builds one function, uploads it, and replaces its code in place. It refuses when
the function's configuration changed too: run deploy for that.`,
		Example: fmt.Sprintf("  %[1]s deploy function -f api", name),
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			s, err := openStage(ctx, opts, true)
			if err != nil {
				return err
			}
			if _, ok := s.Functions[function]; !ok {
				return fmt.Errorf("no function %q in %s", function, s.Path)
			}
			unlock, err := s.lock(ctx, "deploy function "+function)
			if err != nil {
				return err
			}
			defer unlock()
			id := desired.FunctionID(function)
			if s.state.Resources[id] == nil {
				return fmt.Errorf("%s is not deployed yet: run deploy", function)
			}
			archives, err := s.buildAll(ctx, []string{function}, true)
			if err != nil {
				return err
			}
			// The other functions keep the code the state says they have.
			for other := range s.Functions {
				if other == function {
					continue
				}
				archives[other] = build.Archive{Function: other, SHA256: deployedSHA(s.state, other)}
			}
			nodes, err := s.nodes(archives)
			if err != nil {
				return err
			}
			e := s.sess.engine(s.Tags)
			plan, err := e.Plan(ctx, nodes, s.state)
			if err != nil {
				return err
			}
			change, _ := plan.Change(id)
			switch {
			case change.Action == engine.NoOp:
				_, err := fmt.Fprintf(cmd.OutOrStdout(), "%s is up to date\n", function)
				return err
			case change.Action != engine.Update || !slices.Equal(change.Fields, []string{"code"}):
				return fmt.Errorf("the configuration of %s changed too (%s): run deploy", function, strings.Join(change.Fields, ", "))
			}
			only := &engine.Plan{App: plan.App, Stage: plan.Stage, Serial: plan.Serial, Changes: []engine.Change{change}}
			e.OnEvent = progress(cmd.ErrOrStderr())
			store := engine.NewStore(s.backend, s.state, s.etag, lockInfo("deploy").Who)
			return e.Apply(ctx, only, nodes, store)
		},
	}
	cmd.Flags().StringVarP(&function, "function", "f", "", "the function to deploy")
	_ = cmd.MarkFlagRequired("function")
	return cmd
}

// deployedSHA is the hash of the code the state says a function has.
func deployedSHA(st *state.State, function string) string {
	r := st.Resources[desired.FunctionID(function)]
	if r == nil {
		return ""
	}
	var in struct {
		Code struct {
			SHA256 string `json:"sha256"`
		} `json:"code"`
	}
	_ = jsonUnmarshal(r.Inputs, &in)
	return in.Code.SHA256
}

func removeCommand(name string, opts *Options) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "remove",
		Short: "delete everything the stage deployed",
		Long: `remove deletes everything the stage's state lists, in reverse order, after asking you to type the
stage's name. Retained resources, such as tables and buckets, are left in place, and the next deploy
of the stage takes them over again.`,
		Example: fmt.Sprintf("  %[1]s remove --stage pr-12", name),
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			s, err := openStage(ctx, opts, false)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if !s.exists || len(s.state.Resources) == 0 {
				_, err := fmt.Fprintf(out, "nothing of %s/%s is deployed in %s\n", s.App, s.Stage, s.Region)
				return err
			}
			if !yes {
				if !isTerminal(os.Stdin) {
					return errors.New("no terminal to ask on: pass --yes")
				}
				_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "remove everything of %s/%s in %s? type the stage's name: ", s.App, s.Stage, s.Region)
				answer, _ := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
				if strings.TrimSpace(answer) != s.Stage {
					return errors.New("cancelled")
				}
			}
			unlock, err := s.lock(ctx, "remove")
			if err != nil {
				return err
			}
			defer unlock()
			e := s.sess.engine(s.Tags)
			plan, err := e.Plan(ctx, nil, s.state)
			if err != nil {
				return err
			}
			e.OnEvent = progress(cmd.ErrOrStderr())
			store := engine.NewStore(s.backend, s.state, s.etag, lockInfo("remove").Who)
			if err := e.Apply(ctx, plan, nil, store); err != nil {
				return fmt.Errorf("%w\nwhat was removed is in the state: remove again to finish", err)
			}
			final := store.Snapshot()
			if len(final.Retained) == 0 {
				if err := s.backend.Delete(ctx, s.App, s.Stage); err != nil {
					return err
				}
				_, err := fmt.Fprintf(out, "removed %s/%s\n", s.App, s.Stage)
				return err
			}
			if err := store.Update(ctx, func(st *state.State) { st.Outputs = nil }); err != nil {
				return err
			}
			_, _ = fmt.Fprintf(out, "removed %s/%s, and left in place:\n", s.App, s.Stage)
			for _, id := range slices.Sorted(maps.Keys(final.Retained)) {
				_, _ = fmt.Fprintf(out, "  %s  %s\n", id, final.Retained[id].PhysicalID)
			}
			return nil
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "remove without asking")
	return cmd
}

func unlockCommand(name string, opts *Options) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "unlock",
		Short: "release a stage's lock left by a deploy that did not finish",
		Long: `unlock shows who holds the stage's lock, and releases it. Make sure that deploy is really gone:
two deploys of one stage at once can leave its state wrong.`,
		Example: fmt.Sprintf("  %[1]s unlock --stage prod", name),
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			s, err := openStage(ctx, opts, false)
			if err != nil {
				return err
			}
			if !s.exists {
				return state.ErrNotLocked
			}
			held, err := s.backend.LockInfo(ctx, s.App, s.Stage)
			if err != nil {
				return err
			}
			if held == nil {
				return fmt.Errorf("%s/%s: %w", s.App, s.Stage, state.ErrNotLocked)
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s/%s is locked by %s\n", s.App, s.Stage, held)
			if err := confirm(cmd, yes, "release it?"); err != nil {
				return err
			}
			return s.backend.Unlock(ctx, s.App, s.Stage)
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "release without asking")
	return cmd
}

func outputsCommand(name string, opts *Options) *cobra.Command {
	return &cobra.Command{
		Use:     "outputs",
		Short:   "print the stage's outputs, such as the http api url",
		Example: fmt.Sprintf("  %[1]s outputs --stage prod", name),
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := openStage(cmd.Context(), opts, false)
			if err != nil {
				return err
			}
			printOutputs(cmd.OutOrStdout(), s.state.Outputs)
			return nil
		},
	}
}

func printOutputs(w io.Writer, outputs map[string]string) {
	if len(outputs) == 0 {
		return
	}
	_, _ = fmt.Fprintln(w, "\noutputs:")
	for _, k := range slices.Sorted(maps.Keys(outputs)) {
		_, _ = fmt.Fprintf(w, "  %s = %s\n", k, outputs[k])
	}
}
