package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/user"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/kontrolplane/koldstart/internal/build"
	"github.com/kontrolplane/koldstart/internal/desired"
	"github.com/kontrolplane/koldstart/internal/engine"
	"github.com/kontrolplane/koldstart/internal/provider/cloudcontrol"
	"github.com/kontrolplane/koldstart/internal/resolve"
	"github.com/kontrolplane/koldstart/internal/state"
)

// stage is a loaded configuration in its aws session, with its state.
type stage struct {
	*project
	sess    *session
	backend *state.S3
	// exists says whether the state bucket exists.
	exists bool
	state  *state.State
	etag   string
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

// engine returns the engine for the stage, reading the outputs of other stages from its bucket.
func (s *stage) engine() *engine.Engine {
	e := s.sess.engine(s.Tags)
	e.Outputs = &resolve.Outputs{Backend: s.backend}
	return e
}

// read reads the stage's state, or starts an empty one for a stage never deployed.
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

// lock takes the stage's lock and reads the state again, as it may have changed while waiting
// for the lock. It returns the function that releases the lock, also when ctx was cancelled by
// ctrl+c.
func (s *stage) lock(ctx context.Context, operation string) (func(), error) {
	if err := s.backend.Lock(ctx, s.App, s.Stage, lockInfo(operation)); err != nil {
		return nil, err
	}
	unlock := func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		_ = s.backend.Unlock(ctx, s.App, s.Stage)
	}
	if err := s.read(ctx); err != nil {
		unlock()
		return nil, err
	}
	return unlock, nil
}

// function checks the stage has a function of the name, and says which it has when it does not.
func (s *stage) function(name string) error {
	if _, ok := s.Functions[name]; ok {
		return nil
	}
	return fmt.Errorf("no function %q in %s; it has %s", name, s.Path, list(slices.Sorted(maps.Keys(s.Functions))))
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
			return nil, fmt.Errorf("reading the zip of %s: %w", name, err)
		}
		if err := s.backend.PutZip(ctx, s.App, s.Stage, arc.SHA256, data); err != nil {
			return nil, err
		}
	}
	return archives, nil
}

// nodes are what the engine applies for the stage, with the functions built as archives says.
func (s *stage) nodes(archives map[string]build.Archive) ([]engine.Node, error) {
	return desired.Build(s.Project, desired.Target{
		Account: s.sess.Account, Region: s.Region, Bucket: state.BucketName(s.sess.Account, s.Region), Archives: archives,
	})
}

// plan builds every function, uploading the zips when upload is set, and plans the stage.
func (s *stage) plan(ctx context.Context, upload, allowReplace bool) (*engine.Engine, *engine.Plan, []engine.Node, error) {
	if err := s.checkTypes(ctx); err != nil {
		return nil, nil, nil, err
	}
	archives, err := s.buildAll(ctx, nil, upload)
	if err != nil {
		return nil, nil, nil, err
	}
	nodes, err := s.nodes(archives)
	if err != nil {
		return nil, nil, nil, err
	}
	e := s.engine()
	e.AllowReplace = allowReplace
	plan, err := e.Plan(ctx, nodes, s.state)
	if err != nil {
		return nil, nil, nil, err
	}
	return e, plan, nodes, nil
}

// apply applies the plan with progress on stderr, then prints the outputs. A resource that
// exists already, outside koldstart, says how to take it over.
func (s *stage) apply(ctx context.Context, cmd *cobra.Command, e *engine.Engine, plan *engine.Plan, nodes []engine.Node) error {
	outputs, err := s.applyPlan(ctx, e, plan, nodes, progress(cmd.ErrOrStderr()))
	if exists, ok := errors.AsType[*cloudcontrol.ExistsError](err); ok {
		resource := strings.TrimPrefix(exists.Node, "resource/")
		return fmt.Errorf("%w\ntake it over with %s import %s <identifier>, or give it another name", err, cmd.Root().Name(), resource)
	}
	if err != nil {
		return err
	}
	printOutputs(cmd.OutOrStdout(), outputs)
	return nil
}

// applyPlan applies the plan, telling onEvent as each node starts and finishes, then records
// the outputs in the state and returns them.
func (s *stage) applyPlan(ctx context.Context, e *engine.Engine, plan *engine.Plan, nodes []engine.Node, onEvent func(engine.Event)) (map[string]string, error) {
	store := engine.NewStore(s.backend, s.state, s.etag, whoIsDeploying())
	e.OnEvent = onEvent
	if plan.HasChanges() {
		if err := e.Apply(ctx, plan, nodes, store); err != nil {
			if _, ok := errors.AsType[*cloudcontrol.ExistsError](err); ok {
				return nil, err
			}
			return nil, fmt.Errorf("%w\nwhat was done is in the state: deploy again to finish", err)
		}
	}
	outputs, err := s.outputs(store.Snapshot())
	if err != nil {
		return nil, err
	}
	if err := store.Update(ctx, func(st *state.State) { st.Outputs = outputs }); err != nil {
		return nil, err
	}
	return outputs, nil
}

// confirm asks a question, and goes on when the answer is one of accepted, or y or yes when
// there are none. Without a terminal it refuses, unless yes is set.
func confirm(cmd *cobra.Command, yes bool, question string, accepted ...string) error {
	if yes {
		return nil
	}
	if !isTerminal(os.Stdin) {
		return errors.New("no terminal to ask on: pass --yes")
	}
	if accepted == nil {
		accepted = []string{"y", "yes"}
		question += " [y/N]"
	}
	_, _ = fmt.Fprint(cmd.ErrOrStderr(), question+" ")
	answer, _ := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
	if !slices.Contains(accepted, strings.ToLower(strings.TrimSpace(answer))) {
		return errors.New("cancelled")
	}
	return nil
}

func isTerminal(f *os.File) bool {
	info, err := f.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

// whoIsDeploying names the person or ci job running koldstart, for the lock and the state.
func whoIsDeploying() string {
	if os.Getenv("GITHUB_ACTIONS") == "true" {
		return os.Getenv("GITHUB_ACTOR")
	}
	if u, err := user.Current(); err == nil {
		return u.Username
	}
	return "someone"
}

// lockInfo says who is deploying, and from where, for the lock.
func lockInfo(operation string) state.LockInfo {
	where, _ := os.Hostname()
	if os.Getenv("GITHUB_ACTIONS") == "true" {
		where = os.Getenv("GITHUB_SERVER_URL") + "/" + os.Getenv("GITHUB_REPOSITORY") + "/actions/runs/" + os.Getenv("GITHUB_RUN_ID")
	}
	return state.LockInfo{Who: whoIsDeploying(), Where: where, Operation: operation, Since: time.Now().UTC()}
}

// list names things, or says there are none.
func list(names []string) string {
	if len(names) == 0 {
		return "none"
	}
	return strings.Join(names, ", ")
}
