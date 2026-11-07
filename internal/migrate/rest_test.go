package migrate

import (
	"slices"
	"testing"

	"go.yaml.in/yaml/v3"
)

// TestRestAuthOf checks what the authorizer of a route of the rest api becomes on the http api:
// iam, a lambda authorizer of a function of the service with the serverless framework's
// defaults, a jwt authorizer of a cognito user pool, or nothing, with why the route is open.
func TestRestAuthOf(t *testing.T) {
	header := []string{"$request.header.Authorization"}
	tests := []struct {
		name       string
		authorizer string
		iam        bool
		want       *restAuthorizer
		open       bool
	}{
		{name: "none", authorizer: "~"},
		{name: "aws_iam", authorizer: "aws_iam", iam: true},
		{name: "AWS_IAM as a type", authorizer: "{type: AWS_IAM}", iam: true},
		{name: "a function by its name", authorizer: "auth", want: &restAuthorizer{function: "auth", identity: header, ttl: 300}},
		{name: "a function renamed", authorizer: "{name: check_token}", want: &restAuthorizer{function: "check-token", identity: header, ttl: 300}},
		{
			name:       "a request authorizer",
			authorizer: "{name: auth, type: request, identitySource: 'method.request.header.X, context.y, stageVariables.z', resultTtlInSeconds: 10}",
			want:       &restAuthorizer{function: "auth", identity: []string{"$request.header.X", "$context.y", "$stageVariables.z"}, ttl: 10},
		},
		{name: "a request authorizer not cached", authorizer: "{name: auth, type: request, resultTtlInSeconds: 0}", want: &restAuthorizer{function: "auth"}},
		{name: "a token authorizer not cached", authorizer: "{name: auth, resultTtlInSeconds: 0}", want: &restAuthorizer{function: "auth", identity: header}},
		{name: "a function by its arn's GetAtt", authorizer: "{arn: !GetAtt AuthLambdaFunction.Arn}", want: &restAuthorizer{function: "auth", identity: header, ttl: 300}},
		{
			name:       "a user pool",
			authorizer: "{arn: 'arn:aws:cognito-idp:eu-west-1:123456789012:userpool/eu-west-1_abc'}",
			want:       &restAuthorizer{issuer: `"https://cognito-idp.eu-west-1.amazonaws.com/eu-west-1_abc"`},
		},
		{
			name:       "a user pool in china",
			authorizer: "'arn:aws-cn:cognito-idp:cn-north-1:123456789012:userpool/cn-north-1_abc'",
			want:       &restAuthorizer{issuer: `"https://cognito-idp.cn-north-1.amazonaws.com.cn/cn-north-1_abc"`},
		},
		{name: "a user pool of the stack", authorizer: "{name: users, type: COGNITO_USER_POOLS, arn: !GetAtt UserPool.Arn}", open: true},
		{name: "a function of another service", authorizer: "'arn:aws:lambda:eu-west-1:123456789012:function:shared'", open: true},
		{name: "a function that is not there", authorizer: "missing", open: true},
		{name: "an authorizer by its id", authorizer: "{type: CUSTOM, authorizerId: abc123}", open: true},
		{name: "neither a name nor an arn", authorizer: "{type: token}", open: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src := "service: x\nprovider: {region: eu-west-1}\nfunctions: {auth: {handler: a.h}, check_token: {handler: c.h}}\n" +
				"resources: {Resources: {UserPool: {Type: AWS::Cognito::UserPool}}}\nauthorizer: " + tt.authorizer + "\n"
			var doc yaml.Node
			if err := yaml.Unmarshal([]byte(src), &doc); err != nil {
				t.Fatal(err)
			}
			c := &converter{doc: doc.Content[0], region: "eu-west-1", resourceNames: map[string]string{}}
			c.nameFunctions()
			c.nameResources()
			n := get(doc.Content[0], "authorizer")
			if n.ShortTag() == tagNull {
				n = nil
			}
			got := c.restAuthOf(n)
			if got.iam != tt.iam || (got.open != "") != tt.open {
				t.Errorf("iam %v, open %q; want iam %v, open %v", got.iam, got.open, tt.iam, tt.open)
			}
			switch {
			case tt.want == nil && got.authorizer != nil:
				t.Errorf("authorizer %+v, want none", *got.authorizer)
			case tt.want != nil && (got.authorizer == nil || !got.authorizer.same(tt.want)):
				t.Errorf("authorizer %+v, want %+v", got.authorizer, *tt.want)
			}
		})
	}
}

// TestRestIntegration checks which integrations of the rest api the http api can do, as the
// serverless framework spells them.
func TestRestIntegration(t *testing.T) {
	tests := []struct {
		integration string
		ok          bool
	}{
		{integration: "", ok: true},
		{integration: "lambda-proxy", ok: true},
		{integration: "aws_proxy", ok: true},
		{integration: "lambda", ok: true},
		{integration: "AWS", ok: true},
		{integration: "http-proxy"},
		{integration: "http"},
		{integration: "mock"},
	}
	for _, tt := range tests {
		n := &yaml.Node{Kind: yaml.MappingNode}
		if tt.integration != "" {
			n.Content = []*yaml.Node{{Kind: yaml.ScalarNode, Value: "integration"}, {Kind: yaml.ScalarNode, Value: tt.integration}}
		}
		if _, ok := restIntegration(n); ok != tt.ok {
			t.Errorf("restIntegration(%q) = %v, want %v", tt.integration, ok, tt.ok)
		}
	}
}

// TestNameRestAuthorizers checks routes that take the same authorizer share one in
// http.authorizers, and a name taken by one of provider.httpApi gets a number.
func TestNameRestAuthorizers(t *testing.T) {
	src := `service: x
provider: {httpApi: {authorizers: {auth: {type: request, functionName: auth}}}}
functions:
  auth: {handler: a.h}
  api:
    handler: b.h
    events:
      - http: {method: get, path: a, authorizer: auth}
      - http: {method: get, path: b, authorizer: {name: auth}}
      - http: {method: get, path: c, authorizer: {name: auth, resultTtlInSeconds: 0}}
      - http: {method: get, path: d, authorizer: aws_iam}
      - http: {method: get, path: e, integration: mock, authorizer: {name: auth, resultTtlInSeconds: 5}}
`
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(src), &doc); err != nil {
		t.Fatal(err)
	}
	c := &converter{doc: doc.Content[0], resourceNames: map[string]string{}}
	c.nameFunctions()
	c.nameRestAuthorizers()
	names := make([]string, 0, len(c.restAuthorizers))
	wheres := make([]string, 0, len(c.restAuthorizers))
	for _, a := range c.restAuthorizers {
		names = append(names, a.name)
		wheres = append(wheres, a.where)
	}
	if want := []string{"auth-2", "auth-3"}; !slices.Equal(names, want) {
		t.Errorf("names %q, want %q", names, want)
	}
	if want := []string{"functions.api.events[0].authorizer", "functions.api.events[2].authorizer"}; !slices.Equal(wheres, want) {
		t.Errorf("wheres %q, want %q", wheres, want)
	}
}
