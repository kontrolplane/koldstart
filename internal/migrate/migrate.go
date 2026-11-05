// Package migrate converts a serverless.yml into a koldstart.pkl. What it cannot convert is left
// in the file as a TODO comment, above where it belongs, and listed.
package migrate

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/kontrolplane/koldstart/internal/config"
	schema "github.com/kontrolplane/koldstart/pkl"
)

// Todo is something migrate left for you, and where.
type Todo struct {
	Where string
	What  string
}

func (t Todo) String() string {
	return t.Where + ": " + t.What
}

// Result is a converted serverless.yml.
type Result struct {
	// Pkl is the koldstart.pkl.
	Pkl string
	// Stage is the stage serverless.yml deployed to by default, when it says.
	Stage string
	Todos []Todo
}

// converter converts one serverless.yml.
type converter struct {
	doc     *yaml.Node
	version string
	region  string
	// functionNames are the names functions get in koldstart.pkl, by their name in serverless.yml,
	// and functionsByLogicalID the same by their logical id, such as ApiLambdaFunction.
	functionNames        map[string]string
	functionsByLogicalID map[string]string
	// resourceNames are the names resources get, by their logical id.
	resourceNames map[string]string
	// added are resources serverless.yml makes without declaring them, such as the topic of an
	// sns event given by name, written to resources.
	added     []addedResource
	todos     []Todo
	selfDepth int
}

// addedResource is a resource of an event that serverless.yml names but does not declare.
type addedResource struct {
	name, typ, property, value string
}

// Convert converts a serverless.yml into a koldstart.pkl that amends the schema of version.
func Convert(src []byte, version string) (*Result, error) {
	var file yaml.Node
	if err := yaml.Unmarshal(src, &file); err != nil {
		return nil, fmt.Errorf("reading serverless.yml: %w", err)
	}
	if len(file.Content) == 0 || file.Content[0].Kind != yaml.MappingNode {
		return nil, errors.New("serverless.yml is not a mapping")
	}
	c := &converter{doc: file.Content[0], version: version, resourceNames: map[string]string{}}
	return c.convert()
}

// todo records something left for you, once, however often provider's settings repeat it.
func (c *converter) todo(where, what string) {
	t := Todo{Where: where, What: what}
	if !slices.Contains(c.todos, t) {
		c.todos = append(c.todos, t)
	}
}

var appName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,23}$`)

func (c *converter) convert() (*Result, error) {
	provider := get(c.doc, "provider")
	if name := str(get(provider, "name")); name != "" && name != "aws" {
		return nil, fmt.Errorf("provider.name is %s: koldstart deploys to aws", name)
	}
	c.region = str(get(provider, "region"))
	c.nameFunctions()
	c.nameResources()

	var w writer
	w.comment("Converted from serverless.yml by kdst migrate. Look for TODO: what it could not convert.")
	w.line("amends %s", quote(schema.PackageURI(c.version, schema.Project)))
	w.blank()
	w.line("import %s", quote(schema.PackageURI(c.version, schema.Kdst)))
	w.blank()

	app := c.service()
	w.line("app = %s", quote(app))
	if c.region != "" {
		w.line("region = %s", quote(c.region))
	}
	stage := str(get(provider, "stage"))
	if stage != "" && !strings.Contains(stage, "${") {
		w.blank()
		w.comment(fmt.Sprintf("serverless.yml deployed to %s by default; koldstart deploys to dev: pass --stage %s.", stage, stage))
		c.todo("provider.stage", fmt.Sprintf("koldstart deploys to dev by default: pass --stage %s, or set KOLDSTART_STAGE", stage))
	}
	c.tags(&w, provider)
	c.http(&w, provider)

	functions := get(c.doc, "functions")
	if len(keys(functions)) > 0 {
		w.blank()
		w.open("functions")
		for _, name := range keys(functions) {
			c.function(&w, name, get(functions, name), provider)
		}
		w.close()
	}
	c.resources(&w)
	c.outputs(&w)
	c.leftOver()

	if !appName.MatchString(app) {
		c.todo("service", fmt.Sprintf("%q is not a name koldstart takes: lowercase letters, digits and dashes, at most 24", app))
	}
	return &Result{Pkl: w.String(), Stage: stage, Todos: c.todos}, nil
}

// service is the app's name: the service, lowercase.
func (c *converter) service() string {
	service := get(c.doc, "service")
	if name := get(service, "name"); name != nil {
		service = name
	}
	return strings.ToLower(str(service))
}

var functionName = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9-]{0,39}$`)

// nameFunctions gives each function its name in koldstart.pkl, with dashes for what koldstart
// does not take in a name, and maps the logical ids of the serverless framework to them.
func (c *converter) nameFunctions() {
	c.functionNames = map[string]string{}
	c.functionsByLogicalID = map[string]string{}
	for _, name := range keys(get(c.doc, "functions")) {
		pklName := strings.NewReplacer("_", "-", ".", "-").Replace(name)
		if pklName != name {
			c.todo("functions."+name, "named "+pklName+" in koldstart.pkl, which also renames it and its logs in lambda")
		}
		if !functionName.MatchString(pklName) {
			c.todo("functions."+name, "the name is not one koldstart takes: letters, digits and dashes, at most 40")
		}
		c.functionNames[name] = pklName
		c.functionsByLogicalID[logicalID(name)] = pklName
	}
}

// nameResources gives each resource its name in koldstart.pkl, its logical id.
func (c *converter) nameResources() {
	for _, id := range keys(get(get(c.doc, "resources"), "Resources")) {
		c.resourceNames[id] = id
	}
}

func (c *converter) tags(w *writer, provider *yaml.Node) {
	tags := get(provider, "tags")
	if len(keys(tags)) == 0 {
		return
	}
	w.blank()
	w.open("tags")
	for _, k := range keys(tags) {
		c.assign(w, "provider.tags."+k, "["+quote(k)+"]", c.scalar(get(tags, k)), get(tags, k))
	}
	w.close()
}

// http writes the stage's http api: its cors and authorizers.
func (c *converter) http(w *writer, provider *yaml.Node) {
	api := get(provider, "httpApi")
	cors, authorizers := get(api, "cors"), get(api, "authorizers")
	if cors == nil && authorizers == nil {
		return
	}
	w.blank()
	w.open("http")
	c.cors(w, "provider.httpApi.cors", cors)
	if len(keys(authorizers)) > 0 {
		w.open("authorizers")
		for _, name := range keys(authorizers) {
			c.authorizer(w, name, get(authorizers, name))
		}
		w.close()
	}
	w.close()
}

// cors writes cors = new { ... } of an http api or a url: true for any origin.
func (c *converter) cors(w *writer, where string, n *yaml.Node) {
	if n == nil || isFalse(n) {
		return
	}
	if isTrue(n) {
		w.line(`cors = new { origins { "*" } }`)
		return
	}
	w.open("cors = new")
	lists := []struct{ from, to string }{
		{"allowedOrigins", "origins"}, {"allowedMethods", "methods"}, {"allowedHeaders", "headers"}, {"exposedResponseHeaders", "expose"},
	}
	for _, l := range lists {
		if v := get(n, l.from); v != nil {
			c.member(w, where+"."+l.from, l.to, v)
		}
	}
	if isTrue(get(n, "allowCredentials")) {
		w.line("credentials = true")
	}
	if age, ok := integer(get(n, "maxAge")); ok {
		w.line("maxAge = %d.s", age)
	}
	w.close()
}

func (c *converter) authorizer(w *writer, name string, n *yaml.Node) {
	where := "provider.httpApi.authorizers." + name
	if str(get(n, "type")) == "request" {
		fn, ok := c.functionNames[str(get(n, "functionName"))]
		if !ok {
			c.leaveOut(w, where, "a lambda authorizer of a function that is not in functions", n)
			return
		}
		w.open(fmt.Sprintf("[%s] = new kdst.LambdaAuthorizer", quote(name)))
		w.line("functionName = %s", quote(fn))
		if source := get(n, "identitySource"); source != nil {
			c.member(w, where+".identitySource", "identity", listOf(source))
		}
		if ttl, ok := integer(get(n, "resultTtlInSeconds")); ok {
			w.line("ttl = %d.s", ttl)
		}
		w.close()
		return
	}
	w.open(fmt.Sprintf("[%s] = new kdst.JwtAuthorizer", quote(name)))
	c.assign(w, where+".issuerUrl", "issuer", c.scalar(orEmpty(get(n, "issuerUrl"))), n)
	c.member(w, where+".audience", "audience", listOf(orEmpty(get(n, "audience"))))
	w.close()
}

// listOf is n as a list: itself, or a list of the one value.
func listOf(n *yaml.Node) *yaml.Node {
	if n.Kind == yaml.SequenceNode {
		return n
	}
	return &yaml.Node{Kind: yaml.SequenceNode, Content: []*yaml.Node{n}}
}

func orEmpty(n *yaml.Node) *yaml.Node {
	if n == nil {
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: tagString}
	}
	return n
}

// The runtimes koldstart runs, and those it takes another for.
var (
	nodeRuntimes  = []string{"nodejs22.x", "nodejs24.x"}
	olderNode     = []string{"nodejs14.x", "nodejs16.x", "nodejs18.x", "nodejs20.x"}
	customRuntime = []string{"provided", "provided.al2", config.RuntimeGo, "go1.x"}
)

// function writes one function, with what provider sets for every function.
func (c *converter) function(w *writer, name string, fn, provider *yaml.Node) {
	where := "functions." + name
	pklName := c.functionNames[name]
	if get(fn, "image") != nil {
		c.todo(where, "a container image function, which koldstart does not deploy yet: it is left out")
		w.comment(fmt.Sprintf("TODO: %s is a container image function, which koldstart does not deploy yet.", name))
		return
	}
	runtime := str(get(fn, "runtime"))
	if runtime == "" {
		runtime = str(get(provider, "runtime"))
	}
	switch {
	case slices.Contains(nodeRuntimes, runtime):
	case slices.Contains(olderNode, runtime):
		c.todo(where, runtime+" is deprecated on lambda: it runs on nodejs22.x now")
		runtime = "nodejs22.x"
	case slices.Contains(customRuntime, runtime):
		runtime = config.RuntimeGo
	default:
		c.todo(where, fmt.Sprintf("the runtime %q is not one koldstart builds yet: the function is left out", runtime))
		w.comment(fmt.Sprintf("TODO: %s runs on %s, which koldstart does not build yet.", name, runtime))
		return
	}

	w.open(fmt.Sprintf("[%s]", quote(pklName)))
	w.line("runtime = %s", quote(runtime))
	handler := str(get(fn, "handler"))
	if runtime == config.RuntimeGo {
		c.todo(where+".handler", "a go function's handler is the package to build, such as ./cmd/"+pklName)
		w.comment("TODO: the package to build, such as ./cmd/" + pklName + "; serverless.yml had " + quote(handler) + ".")
		handler = "./cmd/" + pklName
	}
	w.line("handler = %s", quote(handler))
	c.settings(w, where, fn, provider)
	c.environment(w, where, fn, provider)
	c.permissions(w, where, fn, provider)
	c.url(w, where, get(fn, "url"))
	c.events(w, where, get(fn, "events"))
	for _, key := range []string{"layers", "destinations", "onError", "provisionedConcurrency", "role", "tags", "name", "fileSystemConfig"} {
		if v := get(fn, key); v != nil {
			c.todo(where+"."+key, key+" has no equivalent in koldstart yet")
			w.comment("TODO: " + key + " has no equivalent in koldstart yet. was: " + flow(v))
		}
	}
	w.close()
}

// settings writes memory, timeout and the rest a function sets, or provider sets for all.
func (c *converter) settings(w *writer, where string, fn, provider *yaml.Node) {
	either := func(key string) *yaml.Node {
		if v := get(fn, key); v != nil {
			return v
		}
		return get(provider, key)
	}
	if memory, ok := integer(either("memorySize")); ok {
		w.line("memory = %d", memory)
	}
	if timeout, ok := integer(either("timeout")); ok {
		w.line("timeout = %d.s", timeout)
	}
	if arch := str(either("architecture")); arch != "" {
		w.line("architecture = %s", quote(arch))
	}
	if storage, ok := integer(either("ephemeralStorageSize")); ok {
		w.line("ephemeralStorage = %d", storage)
	}
	if reserved, ok := integer(get(fn, "reservedConcurrency")); ok {
		w.line("reservedConcurrency = %d", reserved)
	}
	if days, ok := integer(either("logRetentionInDays")); ok {
		w.line("logRetention = %d", days)
	}
	if vpc := either("vpc"); vpc != nil {
		w.open("vpc = new")
		c.member(w, where+".vpc.subnetIds", "subnetIds", orList(get(vpc, "subnetIds")))
		c.member(w, where+".vpc.securityGroupIds", "securityGroupIds", orList(get(vpc, "securityGroupIds")))
		w.close()
	}
}

func orList(n *yaml.Node) *yaml.Node {
	if n == nil {
		return &yaml.Node{Kind: yaml.SequenceNode}
	}
	return n
}

// environment writes the variables of provider and then the function's, which win.
func (c *converter) environment(w *writer, where string, fn, provider *yaml.Node) {
	type variable struct {
		key   string
		value *yaml.Node
		where string
	}
	var vars []variable
	index := map[string]int{}
	add := func(env *yaml.Node, at string) {
		for _, k := range keys(env) {
			v := variable{key: k, value: get(env, k), where: at + ".environment." + k}
			if i, ok := index[k]; ok {
				vars[i] = v
				continue
			}
			index[k] = len(vars)
			vars = append(vars, v)
		}
	}
	add(get(provider, "environment"), "provider")
	add(get(fn, "environment"), where)
	if len(vars) == 0 {
		return
	}
	w.open("environment")
	for _, v := range vars {
		c.assign(w, v.where, "["+quote(v.key)+"]", c.envValue(v.value), v.value)
	}
	w.close()
}

// envValue is the value of a variable: a string, a ref, a secret or an output of another stage.
func (c *converter) envValue(n *yaml.Node) value {
	if v, ok := c.intrinsic(n); ok {
		return v
	}
	if n.Kind != yaml.ScalarNode {
		return notConverted("a variable that is not a string")
	}
	v := c.text(n.Value)
	if v.ok && n.ShortTag() != tagString {
		return literal(quote(n.Value))
	}
	return v
}

// permissions writes the statements of provider.iam, which serverless.yml gives every function,
// and those of the function itself.
func (c *converter) permissions(w *writer, where string, fn, provider *yaml.Node) {
	statements := get(get(get(provider, "iam"), "role"), "statements")
	if statements == nil {
		statements = get(provider, "iamRoleStatements")
	}
	own := get(fn, "iamRoleStatements")
	if statements == nil && own == nil {
		return
	}
	w.open("permissions")
	if statements != nil {
		w.comment("TODO: serverless.yml gave these statements to every function: keep what this one needs, or use link.")
		c.todo(where+".permissions", "the statements of provider.iam are on every function: narrow them")
		for i, s := range statements.Content {
			c.statement(w, fmt.Sprintf("provider.iam.role.statements[%d]", i), s)
		}
	}
	if own != nil {
		for i, s := range own.Content {
			c.statement(w, fmt.Sprintf("%s.iamRoleStatements[%d]", where, i), s)
		}
	}
	w.close()
}

func (c *converter) statement(w *writer, where string, n *yaml.Node) {
	for _, key := range []string{intrinsicCondition, "NotAction", "NotResource", "Principal"} {
		if get(n, key) != nil {
			c.leaveOut(w, where, key+" in a statement has no equivalent: write the statement as a resource policy, or without it", n)
			return
		}
	}
	w.open("new")
	if effect := str(get(n, "Effect")); effect != "" && effect != "Allow" {
		w.line("effect = %s", quote(effect))
	}
	c.member(w, where+".Action", "actions", listOf(orEmpty(get(n, "Action"))))
	c.member(w, where+".Resource", "resources", listOf(orEmpty(get(n, "Resource"))))
	w.close()
}

// url writes the function's url: true, or with authorizer: aws_iam and cors.
func (c *converter) url(w *writer, where string, n *yaml.Node) {
	if n == nil || isFalse(n) {
		return
	}
	auth := "none"
	if str(get(n, "authorizer")) == "aws_iam" {
		auth = "iam"
	}
	cors := get(n, "cors")
	if cors == nil {
		w.line("url = new { auth = %s }", quote(auth))
		return
	}
	w.open("url = new")
	w.line("auth = %s", quote(auth))
	c.cors(w, where+".url.cors", cors)
	w.close()
}

// resources writes resources.Resources, and the resources events need that serverless.yml makes
// without declaring them.
func (c *converter) resources(w *writer) {
	resources := get(get(c.doc, "resources"), "Resources")
	if len(keys(resources)) == 0 && len(c.added) == 0 {
		return
	}
	w.blank()
	w.open("resources")
	for _, id := range keys(resources) {
		c.resource(w, id, get(resources, id))
	}
	for _, r := range c.added {
		w.comment("serverless.yml made this for an event of a function.")
		w.open(fmt.Sprintf("[%s]", quote(r.name)))
		w.line("type = %s", quote(r.typ))
		w.line("properties { %s = %s }", r.property, quote(r.value))
		w.close()
	}
	w.close()
}

func (c *converter) resource(w *writer, id string, n *yaml.Node) {
	where := "resources.Resources." + id
	typ := str(get(n, "Type"))
	if !strings.HasPrefix(typ, "AWS::") {
		c.leaveOut(w, where, fmt.Sprintf("%s is not an aws type cloud control creates", typ), n)
		return
	}
	if !functionName.MatchString(id) {
		c.todo(where, "the logical id is not a name koldstart takes: letters, digits and dashes, at most 40")
	}
	w.open(fmt.Sprintf("[%s]", quote(id)))
	w.line("type = %s", quote(typ))
	switch strings.ToLower(str(get(n, "DeletionPolicy"))) {
	case "retain", "retainexceptonupdate":
		w.line(`deletionPolicy = "retain"`)
	case "delete":
		w.line(`deletionPolicy = "delete"`)
	case "snapshot":
		c.todo(where+".DeletionPolicy", "Snapshot has no equivalent: the resource is retained")
		w.line(`deletionPolicy = "retain"`)
	}
	if props := get(n, "Properties"); props != nil {
		c.property(w, where+".Properties", "properties", props)
	}
	for _, key := range []string{"DependsOn", intrinsicCondition, "Metadata", "CreationPolicy", "UpdatePolicy"} {
		if v := get(n, key); v != nil {
			c.todo(where+"."+key, key+" has no equivalent: koldstart orders resources by their refs")
			w.comment("TODO: " + key + " has no equivalent. was: " + flow(v))
		}
	}
	w.close()
}

// outputs writes resources.Outputs: each one's value.
func (c *converter) outputs(w *writer) {
	outputs := get(get(c.doc, "resources"), "Outputs")
	if len(keys(outputs)) == 0 {
		return
	}
	w.blank()
	w.open("outputs")
	for _, name := range keys(outputs) {
		o := get(outputs, name)
		where := "resources.Outputs." + name
		v := get(o, "Value")
		if v == nil {
			continue
		}
		if get(o, "Export") != nil {
			c.todo(where+".Export", "exports have no equivalent: other apps read outputs with kdst.stageOutput()")
		}
		val, isIntrinsic := c.intrinsic(v)
		if !isIntrinsic {
			val = c.scalar(v)
		}
		c.assign(w, where, "["+quote(name)+"]", val, v)
	}
	w.close()
}

// plugins say what each well known plugin is in koldstart.
var plugins = map[string]string{
	"serverless-esbuild":                "koldstart bundles typescript with esbuild itself: it can go",
	"serverless-plugin-typescript":      "koldstart bundles typescript with esbuild itself: it can go",
	"serverless-webpack":                "koldstart bundles with esbuild itself: it can go",
	"serverless-offline":                "kdst offline serves the http routes on this machine",
	"serverless-iam-roles-per-function": "every function has a role of its own already",
	"serverless-prune-plugin":           "koldstart keeps no old versions: it can go",
	"serverless-domain-manager":         "set http.domain and http.certificate",
	"serverless-dotenv-plugin":          "read variables with read(\"env:NAME\")",
	"serverless-plugin-log-retention":   "set logRetention on the function",
}

// leftOver lists what serverless.yml has that is not converted: custom, plugins, and the rest.
func (c *converter) leftOver() {
	if custom := get(c.doc, "custom"); custom != nil {
		c.todo("custom", "custom is not converted; what refers to it with ${self:custom...} got its value: "+strings.Join(keys(custom), ", "))
	}
	if list := get(c.doc, "plugins"); list != nil {
		items := list.Content
		if modules := get(list, "modules"); modules != nil {
			items = modules.Content
		}
		for _, p := range items {
			if hint, ok := plugins[p.Value]; ok {
				c.todo("plugins."+p.Value, hint)
			} else {
				c.todo("plugins."+p.Value, "plugins do not run in koldstart: do what it did another way")
			}
		}
	}
	for _, key := range []string{"layers", "stepFunctions", "params", "useDotenv"} {
		if get(c.doc, key) != nil {
			c.todo(key, key+" has no equivalent in koldstart")
		}
	}
	provider := get(c.doc, "provider")
	for _, key := range []string{"apiGateway", "deploymentBucket", "stackTags", "kmsKeyArn", "tracing", "logs"} {
		if get(provider, key) != nil {
			c.todo("provider."+key, key+" is not converted")
		}
	}
	if role := get(get(provider, "iam"), "role"); role != nil && role.Kind == yaml.ScalarNode {
		c.todo("provider.iam.role", "a role of your own is not taken over: every function has its own")
	}
}
