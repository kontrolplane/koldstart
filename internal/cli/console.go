package cli

import (
	"bytes"
	"cmp"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	lambdatypes "github.com/aws/aws-sdk-go-v2/service/lambda/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/kontrolplane/koldstart/internal/config"
	"github.com/kontrolplane/koldstart/internal/engine"
	"github.com/kontrolplane/koldstart/internal/provider/cloudcontrol"
	"github.com/kontrolplane/koldstart/internal/state"
)

// Console is what the terminal interface works with: every app and stage in the state buckets
// of the account, and the project in this directory, when there is one. It is safe to use from
// several goroutines.
type Console struct {
	name    string
	opts    *Options
	sess    *session
	project *project
	// projectErr is why the configuration here did not load.
	projectErr error

	mu       sync.Mutex
	backends map[string]*regionBackend
}

// regionBackend is the session and state bucket of one region, or no bucket when the region
// has none.
type regionBackend struct {
	sess    *session
	backend *state.S3
}

// StageRef names a stage of an app, in the region its state is in.
type StageRef struct {
	Region string
	App    string
	Stage  string
}

func (r StageRef) String() string { return r.App + "/" + r.Stage }

// StageSummary is what the list of stages shows of one.
type StageSummary struct {
	StageRef
	DeployedAt time.Time
	DeployedBy string
	Resources  int
	Retained   int
	Lock       *state.LockInfo
	// Err is why the stage's state or lock could not be read.
	Err error
}

// Identity is who the console works as, and where.
type Identity struct {
	Profile string
	Account string
	Caller  string
	Region  string
	Bucket  string
}

// Invocation is what a function returned.
type Invocation struct {
	Payload []byte
	// Logs is the end of its log in aws, or all of its output on this machine.
	Logs string
	// Error is the kind of error the function failed with, or "".
	Error    string
	Duration time.Duration
}

// maxLogEvents bounds the log lines one read returns, keeping the newest.
const maxLogEvents = 2000

// openConsole connects to the account of the global flags. The configuration here is loaded
// when there is one, for its app and region; one that does not load is reported, not fatal.
func openConsole(ctx context.Context, name string, opts *Options) (*Console, error) {
	c := &Console{name: name, opts: opts, backends: map[string]*regionBackend{}}
	region := opts.Region
	if _, err := os.Stat(opts.Config); err == nil {
		p, err := loadProject(ctx, opts)
		if err != nil {
			c.projectErr = err
		} else {
			c.project = p
			region = p.Region
		}
	}
	sess, err := openSession(ctx, opts, region)
	if err != nil {
		return nil, err
	}
	c.sess = sess
	return c, nil
}

// Identity says who the console works as, and where.
func (c *Console) Identity() Identity {
	return Identity{
		Profile: cmp.Or(c.opts.Profile, "default"),
		Account: c.sess.Account,
		Caller:  c.sess.Caller,
		Region:  c.sess.Region,
		Bucket:  state.BucketName(c.sess.Account, c.sess.Region),
	}
}

// Project returns the app of the configuration here and the stage of --stage, or no app without
// one, and why the configuration did not load when it did not.
func (c *Console) Project() (app, stage string, err error) {
	if c.project == nil {
		return "", c.opts.Stage, c.projectErr
	}
	return c.project.App, c.opts.Stage, nil
}

// Local reports whether the stage is of the project here, so it can be planned, deployed and
// run on this machine.
func (c *Console) Local(ref StageRef) bool {
	return c.project != nil && c.project.App == ref.App
}

// EventsDir is where the events to invoke a function with are kept: .koldstart/events/<function>
// in the project, or in this directory without one.
func (c *Console) EventsDir(function string) string {
	root := "."
	if c.project != nil {
		root = c.project.Root
	}
	return filepath.Join(root, ".koldstart", "events", function)
}

// backend returns the state bucket of a region, or nil when it has none. Only a bucket that
// exists is kept: a region without one is looked at again, as a first deploy there creates it
// while the interface is open.
func (c *Console) backend(ctx context.Context, region string) (*regionBackend, error) {
	c.mu.Lock()
	rb, ok := c.backends[region]
	c.mu.Unlock()
	if ok {
		return rb, nil
	}
	sess := c.sess.in(region)
	b, exists, err := sess.backend(ctx, false)
	if err != nil {
		return nil, err
	}
	rb = &regionBackend{sess: sess}
	if !exists {
		return rb, nil
	}
	rb.backend = b
	c.mu.Lock()
	c.backends[region] = rb
	c.mu.Unlock()
	return rb, nil
}

// regions lists the regions with a state bucket of the account, from its buckets, and the
// session's own. Without permission to list buckets, it is the session's region alone.
func (c *Console) regions(ctx context.Context) []string {
	prefix := state.BucketPrefix(c.sess.Account)
	regions := []string{c.sess.Region}
	p := s3.NewListBucketsPaginator(c.sess.S3, &s3.ListBucketsInput{Prefix: aws.String(prefix)})
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			slog.Debug("listing the state buckets, using the session's region", "error", err)
			return regions
		}
		for _, b := range page.Buckets {
			if name := aws.ToString(b.Name); strings.HasPrefix(name, prefix) {
				regions = append(regions, strings.TrimPrefix(name, prefix))
			}
		}
	}
	slices.Sort(regions)
	return slices.Compact(regions)
}

// Stages lists every stage of every app with a state in the account, sorted by app, stage and
// region. A stage whose state cannot be read is listed with the error.
func (c *Console) Stages(ctx context.Context) ([]StageSummary, error) {
	var refs []StageRef
	backends := map[string]*state.S3{}
	for _, region := range c.regions(ctx) {
		rb, err := c.backend(ctx, region)
		if err != nil {
			return nil, err
		}
		if rb.backend == nil {
			continue
		}
		names, err := rb.backend.Stages(ctx)
		if err != nil {
			return nil, err
		}
		backends[region] = rb.backend
		for _, name := range names {
			app, stage, ok := strings.Cut(name, "/")
			if ok {
				refs = append(refs, StageRef{Region: region, App: app, Stage: stage})
			}
		}
	}
	out := make([]StageSummary, len(refs))
	var wg sync.WaitGroup
	limit := make(chan struct{}, 8)
	for i, ref := range refs {
		wg.Go(func() {
			limit <- struct{}{}
			defer func() { <-limit }()
			out[i] = summarise(ctx, backends[ref.Region], ref)
		})
	}
	wg.Wait()
	slices.SortFunc(out, func(a, b StageSummary) int {
		return cmp.Or(cmp.Compare(a.App, b.App), cmp.Compare(a.Stage, b.Stage), cmp.Compare(a.Region, b.Region))
	})
	return out, nil
}

func summarise(ctx context.Context, b *state.S3, ref StageRef) StageSummary {
	sum := StageSummary{StageRef: ref}
	st, _, err := b.Read(ctx, ref.App, ref.Stage)
	if err != nil {
		sum.Err = err
		return sum
	}
	if st != nil {
		sum.DeployedAt, sum.DeployedBy = st.DeployedAt, st.DeployedBy
		sum.Resources, sum.Retained = len(st.Resources), len(st.Retained)
	}
	sum.Lock, sum.Err = b.LockInfo(ctx, ref.App, ref.Stage)
	return sum
}

// Stage reads a stage's state and who holds its lock. The state is nil for a stage that has none.
func (c *Console) Stage(ctx context.Context, ref StageRef) (*state.State, *state.LockInfo, error) {
	rb, err := c.backend(ctx, ref.Region)
	if err != nil || rb.backend == nil {
		return nil, nil, err
	}
	st, _, err := rb.backend.Read(ctx, ref.App, ref.Stage)
	if err != nil {
		return nil, nil, err
	}
	lock, err := rb.backend.LockInfo(ctx, ref.App, ref.Stage)
	return st, lock, err
}

// History lists the versions of a stage's state, newest first.
func (c *Console) History(ctx context.Context, ref StageRef) ([]state.ObjectVersion, error) {
	rb, err := c.backend(ctx, ref.Region)
	if err != nil || rb.backend == nil {
		return nil, err
	}
	return rb.backend.History(ctx, ref.App, ref.Stage)
}

// Version reads one version of a stage's state.
func (c *Console) Version(ctx context.Context, ref StageRef, id string) (*state.State, error) {
	rb, err := c.backend(ctx, ref.Region)
	if err != nil {
		return nil, err
	}
	if rb.backend == nil {
		return nil, fmt.Errorf("no state bucket in %s", ref.Region)
	}
	return rb.backend.ReadVersion(ctx, ref.App, ref.Stage, id)
}

// Invoke invokes a deployed function, by its name in lambda, and returns what it returned with
// the end of its log.
func (c *Console) Invoke(ctx context.Context, ref StageRef, function string, payload []byte) (Invocation, error) {
	started := time.Now()
	out, err := c.sess.in(ref.Region).Lambda.Invoke(ctx, &lambda.InvokeInput{
		FunctionName: aws.String(function), Payload: payload, LogType: lambdatypes.LogTypeTail,
	})
	if err != nil {
		return Invocation{}, fmt.Errorf("invoking %s: %w", function, err)
	}
	inv := Invocation{Payload: out.Payload, Error: aws.ToString(out.FunctionError), Duration: time.Since(started)}
	if logs, err := base64.StdEncoding.DecodeString(aws.ToString(out.LogResult)); err == nil {
		inv.Logs = string(logs)
	}
	return inv, nil
}

// InvokeLocal builds a function of the project here and runs it once on this machine, against
// what the stage deployed, as invoke --local does.
func (c *Console) InvokeLocal(ctx context.Context, ref StageRef, function string, payload []byte) (Invocation, error) {
	s, err := c.stage(ctx, ref, false)
	if err != nil {
		return Invocation{}, err
	}
	if err := s.function(function); err != nil {
		return Invocation{}, err
	}
	var output bytes.Buffer
	f, err := s.localFunction(ctx, function, &output)
	if err != nil {
		return Invocation{}, err
	}
	r, err := s.runner().Start(ctx, f)
	if err != nil {
		return Invocation{}, err
	}
	defer func() { _ = r.Close() }()
	started := time.Now()
	result, err := r.Invoke(ctx, payload)
	inv := Invocation{Payload: result, Duration: time.Since(started), Logs: output.String()}
	if err != nil {
		inv.Error = err.Error()
	}
	return inv, nil
}

// stage opens a stage of the project here, in the region its state is in.
func (c *Console) stage(ctx context.Context, ref StageRef, create bool) (*stage, error) {
	if !c.Local(ref) {
		return nil, fmt.Errorf("%s is not the app of the %s here: run %s ui in its directory", ref.App, c.opts.Config, c.name)
	}
	opts := *c.opts
	opts.Stage, opts.Region = ref.Stage, ref.Region
	return openStage(ctx, &opts, create)
}

// stored opens a stage from its state alone, for what needs no configuration: remove.
func (c *Console) stored(ctx context.Context, ref StageRef) (*stage, error) {
	rb, err := c.backend(ctx, ref.Region)
	if err != nil {
		return nil, err
	}
	if rb.backend == nil {
		return nil, fmt.Errorf("no state bucket in %s", ref.Region)
	}
	p := &project{Project: &config.Project{App: ref.App, Stage: ref.Stage, Region: ref.Region}}
	s := &stage{project: p, sess: rb.sess, backend: rb.backend, exists: true}
	return s, s.read(ctx)
}

// Plan plans a deploy of a stage of the project here, or with function, of that function's
// code. It builds the functions and uploads nothing.
func (c *Console) Plan(ctx context.Context, ref StageRef, function string) (*engine.Plan, error) {
	s, err := c.stage(ctx, ref, false)
	if err != nil {
		return nil, err
	}
	if function != "" {
		_, plan, _, err := s.planFunction(ctx, function, false)
		return plan, err
	}
	_, plan, _, err := s.plan(ctx, false, false)
	return plan, err
}

// Deploy takes the stage's lock, plans again with the zips uploaded, and applies the plan when
// it is the one shown, telling onEvent as each node starts and finishes. When the configuration
// or the state changed since, it applies nothing and returns the new plan, with applied false.
func (c *Console) Deploy(ctx context.Context, ref StageRef, function string, shown *engine.Plan, onEvent func(engine.Event)) (plan *engine.Plan, applied bool, err error) {
	s, err := c.stage(ctx, ref, true)
	if err != nil {
		return nil, false, err
	}
	operation := "deploy"
	if function != "" {
		operation += " function " + function
	}
	unlock, err := s.lock(ctx, operation)
	if err != nil {
		return nil, false, err
	}
	defer unlock()
	var e *engine.Engine
	var nodes []engine.Node
	if function != "" {
		e, plan, nodes, err = s.planFunction(ctx, function, true)
	} else {
		e, plan, nodes, err = s.plan(ctx, true, false)
	}
	if err != nil || !samePlan(plan, shown) {
		return plan, false, err
	}
	if function == "" {
		_, err = s.applyPlan(ctx, e, plan, nodes, onEvent)
		return plan, true, c.existsHint(err)
	}
	e.OnEvent = onEvent
	if plan.HasChanges() {
		err = e.Apply(ctx, plan, nodes, engine.NewStore(s.backend, s.state, s.etag, whoIsDeploying()))
	}
	return plan, true, err
}

// existsHint says how to take over a resource that exists already, outside koldstart.
func (c *Console) existsHint(err error) error {
	if exists, ok := errors.AsType[*cloudcontrol.ExistsError](err); ok {
		resource := strings.TrimPrefix(exists.Node, "resource/")
		return fmt.Errorf("%w\ntake it over with %s import %s <identifier>, or give it another name", err, c.name, resource)
	}
	return err
}

// PlanRemove plans removing a stage: what is deleted, and what is left in place.
func (c *Console) PlanRemove(ctx context.Context, ref StageRef) (*engine.Plan, error) {
	s, err := c.stored(ctx, ref)
	if err != nil {
		return nil, err
	}
	_, plan, err := s.planRemove(ctx)
	return plan, err
}

// Remove takes the stage's lock and removes it, when the plan is still the one shown, as Deploy
// does. gone reports whether nothing of the stage is left, its state deleted too.
func (c *Console) Remove(ctx context.Context, ref StageRef, shown *engine.Plan, onEvent func(engine.Event)) (plan *engine.Plan, applied, gone bool, err error) {
	s, err := c.stored(ctx, ref)
	if err != nil {
		return nil, false, false, err
	}
	unlock, err := s.lock(ctx, "remove")
	if err != nil {
		return nil, false, false, err
	}
	defer unlock()
	e, plan, err := s.planRemove(ctx)
	if err != nil || !samePlan(plan, shown) {
		return plan, false, false, err
	}
	retained, err := s.applyRemove(ctx, e, plan, onEvent)
	return plan, true, err == nil && len(retained) == 0, err
}

// Unlock releases a stage's lock.
func (c *Console) Unlock(ctx context.Context, ref StageRef) error {
	rb, err := c.backend(ctx, ref.Region)
	if err != nil {
		return err
	}
	if rb.backend == nil {
		return fmt.Errorf("%s: %w", ref, state.ErrNotLocked)
	}
	return rb.backend.Unlock(ctx, ref.App, ref.Stage)
}

// samePlan reports whether two plans make the same changes, whatever the state's serial.
func samePlan(a, b *engine.Plan) bool {
	if a == nil || b == nil {
		return a == b
	}
	return reflect.DeepEqual(a.Changes, b.Changes)
}
