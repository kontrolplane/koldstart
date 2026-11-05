package cli

import (
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
	cmd.Flags().BoolVar(&plan.AllowReplace, "allow-replace", false, "plan replacing retained resources, which leaves the old ones in place")
	cmd.Flags().BoolVar(&plan.Refresh, "refresh", false, "read every resource as it is, to find what was deleted or changed outside koldstart")
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
	cmd.Flags().BoolVar(&plan.AllowReplace, "allow-replace", false, "replace retained resources, which leaves the old ones in place")
	cmd.Flags().BoolVar(&plan.Refresh, "refresh", false, "read every resource as it is, and create again or set back what was deleted or changed outside koldstart")
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
			e.OnEvent = p.event
			err = e.Apply(ctx, plan, nodes, engine.NewStore(s.backend, s.state, s.etag, whoIsDeploying()))
			if err != nil {
				err = applyFailed(ctx, err, "deploy function -f "+function)
			}
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
		return nil, nil, nil, fmt.Errorf("the configuration of %s changed too (%s): run deploy", function, strings.Join(change.Fields, ", "))
	}
	only.Changes = []engine.Change{change}
	return e, only, nodes, nil
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

// lockHolder says who holds a stage's lock, doing what and since when: the time alone when it
// was taken today.
func lockHolder(app, stage string, held state.LockInfo, now time.Time) string {
	since, today := held.Since.In(now.Location()), now
	at := since.Format(time.DateTime)
	if since.YearDay() == today.YearDay() && since.Year() == today.Year() {
		at = since.Format(time.TimeOnly)
	}
	who := held.Who
	if held.Where != "" {
		who += " on " + held.Where
	}
	return fmt.Sprintf("%s/%s is locked: %s has been running %s since %s, %s ago", app, stage, who, held.Operation, at, ago(now.Sub(held.Since)))
}

// ago writes how long ago something happened, to the second under an hour and to the minute
// after.
func ago(d time.Duration) string {
	if d < time.Hour {
		return max(d, 0).Round(time.Second).String()
	}
	return strings.TrimSuffix(d.Round(time.Minute).String(), "0s")
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
