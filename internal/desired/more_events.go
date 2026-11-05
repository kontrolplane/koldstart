package desired

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/kontrolplane/koldstart/internal/config"
	"github.com/kontrolplane/koldstart/internal/engine"
	"github.com/kontrolplane/koldstart/internal/provider/awsp"
)

// WebSocketID is the id of the stage's websocket api.
const WebSocketID = "websocket"

// The kinds of the nodes the events of M13 add, which start their ids.
const (
	kindRule          = "rule"
	kindWSIntegration = "wsintegration"
	kindWSRoute       = "wsroute"
	kindTargetGroup   = "targetgroup"
	kindTargets       = "lbtarget"
	kindListenerRule  = "lbrule"
)

func wsIntegrationID(fn string) string { return kindWSIntegration + "/" + fn }
func wsRouteID(route string) string    { return kindWSRoute + "/" + route }

// hasWebSocket reports whether any function has a websocket route, which gives the stage a
// websocket api.
func (b *builder) hasWebSocket() bool {
	for _, fn := range b.project.Functions {
		if hasEvent(fn, config.EventWebSocket) {
			return true
		}
	}
	return false
}

// webSocketAPI returns the stage's websocket api, with a stage of the stage's name.
func (b *builder) webSocketAPI() []engine.Node {
	if !b.hasWebSocket() {
		return nil
	}
	selection := "$request.body.action"
	if ws := b.project.WebSocket; ws != nil && ws.RouteSelection != "" {
		selection = ws.RouteSelection
	}
	name := Name(128, b.project.App, b.project.Stage, "websocket")
	return []engine.Node{{ID: WebSocketID, Type: awsp.TypeHTTPAPI, Name: name, Inputs: map[string]any{
		inputName: name, inputCORS: nil, "throttling": nil, "protocol": "WEBSOCKET", "routeSelection": selection, "stage": b.project.Stage,
	}}}
}

// webSocketEnvironment is the url a function with websocket routes sends to connections at.
func (b *builder) webSocketEnvironment(fn config.Function, env map[string]any) {
	if !hasEvent(fn, config.EventWebSocket) {
		return
	}
	if _, set := fn.Environment["WEBSOCKET_CALLBACK_URL"]; set {
		return
	}
	env["WEBSOCKET_CALLBACK_URL"] = engine.Join("https://", engine.Ref(WebSocketID, "ApiId"), ".execute-api."+b.target.Region+"."+b.pseudo(config.PseudoURLSuffix)+"/"+b.project.Stage)
}

// webSocketStatements let a function with websocket routes send to the connections of the api.
func (b *builder) webSocketStatements(fn config.Function) []any {
	if !hasEvent(fn, config.EventWebSocket) {
		return nil
	}
	return []any{statement("Allow", []string{"execute-api:ManageConnections"},
		[]any{engine.Join(b.arnPrefix("execute-api"), engine.Ref(WebSocketID, "ApiId"), "/"+b.project.Stage+"/POST/@connections/*")})}
}

// webSocketRoutes are a function's websocket routes, its integration with the api, by the
// invocation uri websocket apis take, and its permission.
func (b *builder) webSocketRoutes(name string, fn config.Function) []engine.Node {
	if !hasEvent(fn, config.EventWebSocket) {
		return nil
	}
	api := engine.Ref(WebSocketID, "ApiId")
	uri := engine.Join("arn:"+awsp.Partition(b.target.Region)+":apigateway:"+b.target.Region+":lambda:path/2015-03-31/functions/",
		engine.Ref(FunctionID(name), attrArn), "/invocations")
	nodes := []engine.Node{
		{ID: wsIntegrationID(name), Type: awsp.TypeIntegration, Inputs: map[string]any{inputAPI: api, inputFunction: uri, "webSocket": true}},
		b.permission(name, "websocket", "apigateway.amazonaws.com", engine.Join(b.arnPrefix("execute-api"), api, "/*"), false),
	}
	for _, e := range fn.Events {
		if e.Type != config.EventWebSocket {
			continue
		}
		nodes = append(nodes, engine.Node{ID: wsRouteID(e.Route), Type: awsp.TypeRoute, Inputs: map[string]any{
			inputAPI: api, "routeKey": e.Route, "target": engine.Join("integrations/", engine.Ref(wsIntegrationID(name), "IntegrationId")),
			"authorizationType": authNone, "authorizer": "", "scopes": nil, "stage": b.project.Stage,
		}})
	}
	return nodes
}

// rule is the n-th eventbridge rule of a function, numbered among its rules, after which the
// permission for eventbridge to invoke it.
func (b *builder) rule(name string, e config.Event, n int) []engine.Node {
	pattern, err := e.PatternJSON()
	if err != nil {
		b.errs = append(b.errs, fmt.Errorf("functions[%s]: the pattern of an event rule: %w", name, err))
	}
	ruleName := Name(64, b.project.App, b.project.Stage, name, fmt.Sprint(n))
	id := eventID(kindRule, name, fmt.Sprint(n))
	bus := b.value(e.Bus)
	if s, ok := bus.(string); ok && s == "" {
		bus = "default"
	}
	return []engine.Node{
		{ID: id, Type: awsp.TypeRule, Name: ruleName, Inputs: map[string]any{
			inputName: ruleName, "bus": bus, "pattern": pattern, inputFunction: engine.Ref(FunctionID(name), attrArn),
			"input": e.Input, "enabled": e.Enabled == nil || *e.Enabled,
		}},
		b.permission(name, "rule-"+fmt.Sprint(n), "events.amazonaws.com", engine.Ref(id, attrArn), false),
	}
}

// targetGroupName is a target group's name, at most 32 long: the app, the stage and the function
// as far as they fit, and a hash that keeps it unique.
func (b *builder) targetGroupName(fn string, n int) string {
	sum := sha256.Sum256([]byte(b.project.App + "/" + b.project.Stage + "/" + fn + "/" + fmt.Sprint(n)))
	prefix := strings.Trim(Name(23, b.project.App, b.project.Stage, fn), "-")
	return prefix + "-" + hex.EncodeToString(sum[:])[:8]
}

// loadBalancer is the n-th load balancer rule of a function: a target group of it, the
// permission for the load balancer to invoke it, the function registered in the group once it
// may, and the listener's rule that forwards to the group.
func (b *builder) loadBalancer(name string, e config.Event, n int) []engine.Node {
	key := fmt.Sprint(n)
	groupID := eventID(kindTargetGroup, name, key)
	group := b.targetGroupName(name, n)
	permission := b.permission(name, "alb-"+key, "elasticloadbalancing.amazonaws.com", engine.Ref(groupID, attrArn), false)
	targetsID := eventID(kindTargets, name, key)
	return []engine.Node{
		{ID: groupID, Type: awsp.TypeTargetGroup, Name: group, Inputs: map[string]any{inputName: group, "multiValueHeaders": e.MultiValueHeaders}},
		permission,
		{ID: targetsID, Type: awsp.TypeTargets, After: []string{permission.ID}, Inputs: map[string]any{
			"targetGroup": engine.Ref(groupID, attrArn), inputFunction: engine.Ref(FunctionID(name), attrArn),
		}},
		{ID: eventID(kindListenerRule, name, key), Type: awsp.TypeListenerRule, After: []string{targetsID}, Inputs: map[string]any{
			"listener": b.value(e.Listener), "priority": e.Priority, "targetGroup": engine.Ref(groupID, attrArn),
			"paths": e.Paths, "hosts": e.Hosts, "methods": e.Methods,
		}},
	}
}
