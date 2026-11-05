package cli

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/kontrolplane/koldstart/internal/build"
	"github.com/kontrolplane/koldstart/internal/desired"
	"github.com/kontrolplane/koldstart/internal/engine"
	"github.com/kontrolplane/koldstart/internal/state"
)

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
			_, plan, _, err := s.plan(ctx, false, allowReplace)
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
			e, plan, nodes, err := s.plan(ctx, true, allowReplace)
			if err != nil {
				return err
			}
			printPlan(cmd.OutOrStdout(), s, plan)
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

// deployFunctionCommand deploys the code of one function. The other functions keep the code the
// state says they have.
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
			if err := s.function(function); err != nil {
				return err
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
			for other := range s.Functions {
				if other != function {
					archives[other] = build.Archive{Function: other, SHA256: deployedSHA(s.state, other)}
				}
			}
			nodes, err := s.nodes(archives)
			if err != nil {
				return err
			}
			e := s.engine()
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
			return e.Apply(ctx, only, nodes, engine.NewStore(s.backend, s.state, s.etag, whoIsDeploying()))
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
	_ = json.Unmarshal(r.Inputs, &in)
	return in.Code.SHA256
}
