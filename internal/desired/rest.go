package desired

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"maps"
	"slices"
	"strings"

	"github.com/kontrolplane/koldstart/internal/config"
	"github.com/kontrolplane/koldstart/internal/engine"
	"github.com/kontrolplane/koldstart/internal/provider/awsp"
)

// The ids of the nodes of the rest api there is one of in a stage.
const (
	RESTAPIID       = "restapi"
	RESTStageID     = "reststage"
	RESTKeyID       = "restapikey"
	RESTUsagePlanID = "restusageplan"
)

// The kinds of the nodes of the rest api there are many of, which start their ids.
const (
	kindRESTResource   = "restresource"
	kindRESTRoute      = "restroute"
	kindRESTAuthorizer = "restauthorizer"
)

// restResourceID is the node of the resource of a path, its parameters unnamed, as
// restresource/orders/{} for /orders/{id}: a resource has one parameter after it at most, so a
// renamed parameter replaces the resource, and what is under it, rather than adds a sibling api
// gateway refuses.
func restResourceID(path string) string {
	segments := config.RESTSegments(path)
	for i, s := range segments {
		if strings.HasPrefix(s, "{") {
			segments[i] = "{}"
		}
	}
	return kindRESTResource + "/" + strings.Join(segments, "/")
}

// restRouteID is the node of a rest route, such as restroute/GET /orders/{id}.
func restRouteID(method, path string) string {
	return kindRESTRoute + "/" + strings.ToUpper(method) + " " + path
}

func restAuthorizerID(name string) string { return kindRESTAuthorizer + "/" + name }

// The authorization types of a method of the rest api.
const (
	restAuthIAM     = "AWS_IAM"
	restAuthCustom  = "CUSTOM"
	restAuthCognito = "COGNITO_USER_POOLS"
)

// hasREST reports whether any function has a rest route, which gives the stage a rest api.
func (b *builder) hasREST() bool {
	for _, fn := range b.project.Functions {
		if hasEvent(fn, config.EventRESTRoute) {
			return true
		}
	}
	return false
}

// invocationURI is how api gateway invokes a function: the arn of what invokes it, in the path of
// lambda's api.
func (b *builder) invocationURI(fn string) any {
	return engine.Join("arn:"+awsp.Partition(b.target.Region)+":apigateway:"+b.target.Region+":lambda:path/2015-03-31/functions/",
		b.invoked(fn, attrArn), "/invocations")
}

// restAPI returns the stage's rest api: the api, its authorizers and their permissions, a
// resource for each segment of the routes' paths, the routes and the permissions of their
// functions, the stage with its deployment, and the api key and usage plan of the private
// routes.
func (b *builder) restAPI() []engine.Node {
	if !b.hasREST() {
		return nil
	}
	name := Name(128, b.project.App, b.project.Stage)
	api := engine.Ref(RESTAPIID, "RestApiId")
	nodes := []engine.Node{{ID: RESTAPIID, Type: awsp.TypeRESTAPI, Name: name, Inputs: map[string]any{inputName: name}}}
	shape := b.restAuthorizers(api)
	resources := map[string]bool{}
	private := false
	for _, fn := range slices.Sorted(maps.Keys(b.project.Functions)) {
		f := b.project.Functions[fn]
		if !hasEvent(f, config.EventRESTRoute) {
			continue
		}
		for _, e := range f.Events {
			if e.Type != config.EventRESTRoute {
				continue
			}
			shape = append(shape, b.restResources(api, e.Path, resources)...)
			shape = append(shape, b.restRoute(api, fn, e))
			private = private || e.Private
		}
		nodes = append(nodes, b.permission(fn, "restapi", "apigateway.amazonaws.com", engine.Join(b.arnPrefix("execute-api"), api, "/*/*"), false))
	}
	nodes = append(nodes, shape...)
	nodes = append(nodes, b.restStage(api, shape))
	if private {
		nodes = append(nodes,
			engine.Node{ID: RESTKeyID, Type: awsp.TypeRESTKey, Name: name, Inputs: map[string]any{inputName: name}},
			engine.Node{ID: RESTUsagePlanID, Type: awsp.TypeRESTUsagePlan, Name: name, Inputs: map[string]any{
				inputName: name, inputAPI: api, inputStage: engine.Ref(RESTStageID, "StageName"), "key": engine.Ref(RESTKeyID, "APIKeyId"),
			}},
		)
	}
	return nodes
}

// restAuthorizers are the authorizers of the rest api, each of a function after the permission
// that lets api gateway invoke it.
func (b *builder) restAuthorizers(api any) []engine.Node {
	if b.project.REST == nil {
		return nil
	}
	var nodes []engine.Node
	for _, name := range slices.Sorted(maps.Keys(b.project.REST.Authorizers)) {
		a := b.project.REST.Authorizers[name]
		in := map[string]any{inputAPI: api, inputName: name, "ttl": a.TTL}
		switch a.Type {
		case config.RESTAuthorizerToken:
			in["type"], in["identity"] = "TOKEN", "method.request.header."+a.Header
			if a.Validation != "" {
				in["validation"] = a.Validation
			}
		case config.RESTAuthorizerRequest:
			in["type"], in["identity"] = "REQUEST", strings.Join(a.Identity, ",")
		case config.RESTAuthorizerCognito:
			in["type"], in["identity"], in["userPools"] = restAuthCognito, "method.request.header."+a.Header, b.values(a.UserPools)
		}
		if a.IsLambda() {
			in["uri"] = b.invocationURI(a.Function)
			nodes = append(nodes, b.permission(a.Function, "restauthorizer-"+name, "apigateway.amazonaws.com",
				engine.Join(b.arnPrefix("execute-api"), api, "/authorizers/*"), false))
		}
		nodes = append(nodes, engine.Node{ID: restAuthorizerID(name), Type: awsp.TypeRESTAuthorizer, Inputs: in})
	}
	return nodes
}

// restParent is the resource the last segment of path hangs from: the api's root for a path of
// one segment.
func restParent(segments []string) any {
	if len(segments) <= 1 {
		return engine.Ref(RESTAPIID, "RootResourceId")
	}
	return engine.Ref(restResourceID("/"+strings.Join(segments[:len(segments)-1], "/")), "ResourceId")
}

// restResources are the resources of the segments of path that seen does not have yet, each
// under the one before it.
func (b *builder) restResources(api any, path string, seen map[string]bool) []engine.Node {
	segments := config.RESTSegments(path)
	var nodes []engine.Node
	for i := range segments {
		full := "/" + strings.Join(segments[:i+1], "/")
		if seen[full] {
			continue
		}
		seen[full] = true
		nodes = append(nodes, engine.Node{ID: restResourceID(full), Type: awsp.TypeRESTResource, Inputs: map[string]any{
			inputAPI: api, "parent": restParent(segments[:i+1]), "pathPart": segments[i], "path": full,
		}})
	}
	return nodes
}

// restRoute is a route of the rest api: a method on the resource of its path, and the function's
// proxy integration. What a route does not set is left out of its inputs.
func (b *builder) restRoute(api any, fn string, e config.Event) engine.Node {
	segments := config.RESTSegments(e.Path)
	resource := engine.Ref(RESTAPIID, "RootResourceId")
	if len(segments) > 0 {
		resource = engine.Ref(restResourceID(e.Path), "ResourceId")
	}
	in := map[string]any{
		inputAPI: api, "resource": resource, "method": strings.ToUpper(e.Method),
		inputAuthType: authNone, inputAuth: "", "uri": b.invocationURI(fn),
	}
	switch {
	case e.IAM:
		in[inputAuthType] = restAuthIAM
	case e.Authorizer != "":
		in[inputAuth] = engine.Ref(restAuthorizerID(e.Authorizer), "AuthorizerId")
		in[inputAuthType] = restAuthCustom
		if b.project.REST.Authorizers[e.Authorizer].Type == config.RESTAuthorizerCognito {
			in[inputAuthType] = restAuthCognito
		}
	}
	if len(e.Scopes) > 0 {
		in["scopes"] = e.Scopes
	}
	if e.Private {
		in["apiKeyRequired"] = true
	}
	params := map[string]any{}
	for _, q := range e.RequiredQuery {
		params["method.request.querystring."+q] = true
	}
	for _, h := range e.RequiredHeaders {
		params["method.request.header."+h] = true
	}
	if len(params) > 0 {
		in["parameters"] = params
		in["validator"] = engine.Ref(RESTAPIID, "ParametersValidatorId")
	}
	if e.TimeoutMillis != nil {
		in["timeoutMillis"] = *e.TimeoutMillis
	}
	return engine.Node{ID: restRouteID(e.Method, e.Path), Type: awsp.TypeRESTRoute, Inputs: in}
}

// restStage is the stage of the rest api, after everything a deployment takes a snapshot of and
// after the deletes, so the snapshot has the routes the deploy leaves. Its deployment is a hash
// of what it is after, which changes when they do.
func (b *builder) restStage(api any, shape []engine.Node) engine.Node {
	type part struct {
		ID     string         `json:"id"`
		Inputs map[string]any `json:"inputs"`
	}
	parts := make([]part, 0, len(shape))
	after := make([]string, 0, len(shape))
	for _, n := range shape {
		if n.Type == awsp.TypePermission {
			continue
		}
		parts = append(parts, part{ID: n.ID, Inputs: n.Inputs})
		after = append(after, n.ID)
	}
	slices.SortFunc(parts, func(a, b part) int { return strings.Compare(a.ID, b.ID) })
	data, _ := json.Marshal(parts)
	sum := sha256.Sum256(data)
	in := map[string]any{inputAPI: api, inputStage: b.project.Stage, "deployment": hex.EncodeToString(sum[:])[:16]}
	if r := b.project.REST; r != nil && r.Throttling != nil {
		in["throttling"] = throttlingInputs(r.Throttling)
	}
	return engine.Node{ID: RESTStageID, Type: awsp.TypeRESTStage, Name: b.project.Stage, Inputs: in, After: after, AfterDeletes: true}
}
