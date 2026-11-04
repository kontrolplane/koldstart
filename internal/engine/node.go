package engine

import (
	"fmt"
	"maps"
	"slices"
	"strings"
)

// Node is one resource the configuration asks for.
type Node struct {
	// ID is unique in the stage, such as function/api or role/api.
	ID string
	// Type picks the provider, such as AWS::Lambda::Function.
	Type string
	// Name is the physical name, for resources koldstart names.
	Name string
	// Inputs are the desired properties, as json values with refs and secrets inside.
	Inputs map[string]any
	// After are nodes to apply first that no ref in the inputs names.
	After []string
	// DeletionPolicy is retain for a resource remove and replacements must leave in place.
	DeletionPolicy string
}

// Retain is the deletion policy of a resource left in place.
const Retain = "retain"

// Deps returns the nodes this one is applied after, sorted.
func (n Node) Deps() []string {
	set := map[string]bool{}
	for _, r := range refs(n.Inputs) {
		set[r.Node] = true
	}
	for _, id := range n.After {
		set[id] = true
	}
	delete(set, n.ID)
	return slices.Sorted(maps.Keys(set))
}

// order sorts nodes so each comes after what it depends on, in levels that can each be applied
// at once, and refuses a dependency on a node that is not there and a cycle.
func order(nodes []Node) ([][]Node, error) {
	byID := make(map[string]Node, len(nodes))
	for _, n := range nodes {
		if _, ok := byID[n.ID]; ok {
			return nil, fmt.Errorf("node %s is there twice", n.ID)
		}
		byID[n.ID] = n
	}
	for _, n := range nodes {
		for _, dep := range n.Deps() {
			if _, ok := byID[dep]; !ok {
				return nil, fmt.Errorf("%s refers to %s, which is not in the configuration", n.ID, dep)
			}
		}
	}

	level := map[string]int{}
	visiting := map[string]bool{}
	var visit func(id string, path []string) (int, error)
	visit = func(id string, path []string) (int, error) {
		if l, ok := level[id]; ok {
			return l, nil
		}
		if visiting[id] {
			return 0, fmt.Errorf("a cycle: %s", strings.Join(append(path, id), " -> "))
		}
		visiting[id] = true
		l := 0
		for _, dep := range byID[id].Deps() {
			d, err := visit(dep, append(path, id))
			if err != nil {
				return 0, err
			}
			l = max(l, d+1)
		}
		visiting[id] = false
		level[id] = l
		return l, nil
	}

	var levels [][]Node
	for _, id := range slices.Sorted(maps.Keys(byID)) {
		l, err := visit(id, nil)
		if err != nil {
			return nil, err
		}
		for len(levels) <= l {
			levels = append(levels, nil)
		}
		levels[l] = append(levels[l], byID[id])
	}
	for _, l := range levels {
		slices.SortFunc(l, func(a, b Node) int { return strings.Compare(a.ID, b.ID) })
	}
	return levels, nil
}
