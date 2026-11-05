// Package desired turns a configuration into the nodes the engine applies: for each function
// its log group, role, the function and its url, and every resource.
package desired

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/kontrolplane/koldstart/internal/build"
	"github.com/kontrolplane/koldstart/internal/config"
	"github.com/kontrolplane/koldstart/internal/engine"
	"github.com/kontrolplane/koldstart/internal/provider/awsp"
)

// The inputs several kinds of node have.
const (
	inputName     = "name"
	inputFunction = "function"
	inputAPI      = "api"
)

// Target is where the nodes are deployed.
type Target struct {
	Account string
	Region  string
	// Bucket is the state bucket, which holds the zips.
	Bucket string
	// Archives are the built functions, by name.
	Archives map[string]build.Archive
}

// The ids of a function's nodes, and of a resource's.
func FunctionID(fn string) string { return "function/" + fn }
func RoleID(fn string) string     { return "role/" + fn }
func LogGroupID(fn string) string { return "loggroup/" + fn }
func URLID(fn string) string      { return "url/" + fn }
func ResourceID(r string) string  { return "resource/" + r }

// Name joins parts with dashes into a name of at most max characters. A longer one is cut, with
// a hash of the whole after it, so two long names stay apart.
func Name(max int, parts ...string) string {
	full := strings.Join(parts, "-")
	if len(full) <= max {
		return full
	}
	sum := sha256.Sum256([]byte(full))
	return strings.TrimRight(full[:max-9], "-") + "-" + hex.EncodeToString(sum[:])[:8]
}

// FunctionName is the name of a function in lambda.
func FunctionName(p *config.Project, fn string) string {
	return Name(64, p.App, p.Stage, fn)
}

// RoleName is the name of a function's role. Roles are global and states are per region, so
// the region is in the name.
func RoleName(p *config.Project, region, fn string) string {
	return Name(64, p.App, p.Stage, fn, region)
}

// LogGroupName is the log group lambda writes a function's logs to.
func LogGroupName(p *config.Project, fn string) string {
	return "/aws/lambda/" + FunctionName(p, fn)
}

// Build returns the nodes for p deployed to t.
func Build(p *config.Project, t Target) ([]engine.Node, error) {
	b := builder{p: p, t: t}
	var nodes []engine.Node
	for _, name := range slices.Sorted(maps.Keys(p.Functions)) {
		fnNodes, err := b.function(name, p.Functions[name])
		if err != nil {
			b.errs = append(b.errs, fmt.Errorf("functions[%s]: %w", name, err))
			continue
		}
		nodes = append(nodes, fnNodes...)
	}
	nodes = append(nodes, b.httpAPI()...)
	for _, name := range slices.Sorted(maps.Keys(p.Functions)) {
		nodes = append(nodes, b.events(name, p.Functions[name])...)
	}
	nodes = append(nodes, b.notifications()...)
	for _, name := range slices.Sorted(maps.Keys(p.Resources)) {
		nodes = append(nodes, b.resource(name, p.Resources[name]))
	}
	return nodes, errors.Join(b.errs...)
}

type builder struct {
	p    *config.Project
	t    Target
	errs []error
}

// nodeOf is the id of the node a name in the configuration stands for.
func (b *builder) nodeOf(name string) string {
	if _, ok := b.p.Functions[name]; ok {
		return FunctionID(name)
	}
	return ResourceID(name)
}

// value turns a configuration value into an input.
func (b *builder) value(v config.Value) any {
	switch {
	case v.Ref != nil:
		return engine.Ref(b.nodeOf(v.Ref.Resource), v.Ref.Attribute)
	case v.Secret != nil:
		return engine.SecretValue(*v.Secret)
	case v.Output != nil:
		return engine.StageOutput(v.Output.App, v.Output.Stage, v.Output.Output)
	default:
		return v.Literal
	}
}

func (b *builder) values(vs []config.Value) []any {
	out := make([]any, 0, len(vs))
	for _, v := range vs {
		out = append(out, b.value(v))
	}
	return out
}

func (b *builder) function(name string, fn config.Function) ([]engine.Node, error) {
	fnName := FunctionName(b.p, name)
	logGroup := engine.Node{
		ID: LogGroupID(name), Type: awsp.TypeLogGroup, Name: LogGroupName(b.p, name),
		Inputs: map[string]any{inputName: LogGroupName(b.p, name), "retention": fn.LogRetention},
	}

	policy, managed, env, err := b.permissions(name, fn)
	if err != nil {
		return nil, err
	}
	role := engine.Node{
		ID: RoleID(name), Type: awsp.TypeRole, Name: RoleName(b.p, b.t.Region, name),
		Inputs: map[string]any{
			inputName:          RoleName(b.p, b.t.Region, name),
			"assumeRolePolicy": assumeRolePolicy,
			"policy":           policy,
			"managedPolicies":  managed,
		},
	}

	for _, key := range slices.Sorted(maps.Keys(fn.Environment)) {
		v := fn.Environment[key]
		env[key] = b.value(v)
		if s := v.Secret; s != nil && s.AtRuntime && s.Key != "" {
			if _, set := fn.Environment[key+"_KEY"]; !set {
				env[key+"_KEY"] = s.Key
			}
		}
	}
	if fn.IsNode() && fn.Bundle.Sourcemap {
		env["NODE_OPTIONS"] = "--enable-source-maps"
	}

	archive, ok := b.t.Archives[name]
	if !ok {
		return nil, errors.New("it was not built")
	}
	handler := fn.Handler
	if fn.IsGo() {
		handler = "bootstrap"
	}
	inputs := map[string]any{
		inputName:             fnName,
		"runtime":             fn.Runtime,
		"handler":             handler,
		"memory":              fn.Memory,
		"timeout":             fn.Timeout,
		"architecture":        fn.Architecture,
		"ephemeralStorage":    fn.EphemeralStorage,
		"reservedConcurrency": fn.ReservedConcurrency,
		"environment":         env,
		"role":                engine.Ref(RoleID(name), "Arn"),
		"logGroup":            LogGroupName(b.p, name),
		"code": map[string]any{
			"bucket": b.t.Bucket,
			"key":    ZipKey(b.p, archive.SHA256),
			"sha256": archive.SHA256,
		},
		"vpc": nil,
	}
	if fn.VPC != nil {
		inputs["vpc"] = map[string]any{"subnetIds": b.values(fn.VPC.SubnetIDs), "securityGroupIds": b.values(fn.VPC.SecurityGroupIDs)}
	}
	function := engine.Node{ID: FunctionID(name), Type: awsp.TypeFunction, Name: fnName, Inputs: inputs, After: []string{LogGroupID(name)}}

	nodes := []engine.Node{logGroup, role, function}
	if fn.URL != nil {
		auth := "AWS_IAM"
		if fn.URL.Auth == "none" {
			auth = "NONE"
		}
		nodes = append(nodes, engine.Node{
			ID: URLID(name), Type: awsp.TypeURL, Name: fnName,
			Inputs: map[string]any{inputFunction: engine.Ref(FunctionID(name), "FunctionName"), "auth": auth, "cors": urlCORS(fn.URL.CORS)},
		})
	}
	return nodes, nil
}

// ZipKey is where a function's zip goes in the state bucket.
func ZipKey(p *config.Project, sha string) string {
	return "zips/" + p.App + "/" + p.Stage + "/" + sha + ".zip"
}

func urlCORS(c *config.CORS) any {
	if c == nil {
		return nil
	}
	return map[string]any{
		"origins": c.Origins, "methods": c.Methods, "headers": c.Headers,
		"expose": c.Expose, "credentials": c.Credentials, "maxAge": c.MaxAge,
	}
}

var assumeRolePolicy = trust("lambda.amazonaws.com", nil)

// resource is a node for anything in resources, applied through cloud control.
func (b *builder) resource(name string, r config.Resource) engine.Node {
	props := b.properties(r.Properties)
	policy := r.DeletionPolicy
	if policy == "" && Stateful(r.Type) {
		policy = engine.Retain
	}
	return engine.Node{
		ID: ResourceID(name), Type: r.Type, DeletionPolicy: policy,
		Inputs: map[string]any{"type": r.Type, "properties": props},
	}
}

// properties turns the refs inside resource properties into refs to nodes.
func (b *builder) properties(props map[string]any) any {
	var conv func(v any) any
	conv = func(v any) any {
		if r, ok := config.AsRef(v); ok {
			return engine.Ref(b.nodeOf(r.Resource), r.Attribute)
		}
		switch x := v.(type) {
		case map[string]any:
			out := make(map[string]any, len(x))
			for k, item := range x {
				out[k] = conv(item)
			}
			return out
		case []any:
			out := make([]any, len(x))
			for i, item := range x {
				out[i] = conv(item)
			}
			return out
		default:
			return v
		}
	}
	if props == nil {
		return map[string]any{}
	}
	return conv(props)
}

// stateful are the types that hold data, retained unless deletionPolicy says delete.
var stateful = []string{
	"AWS::DynamoDB::Table", "AWS::DynamoDB::GlobalTable", "AWS::S3::Bucket",
	"AWS::RDS::DBInstance", "AWS::RDS::DBCluster", "AWS::EFS::FileSystem",
	"AWS::Kinesis::Stream", "AWS::KMS::Key", "AWS::SecretsManager::Secret",
	"AWS::Cognito::UserPool", "AWS::ECR::Repository", "AWS::ElastiCache::ReplicationGroup",
	"AWS::OpenSearchService::Domain", "AWS::DocDB::DBCluster", "AWS::Neptune::DBCluster",
	"AWS::Redshift::Cluster", "AWS::Backup::BackupVault", "AWS::Timestream::Table",
}

// Stateful reports whether a resource type holds data, and is retained by default.
func Stateful(typ string) bool {
	return slices.Contains(stateful, typ)
}
