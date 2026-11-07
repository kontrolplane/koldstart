package config

import (
	"strings"
	"testing"
)

// restProject is a project with a rest route of api and a token authorizer of auth.
func restProject() *Project {
	return &Project{
		App: "orders", Stage: "dev",
		Functions: map[string]Function{
			"api":  {Runtime: RuntimeNode22, Handler: "src/api.handler", Events: []Event{{Type: EventRESTRoute, Method: "GET", Path: "/orders/{id}", Authorizer: "token"}}},
			"auth": {Runtime: RuntimeNode22, Handler: "src/auth.handler"},
		},
		Resources: map[string]Resource{},
		REST: &REST{Authorizers: map[string]RESTAuthorizer{
			"token": {Type: RESTAuthorizerToken, Function: "auth", Header: "Authorization"},
		}},
	}
}

// TestValidateREST checks what api gateway would refuse of the rest routes and their authorizers
// is reported before a deploy, and a project that has none of it passes.
func TestValidateREST(t *testing.T) {
	tests := []struct {
		name   string
		change func(p *Project)
		want   string
	}{
		{name: "valid", change: func(*Project) {}},
		{
			name:   "a path without a leading slash",
			change: func(p *Project) { p.Functions["api"].Events[0].Path = "orders" },
			want:   `path "orders": it must start with /`,
		},
		{
			name:   "a greedy parameter before the end",
			change: func(p *Project) { p.Functions["api"].Events[0].Path = "/{proxy+}/x" },
			want:   "{proxy+} takes the rest of the path, so it must be the last segment",
		},
		{
			name:   "a trailing slash",
			change: func(p *Project) { p.Functions["api"].Events[0].Path = "/orders/" },
			want:   "it has an empty segment or ends with /",
		},
		{
			name:   "a missing authorizer",
			change: func(p *Project) { p.REST.Authorizers = nil },
			want:   `no authorizer "token" in rest.authorizers`,
		},
		{
			name:   "iam and an authorizer",
			change: func(p *Project) { p.Functions["api"].Events[0].IAM = true },
			want:   "a route takes iam or an authorizer, not both",
		},
		{
			name:   "scopes without a cognito authorizer",
			change: func(p *Project) { p.Functions["api"].Events[0].Scopes = []string{"orders/read"} },
			want:   "scopes are of a cognito authorizer",
		},
		{
			name: "the same route twice",
			change: func(p *Project) {
				fn := p.Functions["auth"]
				fn.Events = []Event{{Type: EventRESTRoute, Method: "GET", Path: "/orders/{id}"}}
				p.Functions["auth"] = fn
			},
			want: "GET /orders/{id} is also a rest route of",
		},
		{
			name: "two parameters after one path",
			change: func(p *Project) {
				fn := p.Functions["auth"]
				fn.Events = []Event{{Type: EventRESTRoute, Method: "DELETE", Path: "/orders/{name}"}}
				p.Functions["auth"] = fn
			},
			want: "after /orders the rest routes have the parameters {id} and {name}",
		},
		{
			name: "an authorizer of no function",
			change: func(p *Project) {
				p.REST.Authorizers["token"] = RESTAuthorizer{Type: RESTAuthorizerToken, Function: "gone"}
			},
			want: `rest.authorizers[token]: function "gone" is not a function of the project`,
		},
		{
			name: "a cached request authorizer without identity",
			change: func(p *Project) {
				p.REST.Authorizers["token"] = RESTAuthorizer{Type: RESTAuthorizerRequest, Function: "auth", TTL: 60}
			},
			want: "set identity, or ttl = 0.s",
		},
		{
			name: "a user pool of no resource",
			change: func(p *Project) {
				p.REST.Authorizers["token"] = RESTAuthorizer{Type: RESTAuthorizerCognito, UserPools: []Value{{Ref: &Ref{Resource: "users", Attribute: "Arn"}}}}
			},
			want: `rest.authorizers[token].userPools[0]: kdst.ref("users", "Arn") refers to no function or resource`,
		},
		{
			name: "the rest api's url as an output",
			change: func(p *Project) {
				p.Outputs = map[string]Value{OutputRESTAPIURL: {Literal: "https://x"}}
			},
			want: "outputs[restApiUrl]: koldstart generates restApiUrl, the url of the rest api",
		},
		{
			name: "the api key's id as an output",
			change: func(p *Project) {
				p.Functions["api"].Events[0].Private = true
				p.Outputs = map[string]Value{OutputRESTAPIKeyID: {Literal: "x"}}
			},
			want: "outputs[restApiKeyId]: koldstart generates restApiKeyId",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := restProject()
			tt.change(p)
			err := p.Validate()
			switch {
			case tt.want == "" && err != nil:
				t.Errorf("Validate = %v, want nil", err)
			case tt.want != "" && (err == nil || !strings.Contains(err.Error(), tt.want)):
				t.Errorf("Validate = %v, want an error with %q", err, tt.want)
			}
		})
	}
}

// TestRESTSegments checks a path splits into the resources api gateway makes of it, the root
// into none.
func TestRESTSegments(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		{path: "/", want: ""},
		{path: "/orders", want: "orders"},
		{path: "/orders/{id}/items/{proxy+}", want: "orders,{id},items,{proxy+}"},
	}
	for _, tt := range tests {
		if got := strings.Join(RESTSegments(tt.path), ","); got != tt.want {
			t.Errorf("RESTSegments(%q) = %q, want %q", tt.path, got, tt.want)
		}
	}
}
