package migrate

import (
	"fmt"
	"regexp"
	"strings"

	"go.yaml.in/yaml/v3"
)

// events writes a function's events.
func (c *converter) events(w *writer, where string, events *yaml.Node) {
	if events == nil || len(events.Content) == 0 {
		return
	}
	w.open("events")
	for i, e := range events.Content {
		kind := ""
		if len(e.Content) >= 2 {
			kind = e.Content[0].Value
		}
		at := fmt.Sprintf("%s.events[%d]", where, i)
		spec := get(e, kind)
		switch kind {
		case "httpApi":
			c.httpAPIEvent(w, at, spec)
		case "http":
			c.restEvent(w, at, spec)
		case "schedule":
			c.scheduleEvent(w, at, spec)
		case "sqs":
			c.queueEvent(w, at, spec)
		case "stream":
			c.streamEvent(w, at, spec)
		case "sns":
			c.topicEvent(w, at, spec)
		case "s3":
			c.bucketEvent(w, at, spec)
		case "eventBridge":
			c.ruleEvent(w, at, spec)
		case "websocket":
			c.webSocketEvent(w, at, spec)
		case "alb":
			c.albEvent(w, at, spec)
		case "cloudwatchLog":
			c.logEvent(w, at, spec)
		default:
			c.leaveOut(w, at, fmt.Sprintf("koldstart has no %s events yet", kind), e)
		}
	}
	w.close()
}

// methodAny is the method of a route that takes every method.
const methodAny = "ANY"

// route splits "GET /orders/{id}" into its method and path; "*" is every request.
func route(s string) (string, string) {
	if s == "*" {
		return methodAny, "$default"
	}
	method, path, ok := strings.Cut(strings.TrimSpace(s), " ")
	if !ok {
		return methodAny, method
	}
	return strings.ToUpper(method), strings.TrimSpace(path)
}

func (c *converter) httpAPIEvent(w *writer, where string, n *yaml.Node) {
	method, path := route(str(n))
	if n.Kind == yaml.MappingNode {
		method, path = strings.ToUpper(str(get(n, "method"))), str(get(n, "path"))
		if method == "*" || method == "" {
			method = methodAny
		}
		if path == "*" {
			path = "$default"
		}
	}
	httpRoute(w, method, path, func(w *writer) { c.routeAuthorizer(w, where, get(n, "authorizer")) })
}

// routeHelpers are the helpers of kdst.pkl for the routes of each method.
var routeHelpers = map[string]string{
	"GET": "get", "POST": "post", "PUT": "put", "PATCH": "patch", "DELETE": "del", "HEAD": "head", "OPTIONS": "options", methodAny: "any",
}

// httpRoute writes a route as its method's helper, kdst.get("/x"), amended with what rest
// writes, or as an HttpRoute for a method with no helper.
func httpRoute(w *writer, method, path string, rest func(w *writer)) {
	helper, ok := routeHelpers[method]
	if !ok {
		w.open("new kdst.HttpRoute")
		w.line("method = %s", quote(method))
		w.line("path = %s", quote(path))
		rest(w)
		w.close()
		return
	}
	w.amend("", fmt.Sprintf("kdst.%s(%s)", helper, quote(path)), rest)
}

func (c *converter) routeAuthorizer(w *writer, where string, n *yaml.Node) {
	if n == nil {
		return
	}
	if n.Kind != yaml.MappingNode {
		w.line("authorizer = %s", quote(str(n)))
		return
	}
	if str(get(n, "type")) == "aws_iam" {
		w.line("iam = true")
		return
	}
	w.line("authorizer = %s", quote(str(get(n, "name"))))
	if scopes := get(n, "scopes"); scopes != nil {
		c.member(w, where+".authorizer.scopes", "scopes", scopes)
	}
}

// scheduleEvent writes a schedule for each rate: kdst.schedule("rate(1 hour)"), amended with
// enabled, timezone and input.
func (c *converter) scheduleEvent(w *writer, where string, n *yaml.Node) {
	rates := []*yaml.Node{n}
	if n.Kind == yaml.MappingNode {
		rates = listOf(orEmpty(get(n, "rate"))).Content
	}
	for _, rate := range rates {
		rest := func(w *writer) {
			if isFalse(get(n, "enabled")) {
				w.line("enabled = false")
			}
			if tz := str(get(n, "timezone")); tz != "" {
				w.line("timezone = %s", quote(tz))
			}
			if input := get(n, "input"); input != nil {
				if input.Kind == yaml.ScalarNode {
					w.line("input = %s", quote(input.Value))
				} else if js, err := jsonString(input); err == nil {
					w.line("input = %s", js)
				}
			}
		}
		expression := c.scalar(rate)
		if !expression.ok || !expression.interpolable {
			w.open("new kdst.Schedule")
			c.assign(w, where+".rate", "expression", expression, rate)
			rest(w)
			w.close()
			continue
		}
		w.amend("", "kdst.schedule("+expression.expr+")", rest)
	}
}

// arnOf is the node of the arn an event reads from: the event itself, or its arn.
func arnOf(n *yaml.Node) *yaml.Node {
	if n.Kind == yaml.MappingNode {
		if name, _ := intrinsicOf(n); name == "" {
			return orEmpty(get(n, "arn"))
		}
	}
	return n
}

// helperSource is what a helper of kdst.pkl such as kdst.fromQueue() takes for the source of
// an event: the name of a resource of the project, for a Fn::GetAtt of its attribute, or an arn
// given as is. It is false for anything else, which the event's class takes instead.
func (c *converter) helperSource(n *yaml.Node, attribute string) (string, bool) {
	arn := arnOf(n)
	name, arg := intrinsicOf(arn)
	switch {
	case name == intrinsicGetAtt:
		target, attr, _ := strings.Cut(arg.Value, ".")
		if arg.Kind == yaml.SequenceNode && len(arg.Content) == 2 {
			target, attr = arg.Content[0].Value, arg.Content[1].Value
		}
		if resource, ok := c.resourceNames[target]; ok && attr == attribute {
			return quote(resource), true
		}
	case name == "" && arn.Kind == yaml.ScalarNode && strings.HasPrefix(arn.Value, "arn:") && !strings.Contains(arn.Value, "${"):
		return quote(arn.Value), true
	}
	return "", false
}

// sourceEvent writes an event as the helper of kdst.pkl that takes its source, kdst.fromQueue(),
// amended with what rest writes, or as the event's class when helperSource has no source for it.
func (c *converter) sourceEvent(w *writer, where string, n *yaml.Node, helper, class, key, attribute string, rest func(w *writer)) {
	if source, ok := c.helperSource(n, attribute); ok {
		w.amend("", fmt.Sprintf("kdst.%s(%s)", helper, source), rest)
		return
	}
	w.open("new kdst." + class)
	c.source(w, where, key, n)
	rest(w)
	w.close()
}

// source is the arn an event reads from: given as is, or as an intrinsic.
func (c *converter) source(w *writer, where, key string, n *yaml.Node) {
	arn := arnOf(n)
	if v, ok := c.intrinsic(arn); ok {
		c.assign(w, where+".arn", key, v, arn)
		return
	}
	c.assign(w, where+".arn", key, c.scalar(arn), arn)
}

// batching writes what a queue and a stream share: batch size, window, partial failures, filters,
// and whether the mapping is enabled.
func (c *converter) batching(w *writer, n *yaml.Node, window string) {
	if size, ok := integer(get(n, "batchSize")); ok {
		w.line("batchSize = %d", size)
	}
	if seconds, ok := integer(get(n, window)); ok {
		w.line("maximumBatchingWindow = %d.s", seconds)
	}
	report := str(get(n, "functionResponseType")) == "ReportBatchItemFailures"
	w.line("reportBatchItemFailures = %t", report)
	if patterns := get(n, "filterPatterns"); patterns != nil {
		w.open("filters")
		for _, p := range patterns.Content {
			if js, err := jsonString(p); err == nil {
				w.line("%s", js)
			}
		}
		w.close()
	}
	if isFalse(get(n, "enabled")) {
		w.line("enabled = false")
	}
}

func (c *converter) queueEvent(w *writer, where string, n *yaml.Node) {
	c.sourceEvent(w, where, n, "fromQueue", "Queue", "queue", attrArn, func(w *writer) {
		c.batching(w, n, "maximumBatchingWindow")
		if limit, ok := integer(get(n, "maximumConcurrency")); ok {
			w.line("maximumConcurrency = %d", limit)
		}
	})
}

// failures writes how a stream's mapping handles a batch the function fails on: its retries,
// how old a record may get, splitting the batch, parallel batches, the tumbling window, and
// where a batch that is given up on goes.
func (c *converter) failures(w *writer, where string, n *yaml.Node) {
	if retries, ok := integer(get(n, "maximumRetryAttempts")); ok && retries >= 0 {
		w.line("maximumRetryAttempts = %d", retries)
	}
	if age, ok := integer(get(n, "maximumRecordAgeInSeconds")); ok && age >= 0 {
		w.line("maximumRecordAge = %d.s", age)
	}
	if isTrue(get(n, "bisectBatchOnFunctionError")) {
		w.line("bisectBatchOnFunctionError = true")
	}
	if factor, ok := integer(get(n, "parallelizationFactor")); ok && factor > 1 {
		w.line("parallelizationFactor = %d", factor)
	}
	if window, ok := integer(get(n, "tumblingWindowInSeconds")); ok && window > 0 {
		w.line("tumblingWindow = %d.s", window)
	}
	if failure := get(get(n, "destinations"), "onFailure"); failure != nil {
		c.assign(w, where+".destinations.onFailure", "onFailure", c.destination(failure), failure)
	}
	for _, key := range []string{"consumer", "startingPositionTimestamp"} {
		if v := get(n, key); v != nil {
			c.leaveOut(w, where+"."+key, "koldstart has no "+key+" yet", v)
		}
	}
}

// attrArn is the attribute of most resources' arn.
const attrArn = "Arn"

// streamEvent is a kinesis stream, kdst.fromStream(), or a dynamodb table's stream,
// kdst.fromTableStream(), by its arn or its attribute.
func (c *converter) streamEvent(w *writer, where string, n *yaml.Node) {
	tableStream := strings.Contains(arnOf(n).Value, ":dynamodb:")
	if name, _ := intrinsicOf(arnOf(n)); name != "" {
		_, tableStream = c.helperSource(n, "StreamArn")
	}
	helper, attribute := "fromStream", attrArn
	if tableStream {
		helper, attribute = "fromTableStream", "StreamArn"
	}
	c.sourceEvent(w, where, n, helper, "Stream", "stream", attribute, func(w *writer) {
		switch position := str(get(n, "startingPosition")); position {
		case "", "LATEST":
		case "TRIM_HORIZON":
			w.line(`startingPosition = "TRIM_HORIZON"`)
		default:
			c.leave(w, Review, where+".startingPosition", "the stream starts at LATEST, as koldstart has no "+position, nil)
		}
		c.batching(w, n, "batchWindow")
		c.failures(w, where, n)
	})
}

// topicEvent subscribes to a topic by its arn, or to one the serverless framework made by its
// name, which becomes a resource.
func (c *converter) topicEvent(w *writer, where string, n *yaml.Node) {
	rest := func(w *writer) {
		if policy := get(n, "filterPolicy"); policy != nil {
			if js, err := jsonString(policy); err == nil {
				w.line("filterPolicy = %s", js)
			}
		}
	}
	switch {
	case n.Kind == yaml.ScalarNode && !strings.HasPrefix(n.Value, "arn:") && !strings.Contains(n.Value, "${"):
		w.amend("", fmt.Sprintf("kdst.fromTopic(%s)", quote(c.add(where, n.Value, typeTopic, "TopicName"))), rest)
	case n.Kind == yaml.MappingNode && get(n, "arn") == nil && get(n, "topicName") != nil:
		w.amend("", fmt.Sprintf("kdst.fromTopic(%s)", quote(c.add(where, str(get(n, "topicName")), typeTopic, "TopicName"))), rest)
	default:
		c.sourceEvent(w, where, n, "fromTopic", "Topic", "topic", "TopicArn", rest)
	}
}

// bucketEvent is a notification of a bucket the serverless framework made, which becomes a
// resource, or of an existing one, by its name.
func (c *converter) bucketEvent(w *writer, where string, n *yaml.Node) {
	bucket := str(n)
	existing := false
	if n.Kind == yaml.MappingNode {
		bucket = str(get(n, "bucket"))
		existing = isTrue(get(n, "existing"))
	}
	rest := func(w *writer) {
		if event := str(get(n, "event")); event != "" {
			w.line("events { %s }", quote(event))
		}
		for _, rule := range orList(get(n, "rules")).Content {
			if prefix := str(get(rule, "prefix")); prefix != "" {
				w.line("prefix = %s", quote(prefix))
			}
			if suffix := str(get(rule, "suffix")); suffix != "" {
				w.line("suffix = %s", quote(suffix))
			}
		}
	}
	if !existing && !strings.Contains(bucket, "${") {
		w.amend("", fmt.Sprintf("kdst.fromBucket(%s)", quote(c.add(where, bucket, typeBucket, "BucketName"))), rest)
		return
	}
	w.open("new kdst.Bucket")
	c.assign(w, where+".bucket", "bucket", c.text(bucket), n)
	rest(w)
	w.close()
}

var resourceName = regexp.MustCompile(`[^A-Za-z0-9-]+`)

// add records a resource the serverless framework makes without declaring it for the event at
// where, once, and returns its name in resources.
func (c *converter) add(where, physical, typ, property string) string {
	for _, r := range c.added {
		if r.typ == typ && r.value == physical {
			return r.name
		}
	}
	name := strings.Trim(resourceName.ReplaceAllString(physical, "-"), "-")
	if name == "" || name[0] < 'A' || (name[0] > 'Z' && name[0] < 'a') || name[0] > 'z' {
		name = "r-" + name
	}
	if len(name) > 40 {
		name = name[:40]
	}
	if _, taken := c.resourceNames[name]; taken {
		name += "-" + strings.ToLower(strings.TrimPrefix(typ[strings.LastIndex(typ, ":")+1:], ":"))
	}
	c.resourceNames[name] = name
	c.added = append(c.added, addedResource{name: name, typ: typ, property: property, value: physical, where: where})
	return name
}

// ruleEvent converts an eventBridge event: its pattern, bus, input and whether it is enabled. A
// schedule given as an eventBridge event becomes a rule of its own pattern, which eventbridge
// rules without a pattern do not take, so it is left for you.
func (c *converter) ruleEvent(w *writer, where string, spec *yaml.Node) {
	pattern := get(spec, "pattern")
	if pattern == nil {
		c.leaveOut(w, where, "an EventRule needs a pattern: write a schedule as kdst.schedule(\"rate(...)\")", spec)
		return
	}
	w.open("new kdst.EventRule")
	c.member(w, where+".pattern", "pattern = new Dynamic", pattern)
	if bus := get(spec, "eventBus"); bus != nil {
		c.assign(w, where+".eventBus", "bus", c.nodeValue(bus), bus)
	}
	if input := get(spec, "input"); input != nil {
		if s, err := jsonString(input); err == nil {
			w.line("input = %s", s)
		} else {
			c.leaveOut(w, where+".input", err.Error(), input)
		}
	}
	if path := get(spec, "inputPath"); path != nil {
		c.assign(w, where+".inputPath", "inputPath", c.scalar(path), path)
	}
	if t := get(spec, "inputTransformer"); t != nil {
		w.open("inputTransformer = new")
		c.mapping(w, where+".inputTransformer.inputPathsMap", "paths", orMapping(get(t, "inputPathsMap")))
		template := orEmpty(get(t, "inputTemplate"))
		c.assign(w, where+".inputTransformer.inputTemplate", "template", c.scalar(template), template)
		w.close()
	}
	if isFalse(get(spec, "enabled")) {
		w.line("enabled = false")
	}
	retry := get(spec, "retryPolicy")
	if n, ok := integer(get(retry, "maximumRetryAttempts")); ok {
		w.line("maximumRetryAttempts = %d", n)
	}
	if n, ok := integer(get(retry, "maximumEventAge")); ok {
		w.line("maximumEventAge = %d.s", n)
	}
	if dlq := get(spec, "deadLetterQueueArn"); dlq != nil {
		c.assign(w, where+".deadLetterQueueArn", "deadLetter", c.nodeValue(dlq), dlq)
		c.todo(Review, where+".deadLetterQueueArn", "the queue's policy must let events.amazonaws.com send to it")
	}
	w.close()
}

func orMapping(n *yaml.Node) *yaml.Node {
	if n == nil {
		return &yaml.Node{Kind: yaml.MappingNode}
	}
	return n
}

// logEvent converts a cloudwatchLog event, a log group by its name, or { logGroup, filter }, to
// kdst.fromLogs().
func (c *converter) logEvent(w *writer, where string, spec *yaml.Node) {
	group, filter := spec, (*yaml.Node)(nil)
	if spec.Kind == yaml.MappingNode {
		group, filter = orEmpty(get(spec, "logGroup")), get(spec, "filter")
	}
	rest := func(w *writer) {
		if filter != nil && str(filter) != "" {
			c.assign(w, where+".filter", "filter", c.scalar(filter), filter)
		}
	}
	v := c.nodeValue(group)
	if !v.ok {
		c.leaveOut(w, where, v.todo, spec)
		return
	}
	w.amend("", "kdst.fromLogs("+v.expr+")", rest)
}

// webSocketEvent converts a websocket event, a route by its key: { route: $connect }, or the key
// alone.
func (c *converter) webSocketEvent(w *writer, where string, spec *yaml.Node) {
	route := str(spec)
	if spec.Kind == yaml.MappingNode {
		route = str(get(spec, "route"))
		for _, key := range []string{"authorizer", "routeResponseSelectionExpression"} {
			if v := get(spec, key); v != nil {
				c.leaveOut(w, where+"."+key, "koldstart has no "+key+" on a websocket route yet", v)
			}
		}
	}
	if route == "" {
		c.leaveOut(w, where, "give the websocket event its route", spec)
		return
	}
	w.line("kdst.fromWebSocket(%s)", quote(route))
}

// albEvent converts an alb event: its listener, priority and conditions.
func (c *converter) albEvent(w *writer, where string, spec *yaml.Node) {
	listener := get(spec, "listenerArn")
	priority, ok := integer(get(spec, "priority"))
	if listener == nil || !ok {
		c.leaveOut(w, where, "give the alb event its listenerArn and priority", spec)
		return
	}
	conditions := get(spec, "conditions")
	w.open("new kdst.LoadBalancer")
	c.assign(w, where+".listenerArn", "listener", c.nodeValue(listener), listener)
	w.line("priority = %d", priority)
	for _, pair := range [][2]string{{"path", "paths"}, {"host", "hosts"}, {"method", "methods"}} {
		v := get(conditions, pair[0])
		if v == nil {
			continue
		}
		if v.Kind == yaml.ScalarNode {
			v = &yaml.Node{Kind: yaml.SequenceNode, Content: []*yaml.Node{v}}
		}
		c.member(w, where+".conditions."+pair[0], pair[1], v)
	}
	if header := get(conditions, "header"); header != nil {
		name, values := str(get(header, "name")), get(header, "values")
		if name == "" || values == nil {
			c.leaveOut(w, where+".conditions.header", "give the header condition its name and values", header)
		} else {
			w.open("headers")
			c.member(w, where+".conditions.header.values", "["+quote(name)+"]", listOf(values))
			w.close()
		}
	}
	if query := get(conditions, "query"); query != nil {
		c.mapping(w, where+".conditions.query", "query", query)
	}
	if ip := get(conditions, "ip"); ip != nil {
		c.member(w, where+".conditions.ip", "sourceIps", listOf(ip))
	}
	if isTrue(get(spec, "multiValueHeaders")) {
		w.line("multiValueHeaders = true")
	}
	w.close()
}
