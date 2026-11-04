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
func eventID(kind, fn string, i int) string { return fmt.Sprintf("%s/%s/%d", kind, fn, i) }
func notificationID(bucket string) string   { return "notification/" + bucket }

// arnPrefix starts the arns of a service in the target's region and account.
func (b *builder) arnPrefix(service string) string {
	return "arn:" + awsp.Partition(b.t.Region) + ":" + service + ":" + b.t.Region + ":" + b.t.Account + ":"
}

// statementID names a permission on a function's policy after its node, unique per stage.
func (b *builder) statementID(node string) string {
	sum := sha256.Sum256([]byte(b.p.App + "/" + b.p.Stage + "/" + node))
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
		inputs["sourceAccount"] = b.t.Account
	}
	return engine.Node{ID: id, Type: awsp.TypePermission, Inputs: inputs}
}

// hasRoutes reports whether any function has an http route, which makes the stage an http api.
func (b *builder) hasRoutes() bool {
	for _, fn := range b.p.Functions {
		for _, e := range fn.Events {
			if e.Type == config.EventHTTPRoute {
				return true
			}
		}
	}
	return false
}

func cors(c *config.CORS) any {
	if c == nil {
		return nil
	}
	return map[string]any{
		"origins": c.Origins, "methods": c.Methods, "headers": c.Headers,
		"expose": c.Expose, "credentials": c.Credentials, "maxAge": c.MaxAge,
	}
}

// httpAPI returns the stage's http api, its authorizers and its domain.
func (b *builder) httpAPI() []engine.Node {
	if !b.hasRoutes() {
		return nil
	}
	inputs := map[string]any{inputName: Name(128, b.p.App, b.p.Stage), "cors": nil, "throttling": nil}
	h := b.p.HTTP
	if h != nil {
		inputs["cors"] = cors(h.CORS)
		if h.Throttling != nil {
			inputs["throttling"] = map[string]any{"rate": h.Throttling.Rate, "burst": h.Throttling.Burst}
		}
	}
	nodes := []engine.Node{{ID: HTTPAPIID, Type: awsp.TypeHTTPAPI, Name: Name(128, b.p.App, b.p.Stage), Inputs: inputs}}
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
			in["uri"] = engine.Join("arn:"+awsp.Partition(b.t.Region)+":apigateway:"+b.t.Region+":lambda:path/2015-03-31/functions/",
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
// mappings, subscriptions, and the permissions that let each source invoke it.
func (b *builder) events(name string, fn config.Function) []engine.Node {
	var nodes []engine.Node
	arn := engine.Ref(FunctionID(name), attrArn)
	functionName := engine.Ref(FunctionID(name), "FunctionName")
	routed := false
	for i, e := range fn.Events {
		switch e.Type {
		case config.EventHTTPRoute:
			routed = true
			nodes = append(nodes, b.route(name, e))
		case config.EventSchedule:
			enabled := e.Enabled == nil || *e.Enabled
			nodes = append(nodes, engine.Node{
				ID: eventID("schedule", name, i), Type: awsp.TypeSchedule, Name: Name(64, b.p.App, b.p.Stage, name, fmt.Sprint(i)),
				Inputs: map[string]any{
					inputName: Name(64, b.p.App, b.p.Stage, name, fmt.Sprint(i)), "expression": e.Expression, "timezone": e.Timezone,
					inputFunction: arn, "role": engine.Ref(schedulerRoleID(name), attrArn), "input": e.Input, "enabled": enabled,
				},
			})
		case config.EventQueue, config.EventStream:
			source := e.Queue
			in := map[string]any{
				inputFunction: functionName, "batchSize": e.BatchSize, "maximumBatchingWindow": e.MaximumBatchingWindow,
				"reportBatchItemFailures": e.ReportBatchItemFailures, "filters": e.Filters, "startingPosition": "",
			}
			if e.Type == config.EventStream {
				source = e.Stream
				in["startingPosition"] = e.StartingPosition
			}
			in["source"] = b.value(source)
			nodes = append(nodes, engine.Node{ID: eventID("mapping", name, i), Type: awsp.TypeMapping, Inputs: in, After: []string{RoleID(name)}})
		case config.EventTopic:
			topic := b.value(e.Topic)
			id := eventID("subscription", name, i)
			permission := b.permission(name, fmt.Sprintf("topic-%d", i), "sns.amazonaws.com", topic, false)
			nodes = append(nodes, permission, engine.Node{
				ID: id, Type: awsp.TypeSubscription,
				Inputs: map[string]any{"topic": topic, inputFunction: arn, "filterPolicy": e.FilterPolicy},
				After:  []string{permission.ID},
			})
		}
	}
	if routed {
		nodes = append(nodes,
			engine.Node{ID: integrationID(name), Type: awsp.TypeIntegration, Inputs: map[string]any{inputAPI: engine.Ref(HTTPAPIID, "ApiId"), inputFunction: arn}},
			b.permission(name, "httpapi", "apigateway.amazonaws.com", engine.Join(b.arnPrefix("execute-api"), engine.Ref(HTTPAPIID, "ApiId"), "/*/*"), false),
		)
	}
	if slices.ContainsFunc(fn.Events, func(e config.Event) bool { return e.Type == config.EventSchedule }) {
		nodes = append(nodes, engine.Node{
			ID: schedulerRoleID(name), Type: awsp.TypeRole, Name: Name(64, b.p.App, b.p.Stage, name, "scheduler", b.t.Region),
			Inputs: map[string]any{
				inputName:          Name(64, b.p.App, b.p.Stage, name, "scheduler", b.t.Region),
				"assumeRolePolicy": trust("scheduler.amazonaws.com", map[string]any{"StringEquals": map[string]any{"aws:SourceAccount": b.t.Account}}),
				"policy":           policy(statement("Allow", []string{"lambda:InvokeFunction"}, []any{arn})),
				"managedPolicies":  nil,
			},
		})
	}
	return nodes
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
		if b.p.HTTP.Authorizers[e.Authorizer].Type == "jwt" {
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
	prefix := "koldstart-" + b.p.App + "-" + b.p.Stage + "-"
	for _, name := range slices.Sorted(maps.Keys(b.p.Functions)) {
		for i, e := range b.p.Functions[name].Events {
			if e.Type != config.EventBucket {
				continue
			}
			key := e.Bucket.Literal
			if e.Bucket.Ref != nil {
				key = e.Bucket.Ref.Resource
			}
			en := buckets[key]
			if en == nil {
				en = &entry{bucket: b.value(e.Bucket)}
				buckets[key] = en
			}
			permission := b.permission(name, fmt.Sprintf("bucket-%d", i), "s3.amazonaws.com", engine.Join("arn:"+awsp.Partition(b.t.Region)+":s3:::", en.bucket), true)
			nodes = append(nodes, permission)
			en.after = append(en.after, permission.ID)
			en.functions = append(en.functions, map[string]any{
				"id": fmt.Sprintf("%s%s-%d", prefix, name, i), inputFunction: engine.Ref(FunctionID(name), attrArn),
				"events": e.Events, "prefix": e.Prefix, "suffix": e.Suffix,
			})
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
