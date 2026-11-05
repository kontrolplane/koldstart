package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
		return errors.New("the state changed since the plan was made: plan again")
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
	tags[TagApp] = app
	tags[TagStage] = stage
	tags[TagNode] = id
	return tags
}

// applyNode creates or updates one node, its refs resolved against the state as it is now that
// what it depends on is applied. When its inputs come out the same as before, nothing is done.
// The operation is recorded as pending before the call, so a deploy that dies during it knows
// the resource may exist.
func (e *Engine) applyNode(ctx context.Context, n Node, c Change, store *Store) (err error) {
	start := time.Now()
	p, err := e.provider(n.Type)
	if err != nil {
		return err
	}
	snap := store.Snapshot()
	in, err := e.prepare(ctx, n, snap)
	if err != nil {
		return fmt.Errorf("%s: %w", n.ID, err)
	}
	prior := snap.Resources[n.ID]
	if prior == nil {
		prior = snap.Retained[n.ID]
	}
	action, err := e.decide(p, c, prior, in.hash, in.stored)
	if err != nil {
		return fmt.Errorf("%s: %w", n.ID, err)
	}
	if action == NoOp {
		return nil
	}

	e.event(Event{ID: n.ID, Action: action})
	defer func() {
		e.event(Event{ID: n.ID, Action: action, Done: true, Err: err, Duration: time.Since(start)})
	}()

	req, startedAt, err := e.request(ctx, n, snap, prior, in.sent, in.externalHash, store)
	if err != nil {
		return fmt.Errorf("%s: %w", n.ID, err)
	}
	if err := e.recordPending(ctx, store, n, prior, action, startedAt, req.Token); err != nil {
		return fmt.Errorf("%s: recording the %s: %w", n.ID, action, err)
	}
	res, err := e.call(ctx, p, action, prior, req)
	if err != nil {
		return fmt.Errorf("%s: %w", n.ID, err)
	}
	if err := e.recordApplied(ctx, store, n, prior, action, res, in); err != nil {
		return fmt.Errorf("%s: recording it: %w", n.ID, err)
	}
	return nil
}

// prepared are a node's inputs as they are applied.
type prepared struct {
	// sent are the inputs with refs, secrets and outputs of other stages resolved: what the
	// provider gets.
	sent json.RawMessage
	// stored are the inputs with refs resolved and secrets and outputs left as references: what
	// the state keeps.
	stored json.RawMessage
	// hash is the hash of what is sent, and externalHash of the secrets and outputs alone.
	hash         string
	externalHash string
}

// prepare resolves a node's inputs against the state, now that what it depends on is applied.
func (e *Engine) prepare(ctx context.Context, n Node, snap *state.State) (prepared, error) {
	inputs, err := normalise(n.Inputs)
	if err != nil {
		return prepared{}, err
	}
	resolved, err := resolveRefs(inputs, snap, nil)
	if err != nil {
		return prepared{}, err
	}
	if hasUnknown(resolved) {
		return prepared{}, errors.New("it refers to something that does not exist after applying what it depends on")
	}
	withExternal, err := e.resolveExternal(ctx, resolved)
	if err != nil {
		return prepared{}, err
	}
	var in prepared
	if in.hash, err = hashOf(n.Type, withExternal); err != nil {
		return prepared{}, err
	}
	if in.externalHash, err = e.externalHash(ctx, resolved); err != nil {
		return prepared{}, err
	}
	if in.sent, err = json.Marshal(withExternal); err != nil {
		return prepared{}, err
	}
	if in.stored, err = json.Marshal(resolved); err != nil {
		return prepared{}, err
	}
	return in, nil
}

// recordPending writes that action on the node has started, before the call to aws.
func (e *Engine) recordPending(ctx context.Context, store *Store, n Node, prior *state.Resource, action Action, startedAt time.Time, token string) error {
	return store.Update(ctx, func(st *state.State) {
		r := &state.Resource{Type: n.Type, PhysicalID: n.Name}
		if prior != nil {
			r = cloneResource(prior)
		}
		r.Pending = &state.Pending{Operation: string(action), StartedAt: startedAt, Token: token}
		st.Resources[n.ID] = r
		delete(st.Retained, n.ID)
	})
}

// recordApplied writes what exists after action on the node. A retained resource that was
// replaced stays in the state, as retained, under its node and its old id. An imported resource
// stays retained unless the configuration says delete.
func (e *Engine) recordApplied(ctx context.Context, store *Store, n Node, prior *state.Resource, action Action, res Result, in prepared) error {
	policy := n.DeletionPolicy
	imported := prior != nil && prior.Imported && action != Replace
	if policy == "" && imported {
		policy = PolicyRetain
	}
	return store.Update(ctx, func(st *state.State) {
		if action == Replace && prior.DeletionPolicy == PolicyRetain {
			if st.Retained == nil {
				st.Retained = map[string]*state.Resource{}
			}
			old := cloneResource(prior)
			old.Pending, old.Dependencies = nil, nil
			st.Retained[n.ID+"~"+prior.PhysicalID] = old
		}
		st.Resources[n.ID] = &state.Resource{
			Type:           n.Type,
			PhysicalID:     res.PhysicalID,
			Inputs:         in.stored,
			Hash:           in.hash,
			SecretsHash:    in.externalHash,
			Attributes:     res.Attributes,
			Dependencies:   n.Deps(),
			DeletionPolicy: policy,
			Imported:       imported,
		}
	})
}

// decide is what to do to a node now that its inputs are known: what was planned, unless the
// inputs came out the same after all, or a replacement turns out to be needed. A pending
// operation of a crashed deploy is finished.
func (e *Engine) decide(p Provider, c Change, prior *state.Resource, hash string, recorded json.RawMessage) (Action, error) {
	switch {
	case prior == nil:
		return Create, nil
	case prior.Pending != nil && prior.Pending.Operation == string(Replace):
		return Replace, nil
	case prior.Pending != nil:
		return Update, nil
	case c.Action == Adopt:
		return Adopt, nil
	case prior.Hash == hash:
		return NoOp, nil
	case p.Replaces(prior.Inputs, recorded):
		if prior.DeletionPolicy == PolicyRetain && !e.AllowReplace {
			return "", retainedReplace(ChangedKeys(decode(prior.Inputs), decode(recorded)))
		}
		return Replace, nil
	default:
		return Update, nil
	}
}

// request is what the provider is asked for one node, and when the operation started: now, or
// when a crashed deploy started it, which keeps its client token the same.
func (e *Engine) request(ctx context.Context, n Node, snap *state.State, prior *state.Resource, sent json.RawMessage, externalHash string, store *Store) (Request, time.Time, error) {
	startedAt := time.Now().UTC()
	req := Request{ID: n.ID, Name: n.Name, Inputs: sent, Tags: e.tags(n.ID, snap.App, snap.Stage)}
	if prior != nil {
		var err error
		req.PhysicalID, req.Attributes = prior.PhysicalID, prior.Attributes
		req.Prior, err = e.priorWithExternal(ctx, prior, externalHash)
		if err != nil {
			return Request{}, startedAt, err
		}
		if prior.Pending != nil {
			req.Token = prior.Pending.Token
			startedAt = prior.Pending.StartedAt
		}
	}
	req.ClientToken = clientToken(snap.App, snap.Stage, n.ID, startedAt)
	req.Progress = func(token string) error {
		return store.Update(ctx, func(st *state.State) {
			if r := st.Resources[n.ID]; r != nil && r.Pending != nil {
				r.Pending.Token = token
			}
		})
	}
	return req, startedAt, nil
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
		retained := prior.DeletionPolicy == PolicyRetain
		if !retained {
			if err := p.Delete(ctx, req); err != nil {
				return Result{}, fmt.Errorf("deleting the old one: %w", err)
			}
		}
		req.PhysicalID, req.Prior, req.Attributes = "", nil, nil
		res, err := p.Create(ctx, req)
		switch {
		case retained && errors.Is(err, ErrExists):
			return Result{}, errors.New("the old one is retained and has the name the new one needs: give the new one another name, or set deletionPolicy = \"delete\" to replace it")
		case errors.Is(err, ErrExists):
			return e.adopt(ctx, p, req)
		}
		return res, err
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
			if !dependedOn(snap, todo, id) {
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

// dependedOn reports whether another node still to be deleted depends on id.
func dependedOn(snap *state.State, todo map[string]Change, id string) bool {
	for other := range todo {
		if r := snap.Resources[other]; other != id && r != nil && slices.Contains(r.Dependencies, id) {
			return true
		}
	}
	return false
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

// changedMarker stands, in the inputs a provider is given as the prior ones, for a secret or a
// stage output whose value changed: it differs from any real value, so the provider sends the
// field again.
const changedMarker = "\x00koldstart: changed secret\x00"

// isExternal reports whether v is a value the state holds a reference to rather than the value:
// a secret, or an output of another stage.
func isExternal(v any) bool {
	_, secret := asSecret(v)
	_, _, _, output := asOutput(v)
	return secret || output
}

// externalHash is the hash of the values of the secrets and stage outputs in v, in a stable order,
// or "" when there are none.
func (e *Engine) externalHash(ctx context.Context, v any) (string, error) {
	var values []string
	var errs []error
	walk(v, func(x any) bool {
		if !isExternal(x) {
			return false
		}
		val, err := e.resolveExternal(ctx, x)
		if err != nil {
			errs = append(errs, err)
			return true
		}
		values = append(values, fmt.Sprint(val))
		return true
	})
	if err := errors.Join(errs...); err != nil || values == nil {
		return "", err
	}
	return hashOf("secrets", values)
}

// priorWithExternal returns the inputs a resource was last applied with as a provider compares
// them: each secret its current value when no secret changed since, so only real changes are
// sent, and changedMarker when one did.
func (e *Engine) priorWithExternal(ctx context.Context, prior *state.Resource, current string) (json.RawMessage, error) {
	if len(prior.Inputs) == 0 {
		return prior.Inputs, nil
	}
	var v any
	if err := json.Unmarshal(prior.Inputs, &v); err != nil {
		return nil, err
	}
	changed := prior.SecretsHash != current
	out, err := transform(v, func(x any) (any, bool, error) {
		if !isExternal(x) {
			return nil, false, nil
		}
		if changed {
			return changedMarker, true, nil
		}
		val, err := e.resolveExternal(ctx, x)
		return val, true, err
	})
	if err != nil {
		return nil, err
	}
	return json.Marshal(out)
}

// clientToken is the idempotency token of an operation on a node, the same for every try of it
// that starts from one pending entry.
func clientToken(app, stage, id string, startedAt time.Time) string {
	sum := sha256.Sum256([]byte(app + "/" + stage + "/" + id + "/" + startedAt.Format(time.RFC3339Nano)))
	return "koldstart-" + hex.EncodeToString(sum[:])[:32]
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
