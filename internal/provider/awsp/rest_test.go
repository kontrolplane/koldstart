package awsp_test

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/apigateway"

	"github.com/kontrolplane/koldstart/internal/build"
	"github.com/kontrolplane/koldstart/internal/config"
	"github.com/kontrolplane/koldstart/internal/desired"
	"github.com/kontrolplane/koldstart/internal/engine"
	"github.com/kontrolplane/koldstart/internal/provider/awsp"
	"github.com/kontrolplane/koldstart/internal/state"
)

// stub stands in for the providers of what is not the rest api: each resource exists once
// created, with an arn and a name.
type stub struct{}

func (stub) result(req engine.Request) engine.Result {
	name := req.Name
	if name == "" {
		name = req.ID
	}
	return engine.Result{PhysicalID: name, Attributes: map[string]string{"Arn": "arn:aws:lambda:eu-west-1:123456789012:function:" + name, "FunctionName": name}}
}

func (s stub) Create(_ context.Context, req engine.Request) (engine.Result, error) {
	return s.result(req), nil
}
func (s stub) Read(_ context.Context, req engine.Request) (engine.Result, bool, error) {
	return s.result(req), true, nil
}
func (s stub) Update(_ context.Context, req engine.Request) (engine.Result, error) {
	return s.result(req), nil
}
func (stub) Delete(context.Context, engine.Request) error { return nil }
func (stub) Replaces(_, _ json.RawMessage) bool           { return false }

// restEngine is an engine with the providers of the rest api on the fake, and the stub for the
// rest.
func restEngine(f *fakeAPIGateway) *engine.Engine {
	return &engine.Engine{
		Providers: map[string]engine.Provider{
			awsp.TypeRESTAPI:        awsp.RESTAPI{Client: f, Region: "eu-west-1"},
			awsp.TypeRESTResource:   awsp.RESTResource{Client: f},
			awsp.TypeRESTRoute:      awsp.RESTRoute{Client: f},
			awsp.TypeRESTAuthorizer: awsp.RESTAuthorizer{Client: f},
			awsp.TypeRESTStage:      awsp.RESTStage{Client: f, Region: "eu-west-1"},
			awsp.TypeRESTKey:        awsp.RESTKey{Client: f, Region: "eu-west-1"},
			awsp.TypeRESTUsagePlan:  awsp.RESTUsagePlan{Client: f},
		},
		Fallback:    stub{},
		Concurrency: 8,
	}
}

// restRoutes is a project of the function api with the rest routes given, and a token authorizer
// of auth.
func restRoutes(events ...config.Event) *config.Project {
	fn := func(events ...config.Event) config.Function {
		return config.Function{Runtime: config.RuntimeNode22, Handler: "src/x.handler", Memory: 128, Timeout: 6, Events: events}
	}
	return &config.Project{
		App: "orders", Stage: "dev",
		Functions: map[string]config.Function{"api": fn(events...), "auth": fn()},
		Resources: map[string]config.Resource{},
		REST: &config.REST{Authorizers: map[string]config.RESTAuthorizer{
			"token": {Type: config.RESTAuthorizerToken, Function: "auth", Header: "Authorization"},
		}},
	}
}

func rest(method, path string) config.Event {
	return config.Event{Type: config.EventRESTRoute, Method: method, Path: path}
}

// TestRESTDeploy deploys rest routes to the fake as aws takes them, again and again: the stage
// serves a deployment of the routes each deploy leaves, a route removed included, a parameter
// renamed replaces its resource, the api key and usage plan come and go with the private routes,
// a deploy of nothing new changes nothing, and a remove deletes everything.
func TestRESTDeploy(t *testing.T) {
	f := newFakeAPIGateway()
	e := restEngine(f)
	backend := state.NewMemory()
	deploy := func(p *config.Project) *engine.Plan {
		t.Helper()
		var nodes []engine.Node
		if p != nil {
			var err error
			nodes, err = desired.Build(p, desired.Target{Account: "123456789012", Region: "eu-west-1", Bucket: "b",
				Archives: map[string]build.Archive{"api": {SHA256: "a"}, "auth": {SHA256: "b"}}})
			if err != nil {
				t.Fatal(err)
			}
		}
		return apply(t, e, backend, "orders", nodes)
	}
	authorized := rest("GET", "/orders/{id}")
	authorized.Authorizer = "token"
	private := rest("POST", "/orders")
	private.Private, private.RequiredQuery = true, []string{"dryRun"}
	renamed := rest("GET", "/orders/{orderId}")
	renamed.Authorizer = "token"
	tests := []struct {
		name    string
		project *config.Project
		serves  []string
		keys    int
	}{
		{name: "first", project: restRoutes(rest("GET", "/orders"), authorized, private, rest("ANY", "/")), serves: []string{"ANY /", "GET /orders", "GET /orders/{id}", "POST /orders"}, keys: 1},
		{name: "a route gone, none private", project: restRoutes(rest("GET", "/orders"), authorized), serves: []string{"GET /orders", "GET /orders/{id}"}},
		{name: "a parameter renamed", project: restRoutes(rest("GET", "/orders"), renamed), serves: []string{"GET /orders", "GET /orders/{orderId}"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			deploy(tt.project)
			id, api := f.only()
			if got := f.served(id, "dev"); !slices.Equal(got, tt.serves) {
				t.Errorf("the stage serves %v, want %v", got, tt.serves)
			}
			if len(api.deployments) != 1 {
				t.Errorf("%d deployments are left, want the one the stage serves", len(api.deployments))
			}
			if len(f.keys) != tt.keys || len(f.plans) != tt.keys {
				t.Errorf("%d keys and %d usage plans, want %d", len(f.keys), len(f.plans), tt.keys)
			}
			if plan := deploy(tt.project); plan.HasChanges() {
				t.Errorf("a deploy of nothing new changes %+v", plan.Changes)
			}
		})
	}
	_, api := f.only()
	for _, m := range api.methods {
		if aws.ToString(m.put.HttpMethod) == "GET" && aws.ToString(m.put.AuthorizationType) == "CUSTOM" && m.integration == nil {
			t.Error("a route of the authorizer has no integration")
		}
	}
	deploy(nil)
	if id, _ := f.only(); id != "" || len(f.keys) > 0 || len(f.plans) > 0 {
		t.Errorf("after a remove there are still %s, %d keys and %d plans", id, len(f.keys), len(f.plans))
	}
}

// TestRESTRouteInputs checks what a route sends api gateway: its authorization, key, parameters,
// validator and timeout, and a lambda proxy integration of the function by POST.
func TestRESTRouteInputs(t *testing.T) {
	f := newFakeAPIGateway()
	e := restEngine(f)
	timeout := 5000
	route := rest("POST", "/orders")
	route.Private, route.RequiredHeaders, route.TimeoutMillis = true, []string{"X-Tenant"}, &timeout
	nodes, err := desired.Build(restRoutes(route), desired.Target{Account: "123456789012", Region: "eu-west-1", Bucket: "b",
		Archives: map[string]build.Archive{"api": {SHA256: "a"}, "auth": {SHA256: "b"}}})
	if err != nil {
		t.Fatal(err)
	}
	apply(t, e, state.NewMemory(), "orders", nodes)
	_, api := f.only()
	var m *fakeMethod
	for _, got := range api.methods {
		m = got
	}
	if m == nil || !m.put.ApiKeyRequired || !m.put.RequestParameters["method.request.header.X-Tenant"] || m.put.RequestValidatorId == nil ||
		api.validators[aws.ToString(m.put.RequestValidatorId)] != "koldstart-parameters" {
		t.Fatalf("the method = %+v", m)
	}
	in := m.integration
	if in.Type != "AWS_PROXY" || aws.ToString(in.IntegrationHttpMethod) != "POST" || aws.ToInt32(in.TimeoutInMillis) != 5000 ||
		!strings.HasSuffix(aws.ToString(in.Uri), ":function:orders-dev-api/invocations") {
		t.Errorf("the integration = %+v", in)
	}
}

// TestRESTAuthorizerPatches checks an authorizer changes in place, so the methods that name it
// keep it: its fields set again, and the user pools it gains added and those it loses removed.
func TestRESTAuthorizerPatches(t *testing.T) {
	f := newFakeAPIGateway()
	tests := []struct {
		name          string
		before, after awsp.RESTAuthorizerInputs
		pools         []string
	}{
		{
			name:   "a header",
			before: awsp.RESTAuthorizerInputs{Name: "a", Type: "TOKEN", URI: "u", Identity: "method.request.header.Authorization"},
			after:  awsp.RESTAuthorizerInputs{Name: "a", Type: "TOKEN", URI: "u", Identity: "method.request.header.X-Token", TTL: 60},
		},
		{
			name:   "user pools",
			before: awsp.RESTAuthorizerInputs{Name: "a", Type: "COGNITO_USER_POOLS", Identity: "method.request.header.Authorization", UserPools: []string{"p1", "p2"}},
			after:  awsp.RESTAuthorizerInputs{Name: "a", Type: "COGNITO_USER_POOLS", Identity: "method.request.header.Authorization", UserPools: []string{"p2", "p3"}},
			pools:  []string{"p2", "p3"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			id, _ := f.CreateRestApi(ctx, &apigateway.CreateRestApiInput{Name: aws.String("orders")})
			tt.before.API, tt.after.API = aws.ToString(id.Id), aws.ToString(id.Id)
			p := awsp.RESTAuthorizer{Client: f}
			before, _ := json.Marshal(tt.before)
			after, _ := json.Marshal(tt.after)
			res, err := p.Create(ctx, engine.Request{Inputs: before})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := p.Update(ctx, engine.Request{PhysicalID: res.PhysicalID, Prior: before, Inputs: after}); err != nil {
				t.Fatal(err)
			}
			got := f.apis[tt.after.API].authorizers[res.PhysicalID]
			if aws.ToString(got.IdentitySource) != tt.after.Identity || aws.ToInt32(got.AuthorizerResultTtlInSeconds) != tt.after.TTL {
				t.Errorf("the authorizer = %+v", got)
			}
			slices.Sort(got.ProviderARNs)
			if !slices.Equal(got.ProviderARNs, tt.pools) {
				t.Errorf("user pools = %v, want %v", got.ProviderARNs, tt.pools)
			}
			if (awsp.RESTAuthorizer{}).Replaces(before, after) {
				t.Error("the change replaces the authorizer")
			}
		})
	}
}
