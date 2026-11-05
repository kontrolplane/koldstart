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
		default:
			c.leaveOut(w, at, fmt.Sprintf("events: %s has no equivalent in koldstart yet", kind), e)
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
	w.open("new kdst.HttpRoute")
	w.line("method = %s", quote(method))
	w.line("path = %s", quote(path))
	c.routeAuthorizer(w, where, get(n, "authorizer"))
	w.close()
}

func (c *converter) routeAuthorizer(w *writer, where string, n *yaml.Node) {
	if n == nil {
		return
	}
	name := str(n)
	if n.Kind == yaml.MappingNode {
		if str(get(n, "type")) == "aws_iam" {
			c.todo(where+".authorizer", "aws_iam authorization of routes has no equivalent yet")
			w.comment("TODO: aws_iam authorization of routes has no equivalent yet.")
			return
		}
		name = str(get(n, "name"))
		if scopes := get(n, "scopes"); scopes != nil {
			c.member(w, where+".authorizer.scopes", "scopes", scopes)
		}
	}
	w.line("authorizer = %s", quote(name))
}

// restEvent converts a route of the rest api to one of the http api, which is what koldstart
// deploys, and notes what has no equivalent there.
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
	c.todo(where, "a route of the rest api, now one of the http api: request validation, api keys and usage plans have no equivalent")
	w.comment("TODO: this was a route of the rest api (http); it is one of the http api (httpApi) now.")
	w.open("new kdst.HttpRoute")
	w.line("method = %s", quote(method))
	w.line("path = %s", quote(path))
	w.close()
}

func (c *converter) scheduleEvent(w *writer, where string, n *yaml.Node) {
	rates := []*yaml.Node{n}
	if n.Kind == yaml.MappingNode {
		rates = listOf(orEmpty(get(n, "rate"))).Content
	}
	for _, rate := range rates {
		w.open("new kdst.Schedule")
		c.assign(w, where+".rate", "expression", c.scalar(rate), rate)
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
		w.close()
	}
}

// source is the arn an event reads from: given as is, or as an intrinsic.
func (c *converter) source(w *writer, where, key string, n *yaml.Node) {
	arn := n
	if n.Kind == yaml.MappingNode {
		if name, _ := intrinsicOf(n); name == "" {
			arn = orEmpty(get(n, "arn"))
		}
	}
	if v, ok := c.intrinsic(arn); ok {
		c.assign(w, where+".arn", key, v, arn)
		return
	}
	c.assign(w, where+".arn", key, c.scalar(arn), arn)
}

// batching writes what a queue and a stream share: batch size, window, partial failures, filters.
func (c *converter) batching(w *writer, where string, n *yaml.Node, window string) {
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
	for _, key := range []string{"maximumConcurrency", "parallelizationFactor", "bisectBatchOnFunctionError", "maximumRetryAttempts", "destinations", "tumblingWindowInSeconds"} {
		if get(n, key) != nil {
			c.todo(where+"."+key, key+" has no equivalent in koldstart yet")
		}
	}
}

func (c *converter) queueEvent(w *writer, where string, n *yaml.Node) {
	w.open("new kdst.Queue")
	c.source(w, where, "queue", n)
	c.batching(w, where, n, "maximumBatchingWindow")
	w.close()
}

func (c *converter) streamEvent(w *writer, where string, n *yaml.Node) {
	w.open("new kdst.Stream")
	c.source(w, where, "stream", n)
	switch position := str(get(n, "startingPosition")); position {
	case "", "LATEST":
	case "TRIM_HORIZON":
		w.line(`startingPosition = "TRIM_HORIZON"`)
	default:
		c.todo(where+".startingPosition", position+" has no equivalent: the stream starts at LATEST")
	}
	c.batching(w, where, n, "batchWindow")
	w.close()
}

// topicEvent subscribes to a topic by its arn, or to one the serverless framework made by its
// name, which becomes a resource.
func (c *converter) topicEvent(w *writer, where string, n *yaml.Node) {
	w.open("new kdst.Topic")
	switch {
	case n.Kind == yaml.ScalarNode && !strings.HasPrefix(n.Value, "arn:") && !strings.Contains(n.Value, "${"):
		w.line(`topic = kdst.ref(%s, "TopicArn")`, quote(c.add(n.Value, "AWS::SNS::Topic", "TopicName")))
	case n.Kind == yaml.MappingNode && get(n, "arn") == nil && get(n, "topicName") != nil:
		w.line(`topic = kdst.ref(%s, "TopicArn")`, quote(c.add(str(get(n, "topicName")), "AWS::SNS::Topic", "TopicName")))
	default:
		c.source(w, where, "topic", n)
	}
	if policy := get(n, "filterPolicy"); policy != nil {
		if js, err := jsonString(policy); err == nil {
			w.line("filterPolicy = %s", js)
		}
	}
	w.close()
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
	w.open("new kdst.Bucket")
	switch {
	case existing || strings.Contains(bucket, "${"):
		c.assign(w, where+".bucket", "bucket", c.text(bucket), n)
	default:
		w.line(`bucket = kdst.ref(%s, "BucketName")`, quote(c.add(bucket, "AWS::S3::Bucket", "BucketName")))
	}
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
	w.close()
}

var resourceName = regexp.MustCompile(`[^A-Za-z0-9-]+`)

// add records a resource the serverless framework makes without declaring it, once, and returns
// its name in resources.
func (c *converter) add(physical, typ, property string) string {
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
	c.added = append(c.added, addedResource{name: name, typ: typ, property: property, value: physical})
	return name
}
