package cli

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/spf13/cobra"

	"github.com/kontrolplane/koldstart/internal/engine"
	"github.com/kontrolplane/koldstart/internal/state"
)

func removeCommand(name string, opts *Options) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "remove",
		Short: "delete everything the stage deployed",
		Long: `remove prints the plan of deleting everything the stage's state lists, asks you to type the
stage's name, and deletes it in reverse dependency order, holding the stage's lock. Retained
resources, such as tables and buckets, are left in place and listed, and the next deploy of the
stage takes them over again; when nothing is left, the state is deleted too.`,
		Example: fmt.Sprintf(`  %[1]s remove --stage pr-12
  %[1]s remove --stage pr-12 --yes`, name),
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			s, err := openStage(ctx, opts, false)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			nothing := func() error {
				_, err := fmt.Fprintf(out, "nothing of %s/%s is deployed in %s, account %s\n", s.App, s.Stage, s.Region, s.sess.Account)
				return err
			}
			if !s.exists || len(s.state.Resources) == 0 {
				return nothing()
			}
			unlock, err := s.lockFor(cmd, "remove")
			if err != nil {
				return err
			}
			defer unlock()
			if len(s.state.Resources) == 0 {
				return nothing()
			}
			e, plan, err := s.planRemove(ctx)
			if err != nil {
				return err
			}
			printPlan(out, s, plan)
			question := fmt.Sprintf("\nremove %s/%s? type the stage's name to go on:", s.App, s.Stage)
			if err := confirm(cmd, yes, question, s.Stage); err != nil {
				return err
			}
			p := s.beginProgress(cmd, plan)
			retained, err := s.applyRemove(ctx, e, plan, p.event)
			if err := s.endProgress(cmd, p, plan, "removed", err); err != nil {
				return err
			}
			if len(retained) == 0 {
				return nil
			}
			_, _ = fmt.Fprintln(out, "\nleft in place, for the next deploy of the stage to take over again:")
			width := 0
			for id := range retained {
				width = max(width, min(len(id), maxIDWidth))
			}
			for _, id := range slices.Sorted(maps.Keys(retained)) {
				_, _ = fmt.Fprintf(out, "  %s  %s\n", padRight(id, width), retained[id].PhysicalID)
			}
			return nil
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "remove without asking")
	return cmd
}

// planRemove plans deleting everything the stage's state lists.
func (s *stage) planRemove(ctx context.Context) (*engine.Engine, *engine.Plan, error) {
	e := s.engine()
	plan, err := e.Plan(ctx, nil, s.state)
	if err != nil {
		return nil, nil, err
	}
	return e, plan, nil
}

// applyRemove applies a plan of planRemove, telling onEvent as each node starts and finishes. It
// deletes the state when nothing is left, and returns what was left in place.
func (s *stage) applyRemove(ctx context.Context, e *engine.Engine, plan *engine.Plan, onEvent func(engine.Event)) (map[string]*state.Resource, error) {
	e.OnEvent = onEvent
	store := engine.NewStore(s.backend, s.state, s.etag, whoIsDeploying())
	if err := e.Apply(ctx, plan, nil, store); err != nil {
		return nil, applyFailed(ctx, err, "remove")
	}
	final := store.Snapshot()
	if len(final.Retained) == 0 {
		if err := s.backend.Delete(ctx, s.App, s.Stage); err != nil {
			return nil, fmt.Errorf("deleting the state of %s/%s: %w", s.App, s.Stage, err)
		}
		return nil, nil
	}
	if err := store.Update(ctx, func(st *state.State) { st.Outputs = nil }); err != nil {
		return nil, fmt.Errorf("clearing the outputs of %s/%s: %w", s.App, s.Stage, err)
	}
	return final.Retained, nil
}

func unlockCommand(name string, opts *Options) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "unlock",
		Short: "release a stage's lock left by a deploy that did not finish",
		Long: `unlock shows who holds the stage's lock, doing what and since when, asks, and releases it. Make
sure that deploy is really gone: two deploys of one stage at once can leave its state wrong.`,
		Example: fmt.Sprintf(`  %[1]s unlock --stage prod
  %[1]s unlock --stage prod --yes`, name),
		Args: cobra.NoArgs,
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
				return fmt.Errorf("%s/%s: %w: there is nothing to release", s.App, s.Stage, state.ErrNotLocked)
			}
			out := cmd.OutOrStdout()
			_, _ = fmt.Fprintln(out, lockHolder(s.App, s.Stage, *held, time.Now()))
			if err := confirm(cmd, yes, "release it?"); err != nil {
				return err
			}
			if err := s.backend.Unlock(ctx, s.App, s.Stage); err != nil {
				return fmt.Errorf("releasing the lock of %s/%s: %w", s.App, s.Stage, err)
			}
			_, err = fmt.Fprintf(out, "released the lock of %s/%s\n", s.App, s.Stage)
			return err
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "release without asking")
	return cmd
}
