package config

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// SchemaVersion is the newest schema this binary reads. Project.pkl renders the version it was
// published with, and a newer one is refused instead of half read. It goes up with Project.pkl's
// schemaVersion, as AGENTS.md says when.
const SchemaVersion = 1

// Evaluator renders a pkl module as json.
type Evaluator interface {
	EvalJSON(ctx context.Context, path string, props map[string]string) ([]byte, error)
}

var (
	stagePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,15}$`)
	routePattern = regexp.MustCompile(`^(\$default|/[A-Za-z0-9._~{}+/-]*)$`)
)

// Load evaluates the configuration at path for stage, infers the runtimes and handlers its
// functions leave out from the files beside it, and checks it.
func Load(ctx context.Context, ev Evaluator, path, stage string) (*Project, error) {
	return LoadIn(ctx, ev, path, filepath.Dir(path), stage)
}

// LoadIn is Load for a project in root whose configuration is evaluated at path elsewhere, such
// as the copy schema.Local writes.
func LoadIn(ctx context.Context, ev Evaluator, path, root, stage string) (*Project, error) {
	if !stagePattern.MatchString(stage) {
		return nil, fmt.Errorf("stage %q is not a stage name: pass --stage with lowercase letters, digits and dashes, starting with a letter, at most 16", stage)
	}
	out, err := ev.EvalJSON(ctx, path, map[string]string{"stage": stage})
	if err != nil {
		return nil, err
	}
	project, err := Decode(out)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	if project.Stage != stage {
		return nil, fmt.Errorf("%s sets stage to %q, but --stage is %q: leave stage to --stage", path, project.Stage, stage)
	}
	if err := project.Infer(root); err != nil {
		return nil, fmt.Errorf("checking %s: %w", path, err)
	}
	if err := project.Validate(); err != nil {
		return nil, fmt.Errorf("checking %s: %w", path, err)
	}
	return project, nil
}

// Decode reads the json pkl renders for a koldstart.pkl.
func Decode(data []byte) (*Project, error) {
	var head struct {
		SchemaVersion int    `json:"schemaVersion"`
		App           string `json:"app"`
		Stage         string `json:"stage"`
	}
	if err := json.Unmarshal(data, &head); err != nil {
		return nil, err
	}
	if head.SchemaVersion == 0 {
		return nil, errors.New("no schemaVersion: the file must amend koldstart's Project.pkl")
	}
	if head.SchemaVersion > SchemaVersion {
		return nil, fmt.Errorf("the file uses schema %d, this koldstart reads up to %d: upgrade koldstart", head.SchemaVersion, SchemaVersion)
	}
	data, err := scopeSecrets(data, head.App, head.Stage)
	if err != nil {
		return nil, err
	}

	var raw struct {
		Project
		SchemaVersion int `json:"schemaVersion"`
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&raw); err != nil {
		return nil, err
	}
	p := raw.Project
	if p.Functions == nil {
		p.Functions = map[string]Function{}
	}
	if p.Resources == nil {
		p.Resources = map[string]Resource{}
	}
	return &p, nil
}

// Validate checks what the schema cannot: that names refer to something in the project.
func (p *Project) Validate() error {
	v := validator{p: p, routes: map[string]string{}}
	for name := range p.Functions {
		if _, ok := p.Resources[name]; ok {
			v.errorf("%q is both a function and a resource: names are shared", name)
		}
	}
	for _, name := range slices.Sorted(maps.Keys(p.Functions)) {
		v.function(name, p.Functions[name])
	}
	if p.HTTP != nil {
		for _, name := range slices.Sorted(maps.Keys(p.HTTP.Authorizers)) {
			a := p.HTTP.Authorizers[name]
			if _, ok := p.Functions[a.Function]; a.Type == "lambda" && !ok {
				v.errorf("http.authorizers[%s]: function %q is not a function of the project", name, a.Function)
			}
		}
		v.value("http.certificate", p.HTTP.Certificate)
	}
	for _, name := range slices.Sorted(maps.Keys(p.Resources)) {
		walkRefs(p.Resources[name].Properties, func(r Ref) {
			if !v.exists(r.Resource) {
				v.errorf("resources[%s]: kdst.ref(%q, %q) refers to no function or resource", name, r.Resource, r.Attribute)
			}
			if r.Resource == name {
				v.errorf("resources[%s]: refers to itself", name)
			}
		})
	}
	generated := p.GeneratedOutputs()
	for _, name := range slices.Sorted(maps.Keys(p.Outputs)) {
		v.output(name, p.Outputs[name], generated)
	}
	return errors.Join(v.errs...)
}

// output checks an output is one a deploy can resolve from the state, before anything is
// applied, and is not one the deploy adds itself, which would replace it.
func (v *validator) output(name string, val Value, generated map[string]string) {
	where := "outputs[" + name + "]"
	if what, ok := generated[name]; ok {
		v.errorf("%s: koldstart generates %s, %s, which would replace this output: pick another name", where, name, what)
	}
	if what := notOutput(val); what != "" {
		v.errorf("%s: %s cannot be an output: give a string, a kdst.ref(), a kdst.join() of them or a value of the account, kdst.accountId, kdst.partition or kdst.urlSuffix", where, what)
		return
	}
	v.value(where, val)
}

// validator collects what Validate finds.
type validator struct {
	p *Project
	// routes maps each route, such as "GET /path", to the function it belongs to.
	routes map[string]string
	errs   []error
}

func (v *validator) errorf(format string, args ...any) {
	v.errs = append(v.errs, fmt.Errorf(format, args...))
}

func (v *validator) exists(name string) bool {
	_, fn := v.p.Functions[name]
	_, res := v.p.Resources[name]
	return fn || res
}

func (v *validator) value(where string, val Value) {
	for i, part := range val.Join {
		v.value(fmt.Sprintf("%s.join[%d]", where, i), part)
	}
	if val.Ref != nil && !v.exists(val.Ref.Resource) {
		v.errorf("%s: kdst.ref(%q, %q) refers to no function or resource", where, val.Ref.Resource, val.Ref.Attribute)
	}
	if val.Output != nil && val.Output.App == v.p.App && val.Output.Stage == v.p.Stage && !val.Output.Elsewhere(v.p.Region) {
		v.errorf("%s: kdst.stageOutput() of this app and stage: use kdst.ref() instead", where)
	}
}

func (v *validator) function(name string, fn Function) {
	where := "functions[" + name + "]"
	for _, key := range slices.Sorted(maps.Keys(fn.Environment)) {
		v.value(where+".environment["+key+"]", fn.Environment[key])
	}
	for i, s := range fn.Permissions {
		for _, r := range s.Resources {
			v.value(fmt.Sprintf("%s.permissions[%d]", where, i), r)
		}
	}
	for _, target := range slices.Sorted(maps.Keys(fn.Link)) {
		if _, ok := v.p.Resources[target]; !ok {
			v.errorf("%s.link: %q is not a resource", where, target)
		}
	}
	if fn.VPC != nil {
		for _, val := range slices.Concat(fn.VPC.SubnetIDs, fn.VPC.SecurityGroupIDs) {
			v.value(where+".vpc", val)
		}
	}
	for i, e := range fn.Events {
		v.event(name, fmt.Sprintf("%s.events[%d]", where, i), e)
	}
	for i, l := range fn.Layers {
		v.value(fmt.Sprintf("%s.layers[%d]", where, i), l)
	}
	switch {
	case fn.IsImage() && (fn.Runtime != "" || fn.Handler != ""):
		v.errorf("%s: a function with an image has no runtime or handler: the image says what it runs", where)
	case fn.IsImage() && len(fn.Layers) > 0:
		v.errorf("%s: a function with an image has no layers: put what they hold in the image", where)
	case !fn.IsImage() && (fn.Runtime == "" || fn.Handler == ""):
		v.errorf("%s: a function needs a runtime and a handler, or an image", where)
	case fn.IsGo() && len(fn.Layers) > 0:
		v.errorf("%s: a go function has no layers: build what they hold into the binary", where)
	}
	if len(fn.Bundle.Include) > 0 && !fn.IsPython() {
		v.errorf("%s.bundle.include: only python functions take it", where)
	}
	v.value(where+".kmsKey", fn.KMSKey)
	v.destination(where+".deadLetter", fn.DeadLetter, ServiceSQS, ServiceSNS)
	if a := fn.Async; a != nil {
		v.destination(where+".async.onSuccess", a.OnSuccess, ServiceSQS, ServiceSNS, ServiceEvents, ServiceLambda)
		v.destination(where+".async.onFailure", a.OnFailure, ServiceSQS, ServiceSNS, ServiceEvents, ServiceLambda)
	}
	if fs := fn.FileSystem; fs != nil {
		v.value(where+".fileSystem.accessPoint", fs.AccessPoint)
		if fn.VPC == nil {
			v.errorf("%s.fileSystem: a function that mounts efs needs a vpc: set vpc to subnets that reach the file system", where)
		}
	}
	v.versions(where, fn)
	if fn.LogFormat != LogFormatJSON && (fn.ApplicationLogLevel != "" || fn.SystemLogLevel != "") {
		v.errorf("%s: the log levels apply to json logs alone: set logFormat = \"json\", or leave out the levels", where)
	}
}

// versions checks what lambda refuses of provisioned concurrency and snapStart: more instances
// kept started than are reserved, and snapStart with what it does not take.
func (v *validator) versions(where string, fn Function) {
	if pc, reserved := fn.ProvisionedConcurrency, fn.ReservedConcurrency; pc != nil && reserved != nil && *pc > *reserved {
		v.errorf("%s: provisionedConcurrency %d is more than the reservedConcurrency of %d: reserve at least as many", where, *pc, *reserved)
	}
	if !fn.SnapStart {
		return
	}
	switch {
	case !fn.IsPython() || fn.IsImage():
		v.errorf("%s.snapStart: lambda takes snapStart for python functions alone", where)
	case fn.ProvisionedConcurrency != nil:
		v.errorf("%s.snapStart: lambda takes snapStart or provisionedConcurrency, not both", where)
	case fn.FileSystem != nil:
		v.errorf("%s.snapStart: lambda takes no fileSystem with snapStart", where)
	case fn.EphemeralStorage > 512:
		v.errorf("%s.snapStart: lambda takes no more than 512 megabytes of ephemeralStorage with snapStart", where)
	}
}

// The services a destination can be in, as its arn names them.
const (
	ServiceSQS    = "sqs"
	ServiceSNS    = "sns"
	ServiceS3     = "s3"
	ServiceEvents = "events"
	ServiceLambda = "lambda"
)

// Service is the service of a value that names something by its arn: from the arn, or from the
// type of the function or resource of the project a ref names, AWS::SQS::Queue being in sqs. It is empty when it cannot be told
// before deploying, as for an output of another stage.
func (p *Project) Service(v Value) string {
	switch {
	case v.Ref != nil:
		if _, ok := p.Functions[v.Ref.Resource]; ok {
			return ServiceLambda
		}
		parts := strings.Split(p.Resources[v.Ref.Resource].Type, "::")
		if len(parts) == 3 {
			return strings.ToLower(parts[1])
		}
	case strings.HasPrefix(v.Literal, "arn:"):
		parts := strings.SplitN(v.Literal, ":", 4)
		if len(parts) == 4 {
			return parts[2]
		}
	}
	return ""
}

// destination checks a value that is set names something of one of the services, when that can
// be told.
func (v *validator) destination(where string, val Value, services ...string) {
	if !val.IsSet() {
		return
	}
	v.value(where, val)
	if val.Literal != "" && !strings.HasPrefix(val.Literal, "arn:") {
		v.errorf("%s: %q is not an arn: give the arn, or kdst.ref() of a resource of the project", where, val.Literal)
		return
	}
	if service := v.p.Service(val); service != "" && !slices.Contains(services, service) {
		v.errorf("%s: it is in %s, and takes %s alone", where, service, strings.Join(services, ", "))
	}
}

func (v *validator) event(fn, where string, e Event) {
	switch e.Type {
	case EventHTTPRoute:
		if !routePattern.MatchString(e.Path) {
			v.errorf("%s: path %q must start with / or be $default", where, e.Path)
		}
		route := strings.ToUpper(e.Method) + " " + e.Path
		if other, ok := v.routes[route]; ok {
			v.errorf("%s: %s is also a route of %s", where, route, other)
		}
		v.routes[route] = fn
		if e.Authorizer != "" && (v.p.HTTP == nil || v.p.HTTP.Authorizers[e.Authorizer].Type == "") {
			v.errorf("%s: no authorizer %q in http.authorizers", where, e.Authorizer)
		}
		if e.Authorizer != "" && e.IAM {
			v.errorf("%s: a route takes iam or an authorizer, not both", where)
		}
	case EventQueue:
		v.value(where+".queue", e.Queue)
		v.visibility(fn, where, e.Queue)
	case EventTopic:
		v.value(where+".topic", e.Topic)
	case EventStream:
		v.value(where+".stream", e.Stream)
		v.destination(where+".onFailure", e.OnFailure, ServiceSQS, ServiceSNS, ServiceS3)
	case EventRule:
		v.value(where+".bus", e.Bus)
		v.rule(where, e)
	case EventLogs:
		v.value(where+".logGroup", e.LogGroup)
		if e.LogGroup.Literal == "" && e.LogGroup.Ref == nil && e.LogGroup.Join == nil {
			v.errorf("%s: give the log group by its name", where)
		}
	case EventWebSocket:
		key := "websocket " + e.Route
		if other, ok := v.routes[key]; ok {
			v.errorf("%s: the websocket route %s is also a route of %s", where, e.Route, other)
		}
		v.routes[key] = fn
	case EventALB:
		v.value(where+".listener", e.Listener)
		if len(e.Paths)+len(e.Hosts)+len(e.Methods)+len(e.Headers)+len(e.Query)+len(e.SourceIPs) == 0 {
			v.errorf("%s: a load balancer rule needs paths, hosts, methods, headers, query or sourceIps to match", where)
		}
		if e.Listener.Literal != "" {
			key := fmt.Sprintf("listener %s %d", e.Listener.Literal, e.Priority)
			if other, ok := v.routes[key]; ok {
				v.errorf("%s: priority %d is also a rule of %s on that listener", where, e.Priority, other)
			}
			v.routes[key] = fn
		}
	case EventBucket:
		v.value(where+".bucket", e.Bucket)
		if e.Bucket.Ref != nil {
			if _, set := v.p.Resources[e.Bucket.Ref.Resource].Properties["NotificationConfiguration"]; set {
				v.errorf("%s: resources[%s] sets NotificationConfiguration, which koldstart manages for bucket events: move those notifications to events, or leave the bucket out of events", where, e.Bucket.Ref.Resource)
			}
		}
	}
}

// rule checks an event rule has a pattern, at most one of the ways to change what the function
// gets, and a dead letter queue in sqs.
func (v *validator) rule(where string, e Event) {
	if pattern, err := e.PatternJSON(); err != nil || pattern == "" || pattern == "{}" {
		v.errorf("%s: the pattern is not an event pattern: give an object, or json", where)
	}
	given := 0
	for _, set := range []bool{e.Input != "", e.InputPath != "", e.InputTransformer != nil} {
		if set {
			given++
		}
	}
	if given > 1 {
		v.errorf("%s: give one of input, inputPath and inputTransformer", where)
	}
	v.destination(where+".deadLetter", e.DeadLetter, ServiceSQS)
}

// defaultVisibility is how long sqs hides a received message when the queue does not say.
const defaultVisibility = 30

// visibility checks a queue of the project hides a message for at least the function's timeout,
// or lambda refuses the mapping, as a message would come back while the function still runs.
func (v *validator) visibility(fn, where string, queue Value) {
	if queue.Ref == nil {
		return
	}
	r, ok := v.p.Resources[queue.Ref.Resource]
	if !ok || r.Type != "AWS::SQS::Queue" {
		return
	}
	visibility := defaultVisibility
	if t, ok := r.Properties["VisibilityTimeout"].(float64); ok {
		visibility = int(t)
	}
	if timeout := v.p.Functions[fn].Timeout; visibility < timeout {
		v.errorf("%s: resources[%s] hides a message for %ds, less than the function's timeout of %ds: set its VisibilityTimeout to at least %d", where, queue.Ref.Resource, visibility, timeout, timeout)
	}
}

// walkRefs calls fn for every ref inside a json value.
func walkRefs(value any, fn func(Ref)) {
	if r, ok := AsRef(value); ok {
		fn(r)
		return
	}
	switch v := value.(type) {
	case map[string]any:
		for _, k := range slices.Sorted(maps.Keys(v)) {
			walkRefs(v[k], fn)
		}
	case []any:
		for _, item := range v {
			walkRefs(item, fn)
		}
	}
}
