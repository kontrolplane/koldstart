package migrate

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

// restAuthorizer is an authorizer of the rest api that becomes one of the http api: a lambda
// authorizer of a function of the project, or a jwt authorizer of a cognito user pool.
type restAuthorizer struct {
	// name is its name in http.authorizers, and where the authorizer of the first route that
	// has it, which its todos are about.
	name, where string
	// function, identity, ttl and validation are of a lambda authorizer; identity is of the
	// http api, $request.header.Authorization.
	function   string
	identity   []string
	ttl        int
	validation string
	// issuer is of a jwt authorizer, as a pkl string.
	issuer string
}

// same reports whether a and o are the same authorizer, which routes then share.
func (a *restAuthorizer) same(o *restAuthorizer) bool {
	return a.function == o.function && slices.Equal(a.identity, o.identity) && a.ttl == o.ttl &&
		a.validation == o.validation && a.issuer == o.issuer
}

// restAuth is what the authorizer of a route of the rest api becomes on the http api: iam, an
// authorizer, or, with why, nothing, which leaves the route open.
type restAuth struct {
	iam        bool
	authorizer *restAuthorizer
	open       string
}

// restAuthTTL is how long the rest api caches what an authorizer returns when the serverless
// framework is not told.
const restAuthTTL = 300

// cognitoPool is the arn of a cognito user pool: its partition, region and id.
var cognitoPool = regexp.MustCompile(`^arn:([a-z-]+):cognito-idp:([^:]+):[^:]*:userpool/([^:/]+)$`)

// restAuthOf converts the authorizer of a route of the rest api: aws_iam, a function of the
// service by its name or its arn's Fn::GetAtt, or a cognito user pool by its arn. It writes
// nothing, so it gives the same for the same node however often it is called.
func (c *converter) restAuthOf(n *yaml.Node) restAuth {
	if n == nil {
		return restAuth{}
	}
	if n.Kind == yaml.ScalarNode {
		switch {
		case strings.EqualFold(n.Value, "aws_iam"):
			return restAuth{iam: true}
		case strings.Contains(n.Value, ":"):
			return c.restArnAuth(n, n)
		}
		return c.restFunctionAuth(n.Value, n)
	}
	typ := strings.ToUpper(str(get(n, "type")))
	switch {
	case get(n, "authorizerId") != nil:
		return restAuth{open: "it names an authorizer of the rest api by its id, which the http api cannot use"}
	case typ == "AWS_IAM":
		return restAuth{iam: true}
	case get(n, "arn") != nil:
		return c.restArnAuth(get(n, "arn"), n)
	case str(get(n, "name")) != "":
		return c.restFunctionAuth(str(get(n, "name")), n)
	}
	return restAuth{open: "it names neither a function nor an arn"}
}

// restArnAuth converts an authorizer given by its arn: a function of the service by its
// Fn::GetAtt, or a cognito user pool.
func (c *converter) restArnAuth(arn, n *yaml.Node) restAuth {
	if name, arg := intrinsicOf(arn); name == intrinsicGetAtt {
		target, _, _ := strings.Cut(arg.Value, ".")
		if arg.Kind == yaml.SequenceNode && len(arg.Content) == 2 {
			target = arg.Content[0].Value
		}
		if fn, ok := c.functionOf(target); ok {
			return c.lambdaAuth(fn, n)
		}
	}
	v := c.nodeValue(arn)
	inner, literal := stringLiteral(v.expr)
	if !v.ok || !v.interpolable || !literal {
		return restAuth{open: "a jwt authorizer takes the issuer of a user pool as a string, https://cognito-idp.<region>.amazonaws.com/<pool id>, not a ref"}
	}
	m := cognitoPool.FindStringSubmatch(inner)
	if m == nil {
		return restAuth{open: "a lambda authorizer is a function of the project, and " + inner + " is not one"}
	}
	suffix := "amazonaws.com"
	if m[1] == "aws-cn" {
		suffix = "amazonaws.com.cn"
	}
	return restAuth{authorizer: &restAuthorizer{issuer: fmt.Sprintf(`"https://cognito-idp.%s.%s/%s"`, m[2], suffix, m[3])}}
}

// restFunctionAuth converts an authorizer that names a function of the service.
func (c *converter) restFunctionAuth(name string, n *yaml.Node) restAuth {
	fn, ok := c.functionNames[name]
	if !ok {
		return restAuth{open: "its name, " + name + ", names no function in functions"}
	}
	return c.lambdaAuth(fn, n)
}

// lambdaAuth converts a lambda authorizer of the function fn, with its identity sources, ttl
// and validation expression, and the defaults of the serverless framework for them.
func (c *converter) lambdaAuth(fn string, n *yaml.Node) restAuth {
	a := &restAuthorizer{function: fn, ttl: restAuthTTL, validation: str(get(n, "identityValidationExpression"))}
	if ttl, ok := integer(get(n, "resultTtlInSeconds")); ok {
		a.ttl = ttl
	}
	sources := str(get(n, "identitySource"))
	if list := get(n, "identitySource"); list != nil && list.Kind == yaml.SequenceNode {
		items := make([]string, 0, len(list.Content))
		for _, item := range list.Content {
			items = append(items, item.Value)
		}
		sources = strings.Join(items, ",")
	}
	request := strings.EqualFold(str(get(n, "type")), "request")
	if sources == "" && (!request || a.ttl != 0) {
		sources = "method.request.header.Authorization"
	}
	for s := range strings.SplitSeq(sources, ",") {
		if s = strings.TrimSpace(s); s != "" {
			a.identity = append(a.identity, identitySource(s))
		}
	}
	return restAuth{authorizer: a}
}

// identitySources are the prefixes of the rest api's identity sources, and the http api's for
// each.
var identitySources = []struct{ rest, http string }{
	{"method.request.header.", "$request.header."},
	{"method.request.querystring.", "$request.querystring."},
	{"context.", "$context."},
	{"stageVariables.", "$stageVariables."},
}

// identitySource is an identity source of the rest api as the http api writes it:
// method.request.header.Authorization is $request.header.Authorization.
func identitySource(s string) string {
	for _, p := range identitySources {
		if rest, ok := strings.CutPrefix(s, p.rest); ok {
			return p.http + rest
		}
	}
	return s
}

// restAuthorizerName is the safe form of a name in http.authorizers, which koldstart puts in
// the ids of statements.
var restAuthorizerName = regexp.MustCompile(`[^A-Za-z0-9_-]+`)

// nameRestAuthorizers finds the authorizers the routes of the rest api take, converted, and
// gives each a name in http.authorizers, once for the routes that share it: its function's, or
// its name in serverless.yml, with a number when the name is taken.
func (c *converter) nameRestAuthorizers() {
	taken := keys(get(get(get(c.doc, "provider"), "httpApi"), "authorizers"))
	functions := get(c.doc, "functions")
	for _, name := range keys(functions) {
		for i, e := range orList(get(get(functions, name), "events")).Content {
			spec := get(e, "http")
			if spec == nil || spec.Kind != yaml.MappingNode {
				continue
			}
			if _, ok := restIntegration(spec); !ok {
				continue
			}
			a := c.restAuthOf(get(spec, "authorizer")).authorizer
			if a == nil || slices.ContainsFunc(c.restAuthorizers, a.same) {
				continue
			}
			base := a.function
			if base == "" {
				base = str(get(get(spec, "authorizer"), "name"))
			}
			if base == "" {
				base = "cognito"
			}
			base = strings.Trim(restAuthorizerName.ReplaceAllString(base, "-"), "-")
			a.name = base
			for n := 2; slices.Contains(taken, a.name); n++ {
				a.name = base + "-" + strconv.Itoa(n)
			}
			taken = append(taken, a.name)
			a.where = fmt.Sprintf("functions.%s.events[%d].authorizer", name, i)
			c.restAuthorizers = append(c.restAuthorizers, a)
		}
	}
}

// restAuthorizerFor is the authorizer of http.authorizers a route of the rest api takes.
func (c *converter) restAuthorizerFor(a *restAuthorizer) *restAuthorizer {
	i := slices.IndexFunc(c.restAuthorizers, a.same)
	if i < 0 {
		return nil
	}
	return c.restAuthorizers[i]
}

// writeRestAuthorizer writes an authorizer of the rest api in http.authorizers, with what it
// does differently there.
func (c *converter) writeRestAuthorizer(w *writer, a *restAuthorizer) {
	if a.issuer != "" {
		c.leave(w, Fix, a.where, "set audience to the ids of the user pool's app clients: the http api takes a token only when its aud or client_id is one of them, where the rest api took one of any client of the pool", nil)
		c.leave(w, Review, a.where, "the function finds the token's claims in requestContext.authorizer.jwt.claims now, not requestContext.authorizer.claims", nil)
		w.open(fmt.Sprintf("[%s] = new kdst.JwtAuthorizer", quote(a.name)))
		w.line("issuer = %s", a.issuer)
		w.line(`audience { "<app client id>" }`)
		w.close()
		return
	}
	c.leave(w, Fix, a.where, "the authorizer is one of the http api now: it gets the event of payload 2.0, with identitySource and routeArn in place of authorizationToken and methodArn, and returns { isAuthorized, context } in place of an iam policy", nil)
	if a.validation != "" {
		c.leave(w, Review, a.where+".identityValidationExpression", "the http api has no identityValidationExpression: check the token against "+a.validation+" in the authorizer", nil)
	}
	if len(a.identity) == 0 {
		c.leave(w, Review, a.where+".identitySource", "the http api refuses a request without an Authorization header before it calls the authorizer: set identity to what every request has", nil)
	}
	w.open(fmt.Sprintf("[%s] = new kdst.LambdaAuthorizer", quote(a.name)))
	w.line("functionName = %s", quote(a.function))
	if len(a.identity) > 0 {
		w.open("identity")
		for _, s := range a.identity {
			w.line("%s", quote(s))
		}
		w.close()
	}
	if a.ttl > 0 {
		w.line("ttl = %d.s", a.ttl)
	}
	w.close()
}

// restIntegration is how a route of the rest api reaches its function, normalised as the
// serverless framework does, LAMBDA_PROXY by default, and whether the http api can do the
// same: it reaches a function by its proxy integration alone.
func restIntegration(n *yaml.Node) (string, bool) {
	integration := strings.Replace(strings.ToUpper(str(get(n, "integration"))), "-", "_", 1)
	switch integration {
	case "", "LAMBDA_PROXY", "AWS_PROXY", "LAMBDA", "AWS":
		return integration, true
	}
	return integration, false
}

// openRoute is the todo of a route that took only some requests on the rest api and takes any
// on the http api.
func openRoute(why string) string {
	return "the route is open now, as " + why + ": give it an authorizer of http.authorizers, or iam = true"
}

// restEvent converts a route of the rest api to one of the http api, which is what koldstart
// deploys: with its authorizer, and a todo for each of what has no equivalent there.
func (c *converter) restEvent(w *writer, where string, n *yaml.Node) {
	method, path := route(str(n))
	if n.Kind == yaml.MappingNode {
		method, path = strings.ToUpper(str(get(n, "method"))), str(get(n, "path"))
		if method == "*" || method == "" {
			method = methodAny
		}
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	integration, ok := restIntegration(n)
	if !ok {
		c.leaveOut(w, where, "an http api of koldstart routes to its function alone: it has no "+strings.ToLower(integration)+" integration", n)
		return
	}
	c.leave(w, Review, where, "a route of the rest api is one of the http api now: check the function takes the event of payload 2.0, not 1.0, and does without request validation, api keys and usage plans", nil)
	if integration == "LAMBDA" || integration == "AWS" {
		c.leave(w, Review, where+".integration", "the function gets the request and returns the response as the proxy integration does, as the http api has no lambda integration: its request templates and response are gone", nil)
	}
	if request := get(n, "request"); get(request, "parameters") != nil || get(request, "schemas") != nil || get(request, "schema") != nil {
		c.leave(w, Add, where+".request", "the http api checks no parameters or bodies: check them in the function", request)
	}
	if cors := get(n, "cors"); cors != nil && !isFalse(cors) {
		c.leave(w, Add, where+".cors", "an http api has one cors for all its routes: set it in http.cors", cors)
	}
	if isTrue(get(n, "async")) {
		c.leave(w, Review, where+".async", "the http api waits for the function, as it has no async invocation: hand slow work to a queue", nil)
	}
	node := get(n, "authorizer")
	auth := c.restAuthOf(node)
	if auth.open != "" {
		c.leave(w, Fix, where+".authorizer", openRoute(auth.open), node)
	}
	if claims := get(node, "claims"); claims != nil {
		c.leave(w, Drop, where+".authorizer.claims", "the http api hands the function every claim of the token", nil)
	}
	if isTrue(get(n, "private")) {
		if auth.iam || auth.authorizer != nil {
			c.leave(w, Review, where+".private", "an http api has no api keys: the route takes requests without one now, which its authorizer still checks", nil)
		} else {
			c.leave(w, Fix, where+".private", openRoute("it took only requests with an api key, which an http api has none of"), nil)
		}
	}
	httpRoute(w, method, path, func(w *writer) {
		if auth.iam {
			w.line("iam = true")
		}
		if auth.authorizer == nil {
			return
		}
		a := c.restAuthorizerFor(auth.authorizer)
		if a == nil {
			return
		}
		w.line("authorizer = %s", quote(a.name))
		if scopes := get(node, "scopes"); scopes != nil && a.issuer != "" {
			c.member(w, where+".authorizer.scopes", "scopes", listOf(scopes))
		}
	})
}
