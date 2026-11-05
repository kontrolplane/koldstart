package desired

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/kontrolplane/koldstart/internal/config"
	"github.com/kontrolplane/koldstart/internal/engine"
	"github.com/kontrolplane/koldstart/internal/provider/awsp"
	"github.com/kontrolplane/koldstart/internal/state"
)

// The ids of the nodes events add.
const (
	HTTPAPIID = "httpapi"
	DomainID  = "domain"
)

func integrationID(fn string) string        { return "integration/" + fn }
func routeID(method, path string) string    { return "route/" + strings.ToUpper(method) + " " + path }
func authorizerID(name string) string       { return "authorizer/" + name }
func schedulerRoleID(fn string) string      { return "schedulerrole/" + fn }
func permissionID(fn, source string) string { return "permission/" + fn + "/" + source }
func eventID(kind, fn, key string) string   { return kind + "/" + fn + "/" + key }

// The kinds of the events' nodes, which start their ids.
const (
	kindSchedule     = "schedule"
	kindMapping      = "mapping"
	kindSubscription = "subscription"
)

// kindOf is the kind of a node, what its id starts with.
func kindOf(id string) string {
	kind, _, _ := strings.Cut(id, "/")
	return kind
}
func notificationID(bucket string) string { return "notification/" + bucket }

// sourceKey names an event's source in node ids: the resource a ref names, or the last part of
// an arn or name given as is. It does not change when other events are added or removed.
func sourceKey(v config.Value) string {
	if v.Ref != nil {
		return v.Ref.Resource
	}
	key := v.Literal
	if i := strings.LastIndexAny(key, ":/"); i >= 0 && i < len(key)-1 {
		key = key[i+1:]
	}
	return key
}

// arnPrefix starts the arns of a service in the target's region and account.
func (b *builder) arnPrefix(service string) string {
	return "arn:" + awsp.Partition(b.target.Region) + ":" + service + ":" + b.target.Region + ":" + b.target.Account + ":"
}

// statementID names a permission on a function's policy after its node, unique per stage.
func (b *builder) statementID(node string) string {
	sum := sha256.Sum256([]byte(b.project.App + "/" + b.project.Stage + "/" + node))
	return "koldstart-" + hex.EncodeToString(sum[:])[:16]
}

func (b *builder) permission(fn, source, principal string, sourceArn any, account bool) engine.Node {
	id := permissionID(fn, source)
	inputs := map[string]any{
		inputFunction: engine.Ref(FunctionID(fn), "FunctionName"),
		"statementId": b.statementID(id),
		"action":      "lambda:InvokeFunction",
		"principal":   principal,
		"sourceArn":   sourceArn,
	}
	if account {
		inputs["sourceAccount"] = b.target.Account
	}
	return engine.Node{ID: id, Type: awsp.TypePermission, Inputs: inputs}
}

// hasEvent reports whether a function has an event of the type.
func hasEvent(fn config.Function, typ string) bool {
	return slices.ContainsFunc(fn.Events, func(e config.Event) bool { return e.Type == typ })
}

// hasRoutes reports whether any function has an http route, which makes the stage an http api.
func (b *builder) hasRoutes() bool {
	for _, fn := range b.project.Functions {
		if hasEvent(fn, config.EventHTTPRoute) {
			return true
		}
	}
	return false
}

// httpAPI returns the stage's http api, its authorizers and its domain.
func (b *builder) httpAPI() []engine.Node {
	if !b.hasRoutes() {
		return nil
	}
	inputs := map[string]any{inputName: Name(128, b.project.App, b.project.Stage), "cors": nil, "throttling": nil}
	h := b.project.HTTP
	if h != nil {
		inputs["cors"] = corsInputs(h.CORS)
		if h.Throttling != nil {
			inputs["throttling"] = map[string]any{"rate": h.Throttling.Rate, "burst": h.Throttling.Burst}
		}
	}
	nodes := []engine.Node{{ID: HTTPAPIID, Type: awsp.TypeHTTPAPI, Name: Name(128, b.project.App, b.project.Stage), Inputs: inputs}}
	if h == nil {
		return nodes
	}
	api := engine.Ref(HTTPAPIID, "ApiId")
	for _, name := range slices.Sorted(maps.Keys(h.Authorizers)) {
		a := h.Authorizers[name]
		in := map[string]any{inputAPI: api, inputName: name, "identity": a.Identity}
		if a.Type == "jwt" {
			in["type"], in["issuer"], in["audience"] = "JWT", a.Issuer, a.Audience
		} else {
			in["type"], in["ttl"] = "REQUEST", a.TTL
			in["uri"] = engine.Join("arn:"+awsp.Partition(b.target.Region)+":apigateway:"+b.target.Region+":lambda:path/2015-03-31/functions/",
				engine.Ref(FunctionID(a.Function), attrArn), "/invocations")
			nodes = append(nodes, b.permission(a.Function, "authorizer-"+name, "apigateway.amazonaws.com",
				engine.Join(b.arnPrefix("execute-api"), api, "/authorizers/*"), false))
		}
		nodes = append(nodes, engine.Node{ID: authorizerID(name), Type: awsp.TypeAuthorizer, Inputs: in})
	}
	if h.Domain != "" {
		nodes = append(nodes, engine.Node{
			ID: DomainID, Type: awsp.TypeDomain, Name: h.Domain,
			Inputs: map[string]any{"domain": h.Domain, "certificate": b.value(h.Certificate), inputAPI: api},
		})
	}
	return nodes
}

// events returns the nodes a function's events add: its integration and routes, schedules,
// mappings, subscriptions, and the permissions that let each source invoke it. Buckets are left
// to notifications, which gathers the functions of each bucket.
func (b *builder) events(name string, fn config.Function) []engine.Node {
	var nodes []engine.Node
	schedules := 0
	for _, e := range fn.Events {
		switch e.Type {
		case config.EventHTTPRoute:
			nodes = append(nodes, b.route(name, e))
		case config.EventSchedule:
			nodes = append(nodes, b.schedule(name, e, schedules))
			schedules++
		case config.EventQueue, config.EventStream:
			nodes = append(nodes, b.mapping(name, e))
		case config.EventTopic:
			nodes = append(nodes, b.subscription(name, e)...)
		}
	}
	if hasEvent(fn, config.EventHTTPRoute) {
		nodes = append(nodes, b.integration(name)...)
	}
	if hasEvent(fn, config.EventSchedule) {
		nodes = append(nodes, b.schedulerRole(name))
	}
	return nodes
}

// schedule is the n-th schedule of a function. Schedules are numbered among themselves, so one
// keeps its node when an event of another kind comes or goes.
func (b *builder) schedule(name string, e config.Event, n int) engine.Node {
	scheduleName := Name(64, b.project.App, b.project.Stage, name, fmt.Sprint(n))
	return engine.Node{
		ID: eventID(kindSchedule, name, fmt.Sprint(n)), Type: awsp.TypeSchedule, Name: scheduleName,
		Inputs: map[string]any{
			inputName: scheduleName, "expression": e.Expression, "timezone": e.Timezone,
			inputFunction: engine.Ref(FunctionID(name), attrArn), "role": engine.Ref(schedulerRoleID(name), attrArn),
			"input": e.Input, "enabled": e.Enabled == nil || *e.Enabled,
		},
	}
}

// schedulerRole is the role the scheduler invokes a function with.
func (b *builder) schedulerRole(name string) engine.Node {
	role := Name(64, b.project.App, b.project.Stage, name, "scheduler", b.target.Region)
	return engine.Node{
		ID: schedulerRoleID(name), Type: awsp.TypeRole, Name: role,
		Inputs: map[string]any{
			inputName:          role,
			"assumeRolePolicy": trust("scheduler.amazonaws.com", map[string]any{"StringEquals": map[string]any{"aws:SourceAccount": b.target.Account}}),
			"policy":           policy(statement("Allow", []string{"lambda:InvokeFunction"}, []any{engine.Ref(FunctionID(name), attrArn)})),
			"managedPolicies":  nil,
		},
	}
}

// mapping is the event source mapping of a queue or a stream, after the role that may read it.
func (b *builder) mapping(name string, e config.Event) engine.Node {
	source, startingPosition := e.Queue, ""
	if e.Type == config.EventStream {
		source, startingPosition = e.Stream, e.StartingPosition
	}
	return engine.Node{
		ID: eventID(kindMapping, name, sourceKey(source)), Type: awsp.TypeMapping, After: []string{RoleID(name)},
		Inputs: map[string]any{
			inputFunction: engine.Ref(FunctionID(name), "FunctionName"), "source": b.value(source),
			"batchSize": e.BatchSize, "maximumBatchingWindow": e.MaximumBatchingWindow,
			"reportBatchItemFailures": e.ReportBatchItemFailures, "filters": e.Filters, "startingPosition": startingPosition,
		},
	}
}

// subscription is a function's subscription to a topic, after the permission that lets sns
// invoke it.
func (b *builder) subscription(name string, e config.Event) []engine.Node {
	topic := b.value(e.Topic)
	permission := b.permission(name, "topic-"+sourceKey(e.Topic), "sns.amazonaws.com", topic, false)
	return []engine.Node{permission, {
		ID: eventID(kindSubscription, name, sourceKey(e.Topic)), Type: awsp.TypeSubscription, After: []string{permission.ID},
		Inputs: map[string]any{"topic": topic, inputFunction: engine.Ref(FunctionID(name), attrArn), "filterPolicy": e.FilterPolicy},
	}}
}

// integration is how the http api hands a function's routes to it, and its permission to.
func (b *builder) integration(name string) []engine.Node {
	api := engine.Ref(HTTPAPIID, "ApiId")
	return []engine.Node{
		{ID: integrationID(name), Type: awsp.TypeIntegration, Inputs: map[string]any{inputAPI: api, inputFunction: engine.Ref(FunctionID(name), attrArn)}},
		b.permission(name, "httpapi", "apigateway.amazonaws.com", engine.Join(b.arnPrefix("execute-api"), api, "/*/*"), false),
	}
}

func (b *builder) route(fn string, e config.Event) engine.Node {
	in := map[string]any{
		inputAPI:            engine.Ref(HTTPAPIID, "ApiId"),
		"routeKey":          strings.ToUpper(e.Method) + " " + e.Path,
		"target":            engine.Join("integrations/", engine.Ref(integrationID(fn), "IntegrationId")),
		"authorizationType": "NONE",
		"authorizer":        "",
		"scopes":            e.Scopes,
	}
	if e.Path == "$default" {
		in["routeKey"] = "$default"
	}
	if e.Authorizer != "" {
		in["authorizer"] = engine.Ref(authorizerID(e.Authorizer), "AuthorizerId")
		in["authorizationType"] = "CUSTOM"
		if b.project.HTTP.Authorizers[e.Authorizer].Type == "jwt" {
			in["authorizationType"] = "JWT"
		}
	}
	return engine.Node{ID: routeID(e.Method, e.Path), Type: awsp.TypeRoute, Inputs: in}
}

// notifications returns one node per bucket with the notifications of every function it
// invokes, after the permissions that let s3 do so, which s3 checks when they are put.
func (b *builder) notifications() []engine.Node {
	type entry struct {
		bucket    any
		functions []any
		after     []string
	}
	buckets := map[string]*entry{}
	var nodes []engine.Node
	prefix := "koldstart-" + b.project.App + "-" + b.project.Stage + "-"
	for _, name := range slices.Sorted(maps.Keys(b.project.Functions)) {
		seen := map[string]int{}
		for _, e := range b.project.Functions[name].Events {
			if e.Type != config.EventBucket {
				continue
			}
			key := sourceKey(e.Bucket)
			en := buckets[key]
			if en == nil {
				en = &entry{bucket: b.value(e.Bucket)}
				buckets[key] = en
			}
			if seen[key] == 0 {
				permission := b.permission(name, "bucket-"+key, "s3.amazonaws.com", engine.Join("arn:"+awsp.Partition(b.target.Region)+":s3:::", en.bucket), true)
				nodes = append(nodes, permission)
				en.after = append(en.after, permission.ID)
			}
			en.functions = append(en.functions, map[string]any{
				"id": fmt.Sprintf("%s%s-%d", prefix, name, seen[key]), inputFunction: engine.Ref(FunctionID(name), attrArn),
				"events": e.Events, "prefix": e.Prefix, "suffix": e.Suffix,
			})
			seen[key]++
		}
	}
	for _, key := range slices.Sorted(maps.Keys(buckets)) {
		en := buckets[key]
		nodes = append(nodes, engine.Node{
			ID: notificationID(key), Type: awsp.TypeNotification,
			Inputs: map[string]any{"bucket": en.bucket, "prefix": prefix, "functions": en.functions},
			After:  en.after,
		})
	}
	return nodes
}

// sourceStatements are what a function's role needs to read its queues and streams.
func (b *builder) sourceStatements(fn config.Function) []any {
	var out []any
	for _, e := range fn.Events {
		switch e.Type {
		case config.EventQueue:
			out = append(out, statement("Allow", sqsRead, []any{b.value(e.Queue)}))
		case config.EventStream:
			out = append(out, statement("Allow", slices.Concat(kinesisRead, dynamoRead), []any{b.value(e.Stream)}))
		}
	}
	return out
}

// Triggers lists the nodes of a state that invoke a function: its routes, schedules, mappings,
// subscriptions and the notifications of its buckets, sorted.
func Triggers(resources map[string]*state.Resource, fn string) []string {
	var out []string
	for id, r := range resources {
		kind, rest, _ := strings.Cut(id, "/")
		switch {
		case kind == kindSchedule || kind == kindMapping || kind == kindSubscription:
			if strings.HasPrefix(rest, fn+"/") {
				out = append(out, id)
			}
		case kind == "route" && slices.Contains(r.Dependencies, integrationID(fn)):
			out = append(out, id)
		case kind == "notification" && slices.ContainsFunc(r.Dependencies, func(d string) bool { return strings.HasPrefix(d, permissionID(fn, "")) }):
			out = append(out, id)
		}
	}
	slices.Sort(out)
	return out
}

// The groups the nodes of a stage fall in.
const (
	GroupFunction = "function"
	GroupResource = "resource"
	GroupHTTP     = "http"
)

// Group says what a node of a state belongs to, by its id and what it depends on. A function
// has its role, log group, url, integration, permissions and events, and the routes to it; a
// resource has the notifications of a bucket; the stage's http api has its domain and its
// authorizers. Anything else has no group.
func Group(id string, deps []string) (kind, name string) {
	kind, rest, _ := strings.Cut(id, "/")
	first, _, _ := strings.Cut(rest, "/")
	switch kind {
	case kindOf(FunctionID("")), kindOf(RoleID("")), kindOf(LogGroupID("")), kindOf(URLID("")), kindOf(integrationID("")),
		kindOf(schedulerRoleID("")), kindOf(permissionID("", "")), kindSchedule, kindMapping, kindSubscription:
		return GroupFunction, first
	case kindOf(ResourceID("")), kindOf(notificationID("")):
		return GroupResource, rest
	case kindOf(routeID("", "")):
		for _, d := range deps {
			if fn, ok := strings.CutPrefix(d, "integration/"); ok {
				return GroupFunction, fn
			}
		}
		return GroupHTTP, ""
	case HTTPAPIID, DomainID, kindOf(authorizerID("")):
		return GroupHTTP, ""
	}
	return "", ""
}
