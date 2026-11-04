package engine

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"slices"

	"github.com/kontrolplane/koldstart/internal/config"
)

// marker is the key of koldstart's own values inside inputs: a ref to another node, a secret,
// or a value not known until apply.
const marker = "$koldstart"

const (
	kindRef     = "ref"
	kindSecret  = "secret"
	kindUnknown = "unknown"
	kindJoin    = "join"
	kindOutput  = "output"
)

// Ref stands for an attribute of another node, such as its Arn, resolved while applying.
func Ref(node, attribute string) map[string]any {
	return map[string]any{marker: kindRef, "node": node, "attribute": attribute}
}

// SecretValue stands for a secret, resolved while planning and applying, and never written to
// the state.
func SecretValue(s config.Secret) map[string]any {
	return map[string]any{marker: kindSecret, "source": s.Source, "name": s.Name, "key": s.Key, "atRuntime": s.AtRuntime}
}

// StageOutput stands for an output of another app's stage, read from its state while planning
// and applying.
func StageOutput(app, stage, output string) map[string]any {
	return map[string]any{marker: kindOutput, "app": app, "stage": stage, "output": output}
}

func asOutput(v any) (app, stage, output string, ok bool) {
	m, isMap := v.(map[string]any)
	if !isMap || m[marker] != kindOutput {
		return "", "", "", false
	}
	app, _ = m["app"].(string)
	stage, _ = m["stage"].(string)
	output, _ = m["output"].(string)
	return app, stage, output, true
}

// Join stands for its parts, strings and refs, joined into one string once the refs are known,
// such as a table's arn with /index/* after it.
func Join(parts ...any) map[string]any {
	return map[string]any{marker: kindJoin, "parts": parts}
}

func asJoin(v any) ([]any, bool) {
	m, ok := v.(map[string]any)
	if !ok || m[marker] != kindJoin {
		return nil, false
	}
	parts, _ := m["parts"].([]any)
	return parts, true
}

// unknown is a value only known once a node it refers to is applied.
func unknown() map[string]any {
	return map[string]any{marker: kindUnknown}
}

// IsUnknown reports whether v is a value not known until apply.
func IsUnknown(v any) bool {
	m, ok := v.(map[string]any)
	return ok && m[marker] == kindUnknown
}

type refTo struct {
	Node, Attribute string
}

func asRef(v any) (refTo, bool) {
	m, ok := v.(map[string]any)
	if !ok || m[marker] != kindRef {
		return refTo{}, false
	}
	node, _ := m["node"].(string)
	attr, _ := m["attribute"].(string)
	return refTo{Node: node, Attribute: attr}, true
}

func asSecret(v any) (config.Secret, bool) {
	m, ok := v.(map[string]any)
	if !ok || m[marker] != kindSecret {
		return config.Secret{}, false
	}
	s := config.Secret{}
	s.Source, _ = m["source"].(string)
	s.Name, _ = m["name"].(string)
	s.Key, _ = m["key"].(string)
	s.AtRuntime, _ = m["atRuntime"].(bool)
	return s, true
}

// refs returns the refs inside v, in a stable order.
func refs(v any) []refTo {
	var out []refTo
	walk(v, func(x any) bool {
		if r, ok := asRef(x); ok {
			out = append(out, r)
			return true
		}
		return false
	})
	return out
}

// walk calls fn on v and everything inside it, not descending where fn returns true.
func walk(v any, fn func(any) bool) {
	if fn(v) {
		return
	}
	switch x := v.(type) {
	case map[string]any:
		for _, k := range slices.Sorted(maps.Keys(x)) {
			walk(x[k], fn)
		}
	case []any:
		for _, item := range x {
			walk(item, fn)
		}
	}
}

// transform returns a copy of v with every value fn replaces replaced; fn returns false to
// leave a value and descend into it.
func transform(v any, fn func(any) (any, bool, error)) (any, error) {
	if out, ok, err := fn(v); err != nil || ok {
		return out, err
	}
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, item := range x {
			t, err := transform(item, fn)
			if err != nil {
				return nil, err
			}
			out[k] = t
		}
		return out, nil
	case []any:
		out := make([]any, len(x))
		for i, item := range x {
			t, err := transform(item, fn)
			if err != nil {
				return nil, err
			}
			out[i] = t
		}
		return out, nil
	default:
		return v, nil
	}
}

// normalise turns any value into the plain json types inputs are made of: maps of string to
// any, slices of any, strings, float64, bools and nil.
func normalise(v any) (any, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var out any
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// hashOf is the sha256 of the json of v; json sorts map keys, so equal values hash alike.
func hashOf(typ string, v any) (string, error) {
	data, err := json.Marshal(map[string]any{"type": typ, "inputs": v})
	if err != nil {
		return "", fmt.Errorf("hashing: %w", err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// hasUnknown reports whether a value not known until apply is inside v.
func hasUnknown(v any) bool {
	found := false
	walk(v, func(x any) bool {
		if IsUnknown(x) {
			found = true
			return true
		}
		return false
	})
	return found
}
