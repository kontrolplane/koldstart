package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/kontrolplane/koldstart/internal/state"
)

// Store is a stage's state while it is applied: every change is written straight away, each
// write conditional on the one before, so a deploy that dies leaves state that matches what
// exists, and a second deploy that wrote in between is noticed rather than overwritten.
type Store struct {
	Backend state.Backend
	ETag    string
	Who     string

	mu    sync.Mutex
	state *state.State
}

// NewStore wraps the state read with etag.
func NewStore(b state.Backend, st *state.State, etag, who string) *Store {
	return &Store{Backend: b, ETag: etag, Who: who, state: st}
}

// Snapshot returns a copy of the state as it is now.
func (s *Store) Snapshot() *state.State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state.Clone()
}

// Update changes the state with fn and writes it.
func (s *Store) Update(ctx context.Context, fn func(st *state.State)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	fn(s.state)
	s.state.Serial++
	s.state.DeployedAt = time.Now().UTC()
	s.state.DeployedBy = s.Who
	etag, err := s.Backend.Write(ctx, s.state, s.ETag)
	if err != nil {
		return err
	}
	s.ETag = etag
	return nil
}

// Apply makes the plan's changes in dependency order, nodes of one level at once, and the
// deletes last, in reverse. It stops after the first level with a failure; what was done is in
// the state, so the next deploy plans from there.
func (e *Engine) Apply(ctx context.Context, plan *Plan, nodes []Node, store *Store) error {
	if snap := store.Snapshot(); snap.Serial != plan.Serial {
		return fmt.Errorf("the state changed since the plan was made: plan again")
	}
	levels, err := order(nodes)
	if err != nil {
		return err
	}
	changes := map[string]Change{}
	for _, c := range plan.Changes {
		changes[c.ID] = c
	}

	for _, level := range levels {
		var todo []func() error
		for _, n := range level {
			c, ok := changes[n.ID]
			if !ok || c.Action == NoOp {
				continue
			}
			todo = append(todo, func() error { return e.applyNode(ctx, n, c, store) })
		}
		if err := e.run(todo); err != nil {
			return err
		}
	}
	return e.applyDeletes(ctx, plan, store)
}

// run calls the functions, at most Concurrency at once, and returns their errors joined.
func (e *Engine) run(todo []func() error) error {
	limit := max(e.Concurrency, 1)
	sem := make(chan struct{}, limit)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var errs []error
	for _, fn := range todo {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			if err := fn(); err != nil {
				mu.Lock()
				errs = append(errs, err)
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	return errors.Join(errs...)
}

func (e *Engine) event(ev Event) {
	if e.OnEvent != nil {
		e.OnEvent(ev)
	}
}

func (e *Engine) tags(id string, app, stage string) map[string]string {
	tags := maps.Clone(e.Tags)
	if tags == nil {
		tags = map[string]string{}
	}
	tags["koldstart:app"] = app
	tags["koldstart:stage"] = stage
	tags["koldstart:node"] = id
	return tags
}

// applyNode creates or updates one node, its refs resolved against the state as it is now that
// what it depends on is applied.
func (e *Engine) applyNode(ctx context.Context, n Node, c Change, store *Store) (err error) {
	start := time.Now()
	p, err := e.provider(n.Type)
	if err != nil {
		return err
	}
	snap := store.Snapshot()
	inputs, err := normalise(n.Inputs)
	if err != nil {
		return err
	}
	resolved, err := resolveRefs(inputs, snap, nil)
	if err != nil {
		return fmt.Errorf("%s: %w", n.ID, err)
	}
	if hasUnknown(resolved) {
		return fmt.Errorf("%s: refers to something that does not exist after applying what it depends on", n.ID)
	}
	withSecrets, err := e.resolveSecrets(ctx, resolved)
	if err != nil {
		return fmt.Errorf("%s: %w", n.ID, err)
	}
	hash, err := hashOf(n.Type, withSecrets)
	if err != nil {
		return err
	}
	desired, err := json.Marshal(withSecrets)
	if err != nil {
		return err
	}
	recorded, err := json.Marshal(resolved)
	if err != nil {
		return err
	}

	prior := snap.Resources[n.ID]
	if prior == nil && snap.Retained != nil {
		prior = snap.Retained[n.ID]
	}
	action := c.Action
	switch {
	case prior == nil:
		action = Create
	case prior.Pending != nil:
		action = Update
	case c.Action == Adopt:
	case prior.Hash == hash:
		return nil // what it depends on came out the same after all
	case p.Replaces(prior.Inputs, recorded):
		if prior.DeletionPolicy == Retain && !e.AllowReplace {
			return fmt.Errorf("%s: the change needs a new resource, and the old one is retained: pass --allow-replace", n.ID)
		}
		action = Replace
	default:
		action = Update
	}

	e.event(Event{ID: n.ID, Action: action})
	defer func() {
		e.event(Event{ID: n.ID, Action: action, Done: true, Err: err, Duration: time.Since(start)})
	}()

	req := Request{ID: n.ID, Name: n.Name, Inputs: desired, Tags: e.tags(n.ID, snap.App, snap.Stage)}
	if prior != nil {
		req.PhysicalID, req.Prior, req.Attributes = prior.PhysicalID, prior.Inputs, prior.Attributes
		if prior.Pending != nil {
			req.Token = prior.Pending.Token
		}
	}
	req.Progress = func(token string) error {
		return store.Update(ctx, func(st *state.State) {
			if r := st.Resources[n.ID]; r != nil && r.Pending != nil {
				r.Pending.Token = token
			}
		})
	}

	// Recorded before the call, so a deploy that dies during it knows the resource may exist.
	if err := store.Update(ctx, func(st *state.State) {
		r := &state.Resource{Type: n.Type, PhysicalID: n.Name}
		if prior != nil {
			r = cloneResource(prior)
		}
		r.Pending = &state.Pending{Operation: string(action), StartedAt: time.Now().UTC(), Token: req.Token}
		st.Resources[n.ID] = r
		if st.Retained != nil {
			delete(st.Retained, n.ID)
		}
	}); err != nil {
		return fmt.Errorf("%s: recording the %s: %w", n.ID, action, err)
	}

	res, err := e.call(ctx, p, action, prior, req)
	if err != nil {
		return fmt.Errorf("%s: %w", n.ID, err)
	}
	if err := store.Update(ctx, func(st *state.State) {
		st.Resources[n.ID] = &state.Resource{
			Type:           n.Type,
			PhysicalID:     res.PhysicalID,
			Inputs:         recorded,
			Hash:           hash,
			Attributes:     res.Attributes,
			Dependencies:   n.Deps(),
			DeletionPolicy: n.DeletionPolicy,
		}
	}); err != nil {
		return fmt.Errorf("%s: recording it: %w", n.ID, err)
	}
	return nil
}

// call runs the provider for action. A create of something that exists, and an operation a
// crashed deploy left pending, read the resource first and update it when it is there.
func (e *Engine) call(ctx context.Context, p Provider, action Action, prior *state.Resource, req Request) (Result, error) {
	switch action {
	case Create:
		res, err := p.Create(ctx, req)
		if !errors.Is(err, ErrExists) {
			return res, err
		}
		return e.adopt(ctx, p, req)
	case Replace:
		if err := p.Delete(ctx, req); err != nil {
			return Result{}, fmt.Errorf("deleting the old one: %w", err)
		}
		req.PhysicalID, req.Prior, req.Attributes = "", nil, nil
		return p.Create(ctx, req)
	default:
		if prior != nil && prior.Pending != nil {
			return e.adopt(ctx, p, req)
		}
		return p.Update(ctx, req)
	}
}

// adopt reads the resource the request names, and updates it to the inputs when it exists, or
// creates it when it does not.
func (e *Engine) adopt(ctx context.Context, p Provider, req Request) (Result, error) {
	if req.PhysicalID == "" {
		req.PhysicalID = req.Name
	}
	live, ok, err := p.Read(ctx, req)
	if err != nil {
		return Result{}, err
	}
	if !ok {
		req.PhysicalID, req.Prior = "", nil
		return p.Create(ctx, req)
	}
	req.PhysicalID = live.PhysicalID
	req.Attributes = live.Attributes
	return p.Update(ctx, req)
}

// applyDeletes deletes, or leaves in place, the nodes gone from the configuration, each after
// everything the state says depends on it.
func (e *Engine) applyDeletes(ctx context.Context, plan *Plan, store *Store) error {
	todo := map[string]Change{}
	for _, c := range plan.Changes {
		if c.Action == Delete || c.Action == Keep {
			todo[c.ID] = c
		}
	}
	for len(todo) > 0 {
		snap := store.Snapshot()
		var ready []Change
		for _, id := range slices.Sorted(maps.Keys(todo)) {
			needed := false
			for other := range todo {
				if r := snap.Resources[other]; other != id && r != nil && slices.Contains(r.Dependencies, id) {
					needed = true
					break
				}
			}
			if !needed {
				ready = append(ready, todo[id])
			}
		}
		if len(ready) == 0 {
			return errors.New("the resources to delete depend on each other in a cycle")
		}
		var fns []func() error
		for _, c := range ready {
			delete(todo, c.ID)
			fns = append(fns, func() error { return e.deleteNode(ctx, c, store) })
		}
		if err := e.run(fns); err != nil {
			return err
		}
	}
	return nil
}

func (e *Engine) deleteNode(ctx context.Context, c Change, store *Store) (err error) {
	start := time.Now()
	snap := store.Snapshot()
	prior := snap.Resources[c.ID]
	if prior == nil {
		return nil
	}
	e.event(Event{ID: c.ID, Action: c.Action})
	defer func() {
		e.event(Event{ID: c.ID, Action: c.Action, Done: true, Err: err, Duration: time.Since(start)})
	}()

	if c.Action == Keep {
		return store.Update(ctx, func(st *state.State) {
			if st.Retained == nil {
				st.Retained = map[string]*state.Resource{}
			}
			kept := cloneResource(prior)
			kept.Pending, kept.Dependencies = nil, nil
			st.Retained[c.ID] = kept
			delete(st.Resources, c.ID)
		})
	}

	p, err := e.provider(prior.Type)
	if err != nil {
		return err
	}
	if err := store.Update(ctx, func(st *state.State) {
		st.Resources[c.ID].Pending = &state.Pending{Operation: string(Delete), StartedAt: time.Now().UTC()}
	}); err != nil {
		return fmt.Errorf("%s: recording the delete: %w", c.ID, err)
	}
	req := Request{ID: c.ID, PhysicalID: prior.PhysicalID, Prior: prior.Inputs, Attributes: prior.Attributes}
	if err := p.Delete(ctx, req); err != nil {
		return fmt.Errorf("%s: %w", c.ID, err)
	}
	return store.Update(ctx, func(st *state.State) { delete(st.Resources, c.ID) })
}

func cloneResource(r *state.Resource) *state.Resource {
	c := *r
	c.Attributes = maps.Clone(r.Attributes)
	c.Dependencies = slices.Clone(r.Dependencies)
	if r.Pending != nil {
		p := *r.Pending
		c.Pending = &p
	}
	return &c
}
