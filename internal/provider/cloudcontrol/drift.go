package cloudcontrol

import (
	"context"
	"fmt"
	"maps"
	"slices"

	"github.com/kontrolplane/koldstart/internal/engine"
)

// Drift implements engine.Differ: the properties the resource was applied with that cloud
// control reads otherwise now.
func (p Provider) Drift(ctx context.Context, req engine.Request) ([]string, error) {
	in, err := decode(req.Inputs)
	if err != nil {
		return nil, err
	}
	live, err := p.live(ctx, in.Type, req.PhysicalID)
	if err != nil {
		return nil, err
	}
	var s *Schema
	if p.Schemas != nil {
		s, _ = p.Schemas.Schema(ctx, in.Type)
	}
	return Drift(in.Properties, live, s), nil
}

// Drift lists the properties of applied whose value live does not have, sorted. An object
// matches when live has what applied sets of it, as aws fills in what it defaults, and a list
// when each element matches one of live's, in any order. Read only and write only properties
// are left out. Without a schema to say which are write only, a property live lacks is not
// counted, as it may be one aws never returns.
func Drift(applied, live map[string]any, s *Schema) []string {
	var out []string
	for _, k := range slices.Sorted(maps.Keys(applied)) {
		got, ok := live[k]
		switch {
		case s != nil && (slices.Contains(s.ReadOnly, k) || slices.Contains(s.WriteOnly, k)):
		case !ok && (s == nil || empty(applied[k])):
		case !ok || !matches(applied[k], got):
			out = append(out, k)
		}
	}
	return out
}

// matches reports whether got has the value of want, as Drift compares them. Scalars match by
// how they print, as aws returns some numbers as strings.
func matches(want, got any) bool {
	switch w := want.(type) {
	case map[string]any:
		g, ok := got.(map[string]any)
		if !ok {
			return false
		}
		for k, v := range w {
			gv, has := g[k]
			if (!has && !empty(v)) || (has && !matches(v, gv)) {
				return false
			}
		}
		return true
	case []any:
		g, ok := got.([]any)
		if !ok || len(g) != len(w) {
			return false
		}
		used := make([]bool, len(g))
		for _, v := range w {
			i := unusedMatch(g, v, used)
			if i < 0 {
				return false
			}
			used[i] = true
		}
		return true
	case nil:
		return empty(got)
	}
	return fmt.Sprint(want) == fmt.Sprint(got)
}

// unusedMatch is the index of the first element of list that matches v and is not used, or -1.
func unusedMatch(list []any, v any, used []bool) int {
	for i, x := range list {
		if !used[i] && matches(v, x) {
			return i
		}
	}
	return -1
}

// empty reports whether v is nothing: nil, "", or an empty list or object.
func empty(v any) bool {
	switch x := v.(type) {
	case nil:
		return true
	case string:
		return x == ""
	case []any:
		return len(x) == 0
	case map[string]any:
		return len(x) == 0
	}
	return false
}
