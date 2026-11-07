package migrate

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

// The kinds of an authorizer of the rest api, as kdst.pkl's classes are named, and authIAM, the
// authorizer of a route the serverless framework signs with iam.
const (
	authIAM     = "aws_iam"
	authToken   = "TokenAuthorizer"
	authRequest = "RequestAuthorizer"
	authCognito = "CognitoAuthorizer"
)

// restAuthorizer is an authorizer of the rest api, converted for rest.authorizers: a token or a
// request authorizer of a function of the project, or a cognito one of user pools.
type restAuthorizer struct {
	// name is its name in rest.authorizers, and where the authorizer of the first route that has
	// it.
	name, where string
	kind        string
	// function, ttl and validation are of a lambda authorizer.
	function   string
	ttl        int
	validation string
	// identity are the identity sources, as the rest api writes them: a token or cognito
	// authorizer's one header, method.request.header.Authorization.
	identity []string
	// pool is a cognito authorizer's user pool, as a pkl expression.
	pool string
}

// same reports whether a and o are the same authorizer, which routes then share.
func (a *restAuthorizer) same(o *restAuthorizer) bool {
	return a.kind == o.kind && a.function == o.function && slices.Equal(a.identity, o.identity) && a.ttl == o.ttl &&
		a.validation == o.validation && a.pool == o.pool
}

// header is the header a token or cognito authorizer reads, and whether it reads one: the rest
// api reads a token from a header alone.
func (a *restAuthorizer) header() (string, bool) {
	if len(a.identity) != 1 {
		return "", false
	}
	return strings.CutPrefix(a.identity[0], "method.request.header.")
}

// restAuth is what the authorizer of a route of the rest api converts to: iam, an authorizer, or,
// with why, nothing, which leaves the route open.
type restAuth struct {
	iam        bool
	authorizer *restAuthorizer
	open       string
}

// restAuthTTL is how long the rest api caches what an authorizer returns when the serverless
// framework is not told.
const restAuthTTL = 300

// cognitoPool is the arn of a cognito user pool.
var cognitoPool = regexp.MustCompile(`^arn:[a-z-]+:cognito-idp:[^:]+:[^:]*:userpool/[^:/]+$`)

// typeUserPool is the type of a cognito user pool among resources.
const typeUserPool = "AWS::Cognito::UserPool"

// restAuthOf converts the authorizer of a route of the rest api: aws_iam, a function of the
// service by its name or its arn's Fn::GetAtt, or a cognito user pool. It writes nothing, so it
// gives the same for the same node however often it is called.
func (c *converter) restAuthOf(n *yaml.Node) restAuth {
	if n == nil {
		return restAuth{}
	}
	if n.Kind == yaml.ScalarNode {
		switch {
		case strings.EqualFold(n.Value, authIAM):
			return restAuth{iam: true}
		case strings.Contains(n.Value, ":"):
			return c.restArnAuth(n, n)
		}
		return c.restFunctionAuth(n.Value, n)
	}
	typ := strings.ToUpper(str(get(n, "type")))
	switch {
	case get(n, "authorizerId") != nil:
		return restAuth{open: "it names an authorizer of another rest api by its id, which koldstart's cannot use"}
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
// Fn::GetAtt, or a cognito user pool, by its arn or a Fn::GetAtt of one of the stack.
func (c *converter) restArnAuth(arn, n *yaml.Node) restAuth {
	cognito := strings.EqualFold(str(get(n, "type")), "COGNITO_USER_POOLS")
	if name, arg := intrinsicOf(arn); name == intrinsicGetAtt {
		target, _, _ := strings.Cut(arg.Value, ".")
		if arg.Kind == yaml.SequenceNode && len(arg.Content) == 2 {
			target = arg.Content[0].Value
		}
		if fn, ok := c.functionOf(target); ok && !cognito {
			return c.lambdaAuth(fn, n)
		}
		cognito = cognito || str(get(get(get(get(c.doc, "resources"), "Resources"), target), "Type")) == typeUserPool
	}
	v := c.nodeValue(arn)
	if !v.ok {
		return restAuth{open: "its arn does not convert: " + v.todo}
	}
	inner, literal := stringLiteral(v.expr)
	if !cognito && (!literal || !cognitoPool.MatchString(inner)) {
		if literal {
			return restAuth{open: "a lambda authorizer is a function of the project, and " + inner + " is not one"}
		}
		return restAuth{open: "its arn is neither a function of the project nor a cognito user pool"}
	}
	return restAuth{authorizer: &restAuthorizer{kind: authCognito, pool: v.expr, identity: c.identitySources(n, false)}}
}

// restFunctionAuth converts an authorizer that names a function of the service.
func (c *converter) restFunctionAuth(name string, n *yaml.Node) restAuth {
	fn, ok := c.functionNames[name]
	if !ok {
		return restAuth{open: "its name, " + name + ", names no function in functions"}
	}
	return c.lambdaAuth(fn, n)
}

// identitySources are the identity sources of an authorizer, the serverless framework's
// method.request.header.Authorization without any, but for a request authorizer that caches
// nothing, which needs none.
func (c *converter) identitySources(n *yaml.Node, uncachedRequest bool) []string {
	sources := str(get(n, "identitySource"))
	if list := get(n, "identitySource"); list != nil && list.Kind == yaml.SequenceNode {
		items := make([]string, 0, len(list.Content))
		for _, item := range list.Content {
			items = append(items, item.Value)
		}
		sources = strings.Join(items, ",")
	}
	if sources == "" && !uncachedRequest {
		sources = "method.request.header.Authorization"
	}
	var out []string
	for s := range strings.SplitSeq(sources, ",") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// lambdaAuth converts a lambda authorizer of the function fn, a token one or a request one, with
// its identity sources, ttl and validation expression, and the defaults of the serverless
// framework for them.
func (c *converter) lambdaAuth(fn string, n *yaml.Node) restAuth {
	a := &restAuthorizer{kind: authToken, function: fn, ttl: restAuthTTL, validation: str(get(n, "identityValidationExpression"))}
	if ttl, ok := integer(get(n, "resultTtlInSeconds")); ok {
		a.ttl = ttl
	}
	if strings.EqualFold(str(get(n, "type")), "request") {
		a.kind, a.validation = authRequest, ""
	}
	a.identity = c.identitySources(n, a.kind == authRequest && a.ttl == 0)
	return restAuth{authorizer: a}
}

// restAuthorizerName is the safe form of a name in rest.authorizers, which koldstart puts in the
// ids of statements.
var restAuthorizerName = regexp.MustCompile(`[^A-Za-z0-9_-]+`)

// nameRestAuthorizers finds the authorizers the routes of the rest api take, converted, and gives
// each a name in rest.authorizers, once for the routes that share it: its function's, or its
// name in serverless.yml, with a number when the name is taken.
func (c *converter) nameRestAuthorizers() {
	var taken []string
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

// restAuthorizerFor is the authorizer of rest.authorizers a route of the rest api takes.
func (c *converter) restAuthorizerFor(a *restAuthorizer) *restAuthorizer {
	if a == nil {
		return nil
	}
	i := slices.IndexFunc(c.restAuthorizers, a.same)
	if i < 0 {
		return nil
	}
	return c.restAuthorizers[i]
}

// rest writes the stage's rest api: the authorizers of its routes.
func (c *converter) rest(w *writer) {
	if len(c.restAuthorizers) == 0 {
		return
	}
	w.blank()
	w.open("rest")
	w.open("authorizers")
	for _, a := range c.restAuthorizers {
		c.writeRestAuthorizer(w, a)
	}
	w.close()
	w.close()
}

// writeRestAuthorizer writes an authorizer in rest.authorizers.
func (c *converter) writeRestAuthorizer(w *writer, a *restAuthorizer) {
	header, onHeader := a.header()
	if a.kind != authRequest && !onHeader {
		c.leave(w, Fix, a.where+".identitySource", "the rest api takes the token of a token or cognito authorizer from a header: set header to the one callers send it in", nil)
		header = "<header>"
	}
	w.open(fmt.Sprintf("[%s] = new kdst.%s", quote(a.name), a.kind))
	if a.kind == authCognito {
		w.line("userPools { %s }", a.pool)
	} else {
		w.line("functionName = %s", quote(a.function))
	}
	if a.kind != authRequest && header != "Authorization" {
		w.line("header = %s", quote(header))
	}
	if a.validation != "" {
		w.line("validation = %s", quote(a.validation))
	}
	if a.kind == authRequest && len(a.identity) > 0 {
		w.open("identity")
		for _, s := range a.identity {
			w.line("%s", quote(s))
		}
		w.close()
	}
	if a.kind != authCognito && a.ttl > 0 {
		w.line("ttl = %d.s", a.ttl)
	}
	w.close()
}

// restIntegration is how a route of the rest api reaches its function, normalised as the
// serverless framework does, LAMBDA_PROXY by default, and whether koldstart's rest api can do the
// same: it reaches a function by its proxy integration alone.
func restIntegration(n *yaml.Node) (string, bool) {
	integration := strings.Replace(strings.ToUpper(str(get(n, "integration"))), "-", "_", 1)
	switch integration {
	case "", "LAMBDA_PROXY", "AWS_PROXY", "LAMBDA", "AWS":
		return integration, true
	}
	return integration, false
}

// openRoute is the todo of a route that took only some requests and takes any now.
func openRoute(why string) string {
	return "the route is open now, as " + why + ": give it an authorizer of rest.authorizers, or iam = true"
}

// iamCallers is the todo of a route that takes requests signed by a caller allowed to invoke it,
// on the api of output, which koldstart makes anew.
func iamCallers(output string) string {
	return "callers sign their requests and need execute-api:Invoke on the api koldstart makes, a new one: update their policies to its id, which " + output + " has"
}

// restParameter is a request parameter of a route of the rest api, and whether a request must
// have it: true, false, or { required: true }.
func restParameter(n *yaml.Node) bool {
	if n.Kind == yaml.MappingNode {
		return isTrue(get(n, "required"))
	}
	return isTrue(n)
}

// restParameters writes the query strings and headers a request must have, and leaves a todo
// for the rest.
func (c *converter) restParameters(w *writer, where string, params *yaml.Node) {
	if params == nil {
		return
	}
	lists := []struct{ from, to string }{{"querystrings", "requiredQuery"}, {"headers", "requiredHeaders"}}
	var required, optional []string
	for _, l := range lists {
		var names []string
		for _, name := range keys(get(params, l.from)) {
			if restParameter(get(get(params, l.from), name)) {
				names = append(names, name)
			} else {
				optional = append(optional, name)
			}
		}
		if len(names) == 0 {
			continue
		}
		required = append(required, names...)
		quoted := make([]string, 0, len(names))
		for _, n := range names {
			quoted = append(quoted, quote(n))
		}
		w.line("%s { %s }", l.to, strings.Join(quoted, "; "))
	}
	if len(required) > 0 {
		c.todo(Review, where+".request.parameters", "api gateway refuses a request without "+strings.Join(required, ", ")+" before it calls the function: check every caller sends them")
	}
	if len(optional) > 0 {
		c.todo(Drop, where+".request.parameters", "an optional parameter changes nothing for a proxy integration: "+strings.Join(optional, ", "))
	}
	if get(params, "paths") != nil {
		c.todo(Drop, where+".request.parameters.paths", "a path parameter is in the route's path already")
	}
}

// restTimeout writes how long the rest api waits for the function: the route's timeoutInMillis,
// or provider.apiGateway's.
func (c *converter) restTimeout(w *writer, n *yaml.Node) {
	millis, ok := integer(get(n, "timeoutInMillis"))
	if !ok {
		millis, ok = integer(get(get(get(c.doc, "provider"), "apiGateway"), "timeoutInMillis"))
	}
	switch {
	case !ok:
	case millis%1000 == 0:
		w.line("timeout = %d.s", millis/1000)
	default:
		w.line("timeout = %d.ms", millis)
	}
}

// restUnconverted are the keys of a route of the rest api that are not converted, and what to do
// instead.
var restUnconverted = []struct {
	key string
	hint
}{
	{"caching", hint{Add, "koldstart's rest api has no cache yet: cache in the function, or in front of the api"}},
	{"connectionType", hint{Add, "koldstart's rest api reaches its functions alone, through no vpc link"}},
	{"operationId", hint{Drop, "koldstart writes no openapi of the rest api"}},
}

// restEvent converts a route of the rest api to one of koldstart's rest api, kdst.rest(), with its
// authorizer, api key, parameters and timeout, and a todo for each of what does not convert.
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
	if len(path) > 1 {
		path = strings.TrimSuffix(path, "/")
	}
	integration, ok := restIntegration(n)
	c.restRoutes = c.restRoutes || ok
	if !ok {
		c.leaveOut(w, where, "koldstart's rest api reaches its function by the lambda proxy integration alone: it has no "+strings.ToLower(integration)+" integration", n)
		return
	}
	if integration == "LAMBDA" || integration == "AWS" {
		c.leave(w, Review, where+".integration", "the route reaches its function by the lambda proxy integration, the function gets the request and returns the response as it does: the request templates and the response are gone", nil)
	}
	request := get(n, "request")
	if schemas := firstOf(get(request, "schemas"), get(request, "schema")); schemas != nil {
		c.leaveOut(w, where+".request.schemas", "koldstart's rest api checks no bodies: check them in the function", schemas)
	}
	if cors := get(n, "cors"); cors != nil && !isFalse(cors) {
		c.leaveOut(w, where+".cors", fmt.Sprintf("koldstart's rest api answers no preflight requests: add kdst.rest(\"OPTIONS\", %s) to a function that answers them, and set the cors headers in the function's responses", quote(path)), cors)
	}
	if isTrue(get(n, "async")) {
		c.leave(w, Review, where+".async", "the route waits for the function, as koldstart's rest api has no async invocation: hand slow work to a queue", nil)
	}
	for _, k := range restUnconverted {
		if v := get(n, k.key); v != nil {
			if k.kind == Drop {
				c.todo(k.kind, where+"."+k.key, k.what)
				continue
			}
			c.leave(w, k.kind, where+"."+k.key, k.what, v)
		}
	}
	node := get(n, "authorizer")
	auth := c.restAuthOf(node)
	if auth.open != "" {
		c.leave(w, Fix, where+".authorizer", openRoute(auth.open), node)
	}
	if auth.iam {
		c.leave(w, Review, where+".authorizer", iamCallers("restApiUrl"), nil)
	}
	if claims := get(node, "claims"); claims != nil {
		c.todo(Drop, where+".authorizer.claims", "the proxy integration hands the function every claim of the token")
	}
	private := isTrue(get(n, "private"))
	if private && !c.privateNoted {
		c.privateNoted = true
		c.leave(w, Review, where+".private", "koldstart makes an api key of its own for the private routes: hand callers its value, aws apigateway get-api-key --include-value --api-key with the id the output restApiKeyId has", nil)
	}
	w.amend("", fmt.Sprintf("kdst.rest(%s, %s)", quote(method), quote(path)), func(w *writer) {
		if auth.iam {
			w.line("iam = true")
		}
		if a := c.restAuthorizerFor(auth.authorizer); a != nil {
			w.line("authorizer = %s", quote(a.name))
			if scopes := get(node, "scopes"); scopes != nil && a.kind == authCognito {
				c.member(w, where+".authorizer.scopes", "scopes", listOf(scopes))
			} else if scopes != nil {
				c.todo(Drop, where+".authorizer.scopes", "scopes are of a cognito authorizer, and the rest api checks none of a lambda one")
			}
		}
		if private {
			w.line("private = true")
		}
		c.restParameters(w, where, get(request, "parameters"))
		c.restTimeout(w, n)
	})
}

// firstOf is the first of nodes that is there.
func firstOf(nodes ...*yaml.Node) *yaml.Node {
	for _, n := range nodes {
		if n != nil {
			return n
		}
	}
	return nil
}

// apiGatewayKey is a key of provider.apiGateway, and what to do about it.
type apiGatewayKey struct {
	key string
	hint
}

// apiGatewayKeys are the keys of provider.apiGateway: all but timeoutInMillis, which each route
// of the rest api takes, are left.
var apiGatewayKeys = []apiGatewayKey{
	{"timeoutInMillis", hint{Drop, "each route of the rest api has it as its timeout"}},
	{"apiKeys", hint{Review, "koldstart makes one api key of its own for the private routes, not these: hand callers its value, of the key the output restApiKeyId names"}},
	{"usagePlan", hint{Add, "koldstart's usage plan has no quota or limit of its own: set rest.throttling for a limit of the stage"}},
	{"apiKeySourceType", hint{Add, "koldstart's rest api takes the api key from the x-api-key header alone"}},
	{"resourcePolicy", hint{Add, "koldstart sets no resource policy on its rest api yet"}},
	{"binaryMediaTypes", hint{Add, "koldstart's rest api has no binary media types yet: it returns a base64 body as text"}},
	{"minimumCompressionSize", hint{Add, "koldstart's rest api compresses no responses yet"}},
	{"metrics", hint{Add, "koldstart's rest api has no detailed metrics yet"}},
	{"disableDefaultEndpoint", hint{Add, "koldstart's rest api keeps its execute-api endpoint"}},
	{"restApiId", hint{Add, "koldstart makes a rest api of its own for the stage, and shares none with another stack"}},
	{"restApiRootResourceId", hint{Drop, "koldstart makes a rest api of its own for the stage"}},
	{"restApiResources", hint{Drop, "koldstart makes a rest api of its own for the stage"}},
	{"shouldStartNameWithService", hint{Drop, "koldstart names the rest api <app>-<stage>"}},
	{"description", hint{Drop, "koldstart names the rest api <app>-<stage>"}},
}

// apiGateway lists what provider.apiGateway sets that is not converted, and says the rest api is
// regional where serverless.yml's was edge-optimised.
func (c *converter) apiGateway(provider *yaml.Node) {
	settings := get(provider, "apiGateway")
	for _, key := range keys(settings) {
		i := slices.IndexFunc(apiGatewayKeys, func(k apiGatewayKey) bool { return k.key == key })
		if i < 0 {
			c.todo(Add, "provider.apiGateway."+key, "koldstart's rest api has no "+key+" yet")
			continue
		}
		c.todo(apiGatewayKeys[i].kind, "provider.apiGateway."+key, apiGatewayKeys[i].what)
	}
	if c.restRoutes && !strings.EqualFold(str(get(provider, "endpointType")), "regional") {
		c.todo(Review, "provider.endpointType", "koldstart's rest api is regional, where serverless.yml's was edge-optimised: callers far from the region reach it through no cloudfront")
	}
}
