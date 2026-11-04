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
	Keep Action = "retain"
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
}

// Plan is what a deploy would do to a stage.
type Plan struct {
	App, Stage string
	// Serial is the state's serial the plan was made from.
	Serial  int64
	Changes []Change
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
	any := false
	for _, c := range p.Changes {
		switch {
		case c.Action == NoOp:
		case c.Action == Update && slices.Equal(c.Fields, []string{"code"}):
			any = true
		default:
			return false
		}
	}
	return any
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
	plan := &Plan{App: st.App, Stage: st.Stage, Serial: st.Serial}
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
		if r.DeletionPolicy == Retain {
			c.Action = Keep
		}
		plan.Changes = append(plan.Changes, c)
	}
	return plan, errors.Join(errs...)
}

// planNode plans one node. When only a secret changed, the inputs, which hold references, look
// the same, and the change is listed as one of a secret.
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

	prior, adopt := st.Resources[n.ID], false
	if prior == nil && st.Retained != nil && st.Retained[n.ID] != nil {
		prior, adopt = st.Retained[n.ID], true
	}
	if prior == nil {
		c.Action = Create
		return c, nil
	}
	c.Before = decode(prior.Inputs)
	c.Fields = changedFields(c.Before, c.After)
	switch {
	case adopt:
		c.Action = Adopt
		return c, nil
	case prior.Pending != nil:
		c.Action, c.Resume = Update, true
		if prior.Pending.Operation == string(Create) {
			c.Action = Create
		}
		return c, nil
	}

	if !hasUnknown(after) {
		withSecrets, err := e.resolveSecrets(ctx, after)
		if err != nil {
			return Change{}, err
		}
		hash, err := hashOf(n.Type, withSecrets)
		if err != nil {
			return Change{}, err
		}
		if hash == prior.Hash {
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
		c.Action = Replace
		if prior.DeletionPolicy == Retain && !e.AllowReplace {
			return Change{}, fmt.Errorf("changing %s needs a new resource, and the old one is retained: pass --allow-replace to replace it, which leaves the old one in place, or undo the change",
				strings.Join(c.Fields, ", "))
		}
	}
	if len(c.Fields) == 0 && !hasUnknown(after) {
		c.Fields = []string{"(a secret)"}
	}
	return c, nil
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

// resolveSecrets replaces the secrets in v with their values, and the outputs of other stages
// with theirs.
func (e *Engine) resolveSecrets(ctx context.Context, v any) (any, error) {
	return transform(v, func(x any) (any, bool, error) {
		if app, stage, name, ok := asOutput(x); ok {
			if e.Outputs == nil {
				return nil, true, fmt.Errorf("no way to read the output %s of %s/%s", name, app, stage)
			}
			val, err := e.Outputs.Output(ctx, app, stage, name)
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

// changedFields returns the top-level keys whose values differ.
func changedFields(before, after map[string]any) []string {
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

func decode(data json.RawMessage) map[string]any {
	if len(data) == 0 {
		return nil
	}
	var out map[string]any
	_ = json.Unmarshal(data, &out)
	return out
}
