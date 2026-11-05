// Package desired turns a configuration into the nodes the engine applies: for each function its
// log group, role, the function and its url; the http api and the nodes of every event; and
// every resource.
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
	"github.com/kontrolplane/koldstart/internal/state"
)

// The inputs several kinds of node have.
const (
	inputName     = "name"
	inputFunction = "function"
	inputAPI      = "api"
	inputCORS     = "cors"
	authNone      = "NONE"
)

// Target is where the nodes are deployed.
type Target struct {
	Account string
	Region  string
	// Bucket is the state bucket, which holds the zips.
	Bucket string
	// Archives are the built functions, by name.
	Archives map[string]build.Archive
	// ImageRegistry is the ecr registry of the account and region, such as
	// 123456789012.dkr.ecr.eu-west-1.amazonaws.com, which container images are pushed to.
	ImageRegistry string
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

// roleName is the name of a function's role. Roles are global and states are per region, so
// the region is in the name.
func roleName(p *config.Project, region, fn string) string {
	return Name(64, p.App, p.Stage, fn, region)
}

// logGroupName is the log group lambda writes a function's logs to.
func logGroupName(p *config.Project, fn string) string {
	return "/aws/lambda/" + FunctionName(p, fn)
}

// Build returns the nodes for p deployed to t.
func Build(p *config.Project, t Target) ([]engine.Node, error) {
	b := builder{project: p, target: t}
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
	nodes = append(nodes, b.webSocketAPI()...)
	for _, name := range slices.Sorted(maps.Keys(p.Functions)) {
		nodes = append(nodes, b.events(name, p.Functions[name])...)
	}
	nodes = append(nodes, b.notifications()...)
	for _, name := range slices.Sorted(maps.Keys(p.Resources)) {
		nodes = append(nodes, b.resource(name, p.Resources[name]))
	}
	return nodes, errors.Join(b.errs...)
}

// builder builds the nodes of a project for a target, collecting what is wrong as it goes.
type builder struct {
	project *config.Project
	target  Target
	errs    []error
}

// NodeID is the id of the node a name in the configuration stands for: a function or a resource.
func NodeID(p *config.Project, name string) string {
	if _, ok := p.Functions[name]; ok {
		return FunctionID(name)
	}
	return ResourceID(name)
}

// value turns a configuration value into an input.
func (b *builder) value(v config.Value) any {
	switch {
	case v.Ref != nil:
		return engine.Ref(NodeID(b.project, v.Ref.Resource), v.Ref.Attribute)
	case v.Secret != nil:
		return engine.SecretValue(*v.Secret)
	case v.Output != nil:
		return engine.StageOutput(v.Output.App, v.Output.Stage, v.Output.Output)
	case v.Join != nil:
		return b.join(v.Join)
	case v.Pseudo != "":
		return b.pseudo(v.Pseudo)
	default:
		return v.Literal
	}
}

// join joins parts into one string once the refs among them are known, or at once when there are
// none.
func (b *builder) join(parts []config.Value) any {
	values := b.values(parts)
	var whole strings.Builder
	for _, v := range values {
		s, ok := v.(string)
		if !ok {
			return engine.Join(values...)
		}
		whole.WriteString(s)
	}
	return whole.String()
}

// pseudo is a value of the account and region the stage deploys to.
func (b *builder) pseudo(name string) string {
	switch name {
	case config.PseudoAccountID:
		return b.target.Account
	case config.PseudoPartition:
		return awsp.Partition(b.target.Region)
	case config.PseudoURLSuffix:
		if strings.HasPrefix(b.target.Region, "cn-") {
			return "amazonaws.com.cn"
		}
		return "amazonaws.com"
	}
	return ""
}

func (b *builder) values(vs []config.Value) []any {
	out := make([]any, 0, len(vs))
	for _, v := range vs {
		out = append(out, b.value(v))
	}
	return out
}

// function returns a function's nodes: its log group, its role, the function, and its url.
func (b *builder) function(name string, fn config.Function) ([]engine.Node, error) {
	archive, ok := b.target.Archives[name]
	if !ok {
		return nil, errors.New("it was not built")
	}
	role, err := b.roleNode(name, fn)
	if err != nil {
		return nil, err
	}
	env, err := b.environment(fn)
	if err != nil {
		return nil, err
	}
	nodes := []engine.Node{b.logGroupNode(name, fn), role, b.functionNode(name, fn, env, archive)}
	if fn.URL != nil {
		nodes = append(nodes, b.urlNode(name, fn.URL))
	}
	return nodes, nil
}

func (b *builder) logGroupNode(name string, fn config.Function) engine.Node {
	group := logGroupName(b.project, name)
	return engine.Node{
		ID: LogGroupID(name), Type: awsp.TypeLogGroup, Name: group,
		Inputs: map[string]any{inputName: group, "retention": fn.LogRetention},
	}
}

func (b *builder) roleNode(name string, fn config.Function) (engine.Node, error) {
	policy, managed, err := b.permissions(name, fn)
	if err != nil {
		return engine.Node{}, err
	}
	role := roleName(b.project, b.target.Region, name)
	return engine.Node{
		ID: RoleID(name), Type: awsp.TypeRole, Name: role,
		Inputs: map[string]any{
			inputName:          role,
			"assumeRolePolicy": assumeRolePolicy,
			"policy":           policy,
			"managedPolicies":  managed,
		},
	}, nil
}

// environment is a function's variables: those it sets, those link adds, the key of each field
// of a secret it reads at runtime, and source maps turned on for node when it ships them.
func (b *builder) environment(fn config.Function) (map[string]any, error) {
	env, err := b.linkEnvironment(fn)
	if err != nil {
		return nil, err
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
	b.webSocketEnvironment(fn, env)
	return env, nil
}

func (b *builder) functionNode(name string, fn config.Function, env map[string]any, archive build.Archive) engine.Node {
	handler := fn.Handler
	if fn.IsGo() {
		handler = "bootstrap"
	}
	var vpc any
	if fn.VPC != nil {
		vpc = map[string]any{"subnetIds": b.values(fn.VPC.SubnetIDs), "securityGroupIds": b.values(fn.VPC.SecurityGroupIDs)}
	}
	lambdaName := FunctionName(b.project, name)
	inputs := map[string]any{
		inputName:             lambdaName,
		"runtime":             fn.Runtime,
		"handler":             handler,
		"memory":              fn.Memory,
		"timeout":             fn.Timeout,
		"architecture":        fn.Architecture,
		"ephemeralStorage":    fn.EphemeralStorage,
		"reservedConcurrency": fn.ReservedConcurrency,
		"environment":         env,
		"role":                engine.Ref(RoleID(name), attrArn),
		"logGroup":            logGroupName(b.project, name),
		"code": map[string]any{
			"bucket": b.target.Bucket,
			"key":    state.ZipKey(b.project.App, b.project.Stage, archive.SHA256),
			"sha256": archive.SHA256,
		},
		"vpc": vpc,
	}
	if len(fn.Layers) > 0 {
		inputs["layers"] = b.values(fn.Layers)
	}
	if fn.IsImage() {
		delete(inputs, "runtime")
		delete(inputs, "handler")
		inputs["code"] = map[string]any{"imageUri": ImageURI(b.target, b.project, name, archive.SHA256), "sha256": archive.SHA256}
		if c := imageConfig(fn.Image); c != nil {
			inputs["imageConfig"] = c
		}
	}
	return engine.Node{ID: FunctionID(name), Type: awsp.TypeFunction, Name: lambdaName, After: []string{LogGroupID(name)}, Inputs: inputs}
}

// ImageURI is where a function's container image is pushed: its app's repository in the
// target's registry, tagged by what it is built from.
func ImageURI(t Target, p *config.Project, function, sha string) string {
	return t.ImageRegistry + "/" + build.ImageRepository(p.App) + ":" + build.ImageTag(p.Stage, function, sha)
}

// imageConfig is what an image runs in place of its CMD, ENTRYPOINT and WORKDIR, or nil when it
// runs what it says.
func imageConfig(img *config.Image) map[string]any {
	c := map[string]any{}
	if len(img.Command) > 0 {
		c["command"] = img.Command
	}
	if len(img.EntryPoint) > 0 {
		c["entryPoint"] = img.EntryPoint
	}
	if img.WorkingDirectory != "" {
		c["workingDirectory"] = img.WorkingDirectory
	}
	if len(c) == 0 {
		return nil
	}
	return c
}

func (b *builder) urlNode(name string, url *config.FunctionURL) engine.Node {
	auth := "AWS_IAM"
	if url.Auth == "none" {
		auth = authNone
	}
	return engine.Node{
		ID: URLID(name), Type: awsp.TypeURL, Name: FunctionName(b.project, name),
		Inputs: map[string]any{inputFunction: engine.Ref(FunctionID(name), "FunctionName"), "auth": auth, inputCORS: corsInputs(url.CORS)},
	}
}

// corsInputs are the inputs of a cross-origin configuration, of a function url or the http api.
func corsInputs(c *config.CORS) any {
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
	if policy == "" && stateful(r.Type) {
		policy = engine.PolicyRetain
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
		if val, ok := config.AsValue(v); ok {
			return b.value(val)
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

// statefulTypes are the types that hold data, retained unless deletionPolicy says delete.
var statefulTypes = []string{
	"AWS::DynamoDB::Table", "AWS::DynamoDB::GlobalTable", "AWS::S3::Bucket",
	"AWS::RDS::DBInstance", "AWS::RDS::DBCluster", "AWS::EFS::FileSystem",
	"AWS::Kinesis::Stream", "AWS::KMS::Key", "AWS::SecretsManager::Secret",
	"AWS::Cognito::UserPool", "AWS::ECR::Repository", "AWS::ElastiCache::ReplicationGroup",
	"AWS::OpenSearchService::Domain", "AWS::DocDB::DBCluster", "AWS::Neptune::DBCluster",
	"AWS::Redshift::Cluster", "AWS::Backup::BackupVault", "AWS::Timestream::Table",
}

// stateful reports whether a resource type holds data, and is retained by default.
func stateful(typ string) bool {
	return slices.Contains(statefulTypes, typ)
}
