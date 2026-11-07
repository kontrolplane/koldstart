package desired

import (
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/kontrolplane/koldstart/internal/build"
	"github.com/kontrolplane/koldstart/internal/config"
	"github.com/kontrolplane/koldstart/internal/engine"
	"github.com/kontrolplane/koldstart/internal/provider/awsp"
	"github.com/kontrolplane/koldstart/internal/state"
)

// restProject is a project with rest routes of api, a token authorizer of auth and a cognito one
// of a pool of the project.
func restProject() *config.Project {
	timeout := 5500
	fn := func(events ...config.Event) config.Function {
		return config.Function{Runtime: config.RuntimeNode22, Handler: "src/x.handler", Memory: 128, Timeout: 6, Events: events}
	}
	return &config.Project{
		App: "orders", Stage: "dev",
		Functions: map[string]config.Function{
			"api": fn(
				config.Event{Type: config.EventRESTRoute, Method: "GET", Path: "/orders"},
				config.Event{Type: config.EventRESTRoute, Method: "GET", Path: "/orders/{id}", Authorizer: "token"},
				config.Event{Type: config.EventRESTRoute, Method: "POST", Path: "/orders", Private: true, RequiredQuery: []string{"dryRun"}, RequiredHeaders: []string{"X-Tenant"}, TimeoutMillis: &timeout},
				config.Event{Type: config.EventRESTRoute, Method: "ANY", Path: "/", IAM: true},
				config.Event{Type: config.EventRESTRoute, Method: "GET", Path: "/me", Authorizer: "users", Scopes: []string{"orders/read"}},
			),
			"auth": fn(),
		},
		Resources: map[string]config.Resource{"pool": {Type: "AWS::Cognito::UserPool"}},
		REST: &config.REST{
			Throttling: &config.Throttling{Rate: 10, Burst: 20},
			Authorizers: map[string]config.RESTAuthorizer{
				"token": {Type: config.RESTAuthorizerToken, Function: "auth", Header: "Authorization", Validation: "^Bearer .+$", TTL: 300},
				"users": {Type: config.RESTAuthorizerCognito, Header: "Authorization", UserPools: []config.Value{{Ref: &config.Ref{Resource: "pool", Attribute: "Arn"}}}},
			},
		},
	}
}

func restTarget() Target {
	return Target{Account: "123456789012", Region: "eu-west-1", Bucket: "b", Archives: map[string]build.Archive{"api": {SHA256: "a"}, "auth": {SHA256: "b"}}}
}

// TestRESTAPI checks the nodes of the rest api: a resource for each segment under the one before
// it, each route on its resource with its authorization, key, parameters and timeout, the
// authorizers, the permissions, the stage after all of them and after the deletes, and the key
// and usage plan of the private route; and that they plan, in an order without a cycle, against
// an empty state, which calls no provider.
func TestRESTAPI(t *testing.T) {
	nodes, err := Build(restProject(), restTarget())
	if err != nil {
		t.Fatal(err)
	}
	got := byID(nodes)
	for _, id := range []string{
		RESTAPIID, "restresource/orders", "restresource/orders/{}", "restresource/me",
		"restauthorizer/token", "restauthorizer/users", "permission/api/restapi", "permission/auth/restauthorizer-token",
		RESTStageID, RESTKeyID, RESTUsagePlanID,
	} {
		if _, ok := got[id]; !ok {
			t.Errorf("no node %s among %v", id, slices.Sorted(maps.Keys(got)))
		}
	}
	tests := []struct {
		id    string
		input string
		want  any
	}{
		{id: "restresource/orders", input: "parent", want: engine.Ref(RESTAPIID, "RootResourceId")},
		{id: "restresource/orders/{}", input: "parent", want: engine.Ref("restresource/orders", "ResourceId")},
		{id: "restresource/orders/{}", input: "pathPart", want: "{id}"},
		{id: "restroute/ANY /", input: "resource", want: engine.Ref(RESTAPIID, "RootResourceId")},
		{id: "restroute/ANY /", input: "authorizationType", want: "AWS_IAM"},
		{id: "restroute/GET /orders", input: "authorizationType", want: "NONE"},
		{id: "restroute/GET /orders/{id}", input: "resource", want: engine.Ref("restresource/orders/{}", "ResourceId")},
		{id: "restroute/GET /orders/{id}", input: "authorizer", want: engine.Ref("restauthorizer/token", "AuthorizerId")},
		{id: "restroute/GET /orders/{id}", input: "authorizationType", want: "CUSTOM"},
		{id: "restroute/GET /me", input: "authorizationType", want: "COGNITO_USER_POOLS"},
		{id: "restroute/GET /me", input: "scopes", want: []string{"orders/read"}},
		{id: "restroute/POST /orders", input: "apiKeyRequired", want: true},
		{id: "restroute/POST /orders", input: "parameters", want: map[string]any{"method.request.querystring.dryRun": true, "method.request.header.X-Tenant": true}},
		{id: "restroute/POST /orders", input: "validator", want: engine.Ref(RESTAPIID, "ParametersValidatorId")},
		{id: "restroute/POST /orders", input: "timeoutMillis", want: 5500},
		{id: "restroute/GET /orders", input: "uri", want: engine.Join("arn:aws:apigateway:eu-west-1:lambda:path/2015-03-31/functions/", engine.Ref("function/api", "Arn"), "/invocations")},
		{id: "restauthorizer/token", input: "identity", want: "method.request.header.Authorization"},
		{id: "restauthorizer/token", input: "type", want: "TOKEN"},
		{id: "restauthorizer/users", input: "userPools", want: []any{engine.Ref("resource/pool", "Arn")}},
		{id: "permission/api/restapi", input: "sourceArn", want: engine.Join("arn:aws:execute-api:eu-west-1:123456789012:", engine.Ref(RESTAPIID, "RestApiId"), "/*/*")},
		{id: RESTStageID, input: "throttling", want: map[string]any{"rate": 10.0, "burst": 20}},
		{id: RESTUsagePlanID, input: "stage", want: engine.Ref(RESTStageID, "StageName")},
	}
	for _, tt := range tests {
		t.Run(tt.id+" "+tt.input, func(t *testing.T) {
			if g := js(t, got[tt.id].Inputs[tt.input]); g != js(t, tt.want) {
				t.Errorf("%s.%s = %s, want %s", tt.id, tt.input, g, js(t, tt.want))
			}
		})
	}
	for _, key := range []string{"apiKeyRequired", "parameters", "validator", "timeoutMillis", "scopes"} {
		if _, set := got["restroute/GET /orders"].Inputs[key]; set {
			t.Errorf("a plain route sets %s", key)
		}
	}
	stage := got[RESTStageID]
	if !stage.AfterDeletes || !slices.Contains(stage.After, "restroute/GET /orders") || !slices.Contains(stage.After, "restauthorizer/token") ||
		slices.Contains(stage.After, "permission/api/restapi") {
		t.Errorf("the stage is after %v, after the deletes %v", stage.After, stage.AfterDeletes)
	}
	if _, err := (&engine.Engine{Fallback: awsp.RESTKey{}}).Plan(t.Context(), nodes, state.New("orders", "dev", "eu-west-1", "123456789012")); err != nil {
		t.Errorf("the nodes do not plan: %v", err)
	}
}

// TestRESTDeployment checks the stage's deployment changes with what a deployment takes a
// snapshot of, a route or an authorizer, and not with a function, which a route invokes by its
// arn whatever its code.
func TestRESTDeployment(t *testing.T) {
	deployment := func(change func(p *config.Project)) string {
		p := restProject()
		change(p)
		nodes, err := Build(p, restTarget())
		if err != nil {
			t.Fatal(err)
		}
		return byID(nodes)[RESTStageID].Inputs["deployment"].(string)
	}
	base := deployment(func(*config.Project) {})
	tests := []struct {
		name    string
		change  func(p *config.Project)
		changes bool
	}{
		{name: "nothing", change: func(*config.Project) {}},
		{name: "a function's memory", change: func(p *config.Project) {
			fn := p.Functions["api"]
			fn.Memory = 512
			p.Functions["api"] = fn
		}},
		{name: "a route gone", changes: true, change: func(p *config.Project) {
			fn := p.Functions["api"]
			fn.Events = fn.Events[1:]
			p.Functions["api"] = fn
		}},
		{name: "a route made private", changes: true, change: func(p *config.Project) { p.Functions["api"].Events[0].Private = true }},
		{name: "an authorizer's header", changes: true, change: func(p *config.Project) {
			a := p.REST.Authorizers["token"]
			a.Header = "X-Token"
			p.REST.Authorizers["token"] = a
		}},
		{name: "the stage's throttling", change: func(p *config.Project) { p.REST.Throttling.Rate = 99 }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := deployment(tt.change) != base; got != tt.changes {
				t.Errorf("the deployment changes: %v, want %v", got, tt.changes)
			}
		})
	}
}

// TestRESTGroups checks a rest route belongs to its function, by the function its integration
// invokes, and triggers it, and the rest of the rest api is a group of its own.
func TestRESTGroups(t *testing.T) {
	tests := []struct {
		id         string
		deps       []string
		kind, name string
	}{
		{id: "restroute/GET /orders", deps: []string{RESTAPIID, "restresource/orders", "function/api"}, kind: GroupFunction, name: "api"},
		{id: RESTAPIID, kind: GroupREST},
		{id: "restresource/orders/{}", kind: GroupREST},
		{id: "restauthorizer/token", kind: GroupREST},
		{id: RESTStageID, kind: GroupREST},
		{id: RESTUsagePlanID, kind: GroupREST},
	}
	for _, tt := range tests {
		if kind, name := Group(tt.id, tt.deps); kind != tt.kind || name != tt.name {
			t.Errorf("Group(%s) = %s %s, want %s %s", tt.id, kind, name, tt.kind, tt.name)
		}
	}
	resources := map[string]*state.Resource{
		"restroute/GET /orders": {Dependencies: []string{RESTAPIID, "function/api"}},
		"restroute/GET /me":     {Dependencies: []string{RESTAPIID, "function/other"}},
	}
	if got := strings.Join(Triggers(resources, "api"), ","); got != "restroute/GET /orders" {
		t.Errorf("Triggers = %s", got)
	}
}
