// Package migrate converts a serverless.yml into a koldstart.pkl. What it cannot convert is left
// in the file as a TODO comment, above where it belongs, and listed.
package migrate

import (
	"cmp"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/kontrolplane/koldstart/internal/config"
	schema "github.com/kontrolplane/koldstart/pkl"
)

// Kind is what a todo asks of you. The kinds are in the order they matter most.
type Kind int

const (
	// Fix is what a deploy or a build refuses until it is done.
	Fix Kind = iota
	// Add is what was left out of koldstart.pkl, to write by hand.
	Add
	// Review is what was converted but works differently, to check.
	Review
	// Drop is what koldstart does itself or does without: nothing to write.
	Drop
)

// kinds are the kinds in the order todos are listed.
var kinds = []Kind{Fix, Add, Review, Drop}

// String is the heading the todos of a kind are listed under.
func (k Kind) String() string {
	switch k {
	case Fix:
		return "fix before you deploy"
	case Add:
		return "left out, to write by hand"
	case Review:
		return "works differently, to check"
	default:
		return "not needed in koldstart"
	}
}

// Todo is something migrate left for you: what to do, and the key of serverless.yml it is about.
type Todo struct {
	Kind  Kind
	Where string
	What  string
	// Marked says a TODO comment in koldstart.pkl marks where it goes.
	Marked bool
}

func (t Todo) String() string {
	return t.Where + ": " + t.What
}

// WriteTodos lists todos under the heading of their kind, the kinds that matter most first, and
// in the order they came within a kind.
func WriteTodos(w io.Writer, todos []Todo) error {
	first := true
	for _, kind := range kinds {
		var lines []string
		for _, t := range todos {
			if t.Kind == kind {
				lines = append(lines, "  "+t.String()+"\n")
			}
		}
		if len(lines) == 0 {
			continue
		}
		heading := kind.String() + ":\n"
		if !first {
			heading = "\n" + heading
		}
		first = false
		if _, err := io.WriteString(w, heading+strings.Join(lines, "")); err != nil {
			return err
		}
	}
	return nil
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
	doc    *yaml.Node
	region string
	// name is the name the binary was run as, which the file and the todos give its commands by.
	name string
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
	// dir is the directory of serverless.yml, which ${file()} reads relative to.
	dir string
	// usesMappings and usesParams say the locals of the Mappings and the params are needed, and
	// conditions are the conditions converted, by name.
	usesMappings bool
	usesParams   bool
	conditions   map[string]string
	// restAuthorizers are the authorizers the routes of the rest api take, converted, which go
	// in http.authorizers.
	restAuthorizers []*restAuthorizer
}

// addedResource is a resource of an event that serverless.yml names but does not declare, and
// where is the event.
type addedResource struct {
	name, typ, property, value, where string
}

// Option changes how Convert converts.
type Option func(*converter)

// WithName gives the name the binary was run as, kdst or koldstart, for the commands the file and
// the todos name. Without it they say koldstart.
func WithName(name string) Option {
	return func(c *converter) { c.name = name }
}

// Convert converts a serverless.yml into a koldstart.pkl that amends @koldstart/Project.pkl, the
// schema the PklProject beside it names. A ${file()} is read relative to dir, the directory of serverless.yml. The todos come in the order
// of their kinds.
func Convert(src []byte, dir string, opts ...Option) (*Result, error) {
	var file yaml.Node
	if err := yaml.Unmarshal(src, &file); err != nil {
		return nil, fmt.Errorf("reading serverless.yml: %w", err)
	}
	if len(file.Content) == 0 || file.Content[0].Kind != yaml.MappingNode {
		return nil, errors.New("serverless.yml is not a mapping")
	}
	c := &converter{doc: file.Content[0], dir: dir, name: "koldstart", resourceNames: map[string]string{}}
	for _, opt := range opts {
		opt(c)
	}
	return c.convert()
}

// todo records something left for you, once, however often provider's settings repeat it.
func (c *converter) todo(kind Kind, where, what string) {
	c.record(Todo{Kind: kind, Where: where, What: what})
}

// record records t, or marks the one recorded already when t is marked.
func (c *converter) record(t Todo) {
	i := slices.IndexFunc(c.todos, func(o Todo) bool { return o.Kind == t.Kind && o.Where == t.Where && o.What == t.What })
	if i < 0 {
		c.todos = append(c.todos, t)
		return
	}
	c.todos[i].Marked = c.todos[i].Marked || t.Marked
}

// leave records a todo and writes it as a TODO comment where it belongs, with the yaml it is
// about when there is some.
func (c *converter) leave(w *writer, kind Kind, where, what string, n *yaml.Node) {
	c.record(Todo{Kind: kind, Where: where, What: what, Marked: true})
	w.comment("TODO: " + what)
	if n != nil {
		w.comment("was: " + flow(n))
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
	c.nameRestAuthorizers()
	c.convertConditions()

	var w writer
	w.comment(fmt.Sprintf("Converted from serverless.yml by %s migrate.\nA TODO comment marks each thing it could not convert, or converted differently.", c.name))
	stage := str(get(provider, "stage"))
	if stage != "" && stage != "dev" && !strings.Contains(stage, "${") {
		w.comment(fmt.Sprintf("serverless.yml deployed to %s by default; koldstart deploys to dev: pass --stage %s.", stage, stage))
		c.todo(Review, "provider.stage", fmt.Sprintf("deploy with --stage %s, or set KOLDSTART_STAGE=%s: koldstart deploys to dev by default", stage, stage))
	}
	w.blank()
	w.line("amends %s", quote("@koldstart/"+schema.Project))
	w.blank()
	w.line("import %s", quote("@koldstart/"+schema.Kdst))
	w.blank()

	app := c.service()
	w.line("app = %s", quote(app))
	if c.region != "" {
		w.line("region = %s", quote(c.region))
	}
	c.tags(&w, provider)
	c.http(&w, provider)
	if expr := str(get(provider, "websocketsApiRouteSelectionExpression")); expr != "" {
		w.blank()
		w.line("websocket { routeSelection = %s }", quote(expr))
	}

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
	c.locals(&w)
	c.leftOver()

	if !appName.MatchString(app) {
		c.todo(Fix, "service", fmt.Sprintf("rename the app %q: lowercase letters, digits and dashes, at most 24", app))
	}
	slices.SortStableFunc(c.todos, func(a, b Todo) int {
		return cmp.Or(cmp.Compare(a.Kind, b.Kind), cmp.Compare(section(a.Where), section(b.Where)))
	})
	return &Result{Pkl: w.String(), Stage: stage, Todos: c.todos}, nil
}

// sections are the keys of serverless.yml in the order their todos are listed within a kind.
var sections = []string{"service", "provider", "params", "functions", "resources", "layers", "stepFunctions", "useDotenv", "custom", "plugins"}

// section is where the key a todo is about comes among sections, the ones not in it last.
func section(where string) int {
	top, _, _ := strings.Cut(where, ".")
	top, _, _ = strings.Cut(top, "[")
	if i := slices.Index(sections, top); i >= 0 {
		return i
	}
	return len(sections)
}

// service is the app's name: the service, lowercase.
func (c *converter) service() string {
	return strings.ToLower(str(c.serviceNode()))
}

// serviceNode is the service's name as serverless.yml gives it.
func (c *converter) serviceNode() *yaml.Node {
	service := get(c.doc, "service")
	if name := get(service, "name"); name != nil {
		service = name
	}
	return service
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
			c.todo(Review, "functions."+name, renamed(pklName))
		}
		if !functionName.MatchString(pklName) {
			c.todo(Fix, "functions."+name, "rename the function: letters, digits and dashes, at most 40")
		}
		c.functionNames[name] = pklName
		c.functionsByLogicalID[logicalID(name)] = pklName
	}
}

// renamed is the todo of a function whose name koldstart does not take as it is.
func renamed(pklName string) string {
	return "renamed " + pklName + ", as a name takes no _ or .: update what calls the function by name"
}

// nameResources gives each resource its name in koldstart.pkl, its logical id.
func (c *converter) nameResources() {
	for _, id := range keys(get(get(c.doc, "resources"), "Resources")) {
		c.resourceNames[id] = id
	}
}

// tags writes provider.stackTags and provider.tags, which win, as the tags of everything.
func (c *converter) tags(w *writer, provider *yaml.Node) {
	type tag struct {
		key, where string
		value      *yaml.Node
	}
	var tags []tag
	index := map[string]int{}
	for _, from := range []string{"stackTags", "tags"} {
		set := get(provider, from)
		for _, k := range keys(set) {
			t := tag{key: k, where: "provider." + from + "." + k, value: get(set, k)}
			if i, ok := index[k]; ok {
				tags[i] = t
				continue
			}
			index[k] = len(tags)
			tags = append(tags, t)
		}
	}
	if len(tags) == 0 {
		return
	}
	w.blank()
	w.open("tags")
	for _, t := range tags {
		c.assign(w, t.where, "["+quote(t.key)+"]", c.scalar(t.value), t.value)
	}
	w.close()
}

// http writes the stage's http api: its cors, authorizers, those of the rest api's routes
// among them, and access logs.
func (c *converter) http(w *writer, provider *yaml.Node) {
	api := get(provider, "httpApi")
	cors, authorizers, logs := get(api, "cors"), get(api, "authorizers"), get(get(provider, "logs"), "httpApi")
	if isFalse(logs) {
		logs = nil
	}
	if cors == nil && authorizers == nil && logs == nil && len(c.restAuthorizers) == 0 {
		return
	}
	w.blank()
	w.open("http")
	c.cors(w, "provider.httpApi.cors", cors)
	switch format := get(logs, "format"); {
	case logs == nil:
	case format != nil:
		w.open("accessLogs = new")
		c.assign(w, "provider.logs.httpApi.format", "format", c.scalar(format), format)
		w.close()
	default:
		w.line("accessLogs = new {}")
	}
	if len(keys(authorizers)) > 0 || len(c.restAuthorizers) > 0 {
		w.open("authorizers")
		for _, name := range keys(authorizers) {
			c.authorizer(w, name, get(authorizers, name))
		}
		for _, a := range c.restAuthorizers {
			c.writeRestAuthorizer(w, a)
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
			c.leaveOut(w, where, "its functionName names no function in functions", n)
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
	nodeRuntimes   = []string{"nodejs22.x", "nodejs24.x"}
	olderNode      = []string{"nodejs14.x", "nodejs16.x", "nodejs18.x", "nodejs20.x"}
	customRuntime  = []string{"provided", "provided.al2", config.RuntimeGo, "go1.x"}
	pythonRuntimes = []string{config.RuntimePy312, config.RuntimePy313}
	olderPython    = []string{"python3.7", "python3.8", "python3.9", "python3.10", "python3.11"}
)

// unconverted are the keys of a function that are not converted: the kind of todo each is, and
// what to do instead.
var unconverted = []struct {
	key string
	hint
}{
	{"disableLogs", hint{Review, "koldstart always gives a function a log group: set logRetention for how long it keeps them"}},
	{"role", hint{Review, "every function gets a role of its own: give it what it needs with permissions or link"}},
	{"name", hint{Review, "koldstart names the function <app>-<stage>-<function>: update what calls it by name"}},
}

// function writes one function, with what provider sets for every function.
func (c *converter) function(w *writer, name string, fn, provider *yaml.Node) {
	where := "functions." + name
	pklName := c.functionNames[name]
	if pklName != name {
		c.leave(w, Review, where, renamed(pklName), nil)
	}
	if image := get(fn, "image"); image != nil {
		c.imageFunction(w, name, fn, provider, image)
		return
	}
	runtime, runtimeAt := str(get(fn, "runtime")), where+".runtime"
	if runtime == "" {
		runtime, runtimeAt = str(get(provider, "runtime")), "provider.runtime"
	}
	switch {
	case slices.Contains(nodeRuntimes, runtime):
	case slices.Contains(olderNode, runtime):
		c.todo(Review, runtimeAt, upgraded(runtime, config.RuntimeNode22))
		runtime = config.RuntimeNode22
	case slices.Contains(customRuntime, runtime):
		runtime = config.RuntimeGo
	case slices.Contains(pythonRuntimes, runtime):
	case slices.Contains(olderPython, runtime):
		c.todo(Review, runtimeAt, upgraded(runtime, config.RuntimePy312))
		runtime = config.RuntimePy312
	default:
		what := fmt.Sprintf("koldstart does not build %s: build the function from a dockerfile with image, or port it to node, python or go", runtime)
		c.leave(w, Add, where, what, keyed(name, fn))
		return
	}

	w.open(fmt.Sprintf("[%s]", quote(pklName)))
	w.line("runtime = %s", quote(runtime))
	handler := str(get(fn, "handler"))
	if runtime == config.RuntimeGo {
		c.leave(w, Fix, where+".handler", fmt.Sprintf("set handler to the package to build, such as ./cmd/%s: koldstart builds go from source, not %q", pklName, handler), nil)
		handler = "./cmd/" + pklName
	}
	if strings.HasPrefix(runtime, "python") {
		handler = pythonHandler(handler)
	}
	w.line("handler = %s", quote(handler))
	if layers := get(fn, "layers"); layers != nil && runtime == config.RuntimeGo {
		c.leaveOut(w, where+".layers", "build what the layers hold into the binary: a go function takes no layers", layers)
	} else if layers != nil {
		c.member(w, where+".layers", "layers", layers)
	}
	c.settings(w, where, fn, provider)
	if snap := get(fn, "snapStart"); isTrue(snap) && strings.HasPrefix(runtime, "python") {
		w.line("snapStart = true")
	} else if isTrue(snap) {
		c.leave(w, Add, where+".snapStart", "lambda takes snapStart for python alone of the runtimes koldstart builds", snap)
	}
	c.environment(w, where, fn, provider)
	c.permissions(w, where, fn, provider)
	c.url(w, where, get(fn, "url"))
	c.events(w, where, get(fn, "events"))
	for _, u := range unconverted {
		if v := get(fn, u.key); v != nil {
			c.leave(w, u.kind, where+"."+u.key, u.what, v)
		}
	}
	w.close()
}

// keyed is n under its key, name: what a TODO comment shows of a function left out.
func keyed(name string, n *yaml.Node) *yaml.Node {
	return &yaml.Node{Kind: yaml.MappingNode, Content: []*yaml.Node{{Kind: yaml.ScalarNode, Tag: tagString, Value: name}, n}}
}

// upgraded is the todo of a function that runs on a newer runtime than serverless.yml gave it.
func upgraded(from, to string) string {
	return fmt.Sprintf("runs on %s now, as koldstart does not build %s: test it there", to, from)
}

// pythonHandler writes a python handler as koldstart takes it, the module's path and the
// function: src.handler.main is src/handler.main.
func pythonHandler(handler string) string {
	i := strings.LastIndex(handler, ".")
	if i <= 0 {
		return handler
	}
	return strings.ReplaceAll(handler[:i], ".", "/") + handler[i:]
}

// imageFunction writes a container image function: the image serverless.yml builds from
// provider.ecr.images, by its name or as { name, command, entryPoint, workingDirectory }. A
// prebuilt image, given by its uri, is left for you, as koldstart builds images itself.
func (c *converter) imageFunction(w *writer, name string, fn, provider, image *yaml.Node) {
	where := "functions." + name
	imageName := str(image)
	if image.Kind == yaml.MappingNode {
		imageName = str(get(image, "name"))
	}
	built := get(get(get(provider, "ecr"), "images"), imageName)
	path := str(get(built, "path"))
	if built == nil || path == "" {
		c.leave(w, Add, where+".image", "koldstart builds images itself: give the function the dockerfile to build, with image { context; dockerfile }", keyed(name, fn))
		return
	}
	context := strings.TrimSuffix(strings.TrimPrefix(path, "./"), "/")
	if context == "" {
		context = "."
	}
	dockerfile := cmp.Or(str(get(built, "file")), "Dockerfile")
	w.open(fmt.Sprintf("[%s]", quote(c.functionNames[name])))
	w.open("image")
	w.line("context = %s", quote(context))
	w.line("dockerfile = %s", quote(filepath.ToSlash(filepath.Join(context, dockerfile))))
	if args := get(built, "buildArgs"); args != nil {
		c.mapping(w, where+".image.buildArgs", "buildArgs", args)
	}
	if image.Kind == yaml.MappingNode {
		for _, key := range []string{"command", "entryPoint"} {
			if v := get(image, key); v != nil {
				c.member(w, where+".image."+key, key, v)
			}
		}
		if dir := get(image, "workingDirectory"); dir != nil {
			c.member(w, where+".image.workingDirectory", "workingDirectory", dir)
		}
	}
	w.close()
	c.settings(w, where, fn, provider)
	c.environment(w, where, fn, provider)
	c.permissions(w, where, fn, provider)
	c.url(w, where, get(fn, "url"))
	c.events(w, where, get(fn, "events"))
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
	if provisioned, ok := integer(get(fn, "provisionedConcurrency")); ok && provisioned > 0 {
		w.line("provisionedConcurrency = %d", provisioned)
		c.todo(Review, where+".provisionedConcurrency", "it is on the function's live alias, which its events invoke: call the function as <name>:live to use it")
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
	if tracing(get(fn, "tracing"), get(get(provider, "tracing"), "lambda")) {
		w.line("tracing = true")
	}
	if key, at := get(fn, "kmsKeyArn"), where+".kmsKeyArn"; key != nil {
		c.assign(w, at, "kmsKey", c.nodeValue(key), key)
	} else if key := get(provider, "kmsKeyArn"); key != nil {
		c.assign(w, "provider.kmsKeyArn", "kmsKey", c.nodeValue(key), key)
	}
	if tags := get(fn, "tags"); len(keys(tags)) > 0 {
		w.open("tags")
		for _, k := range keys(tags) {
			c.assign(w, where+".tags."+k, "["+quote(k)+"]", c.scalar(get(tags, k)), get(tags, k))
		}
		w.close()
	}
	if dlq := get(fn, "onError"); dlq != nil {
		c.assign(w, where+".onError", "deadLetter", c.nodeValue(dlq), dlq)
	}
	c.async(w, where, fn)
	if fs := get(fn, "fileSystemConfig"); fs != nil {
		w.open("fileSystem = new")
		c.assign(w, where+".fileSystemConfig.arn", "accessPoint", c.nodeValue(orEmpty(get(fs, "arn"))), fs)
		c.assign(w, where+".fileSystemConfig.localMountPath", "mountPath", c.scalar(orEmpty(get(fs, "localMountPath"))), fs)
		w.close()
	}
	c.logFormat(w, where, fn, provider)
}

// tracing reports whether a function traces, by its own tracing, Active or PassThrough, or else
// provider.tracing.lambda, true or one of those.
func tracing(own, provider *yaml.Node) bool {
	mode := own
	if mode == nil {
		mode = provider
	}
	return isTrue(mode) || str(mode) == "Active"
}

// async writes how lambda handles the events a function is handed without waiting: its retries,
// how long an event may wait, and its destinations.
func (c *converter) async(w *writer, where string, fn *yaml.Node) {
	retries, age, destinations := get(fn, "maximumRetryAttempts"), get(fn, "maximumEventAge"), get(fn, "destinations")
	if retries == nil && age == nil && destinations == nil {
		return
	}
	w.open("async = new")
	if n, ok := integer(retries); ok {
		w.line("maximumRetryAttempts = %d", n)
	}
	if n, ok := integer(age); ok {
		w.line("maximumEventAge = %d.s", n)
	}
	for _, key := range []string{"onSuccess", "onFailure"} {
		if d := get(destinations, key); d != nil {
			c.assign(w, where+".destinations."+key, key, c.destination(d), d)
		}
	}
	w.close()
}

// destination is where a function sends results: the arn given, or given as { type, arn }, or a
// function of the service by its name, which is its ref.
func (c *converter) destination(n *yaml.Node) value {
	if n.Kind == yaml.MappingNode {
		if arn := get(n, "arn"); arn != nil {
			return c.nodeValue(arn)
		}
	}
	if fn, ok := c.functionNames[str(n)]; ok {
		return refValue(fmt.Sprintf("kdst.ref(%s, %s)", quote(fn), quote(attrArn)))
	}
	return c.nodeValue(n)
}

// logFormat writes the format and levels of a function's logs, from its logs or
// provider.logs.lambda.
func (c *converter) logFormat(w *writer, where string, fn, provider *yaml.Node) {
	logs, at := get(fn, "logs"), where+".logs"
	if logs == nil {
		logs, at = get(get(provider, "logs"), "lambda"), "provider.logs.lambda"
	}
	if logs == nil {
		return
	}
	if strings.EqualFold(str(get(logs, "logFormat")), "json") {
		w.line(`logFormat = "json"`)
		if level := str(get(logs, "applicationLogLevel")); level != "" {
			w.line("applicationLogLevel = %s", quote(strings.ToUpper(level)))
		}
		if level := str(get(logs, "systemLogLevel")); level != "" {
			w.line("systemLogLevel = %s", quote(strings.ToUpper(level)))
		}
	}
	if group := get(logs, "logGroup"); group != nil {
		c.todo(Review, at+".logGroup", "koldstart writes the logs of each function to a log group of its own, /aws/lambda/<name>")
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
	statements, at := get(get(get(provider, "iam"), "role"), "statements"), "provider.iam.role.statements"
	if statements == nil {
		statements, at = get(provider, "iamRoleStatements"), "provider.iamRoleStatements"
	}
	own := get(fn, "iamRoleStatements")
	if statements == nil && own == nil {
		return
	}
	w.open("permissions")
	if statements != nil {
		c.todo(Review, at, "every function got these statements: keep in each what it needs, or use link")
		w.comment("TODO: serverless.yml gave these statements to every function: keep what this one needs, or use link.")
		for i, s := range statements.Content {
			c.statement(w, fmt.Sprintf("%s[%d]", at, i), s)
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
			c.leaveOut(w, where, "a statement here takes no "+key+": write it as a resource policy, or without it", n)
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
		noun := strings.ToLower(r.typ[strings.LastIndex(r.typ, ":")+1:])
		w.comment(fmt.Sprintf("serverless.yml made this %s for %s.", noun, r.where))
		properties := fmt.Sprintf("properties { %s = %s }", r.property, quote(r.value))
		if r.typ == typeTopic {
			w.amend(fmt.Sprintf("[%s] = ", quote(r.name)), "kdst.topic()", func(w *writer) { w.line("%s", properties) })
			continue
		}
		w.open(fmt.Sprintf("[%s]", quote(r.name)))
		w.line("type = %s", quote(r.typ))
		w.line("%s", properties)
		w.close()
	}
	w.close()
}

func (c *converter) resource(w *writer, id string, n *yaml.Node) {
	where := "resources.Resources." + id
	typ := str(get(n, "Type"))
	if !strings.HasPrefix(typ, "AWS::") {
		c.leaveOut(w, where, fmt.Sprintf("cloud control creates AWS:: types only: do what the %s did another way", typ), n)
		return
	}
	if !functionName.MatchString(id) {
		c.todo(Fix, where, "rename the resource: letters, digits and dashes, at most 40")
	}
	if cond := get(n, intrinsicCondition); cond != nil {
		condition, ok := c.conditionName(cond.Value)
		if !ok {
			c.leaveOut(w, where, notConvertedCondition(cond.Value), n)
			return
		}
		w.open(fmt.Sprintf("when (%s)", condition))
		defer w.close()
	}
	short, props := shorthand(typ, get(n, "Properties"))
	body := func(w *writer) {
		if short.helper == "" {
			w.line("type = %s", quote(typ))
		}
		for _, line := range short.amends {
			w.line("%s", line)
		}
		switch strings.ToLower(str(get(n, "DeletionPolicy"))) {
		case "retain", "retainexceptonupdate":
			w.line(`deletionPolicy = "retain"`)
		case "delete":
			w.line(`deletionPolicy = "delete"`)
		case "snapshot":
			c.leave(w, Review, where+".DeletionPolicy", "retained, as koldstart has no Snapshot: take a snapshot before you remove it", nil)
			w.line(`deletionPolicy = "retain"`)
		}
		if props != nil && (short.helper == "" || len(props.Content) > 0) {
			c.property(w, where+".Properties", "properties", props)
		}
		if v := get(n, "DependsOn"); v != nil {
			c.leave(w, Review, where+".DependsOn", "koldstart orders resources by their refs: refer with kdst.ref() to what it must wait for", v)
		}
		if v := get(n, "Metadata"); v != nil {
			c.leave(w, Drop, where+".Metadata", "koldstart has no Metadata", v)
		}
		for _, key := range []string{"CreationPolicy", "UpdatePolicy"} {
			if v := get(n, key); v != nil {
				c.leave(w, Add, where+"."+key, "koldstart has no "+key, v)
			}
		}
	}
	if short.helper != "" {
		w.amend(fmt.Sprintf("[%s] = ", quote(id)), short.helper, body)
		return
	}
	w.open(fmt.Sprintf("[%s]", quote(id)))
	body(w)
	w.close()
}

// notConvertedCondition is the todo of what refers to a condition that is not converted.
func notConvertedCondition(name string) string {
	return "its condition " + name + " is not converted: see resources.Conditions." + name
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
			c.leave(w, Review, where+".Export", "koldstart has no exports: read the output in other apps with kdst.stageOutput()", nil)
		}
		if cond := get(o, intrinsicCondition); cond != nil {
			condition, ok := c.conditionName(cond.Value)
			if !ok {
				c.leaveOut(w, where, notConvertedCondition(cond.Value), o)
				continue
			}
			w.open(fmt.Sprintf("when (%s)", condition))
			c.assign(w, where, "["+quote(name)+"]", c.nodeValue(v), v)
			w.close()
			continue
		}
		c.assign(w, where, "["+quote(name)+"]", c.nodeValue(v), v)
	}
	w.close()
}

// hint is a todo of a key that is not converted: its kind, and what to do instead.
type hint struct {
	kind Kind
	what string
}

// plugin is what a plugin is in koldstart: what the well known ones do there, or what to do about
// another.
func (c *converter) plugin(name string) hint {
	switch name {
	case "serverless-esbuild", "serverless-plugin-typescript":
		return hint{Drop, "koldstart bundles typescript with esbuild itself"}
	case "serverless-webpack":
		return hint{Drop, "koldstart bundles with esbuild itself"}
	case "serverless-offline":
		return hint{Drop, c.name + " offline serves the http routes on this machine"}
	case "serverless-iam-roles-per-function":
		return hint{Drop, "every function has a role of its own already"}
	case "serverless-prune-plugin":
		return hint{Drop, "koldstart keeps the last 3 versions of a function with an alias, and publishes none for the rest"}
	case "serverless-python-requirements":
		return hint{Drop, "koldstart installs requirements.txt or pyproject.toml with uv itself"}
	case "serverless-domain-manager":
		return hint{Add, "set http.domain and http.certificate"}
	case "serverless-dotenv-plugin":
		return hint{Add, `read each variable with read("env:NAME")`}
	case "serverless-plugin-log-retention":
		return hint{Add, "set logRetention on each function"}
	}
	return hint{Add, "plugins do not run in koldstart: do what it did another way"}
}

// topLevel and providerLevel are the keys of serverless.yml and of provider that are not
// converted, and what to do about each.
var (
	topLevel = []struct {
		key string
		hint
	}{
		{"layers", hint{Add, "publish the layers yourself, and give functions their arns in layers: koldstart does not build layers"}},
		{"stepFunctions", hint{Add, "declare the state machines in resources, as AWS::StepFunctions::StateMachine"}},
		{"useDotenv", hint{Add, `koldstart reads no .env: export the variables before you deploy, and read them with read("env:NAME")`}},
	}
	providerLevel = []struct {
		key string
		hint
	}{
		{"apiGateway", hint{Add, "koldstart deploys an http api, not a rest api: set what it needs on http"}},
		{"deploymentBucket", hint{Drop, "koldstart keeps its zips and state in a bucket of its own"}},
		{"versionFunctions", hint{Drop, "koldstart publishes versions only of functions with provisionedConcurrency or snapStart"}},
	}
)

// leftOver lists what serverless.yml has that is not converted: custom, plugins, and the rest.
func (c *converter) leftOver() {
	if custom := get(c.doc, "custom"); custom != nil {
		c.todo(Drop, "custom", "each ${self:custom...} got its value where it was used: "+strings.Join(keys(custom), ", "))
	}
	if list := get(c.doc, "plugins"); list != nil {
		items := list.Content
		if modules := get(list, "modules"); modules != nil {
			items = modules.Content
		}
		for i, p := range items {
			h := c.plugin(p.Value)
			if strings.HasPrefix(p.Value, ".") || strings.HasPrefix(p.Value, "/") {
				c.todo(h.kind, fmt.Sprintf("plugins[%d]", i), p.Value+" does not run in koldstart: do what it did another way")
				continue
			}
			c.todo(h.kind, "plugins."+p.Value, h.what)
		}
	}
	for _, k := range topLevel {
		if get(c.doc, k.key) != nil {
			c.todo(k.kind, k.key, k.what)
		}
	}
	provider := get(c.doc, "provider")
	for _, k := range providerLevel {
		if get(provider, k.key) != nil {
			c.todo(k.kind, "provider."+k.key, k.what)
		}
	}
	if role := get(get(provider, "iam"), "role"); role != nil && role.Kind == yaml.ScalarNode {
		c.todo(Review, "provider.iam.role", "every function gets a role of its own: give each what it needs with permissions or link")
	}
	if tracing := get(get(provider, "tracing"), "apiGateway"); isTrue(tracing) {
		c.todo(Review, "provider.tracing.apiGateway", "an http api has no x-ray tracing: the functions trace with tracing")
	}
	logs := get(provider, "logs")
	if get(logs, "restApi") != nil {
		c.todo(Add, "provider.logs.restApi", "the routes of the rest api are on the http api: give it access logs with http.accessLogs")
	}
	if get(logs, "websocket") != nil {
		c.todo(Add, "provider.logs.websocket", "koldstart has no access logs of the websocket api yet")
	}
	if get(logs, "frameworkLambda") != nil {
		c.todo(Drop, "provider.logs.frameworkLambda", "koldstart deploys no functions of its own")
	}
}
