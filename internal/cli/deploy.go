package cli

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/kontrolplane/koldstart/internal/build"
	"github.com/kontrolplane/koldstart/internal/desired"
	"github.com/kontrolplane/koldstart/internal/engine"
	"github.com/kontrolplane/koldstart/internal/state"
)

func planCommand(name string, opts *Options) *cobra.Command {
	var plan planOptions
	cmd := &cobra.Command{
		Use:   "plan",
		Short: "show what a deploy would create, change and delete",
		Long: `plan builds every function, compares the configuration with what the state says is deployed,
and prints what a deploy would create, update, replace and delete. It changes nothing in aws and
writes no state; it reads secrets and other stages' outputs, to tell whether they changed.

With --refresh it also reads every resource as it is in aws: one deleted outside koldstart is
planned to be created again, and one changed outside koldstart to be set back to what the
configuration says. Functions and resources say which inputs were changed; the rest only whether
they are still there.`,
		Example: fmt.Sprintf(`  %[1]s plan
  %[1]s plan --stage prod --refresh
  %[1]s plan --allow-replace`, name),
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			s, err := openStage(ctx, opts, false)
			if err != nil {
				return err
			}
			_, p, _, err := s.plan(ctx, false, plan)
			if err != nil {
				return err
			}
			printPlan(cmd.OutOrStdout(), s, p)
			return nil
		},
	}
	cmd.Flags().BoolVar(&plan.AllowReplace, "allow-replace", false, "plan replacing retained resources; the old ones stay")
	cmd.Flags().BoolVar(&plan.Refresh, "refresh", false, "show what changed outside koldstart")
	return cmd
}

func deployCommand(name string, opts *Options) *cobra.Command {
	var yes bool
	var plan planOptions
	cmd := &cobra.Command{
		Use:   "deploy",
		Short: "build, upload and apply the plan",
		Long: `deploy builds every function, uploads the zips, prints the plan, asks, applies it and prints the
outputs, holding the stage's lock throughout. Every step is written to the state as it is made, so
a deploy that fails or is stopped is finished by the next.

Without a terminal to ask on, pass --yes. Progress goes to stderr: a status line on a terminal,
and plain lines elsewhere, as in ci.`,
		Example: fmt.Sprintf(`  %[1]s deploy
  %[1]s deploy --stage prod --yes
  %[1]s deploy --refresh`, name),
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			s, err := openStage(ctx, opts, true)
			if err != nil {
				return err
			}
			unlock, err := s.lockFor(cmd, "deploy")
			if err != nil {
				return err
			}
			defer unlock()
			e, p, nodes, err := s.plan(ctx, true, plan)
			if err != nil {
				return err
			}
			printPlan(cmd.OutOrStdout(), s, p)
			if p.HasChanges() {
				if err := confirm(cmd, yes, "\napply this plan?"); err != nil {
					return err
				}
			}
			return s.apply(ctx, cmd, e, p, nodes)
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "apply without asking")
	cmd.Flags().BoolVar(&plan.AllowReplace, "allow-replace", false, "replace retained resources; the old ones stay")
	cmd.Flags().BoolVar(&plan.Refresh, "refresh", false, "undo what changed outside koldstart")
	cmd.AddCommand(deployFunctionCommand(name, opts))
	return cmd
}

// deployFunctionCommand deploys the code of one function. The other functions keep the code the
// state says they have.
func deployFunctionCommand(name string, opts *Options) *cobra.Command {
	var function string
	cmd := &cobra.Command{
		Use:   "function",
		Short: "deploy only the code of one function",
		Long: `deploy function builds one function, uploads it, and replaces its code in place, without asking,
holding the stage's lock. It says so when the code is the code deployed, and refuses when the
function is not deployed yet or its configuration changed too: run deploy for those.`,
		Example: fmt.Sprintf(`  %[1]s deploy function -f api
  %[1]s deploy function -f worker --stage prod`, name),
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			s, err := openStage(ctx, opts, true)
			if err != nil {
				return err
			}
			if err := s.function(function); err != nil {
				return err
			}
			unlock, err := s.lockFor(cmd, "deploy function "+function)
			if err != nil {
				return err
			}
			defer unlock()
			e, plan, nodes, err := s.planFunction(ctx, function, true)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if !plan.HasChanges() {
				_, err := fmt.Fprintf(out, "%s is up to date: its code is the code deployed to %s/%s\n", function, s.App, s.Stage)
				return err
			}
			_, _ = fmt.Fprintf(out, "deploying the code of %s to %s/%s in %s, account %s\n", function, s.App, s.Stage, s.Region, s.sess.Account)
			p := s.beginProgress(cmd, plan)
			err = s.applyFunction(ctx, e, plan, nodes, function, p.event)
			return s.endProgress(cmd, p, plan, "deployed", err)
		},
	}
	cmd.Flags().StringVarP(&function, "function", "f", "", "the function to deploy")
	_ = cmd.MarkFlagRequired("function")
	return cmd
}

// planFunction builds one function, uploading its zip when upload is set, and plans replacing
// its code, the other functions keeping the code the state says they have. The plan has the one
// change, or none when the function is up to date. It refuses when the function's configuration
// changed too.
func (s *stage) planFunction(ctx context.Context, function string, upload bool) (*engine.Engine, *engine.Plan, []engine.Node, error) {
	id := desired.FunctionID(function)
	if s.state.Resources[id] == nil {
		return nil, nil, nil, fmt.Errorf("%s is not deployed yet: run deploy", function)
	}
	archives, err := s.buildAll(ctx, []string{function}, upload)
	if err != nil {
		return nil, nil, nil, err
	}
	for other, fn := range s.Functions {
		if other != function {
			archives[other] = build.Archive{Function: other, SHA256: deployedSHA(s.state, other), Image: fn.IsImage()}
			if fn.IsImage() {
				if _, err := s.imageRegistry(ctx); err != nil {
					return nil, nil, nil, err
				}
			}
		}
	}
	nodes, err := s.nodes(archives)
	if err != nil {
		return nil, nil, nil, err
	}
	e := s.engine()
	plan, err := e.Plan(ctx, nodes, s.state)
	if err != nil {
		return nil, nil, nil, err
	}
	change, _ := plan.Change(id)
	only := &engine.Plan{App: plan.App, Stage: plan.Stage, Serial: plan.Serial}
	switch {
	case change.Action == engine.NoOp:
		return e, only, nodes, nil
	case change.Action != engine.Update || !slices.Equal(change.Fields, []string{"code"}):
		return nil, nil, nil, configChanged(function, change)
	}
	only.Changes = []engine.Change{change}
	return e, only, nodes, nil
}

// applyFunction applies a plan of planFunction, telling onEvent as the function's code starts
// and finishes being replaced. A failed or stopped apply says to deploy the function again.
func (s *stage) applyFunction(ctx context.Context, e *engine.Engine, plan *engine.Plan, nodes []engine.Node, function string, onEvent func(engine.Event)) error {
	if !plan.HasChanges() {
		return nil
	}
	e.OnEvent = onEvent
	if err := e.Apply(ctx, plan, nodes, engine.NewStore(s.backend, s.state, s.etag, whoIsDeploying())); err != nil {
		return applyFailed(ctx, err, "deploy function -f "+function)
	}
	return nil
}

// configChanged refuses to deploy only the code of a function whose configuration changed,
// naming what did and saying to deploy the stage.
func configChanged(function string, change engine.Change) error {
	fields := slices.DeleteFunc(slices.Clone(change.Fields), func(f string) bool { return f == "code" })
	switch {
	case len(fields) == 0:
		return fmt.Errorf("%s needs more than its code deployed, to %s it: run deploy", function, verbs[change.Action])
	case len(fields) < len(change.Fields):
		return fmt.Errorf("the configuration of %s changed too (%s): run deploy, which deploys both", function, strings.Join(fields, ", "))
	default:
		return fmt.Errorf("the code of %s is as deployed, but its configuration changed (%s): run deploy", function, strings.Join(fields, ", "))
	}
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
	_ = json.Unmarshal(r.Inputs, &in)
	return in.Code.SHA256
}

// lockFor takes the stage's lock for operation, as lock does. When someone else holds it, the
// error says who, since when, and the command that releases a lock left behind.
func (s *stage) lockFor(cmd *cobra.Command, operation string) (func(), error) {
	unlock, err := s.lock(cmd.Context(), operation)
	if locked, ok := errors.AsType[*state.LockedError](err); ok {
		return nil, lockedError(s.App, s.Stage, locked.Info, unlockHint(cmd, s.Stage), time.Now())
	}
	return unlock, err
}

// lockedError says who holds a stage's lock, doing what and since when, and how to release it
// once they are gone.
func lockedError(app, stage string, held state.LockInfo, unlock string, now time.Time) error {
	return fmt.Errorf("%s\nwait for it to finish, or, when it is gone, release the lock with %s", lockHolder(app, stage, held, now), unlock)
}

// lockHolder says a stage is locked, and who holds its lock, as LockHeld does.
func lockHolder(app, stage string, held state.LockInfo, now time.Time) string {
	return fmt.Sprintf("%s/%s is locked: %s", app, stage, LockHeld(held, now))
}

// LockHeld says who holds a lock, from where, doing what and since when, in now's time zone: levi
// on laptop has been running deploy since 14:26:48, 3m12s ago. The date is left out when the lock
// was taken today.
func LockHeld(held state.LockInfo, now time.Time) string {
	who := cmp.Or(held.Who, "someone")
	if held.Where != "" {
		who += " on " + held.Where
	}
	doing := "has held it"
	if held.Operation != "" {
		doing = "has been running " + held.Operation
	}
	if held.Since.IsZero() {
		return who + " " + doing + ", since a time it did not record"
	}
	since := held.Since.In(now.Location())
	at := since.Format(time.DateTime)
	if since.YearDay() == now.YearDay() && since.Year() == now.Year() {
		at = since.Format(time.TimeOnly)
	}
	return fmt.Sprintf("%s %s since %s, %s ago", who, doing, at, Elapsed(now.Sub(held.Since)))
}

// unlockHint is the command that releases the stage's lock, with the flags cmd was given that
// pick where its state is: the configuration, profile and region.
func unlockHint(cmd *cobra.Command, stage string) string {
	parts := []string{cmd.Root().Name(), "unlock", "--stage", stage}
	for _, name := range []string{"config", "profile", "region"} {
		if f := cmd.Flag(name); f != nil && f.Changed {
			parts = append(parts, "--"+name, f.Value.String())
		}
	}
	return strings.Join(parts, " ")
}
