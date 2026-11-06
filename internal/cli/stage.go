package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"os/user"
	"slices"
	"strings"
	"time"

	"github.com/charmbracelet/x/term"
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
	// registry is the ecr registry container images go in, read when a function has one.
	registry registry
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
	e.Outputs = &resolve.Outputs{Backend: s.backend, Elsewhere: s.sess.stateIn}
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
	return fmt.Errorf("no function %q in %s; %s", name, s.shownPath(), has(slices.Sorted(maps.Keys(s.Functions))))
}

// buildAll builds the functions named, or all of them, and uploads their zips when upload is set.
func (s *stage) buildAll(ctx context.Context, names []string, upload bool) (map[string]build.Archive, error) {
	if names == nil {
		names = slices.Sorted(maps.Keys(s.Functions))
	}
	b := build.Builder{Root: s.Root, Stage: s.Stage}
	archives := map[string]build.Archive{}
	for _, name := range names {
		fn := s.Functions[name]
		arc, err := b.Build(ctx, name, fn)
		if err != nil {
			return nil, err
		}
		archives[name] = arc
		if arc.Image {
			if _, err := s.imageRegistry(ctx); err != nil {
				return nil, err
			}
			if upload {
				if err := s.pushImage(ctx, name, fn, arc); err != nil {
					return nil, err
				}
			}
			continue
		}
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
		ImageRegistry: s.registry.host,
	})
}

// planOptions change how a stage is planned.
type planOptions struct {
	// AllowReplace plans replacing retained resources, which leaves the old ones in place.
	AllowReplace bool
	// Refresh reads every resource as it is, to create again what was deleted outside koldstart
	// and set back what was changed.
	Refresh bool
}

// plan builds every function, uploading the zips when upload is set, and plans the stage.
func (s *stage) plan(ctx context.Context, upload bool, opts planOptions) (*engine.Engine, *engine.Plan, []engine.Node, error) {
	if err := s.checkResources(ctx); err != nil {
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
	e.AllowReplace, e.Refresh = opts.AllowReplace, opts.Refresh
	plan, err := e.Plan(ctx, nodes, s.state)
	if err != nil {
		return nil, nil, nil, listed(err, "changes cannot be planned")
	}
	return e, plan, nodes, nil
}

// apply applies the plan with progress on stderr, then says what it did and prints the
// outputs. A resource that exists already, outside koldstart, says how to take it over.
func (s *stage) apply(ctx context.Context, cmd *cobra.Command, e *engine.Engine, plan *engine.Plan, nodes []engine.Node) error {
	p := s.beginProgress(cmd, plan)
	outputs, err := s.applyPlan(ctx, e, plan, nodes, p.event)
	if err := s.endProgress(cmd, p, plan, "deployed", err); err != nil {
		if exists, ok := errors.AsType[*cloudcontrol.ExistsError](err); ok {
			resource := strings.TrimPrefix(exists.Node, "resource/")
			return fmt.Errorf("%w\ntake it over with %s import %s <identifier>, or give it another name", err, cmd.Root().Name(), resource)
		}
		return err
	}
	printOutputs(cmd.OutOrStdout(), outputs)
	return nil
}

// beginProgress starts showing the plan being applied on stderr, after a blank line.
func (s *stage) beginProgress(cmd *cobra.Command, plan *engine.Plan) *progress {
	p := newProgress(cmd.ErrOrStderr(), plan)
	if plan.HasChanges() {
		_, _ = fmt.Fprintln(cmd.ErrOrStderr())
	}
	p.begin()
	return p
}

// endProgress stops showing progress and says what applying the plan did, and how long it took:
// on stdout as done, such as deployed, or on stderr when err says it failed or was stopped. It
// returns err.
func (s *stage) endProgress(cmd *cobra.Command, p *progress, plan *engine.Plan, done string, err error) error {
	took := Duration(p.end())
	switch {
	case !plan.HasChanges() && err == nil:
	case errors.Is(err, errStopped):
		_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "\nstopped after %s: %s\n", took, p.summary())
	case err != nil:
		_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "\nfailed after %s: %s\n", took, p.summary())
	default:
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "\n%s %s/%s in %s: %s\n", done, s.App, s.Stage, took, p.summary())
	}
	return err
}

// errStopped is an apply stopped by ctrl+c.
var errStopped = errors.New("stopped")

// applyFailed wraps what applying a plan returned, saying what to run to finish it: deploy or
// remove. An apply stopped by ctrl+c says so rather than listing each call it cut short.
func applyFailed(ctx context.Context, err error, again string) error {
	if ctx.Err() != nil {
		return fmt.Errorf("%w: what was done is in the state, %s again to finish", errStopped, again)
	}
	return fmt.Errorf("%w\nwhat was done is in the state: %s again to finish", listed(err, "changes failed"), again)
}

// errorList is errors of several changes at once, one a line under how many there are and what
// went wrong with them.
type errorList struct {
	what string
	errs []error
}

func (e *errorList) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%d %s:", len(e.errs), e.what)
	for _, err := range e.errs {
		b.WriteString("\n  " + strings.ReplaceAll(err.Error(), "\n", "\n  "))
	}
	return b.String()
}

func (e *errorList) Unwrap() []error {
	return e.errs
}

// listed lists the errors err joins, under how many there are and what, such as changes failed,
// or returns err as it is when it is one.
func listed(err error, what string) error {
	if joined, ok := err.(interface{ Unwrap() []error }); ok && len(joined.Unwrap()) > 1 {
		return &errorList{what: what, errs: joined.Unwrap()}
	}
	return err
}

// applyPlan applies the plan, telling onEvent as each node starts and finishes, then records
// the outputs in the state and returns them.
func (s *stage) applyPlan(ctx context.Context, e *engine.Engine, plan *engine.Plan, nodes []engine.Node, onEvent func(engine.Event)) (map[string]string, error) {
	store := engine.NewStore(s.backend, s.state, s.etag, whoIsDeploying())
	e.OnEvent = onEvent
	if plan.HasChanges() {
		if err := e.Apply(ctx, plan, nodes, store); err != nil {
			if _, ok := errors.AsType[*cloudcontrol.ExistsError](err); ok && ctx.Err() == nil {
				return nil, err
			}
			return nil, applyFailed(ctx, err, "deploy")
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
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	return ask(ctx, cmd.InOrStdin(), cmd.ErrOrStderr(), question, accepted...)
}

// ask writes the question to w and reads the answer from r, as confirm does. ctrl+c, which
// cancels ctx, or the end of r cancels without waiting for a line. A name typed wrong says what
// was typed.
func ask(ctx context.Context, r io.Reader, w io.Writer, question string, accepted ...string) error {
	yesNo := accepted == nil
	if yesNo {
		accepted = []string{"y", "yes"}
		question += " [y/N]"
	}
	_, _ = fmt.Fprint(w, question+" ")
	answers := make(chan string, 1)
	go func() {
		line, err := bufio.NewReader(r).ReadString('\n')
		if err != nil {
			_, _ = fmt.Fprintln(w)
		}
		answers <- line
	}()
	cancelled := errors.New("cancelled: nothing was changed")
	select {
	case <-ctx.Done():
		_, _ = fmt.Fprintln(w)
		return cancelled
	case answer := <-answers:
		answer = strings.TrimSpace(answer)
		switch {
		case slices.Contains(accepted, strings.ToLower(answer)):
			return nil
		case !yesNo && answer != "":
			return fmt.Errorf("%q is not %s: nothing was changed", answer, accepted[0])
		}
		return cancelled
	}
}

// isTerminal reports whether f is a terminal, which /dev/null, a character device too, is not.
func isTerminal(f *os.File) bool {
	return f != nil && term.IsTerminal(f.Fd())
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

// has says what the configuration has of something, such as "it has api and worker", or "it
// has none".
func has(names []string) string {
	if len(names) == 0 {
		return "it has none"
	}
	return "it has " + andList(names)
}
