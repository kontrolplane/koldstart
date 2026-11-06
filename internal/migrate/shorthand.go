package migrate

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"
)

// The types kdst.pkl has a shorthand for.
const (
	typeTable  = "AWS::DynamoDB::Table"
	typeQueue  = "AWS::SQS::Queue"
	typeBucket = "AWS::S3::Bucket"
	typeTopic  = "AWS::SNS::Topic"
)

// short is a resource written as a shorthand of kdst.pkl: the call, kdst.table("id"), and what
// amends it besides its properties, such as sortKey = "createdAt". No helper is the full form.
type short struct {
	helper string
	amends []string
}

// queueDefaults are the properties kdst.queue() sets, and bucketDefaults kdst.bucket()'s.
var (
	queueDefaults  = map[string]any{"MessageRetentionPeriod": 1209600}
	bucketDefaults = map[string]any{
		"PublicAccessBlockConfiguration": map[string]any{"BlockPublicAcls": true, "BlockPublicPolicy": true, "IgnorePublicAcls": true, "RestrictPublicBuckets": true},
		"BucketEncryption": map[string]any{"ServerSideEncryptionConfiguration": []any{
			map[string]any{"ServerSideEncryptionByDefault": map[string]any{"SSEAlgorithm": "AES256"}},
		}},
	}
)

// shorthand returns the shorthand of kdst.pkl a resource of typ with props is, and the properties
// it leaves to an amend, when props has each property the shorthand sets exactly as it sets it.
// Otherwise it returns the full form and props as they are.
func shorthand(typ string, props *yaml.Node) (short, *yaml.Node) {
	switch typ {
	case typeTopic:
		return short{helper: "kdst.topic()"}, props
	case typeQueue:
		if rest, ok := without(props, queueDefaults); ok {
			return short{helper: "kdst.queue()"}, rest
		}
	case typeBucket:
		if rest, ok := without(props, bucketDefaults); ok {
			return short{helper: "kdst.bucket()"}, rest
		}
	case typeTable:
		if s, rest, ok := table(props); ok {
			return s, rest
		}
	}
	return short{}, props
}

// keyTypes are the types of a table's keys, S the one kdst.table() gives without an amend.
var keyTypes = []string{"S", "N", "B"}

// table is the kdst.table() a table's props are: on-demand, with point-in-time recovery, and its
// keys alone in AttributeDefinitions, in the order of KeySchema.
func table(props *yaml.Node) (short, *yaml.Node, bool) {
	schema, ok := records(get(props, "KeySchema"))
	if !ok || len(schema) == 0 || len(schema) > 2 {
		return short{}, nil, false
	}
	definitions, ok := records(get(props, "AttributeDefinitions"))
	if !ok || len(definitions) != len(schema) {
		return short{}, nil, false
	}
	names, types := make([]string, len(schema)), make([]string, len(schema))
	for i, roles := range []string{"HASH", "RANGE"}[:len(schema)] {
		name, _ := schema[i]["AttributeName"].(string)
		if len(schema[i]) != 2 || name == "" || schema[i]["KeyType"] != roles {
			return short{}, nil, false
		}
		typ, _ := definitions[i]["AttributeType"].(string)
		if len(definitions[i]) != 2 || definitions[i]["AttributeName"] != name || !slices.Contains(keyTypes, typ) {
			return short{}, nil, false
		}
		names[i], types[i] = name, typ
	}
	rest, ok := without(props, map[string]any{
		"BillingMode":                      "PAY_PER_REQUEST",
		"PointInTimeRecoverySpecification": map[string]any{"PointInTimeRecoveryEnabled": true},
		"KeySchema":                        schema,
		"AttributeDefinitions":             definitions,
	})
	if !ok {
		return short{}, nil, false
	}
	s := short{helper: fmt.Sprintf("kdst.table(%s)", quote(names[0]))}
	if types[0] != "S" {
		s.amends = append(s.amends, fmt.Sprintf("partitionKeyType = %s", quote(types[0])))
	}
	if len(names) == 2 {
		s.amends = append(s.amends, fmt.Sprintf("sortKey = %s", quote(names[1])))
		if types[1] != "S" {
			s.amends = append(s.amends, fmt.Sprintf("sortKeyType = %s", quote(types[1])))
		}
	}
	return s, rest, true
}

// records decodes a list of objects written out in plain yaml, or is false.
func records(n *yaml.Node) ([]map[string]any, bool) {
	if n == nil || !plain(n) {
		return nil, false
	}
	var out []map[string]any
	if err := n.Decode(&out); err != nil {
		return nil, false
	}
	return out, true
}

// without returns props without the properties of want, when props has each of them with the
// value want gives it, or is false.
func without(props *yaml.Node, want map[string]any) (*yaml.Node, bool) {
	if props == nil || props.Kind != yaml.MappingNode {
		return nil, false
	}
	rest := &yaml.Node{Kind: yaml.MappingNode, Tag: props.Tag}
	found := 0
	for i := 0; i+1 < len(props.Content); i += 2 {
		key, value := props.Content[i], props.Content[i+1]
		w, ok := want[key.Value]
		if !ok {
			rest.Content = append(rest.Content, key, value)
			continue
		}
		if !plain(value) || !same(value, w) {
			return nil, false
		}
		found++
	}
	return rest, found == len(want)
}

// same reports whether a yaml value is want, compared as json.
func same(n *yaml.Node, want any) bool {
	var got any
	if err := n.Decode(&got); err != nil {
		return false
	}
	a, errA := json.Marshal(got)
	b, errB := json.Marshal(want)
	return errA == nil && errB == nil && string(a) == string(b)
}

// plain reports whether a yaml value is written out: no intrinsic tag, alias or serverless
// variable in it.
func plain(n *yaml.Node) bool {
	if n.Kind == yaml.AliasNode || (strings.HasPrefix(n.Tag, "!") && !strings.HasPrefix(n.Tag, "!!")) || strings.Contains(n.Value, "${") {
		return false
	}
	for _, child := range n.Content {
		if !plain(child) {
			return false
		}
	}
	return true
}
