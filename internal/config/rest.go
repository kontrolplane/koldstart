package config

import (
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
)

// restLiteral and restParameter are the segments of the path of a rest route: one of letters,
// digits and ._~-, or a parameter, {id}, or {proxy+} for the rest of the path.
var (
	restLiteral   = regexp.MustCompile(`^[A-Za-z0-9._~-]+$`)
	restParameter = regexp.MustCompile(`^\{[A-Za-z0-9._-]+\+?\}$`)
)

// RESTSegments are the parts of the path of a rest route, as api gateway makes a resource of
// each: /orders/{id} is orders and {id}, and / is none.
func RESTSegments(path string) []string {
	trimmed := strings.TrimPrefix(path, "/")
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, "/")
}

// restPathProblem says what api gateway would refuse of the path of a rest route, or is empty
// when it takes it.
func restPathProblem(path string) string {
	if !strings.HasPrefix(path, "/") {
		return "it must start with /"
	}
	segments := RESTSegments(path)
	for i, s := range segments {
		switch {
		case s == "":
			return "it has an empty segment or ends with /: write /a/b, or / for the root"
		case restParameter.MatchString(s) && strings.HasSuffix(s, "+}") && i < len(segments)-1:
			return s + " takes the rest of the path, so it must be the last segment"
		case !restParameter.MatchString(s) && !restLiteral.MatchString(s):
			return s + " is neither letters, digits and ._~- nor a parameter such as {id} or {proxy+}"
		}
	}
	return ""
}

// restRoute checks a route of the rest api: its path, that it is the only one of its method and
// path, and its authorizer.
func (v *validator) restRoute(fn, where string, e Event) {
	if problem := restPathProblem(e.Path); problem != "" {
		v.errorf("%s: path %q: %s", where, e.Path, problem)
		return
	}
	route := "rest " + strings.ToUpper(e.Method) + " " + e.Path
	if other, ok := v.routes[route]; ok {
		v.errorf("%s: %s %s is also a rest route of %s", where, strings.ToUpper(e.Method), e.Path, other)
	}
	v.routes[route] = fn
	segments := RESTSegments(e.Path)
	for i, s := range segments {
		if !restParameter.MatchString(s) {
			continue
		}
		parent := "/" + strings.Join(segments[:i], "/")
		if v.restParams == nil {
			v.restParams = map[string]map[string]string{}
		}
		if v.restParams[parent] == nil {
			v.restParams[parent] = map[string]string{}
		}
		if _, seen := v.restParams[parent][s]; !seen {
			v.restParams[parent][s] = where
		}
	}
	var a RESTAuthorizer
	if e.Authorizer != "" {
		if v.p.REST != nil {
			a = v.p.REST.Authorizers[e.Authorizer]
		}
		if a.Type == "" {
			v.errorf("%s: no authorizer %q in rest.authorizers", where, e.Authorizer)
		}
	}
	if e.Authorizer != "" && e.IAM {
		v.errorf("%s: a route takes iam or an authorizer, not both", where)
	}
	if len(e.Scopes) > 0 && a.Type != RESTAuthorizerCognito {
		v.errorf("%s: scopes are of a cognito authorizer: give the route one of rest.authorizers that is a kdst.CognitoAuthorizer, or leave out scopes", where)
	}
}

// restSiblings checks no two rest routes have different parameters after the same path, such as
// /orders/{id} and /orders/{name}, which api gateway refuses: a resource has one at most.
func (v *validator) restSiblings() {
	for _, parent := range slices.Sorted(maps.Keys(v.restParams)) {
		params := v.restParams[parent]
		if len(params) < 2 {
			continue
		}
		names := slices.Sorted(maps.Keys(params))
		v.errorf("%s: after %s the rest routes have the parameters %s, and api gateway takes one: name them the same", params[names[1]], parent, strings.Join(names, " and "))
	}
}

// restAuthorizers checks the authorizers of the rest api: a lambda one is a function of the
// project, a cached request one says what it is cached by, and a cognito one's user pools exist.
func (v *validator) restAuthorizers() {
	if v.p.REST == nil {
		return
	}
	for _, name := range slices.Sorted(maps.Keys(v.p.REST.Authorizers)) {
		a := v.p.REST.Authorizers[name]
		where := fmt.Sprintf("rest.authorizers[%s]", name)
		if _, ok := v.p.Functions[a.Function]; a.IsLambda() && !ok {
			v.errorf("%s: function %q is not a function of the project", where, a.Function)
		}
		if a.Type == RESTAuthorizerRequest && a.TTL > 0 && len(a.Identity) == 0 {
			v.errorf("%s: a request authorizer that caches its results caches them by identity: set identity, or ttl = 0.s", where)
		}
		for i, pool := range a.UserPools {
			v.value(fmt.Sprintf("%s.userPools[%d]", where, i), pool)
		}
	}
}
