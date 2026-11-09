package desired

import (
	"encoding/json"
	"maps"
	"slices"
	"strings"

	"github.com/kontrolplane/koldstart/internal/provider/awsp"
	"github.com/kontrolplane/koldstart/internal/state"
)

// Link is a resource of a stage a function links to, by its id, and the access the function's
// role has on it: "read", "write" or "readwrite", or "" where its policy grants none of what link
// gives.
type Link struct {
	ID     string
	Access string
}

// Links are the resources of a stage fn links to, sorted by id. A resource is linked when the
// function has every environment variable link gives it; its access is read from the statements
// of the role that name the resource, so what an event or permissions add to it counts too.
func Links(resources map[string]*state.Resource, fn string) []Link {
	return linksOf(resources, linkable(resources), fn)
}

// LinksByFunction is what Links finds of every function of a state, by the function's name.
func LinksByFunction(resources map[string]*state.Resource) map[string][]Link {
	ids := linkable(resources)
	out := map[string][]Link{}
	for id, r := range resources {
		if fn, ok := strings.CutPrefix(id, "function/"); ok && r.Type == awsp.TypeFunction {
			if l := linksOf(resources, ids, fn); l != nil {
				out[fn] = l
			}
		}
	}
	return out
}

// linkable are the resources of a state of a type link knows, sorted by id.
func linkable(resources map[string]*state.Resource) []string {
	var out []string
	for id, r := range resources {
		if _, known := links[r.Type]; known && strings.HasPrefix(id, "resource/") {
			out = append(out, id)
		}
	}
	slices.Sort(out)
	return out
}

// linksOf are the resources of ids that fn links to.
func linksOf(resources map[string]*state.Resource, ids []string, fn string) []Link {
	f := resources[FunctionID(fn)]
	if f == nil {
		return nil
	}
	var in struct {
		Environment map[string]any `json:"environment"`
	}
	_ = json.Unmarshal(f.Inputs, &in)
	if len(in.Environment) == 0 {
		return nil
	}
	var allowed []allowStatement
	if role := resources[RoleID(fn)]; role != nil {
		var r awsp.RoleInputs
		_ = json.Unmarshal(role.Inputs, &r)
		allowed = allowStatements(r.Policy)
	}
	var out []Link
	for _, id := range ids {
		r := resources[id]
		a := links[r.Type]
		if !linkedBy(in.Environment, strings.TrimPrefix(id, "resource/"), a) {
			continue
		}
		var actions []string
		for _, s := range allowed {
			if slices.ContainsFunc(s.resources, func(arn string) bool { return names(arn, r) }) {
				actions = append(actions, s.actions...)
			}
		}
		out = append(out, Link{ID: id, Access: accessOf(a, actions)})
	}
	return out
}

// linkedBy says env holds every variable link gives a function for the resource name.
func linkedBy(env map[string]any, name string, a access) bool {
	for suffix := range a.env {
		if _, ok := env[envName(name)+"_"+suffix]; !ok {
			return false
		}
	}
	return len(a.env) > 0
}

// allowStatement is what a statement of a role's policy allows: its actions, on its resources.
type allowStatement struct {
	actions, resources []string
}

// allowStatements are the statements of a policy document, as a role's inputs hold it, that
// allow something.
func allowStatements(policy any) []allowStatement {
	if s, ok := policy.(string); ok {
		_ = json.Unmarshal([]byte(s), &policy)
	}
	doc, _ := policy.(map[string]any)
	var list []any
	switch v := doc["Statement"].(type) {
	case []any:
		list = v
	case map[string]any:
		list = []any{v}
	}
	var out []allowStatement
	for _, s := range list {
		st, _ := s.(map[string]any)
		if st["Effect"] != "Allow" {
			continue
		}
		out = append(out, allowStatement{actions: stringsOf(st["Action"]), resources: stringsOf(st["Resource"])})
	}
	return out
}

// stringsOf is a value of a policy that is a string or a list of them, as a list.
func stringsOf(v any) []string {
	switch v := v.(type) {
	case string:
		return []string{v}
	case []any:
		var out []string
		for _, s := range v {
			if s, ok := s.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// names says the arn of a statement names r: it is one of r's arns, or what is under it, as a
// table's indexes or a bucket's objects, or it ends with r's id, as a parameter's arn ends with
// its name.
func names(arn string, r *state.Resource) bool {
	for _, v := range slices.Concat(slices.Collect(maps.Values(r.Attributes)), []string{r.PhysicalID}) {
		switch {
		case v == "":
		case strings.HasPrefix(v, "arn:"):
			if arn == v || strings.HasPrefix(arn, v+"/") {
				return true
			}
		case v == r.PhysicalID:
			if strings.HasSuffix(arn, "/"+v) || strings.HasSuffix(arn, ":"+v) {
				return true
			}
		}
	}
	return false
}

// accessOf is the access actions give on a type link knows: read for an action only reading
// gives, write for one only writing gives, readwrite for both, and "" for neither.
func accessOf(a access, actions []string) string {
	read := slices.ContainsFunc(actions, func(x string) bool { return slices.Contains(a.read, x) && !slices.Contains(a.write, x) })
	write := slices.ContainsFunc(actions, func(x string) bool { return slices.Contains(a.write, x) && !slices.Contains(a.read, x) })
	switch {
	case read && write:
		return "readwrite"
	case read:
		return "read"
	case write:
		return "write"
	}
	return ""
}
