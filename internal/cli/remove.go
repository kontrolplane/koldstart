package cli

import (
	"fmt"
	"maps"
	"slices"

	"github.com/spf13/cobra"

	"github.com/kontrolplane/koldstart/internal/engine"
	"github.com/kontrolplane/koldstart/internal/state"
)

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
			question := fmt.Sprintf("remove everything of %s/%s in %s? type the stage's name:", s.App, s.Stage, s.Region)
			if err := confirm(cmd, yes, question, s.Stage); err != nil {
				return err
			}
			unlock, err := s.lock(ctx, "remove")
			if err != nil {
				return err
			}
			defer unlock()
			e := s.engine()
			plan, err := e.Plan(ctx, nil, s.state)
			if err != nil {
				return err
			}
			e.OnEvent = progress(cmd.ErrOrStderr())
			store := engine.NewStore(s.backend, s.state, s.etag, whoIsDeploying())
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
			var held *state.LockInfo
			if s.exists {
				if held, err = s.backend.LockInfo(ctx, s.App, s.Stage); err != nil {
					return err
				}
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
