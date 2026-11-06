package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/kontrolplane/koldstart/internal/state"
)

// Action is what a deploy does to one node.
type Action string

// The actions.
const (
	NoOp    Action = "no-op"
	Create  Action = "create"
	Update  Action = "update"
	Replace Action = "replace"
	Delete  Action = "delete"
	// Keep is a node gone from the configuration whose resource is left in place.
	Keep Action = "keep"
	// Adopt is a resource remove left in place, managed again.
	Adopt Action = "adopt"
)

// Change is the plan for one node.
type Change struct {
	ID     string
	Type   string
	Action Action
	// Before are the inputs it was last applied with; After are the desired ones, with values
	// known only after apply marked unknown and secrets left as references.
	Before map[string]any
	After  map[string]any
	// Fields are the top-level inputs that differ.
	Fields []string
	// Resume is an operation a deploy started and may not have finished.
	Resume bool
	// Gone is a resource the state holds that is no longer in aws, deleted outside koldstart,
	// and Drift are the inputs that were changed outside koldstart: what a refresh found.
	Gone  bool
	Drift []string
	// Forces are the inputs of a replace that each need a new resource, a resource's properties
	// one by one, as properties.KeySchema; the rest of its fields could change in place.
	Forces []string
	// Retained is a replace of a resource that is retained, which --allow-replace lets leave in
	// place beside the new one.
	Retained bool
}

// Plan is what a deploy would do to a stage.
type Plan struct {
	App, Stage string
	// Serial is the state's serial the plan was made from.
	Serial  int64
	Changes []Change
	// Refreshed says each resource was read as it is in aws while planning.
	Refreshed bool
}

// HasChanges reports whether applying the plan would do anything.
func (p *Plan) HasChanges() bool {
	return slices.ContainsFunc(p.Changes, func(c Change) bool { return c.Action != NoOp })
}

// Counts returns how many changes there are of each action.
func (p *Plan) Counts() map[Action]int {
	out := map[Action]int{}
	for _, c := range p.Changes {
		out[c.Action]++
	}
	return out
}

// Change returns the change for a node.
func (p *Plan) Change(id string) (Change, bool) {
	for _, c := range p.Changes {
		if c.ID == id {
			return c, true
		}
	}
	return Change{}, false
}

// OnlyCode reports whether the plan changes nothing but the code of functions: then a deploy
// only uploads zips and calls UpdateFunctionCode.
func (p *Plan) OnlyCode() bool {
	codeChanged := false
	for _, c := range p.Changes {
		switch {
		case c.Action == NoOp:
		case c.Action == Update && slices.Equal(c.Fields, []string{"code"}):
			codeChanged = true
		default:
			return false
		}
	}
	return codeChanged
}

// Event is progress while applying, for the cli and the tui to show.
type Event struct {
	ID       string
	Action   Action
	Done     bool
	Err      error
	Duration time.Duration
}

// Engine plans and applies.
type Engine struct {
	Providers map[string]Provider
	// Fallback handles the types Providers has no provider for, such as cloud control for
	// any AWS:: type.
	Fallback Provider
	Secrets  Secrets
	// Outputs reads what output() refers to.
	Outputs Outputs
	// Concurrency is how many nodes are applied at once.
	Concurrency int
	// AllowReplace lets a plan replace a resource that is retained.
	AllowReplace bool
	// Refresh reads each resource as it is while planning, to create again what was deleted
	// outside koldstart and to set back what was changed.
	Refresh bool
	// Tags go on every resource, next to koldstart's own.
	Tags map[string]string
	// OnEvent is called as nodes start and finish applying.
	OnEvent func(Event)
}

func (e *Engine) provider(typ string) (Provider, error) {
	if p, ok := e.Providers[typ]; ok {
		return p, nil
	}
	if e.Fallback != nil {
		return e.Fallback, nil
	}
	return nil, fmt.Errorf("no provider for %s", typ)
}

// Plan compares nodes with the state and returns what applying them would do. It reads
// secrets to tell whether they changed, and changes nothing.
func (e *Engine) Plan(ctx context.Context, nodes []Node, st *state.State) (*Plan, error) {
	levels, err := order(nodes)
	if err != nil {
		return nil, err
	}
	plan := &Plan{App: st.App, Stage: st.Stage, Serial: st.Serial, Refreshed: e.Refresh}
	unknownNodes := map[string]bool{}
	desired := map[string]bool{}
	var errs []error

	for _, level := range levels {
		for _, n := range level {
			desired[n.ID] = true
			c, err := e.planNode(ctx, n, st, unknownNodes)
			if err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", n.ID, err))
				continue
			}
			if c.Action == Create || c.Action == Replace {
				unknownNodes[n.ID] = true
			}
			plan.Changes = append(plan.Changes, c)
		}
	}

	for _, id := range st.IDs() {
		if desired[id] {
			continue
		}
		r := st.Resources[id]
		c := Change{ID: id, Type: r.Type, Action: Delete, Before: decode(r.Inputs), Resume: r.Pending != nil}
		if r.DeletionPolicy == PolicyRetain {
			c.Action = Keep
		}
		plan.Changes = append(plan.Changes, c)
	}
	return plan, errors.Join(errs...)
}

// SecretChanged is the field a change lists when only a secret or an output of another stage
// changed: the inputs, which hold references to them, look the same.
const SecretChanged = "(a secret)"

// retainedReplace is the error of a change that needs a new resource where the old one is retained.
func retainedReplace(fields []string) error {
	return fmt.Errorf("changing %s needs a new resource, and the old one is retained: pass --allow-replace to replace it, which leaves the old one in place, or undo the change", strings.Join(fields, ", "))
}

// planNode plans one node.
func (e *Engine) planNode(ctx context.Context, n Node, st *state.State, unknownNodes map[string]bool) (Change, error) {
	p, err := e.provider(n.Type)
	if err != nil {
		return Change{}, err
	}
	inputs, err := normalise(n.Inputs)
	if err != nil {
		return Change{}, err
	}
	after, err := resolveRefs(inputs, st, unknownNodes)
	if err != nil {
		return Change{}, err
	}
	c := Change{ID: n.ID, Type: n.Type, After: after.(map[string]any)}
	withSecrets, err := e.resolveExternal(ctx, after)
	if err != nil {
		return Change{}, err
	}

	prior, adopt := st.Resources[n.ID], false
	if prior == nil && st.Retained[n.ID] != nil {
		prior, adopt = st.Retained[n.ID], true
	}
	if prior == nil {
		c.Action = Create
		return c, nil
	}
	c.Before = decode(prior.Inputs)
	c.Fields = ChangedKeys(c.Before, c.After)
	switch {
	case adopt:
		c.Action = Adopt
		return c, nil
	case prior.Pending != nil:
		c.Action, c.Resume = Update, true
		switch prior.Pending.Operation {
		case string(Create):
			c.Action = Create
		case string(Replace):
			c.Action = Replace
		}
		return c, nil
	}
	if e.Refresh {
		r, err := e.refresh(ctx, p, n, st, prior)
		if err != nil {
			return Change{}, err
		}
		if r.gone {
			c.Action, c.Gone = Create, true
			return c, nil
		}
		c.Drift = r.drift
	}

	if !hasUnknown(after) {
		hash, err := hashOf(n.Type, withSecrets)
		if err != nil {
			return Change{}, err
		}
		switch {
		case hash == prior.Hash && len(c.Drift) > 0:
			c.Action = Update
			return c, nil
		case hash == prior.Hash:
			c.Action = NoOp
			return c, nil
		}
	}
	afterJSON, err := json.Marshal(after)
	if err != nil {
		return Change{}, err
	}
	c.Action = Update
	if p.Replaces(prior.Inputs, afterJSON) {
		c.Action, c.Retained = Replace, prior.DeletionPolicy == PolicyRetain
		if c.Retained && !e.AllowReplace {
			return Change{}, retainedReplace(c.Fields)
		}
		c.Forces = forcing(p, prior.Inputs, c.Before, c.After, c.Fields)
	}
	if len(c.Fields) == 0 && !hasUnknown(after) {
		c.Fields = []string{SecretChanged}
	}
	return c, nil
}

// Resolve returns v with its refs resolved against st, and its secrets and outputs of other
// stages read, as a function gets them when it is deployed. A ref to something st does not hold
// is an error that names it.
func (e *Engine) Resolve(ctx context.Context, v any, st *state.State) (any, error) {
	n, err := normalise(v)
	if err != nil {
		return nil, err
	}
	for _, r := range refs(n) {
		if st.Resources[r.Node] == nil {
			return nil, fmt.Errorf("it refers to %s, which stage %s has not deployed", r.Node, st.Stage)
		}
	}
	resolved, err := resolveRefs(n, st, nil)
	if err != nil {
		return nil, err
	}
	return e.resolveExternal(ctx, resolved)
}

// resolveRefs replaces the refs in v with the attributes the state holds, or with unknown for
// nodes in unknownNodes, which are created or replaced before.
func resolveRefs(v any, st *state.State, unknownNodes map[string]bool) (any, error) {
	return transform(v, func(x any) (any, bool, error) {
		if parts, ok := asJoin(x); ok {
			var b strings.Builder
			for _, part := range parts {
				resolved, err := resolveRefs(part, st, unknownNodes)
				if err != nil {
					return nil, true, err
				}
				if IsUnknown(resolved) {
					return unknown(), true, nil
				}
				s, ok := resolved.(string)
				if !ok {
					return nil, true, fmt.Errorf("a join of something that is not a string: %v", resolved)
				}
				b.WriteString(s)
			}
			return b.String(), true, nil
		}
		r, ok := asRef(x)
		if !ok {
			return nil, false, nil
		}
		if unknownNodes[r.Node] {
			return unknown(), true, nil
		}
		res := st.Resources[r.Node]
		if res == nil {
			return unknown(), true, nil
		}
		val, ok := res.Attributes[r.Attribute]
		if !ok {
			return nil, true, fmt.Errorf("%s has no attribute %s; it has %s", r.Node, r.Attribute, strings.Join(slices.Sorted(maps.Keys(res.Attributes)), ", "))
		}
		return val, true, nil
	})
}

// resolveExternal replaces the secrets in v with their values, and the outputs of other stages
// with theirs.
func (e *Engine) resolveExternal(ctx context.Context, v any) (any, error) {
	return transform(v, func(x any) (any, bool, error) {
		if o, ok := asOutput(x); ok {
			if e.Outputs == nil {
				return nil, true, fmt.Errorf("no way to read the output %s", DescribeOutput(o))
			}
			val, err := e.Outputs.Output(ctx, o)
			return val, true, err
		}
		s, ok := asSecret(x)
		if !ok {
			return nil, false, nil
		}
		if e.Secrets == nil {
			return nil, true, fmt.Errorf("no way to read the secret %s:%s", s.Source, s.Name)
		}
		val, err := e.Secrets.Resolve(ctx, s.Source, s.Name, s.Key, s.AtRuntime)
		if err != nil {
			return nil, true, err
		}
		return val, true, nil
	})
}

// ChangedKeys returns the top-level keys whose values differ, sorted.
func ChangedKeys(before, after map[string]any) []string {
	keys := map[string]bool{}
	for k := range before {
		keys[k] = true
	}
	for k := range after {
		keys[k] = true
	}
	var out []string
	for _, k := range slices.Sorted(maps.Keys(keys)) {
		if !reflect.DeepEqual(before[k], after[k]) {
			out = append(out, k)
		}
	}
	return out
}

// forcing are the fields of a replace that need a new resource each on its own, as the provider
// says when only that field is changed from before: a resource's properties one by one.
func forcing(p Provider, prior json.RawMessage, before, after map[string]any, fields []string) []string {
	var out []string
	try := func(name string, with func(map[string]any)) {
		candidate := maps.Clone(before)
		if candidate == nil {
			candidate = map[string]any{}
		}
		with(candidate)
		if data, err := json.Marshal(candidate); err == nil && p.Replaces(prior, data) {
			out = append(out, name)
		}
	}
	for _, f := range fields {
		b, isMapB := before[f].(map[string]any)
		a, isMapA := after[f].(map[string]any)
		if f != "properties" || !isMapB || !isMapA {
			try(f, func(m map[string]any) { setFrom(m, after, f) })
			continue
		}
		for _, k := range ChangedKeys(b, a) {
			try(f+"."+k, func(m map[string]any) {
				props := maps.Clone(b)
				setFrom(props, a, k)
				m[f] = props
			})
		}
	}
	return out
}

// setFrom sets the key k of m to what from holds under it, or deletes it when from has none.
func setFrom(m, from map[string]any, k string) {
	if v, ok := from[k]; ok {
		m[k] = v
		return
	}
	delete(m, k)
}

func decode(data json.RawMessage) map[string]any {
	if len(data) == 0 {
		return nil
	}
	var out map[string]any
	_ = json.Unmarshal(data, &out)
	return out
}
