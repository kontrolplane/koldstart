package awsp_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/apigatewayv2"
	apigwtypes "github.com/aws/aws-sdk-go-v2/service/apigatewayv2/types"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	elb "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	elbtypes "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2/types"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	"github.com/aws/aws-sdk-go-v2/service/lambda/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/aws-sdk-go-v2/service/scheduler"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sts"

	"github.com/kontrolplane/koldstart/internal/awstest"
	"github.com/kontrolplane/koldstart/internal/build"
	"github.com/kontrolplane/koldstart/internal/config"
	"github.com/kontrolplane/koldstart/internal/desired"
	"github.com/kontrolplane/koldstart/internal/engine"
	"github.com/kontrolplane/koldstart/internal/provider/awsp"
	"github.com/kontrolplane/koldstart/internal/state"
)

// TestProvidersEmulator deploys a node function with its log group, role and a public url to the
// emulator, invokes it, changes its memory, then its code alone, and removes it all.
func TestProvidersEmulator(t *testing.T) {
	cfg := awstest.Emulator(t)
	ctx := context.Background()
	ident, err := sts.NewFromConfig(cfg).GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		t.Fatal(err)
	}
	account := *ident.Account
	backend, err := state.Open(ctx, s3.NewFromConfig(cfg, func(o *s3.Options) { o.UsePathStyle = true }), account, awstest.Region)
	if err != nil {
		t.Fatal(err)
	}
	lambdaClient := lambda.NewFromConfig(cfg)
	e := &engine.Engine{
		Providers: map[string]engine.Provider{
			awsp.TypeLogGroup: awsp.LogGroup{Client: cloudwatchlogs.NewFromConfig(cfg)},
			awsp.TypeRole:     awsp.Role{Client: iam.NewFromConfig(cfg)},
			awsp.TypeFunction: awsp.Function{Client: lambdaClient, Wait: time.Minute, RetryEvery: 200 * time.Millisecond},
			awsp.TypeURL:      awsp.URL{Client: lambdaClient},
		},
		Concurrency: 4,
	}

	root := t.TempDir()
	app := awstest.Name("orders")
	p := &config.Project{App: app, Stage: "dev", Region: awstest.Region, Functions: map[string]config.Function{
		"api": {
			Runtime: config.RuntimeNode22, Handler: "src/api.handler", Memory: 128, Timeout: 5,
			Architecture: config.ArchARM64, EphemeralStorage: 512, LogRetention: 7,
			Environment: map[string]config.Value{"GREETING": {Literal: "hello"}},
			URL:         &config.FunctionURL{Auth: "none"},
		},
	}}

	deploy := func(source string) *engine.Plan {
		t.Helper()
		if err := os.MkdirAll(filepath.Join(root, "src"), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "src", "api.ts"), []byte(source), 0o600); err != nil {
			t.Fatal(err)
		}
		arc, err := build.Builder{Root: root, Stage: "dev"}.Build(ctx, "api", p.Functions["api"])
		if err != nil {
			t.Fatal(err)
		}
		data, _ := os.ReadFile(arc.Path)
		if err := backend.PutZip(ctx, app, "dev", arc.SHA256, data); err != nil {
			t.Fatal(err)
		}
		nodes, err := desired.Build(p, desired.Target{Account: account, Region: awstest.Region, Bucket: backend.Bucket, Archives: map[string]build.Archive{"api": arc}})
		if err != nil {
			t.Fatal(err)
		}
		return apply(t, e, backend, app, nodes)
	}

	plan := deploy("export const handler = async () => `${process.env.GREETING} one`\n")
	if plan.Counts()[engine.Create] != 4 {
		t.Fatalf("first deploy: %v", plan.Counts())
	}
	name := desired.FunctionName(p, "api")
	if got := invoke(t, lambdaClient, name); got != `"hello one"` {
		t.Errorf("invoke = %s", got)
	}
	st, _, _ := backend.Read(ctx, app, "dev")
	if url := st.Resources["url/api"].Attributes["FunctionUrl"]; url == "" {
		t.Error("the url has no FunctionUrl")
	}

	fn := p.Functions["api"]
	fn.Memory = 256
	p.Functions["api"] = fn
	plan = deploy("export const handler = async () => `${process.env.GREETING} one`\n")
	if c, _ := plan.Change("function/api"); c.Action != engine.Update || strings.Join(c.Fields, ",") != "memory" {
		t.Errorf("memory change: %+v", c)
	}
	conf, _ := lambdaClient.GetFunctionConfiguration(ctx, &lambda.GetFunctionConfigurationInput{FunctionName: &name})
	if *conf.MemorySize != 256 {
		t.Errorf("memory = %d after the update", *conf.MemorySize)
	}

	plan = deploy("export const handler = async () => `${process.env.GREETING} two`\n")
	if !plan.OnlyCode() {
		t.Errorf("a code change is not only code: %+v", plan.Changes)
	}
	if got := invoke(t, lambdaClient, name); got != `"hello two"` {
		t.Errorf("invoke after the code change = %s", got)
	}

	e.Refresh = true
	refreshed(t, lambdaClient, name, func() *engine.Plan {
		return deploy("export const handler = async () => `${process.env.GREETING} two`\n")
	})
	e.Refresh = false

	plan = apply(t, e, backend, app, nil)
	if plan.Counts()[engine.Delete] != 4 {
		t.Errorf("remove: %v", plan.Counts())
	}
	if _, err := lambdaClient.GetFunction(ctx, &lambda.GetFunctionInput{FunctionName: &name}); !isNotFound(err) {
		t.Errorf("the function is still there after remove: %v", err)
	}
}

// TestEventsEmulator deploys a function with an http route, a queue, a schedule and a bucket to the
// emulator, next to a notification on the bucket koldstart did not make, calls the route, and
// removes it all, the other notification left in place.
func TestEventsEmulator(t *testing.T) {
	cfg := awstest.Emulator(t)
	ctx := context.Background()
	ident, err := sts.NewFromConfig(cfg).GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		t.Fatal(err)
	}
	account := *ident.Account
	s3Client := s3.NewFromConfig(cfg, func(o *s3.Options) { o.UsePathStyle = true })
	backend, err := state.Open(ctx, s3Client, account, awstest.Region)
	if err != nil {
		t.Fatal(err)
	}
	app := awstest.Name("events")
	_, err = sqs.NewFromConfig(cfg).CreateQueue(ctx, &sqs.CreateQueueInput{QueueName: aws.String(app + "-jobs"), Attributes: map[string]string{"VisibilityTimeout": "60"}})
	if err != nil {
		t.Fatal(err)
	}
	queueArn := "arn:aws:sqs:" + awstest.Region + ":" + account + ":" + app + "-jobs"
	bucket := app + "-uploads"
	if _, err := s3Client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: &bucket, CreateBucketConfiguration: &s3types.CreateBucketConfiguration{LocationConstraint: s3types.BucketLocationConstraint(awstest.Region)}}); err != nil {
		t.Fatal(err)
	}
	_, err = s3Client.PutBucketNotificationConfiguration(ctx, &s3.PutBucketNotificationConfigurationInput{Bucket: &bucket, NotificationConfiguration: &s3types.NotificationConfiguration{
		QueueConfigurations: []s3types.QueueConfiguration{{Id: aws.String("theirs"), QueueArn: &queueArn, Events: []s3types.Event{"s3:ObjectRemoved:*"}}},
	}})
	if err != nil {
		t.Fatal(err)
	}

	lambdaClient := lambda.NewFromConfig(cfg)
	apigw := apigatewayv2.NewFromConfig(cfg)
	e := &engine.Engine{
		Providers: map[string]engine.Provider{
			awsp.TypeLogGroup:     awsp.LogGroup{Client: cloudwatchlogs.NewFromConfig(cfg)},
			awsp.TypeRole:         awsp.Role{Client: iam.NewFromConfig(cfg)},
			awsp.TypeFunction:     awsp.Function{Client: lambdaClient, Wait: time.Minute, RetryEvery: 200 * time.Millisecond},
			awsp.TypePermission:   awsp.Permission{Client: lambdaClient},
			awsp.TypeHTTPAPI:      awsp.HTTPAPI{Client: apigw, Region: awstest.Region},
			awsp.TypeIntegration:  awsp.Integration{Client: apigw},
			awsp.TypeRoute:        awsp.Route{Client: apigw},
			awsp.TypeSchedule:     awsp.Schedule{Client: scheduler.NewFromConfig(cfg)},
			awsp.TypeMapping:      awsp.Mapping{Client: lambdaClient},
			awsp.TypeNotification: awsp.Notification{Client: s3Client, Account: account},
		},
		Concurrency: 4,
	}

	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "src"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "src", "api.ts"), []byte("export const handler = async (e) => ({ statusCode: 200, body: `hi ${e.pathParameters.name}` })\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fn := config.Function{
		Runtime: config.RuntimeNode22, Handler: "src/api.handler", Memory: 128, Timeout: 5,
		Architecture: config.ArchARM64, EphemeralStorage: 512, LogRetention: 7,
		Events: []config.Event{
			{Type: config.EventHTTPRoute, Method: "GET", Path: "/hi/{name}"},
			{Type: config.EventQueue, Queue: config.Value{Literal: queueArn}, BatchSize: 5},
			{Type: config.EventSchedule, Expression: "rate(1 hour)", Timezone: "UTC"},
			{Type: config.EventBucket, Bucket: config.Value{Literal: bucket}, Prefix: "in/"},
		},
	}
	p := &config.Project{App: app, Stage: "dev", Region: awstest.Region, Functions: map[string]config.Function{"api": fn}}
	arc, err := build.Builder{Root: root, Stage: "dev"}.Build(ctx, "api", fn)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(arc.Path)
	if err := backend.PutZip(ctx, app, "dev", arc.SHA256, data); err != nil {
		t.Fatal(err)
	}
	nodes, err := desired.Build(p, desired.Target{Account: account, Region: awstest.Region, Bucket: backend.Bucket, Archives: map[string]build.Archive{"api": arc}})
	if err != nil {
		t.Fatal(err)
	}
	apply(t, e, backend, app, nodes)

	st, _, _ := backend.Read(ctx, app, "dev")
	resp, err := awstest.Get(t, st.Resources[desired.HTTPAPIID].Attributes["ApiEndpoint"]+"/hi/levi")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(body) != "hi levi" {
		t.Errorf("GET /hi/levi = %d %q", resp.StatusCode, body)
	}
	mappings, err := lambdaClient.ListEventSourceMappings(ctx, &lambda.ListEventSourceMappingsInput{FunctionName: aws.String(desired.FunctionName(p, "api"))})
	if err != nil || len(mappings.EventSourceMappings) != 1 || *mappings.EventSourceMappings[0].BatchSize != 5 {
		t.Errorf("mappings = %+v, %v", mappings, err)
	}
	notifications := func() *s3.GetBucketNotificationConfigurationOutput {
		out, err := s3Client.GetBucketNotificationConfiguration(ctx, &s3.GetBucketNotificationConfigurationInput{Bucket: &bucket})
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	if n := notifications(); len(n.QueueConfigurations) != 1 || len(n.LambdaFunctionConfigurations) != 1 {
		t.Errorf("after deploy the bucket notifies %d queues and %d functions, want 1 and 1", len(n.QueueConfigurations), len(n.LambdaFunctionConfigurations))
	}

	apply(t, e, backend, app, nil)
	if n := notifications(); len(n.QueueConfigurations) != 1 || len(n.LambdaFunctionConfigurations) != 0 {
		t.Errorf("after remove the bucket notifies %d queues and %d functions, want 1 and 0", len(n.QueueConfigurations), len(n.LambdaFunctionConfigurations))
	}
}

// refreshed changes the memory and the environment of a deployed function outside koldstart,
// and deletes its url, then checks a deploy with refresh finds that, and only that, and sets
// it all back.
func refreshed(t *testing.T, client *lambda.Client, name string, deploy func() *engine.Plan) {
	t.Helper()
	ctx := context.Background()
	if plan := deploy(); plan.HasChanges() {
		t.Fatalf("a refresh of what was just deployed has changes: %+v", plan.Changes)
	}
	_, err := client.UpdateFunctionConfiguration(ctx, &lambda.UpdateFunctionConfigurationInput{
		FunctionName: &name, MemorySize: aws.Int32(512), Environment: &types.Environment{Variables: map[string]string{"GREETING": "changed"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.DeleteFunctionUrlConfig(ctx, &lambda.DeleteFunctionUrlConfigInput{FunctionName: &name}); err != nil {
		t.Fatal(err)
	}
	plan := deploy()
	fn, _ := plan.Change("function/api")
	url, _ := plan.Change("url/api")
	if fn.Action != engine.Update || strings.Join(fn.Drift, ",") != "environment,memory" || url.Action != engine.Create || !url.Gone {
		t.Errorf("the refreshed plan: function %s %v, url %s gone %v", fn.Action, fn.Drift, url.Action, url.Gone)
	}
	conf, err := client.GetFunctionConfiguration(ctx, &lambda.GetFunctionConfigurationInput{FunctionName: &name})
	if err != nil || aws.ToInt32(conf.MemorySize) != 256 || conf.Environment.Variables["GREETING"] != "hello" {
		t.Errorf("the function after the refreshed deploy: %+v, %v", conf, err)
	}
	if _, err := client.GetFunctionUrlConfig(ctx, &lambda.GetFunctionUrlConfigInput{FunctionName: &name}); err != nil {
		t.Errorf("the url was not created again: %v", err)
	}
	if plan := deploy(); plan.HasChanges() {
		t.Errorf("a refresh after setting it back has changes: %+v", plan.Changes)
	}
}

func apply(t *testing.T, e *engine.Engine, backend state.Backend, app string, nodes []engine.Node) *engine.Plan {
	t.Helper()
	ctx := context.Background()
	st, etag, err := backend.Read(ctx, app, "dev")
	if err != nil {
		t.Fatal(err)
	}
	if st == nil {
		st = state.New(app, "dev", awstest.Region, "")
	}
	plan, err := e.Plan(ctx, nodes, st)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if err := e.Apply(ctx, plan, nodes, engine.NewStore(backend, st, etag, "test")); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	return plan
}

func invoke(t *testing.T, client *lambda.Client, name string) string {
	t.Helper()
	out, err := client.Invoke(context.Background(), &lambda.InvokeInput{FunctionName: &name, Payload: []byte(`{}`)})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if out.FunctionError != nil {
		t.Fatalf("Invoke: %s: %s", *out.FunctionError, out.Payload)
	}
	return string(out.Payload)
}

func isNotFound(err error) bool {
	_, ok := errors.AsType[*types.ResourceNotFoundException](err)
	return ok
}

// TestLayersEmulator creates a python function with a layer, and takes the layer off it again.
func TestLayersEmulator(t *testing.T) {
	cfg := awstest.Emulator(t)
	ctx := context.Background()
	ident, err := sts.NewFromConfig(cfg).GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		t.Fatal(err)
	}
	backend, err := state.Open(ctx, s3.NewFromConfig(cfg, func(o *s3.Options) { o.UsePathStyle = true }), *ident.Account, awstest.Region)
	if err != nil {
		t.Fatal(err)
	}
	app := awstest.Name("layers")
	code, err := build.Zip([]build.File{{Name: "app.py", Data: []byte("def handler(event, context):\n    return 1\n")}})
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(code)
	sha := hex.EncodeToString(sum[:])
	if err := backend.PutZip(ctx, app, "dev", sha, code); err != nil {
		t.Fatal(err)
	}
	content, err := build.Zip([]build.File{{Name: "python/shared.py", Data: []byte("X = 1\n")}})
	if err != nil {
		t.Fatal(err)
	}
	client := lambda.NewFromConfig(cfg)
	layer, err := client.PublishLayerVersion(ctx, &lambda.PublishLayerVersionInput{LayerName: aws.String(app), Content: &types.LayerVersionContentInput{ZipFile: content}})
	if err != nil {
		t.Fatalf("PublishLayerVersion: %v", err)
	}
	p := awsp.Function{Client: client, Wait: time.Minute, RetryEvery: 200 * time.Millisecond}
	in := awsp.FunctionInputs{
		Name: app, Runtime: config.RuntimePy312, Handler: "app.handler", Role: "arn:aws:iam::" + *ident.Account + ":role/" + app,
		Memory: 128, Timeout: 5, Architecture: config.ArchARM64, EphemeralStorage: 512, LogGroup: "/aws/lambda/" + app,
		Code:   awsp.Code{Bucket: backend.Bucket, Key: state.ZipKey(app, "dev", sha), SHA256: sha},
		Layers: []string{aws.ToString(layer.LayerVersionArn)},
	}
	with, _ := json.Marshal(in)
	if _, err := p.Create(ctx, engine.Request{ID: "function/x", Inputs: with}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	t.Cleanup(func() { _ = p.Delete(context.Background(), engine.Request{PhysicalID: app}) })
	layers := func() []string {
		t.Helper()
		out, err := client.GetFunctionConfiguration(ctx, &lambda.GetFunctionConfigurationInput{FunctionName: &app})
		if err != nil {
			t.Fatal(err)
		}
		arns := make([]string, 0, len(out.Layers))
		for _, l := range out.Layers {
			arns = append(arns, aws.ToString(l.Arn))
		}
		return arns
	}
	if got := layers(); len(got) != 1 || got[0] != aws.ToString(layer.LayerVersionArn) {
		t.Fatalf("layers after create: %v", got)
	}
	in.Layers = nil
	without, _ := json.Marshal(in)
	if _, err := p.Update(ctx, engine.Request{ID: "function/x", PhysicalID: app, Inputs: without, Prior: with}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if got := layers(); len(got) != 0 {
		t.Errorf("layers after taking them off: %v", got)
	}
}

// emulatorFunction is the arn of a function the emulator does not have, which rules and target
// groups take without checking.
func emulatorFunction(name string) string {
	return "arn:aws:lambda:" + awstest.Region + ":000000000000:function:" + name
}

// TestRuleEmulator creates, changes and deletes an eventbridge rule.
func TestRuleEmulator(t *testing.T) {
	cfg := awstest.Emulator(t)
	ctx := context.Background()
	name := awstest.Name("rule")
	fn := emulatorFunction(name)

	rules := awsp.Rule{Client: eventbridge.NewFromConfig(cfg)}
	in := awsp.RuleInputs{Name: name, Bus: "default", Pattern: `{"source":["shop"]}`, Function: fn, Enabled: true}
	ruleIn, _ := json.Marshal(in)
	res, err := rules.Create(ctx, engine.Request{ID: "rule/x/0", Inputs: ruleIn})
	if err != nil || !strings.Contains(res.Attributes["Arn"], name) {
		t.Fatalf("Rule.Create = %+v, %v", res, err)
	}
	in.Enabled = false
	changed, _ := json.Marshal(in)
	if _, err := rules.Update(ctx, engine.Request{ID: "rule/x/0", PhysicalID: name, Inputs: changed, Prior: ruleIn}); err != nil {
		t.Fatalf("Rule.Update: %v", err)
	}
	if _, found, err := rules.Read(ctx, engine.Request{Inputs: changed}); err != nil || !found {
		t.Fatalf("Rule.Read = %v, %v", found, err)
	}
	if err := rules.Delete(ctx, engine.Request{PhysicalID: name, Prior: changed}); err != nil {
		t.Fatalf("Rule.Delete: %v", err)
	}
}

// TestWebSocketEmulator creates a websocket api, and a route limited on its own whose limit is
// then taken off.
func TestWebSocketEmulator(t *testing.T) {
	cfg := awstest.Emulator(t)
	ctx := context.Background()
	name := awstest.Name("ws")

	apigw := apigatewayv2.NewFromConfig(cfg)
	apis := awsp.HTTPAPI{Client: apigw, Region: awstest.Region}
	wsIn, _ := json.Marshal(awsp.HTTPAPIInputs{Name: name, Protocol: "WEBSOCKET", RouteSelection: "$request.body.action", Stage: "dev"})
	ws, err := apis.Create(ctx, engine.Request{ID: "websocket", Inputs: wsIn})
	if err != nil || ws.Attributes["Stage"] != "dev" {
		t.Fatalf("websocket api = %+v, %v", ws, err)
	}
	t.Cleanup(func() { _ = apis.Delete(context.Background(), engine.Request{PhysicalID: ws.PhysicalID}) })
	if out, err := apigw.GetApi(ctx, &apigatewayv2.GetApiInput{ApiId: &ws.PhysicalID}); err != nil || out.ProtocolType != "WEBSOCKET" {
		t.Fatalf("GetApi = %+v, %v", out, err)
	}
	routes := awsp.Route{Client: apigw}
	route := awsp.RouteInputs{API: ws.PhysicalID, RouteKey: "sendMessage", AuthorizationType: "NONE", Stage: "dev", Throttling: &awsp.Throttling{Rate: 5, Burst: 10}}
	routeIn, _ := json.Marshal(route)
	r, err := routes.Create(ctx, engine.Request{ID: "wsroute/sendMessage", Inputs: routeIn})
	if err != nil {
		t.Fatalf("Route.Create: %v", err)
	}
	settings := func() map[string]apigwtypes.RouteSettings {
		t.Helper()
		out, err := apigw.GetStage(ctx, &apigatewayv2.GetStageInput{ApiId: &ws.PhysicalID, StageName: aws.String("dev")})
		if err != nil {
			t.Fatal(err)
		}
		return out.RouteSettings
	}
	if s, ok := settings()["sendMessage"]; !ok || aws.ToInt32(s.ThrottlingBurstLimit) != 10 {
		t.Errorf("route settings after create: %+v", settings())
	}
	route.Throttling = nil
	unlimited, _ := json.Marshal(route)
	_, err = routes.Update(ctx, engine.Request{ID: "wsroute/sendMessage", PhysicalID: r.PhysicalID, Inputs: unlimited, Prior: routeIn})
	switch {
	case err != nil && strings.Contains(err.Error(), "UnknownOperation"):
		t.Log("the emulator has no DeleteRouteSettings: removing a route's limit is not checked")
	case err != nil:
		t.Fatalf("Route.Update: %v", err)
	default:
		if _, ok := settings()["sendMessage"]; ok {
			t.Errorf("the route's limit was not removed: %+v", settings())
		}
	}
}

// TestLoadBalancerEmulator creates, changes and deletes a target group, its registration and a
// listener rule on a load balancer the test makes.
func TestLoadBalancerEmulator(t *testing.T) {
	cfg := awstest.Emulator(t)
	ctx := context.Background()
	name := awstest.Name("lb")
	fn := emulatorFunction(name)

	ec := ec2.NewFromConfig(cfg)
	vpc, err := ec.CreateVpc(ctx, &ec2.CreateVpcInput{CidrBlock: aws.String("10.9.0.0/16")})
	if err != nil {
		t.Fatalf("CreateVpc: %v", err)
	}
	subnets := make([]string, 0, 2)
	for i, zone := range []string{"a", "b"} {
		sub, err := ec.CreateSubnet(ctx, &ec2.CreateSubnetInput{VpcId: vpc.Vpc.VpcId, CidrBlock: aws.String(fmt.Sprintf("10.9.%d.0/24", i)), AvailabilityZone: aws.String(awstest.Region + zone)})
		if err != nil {
			t.Fatalf("CreateSubnet: %v", err)
		}
		subnets = append(subnets, aws.ToString(sub.Subnet.SubnetId))
	}
	lbs := elb.NewFromConfig(cfg)
	lb, err := lbs.CreateLoadBalancer(ctx, &elb.CreateLoadBalancerInput{Name: aws.String(name), Subnets: subnets})
	if err != nil {
		t.Fatalf("CreateLoadBalancer: %v", err)
	}
	listener, err := lbs.CreateListener(ctx, &elb.CreateListenerInput{
		LoadBalancerArn: lb.LoadBalancers[0].LoadBalancerArn, Protocol: elbtypes.ProtocolEnumHttp, Port: aws.Int32(80),
		DefaultActions: []elbtypes.Action{{Type: elbtypes.ActionTypeEnumFixedResponse, FixedResponseConfig: &elbtypes.FixedResponseActionConfig{StatusCode: aws.String("404")}}},
	})
	if err != nil {
		t.Fatalf("CreateListener: %v", err)
	}
	t.Cleanup(func() {
		_, _ = lbs.DeleteLoadBalancer(context.Background(), &elb.DeleteLoadBalancerInput{LoadBalancerArn: lb.LoadBalancers[0].LoadBalancerArn})
	})

	groups := awsp.TargetGroup{Client: lbs}
	groupIn, _ := json.Marshal(awsp.TargetGroupInputs{Name: name})
	group, err := groups.Create(ctx, engine.Request{ID: "targetgroup/x/0", Inputs: groupIn})
	if err != nil {
		t.Fatalf("TargetGroup.Create: %v", err)
	}
	targets := awsp.Targets{Client: lbs}
	targetsIn, _ := json.Marshal(awsp.TargetsInputs{TargetGroup: group.PhysicalID, Function: fn})
	if _, err := targets.Create(ctx, engine.Request{ID: "lbtarget/x/0", Inputs: targetsIn}); err != nil {
		t.Fatalf("Targets.Create: %v", err)
	}
	if _, found, err := targets.Read(ctx, engine.Request{Inputs: targetsIn}); err != nil || !found {
		t.Errorf("Targets.Read = %v, %v", found, err)
	}
	listenerRules := awsp.ListenerRule{Client: lbs}
	ruleInputs := awsp.ListenerRuleInputs{Listener: aws.ToString(listener.Listeners[0].ListenerArn), Priority: 10, TargetGroup: group.PhysicalID, Paths: []string{"/orders/*"}}
	lrIn, _ := json.Marshal(ruleInputs)
	lr, err := listenerRules.Create(ctx, engine.Request{ID: "lbrule/x/0", Inputs: lrIn})
	if err != nil {
		t.Fatalf("ListenerRule.Create: %v", err)
	}
	ruleInputs.Priority, ruleInputs.Paths = 20, []string{"/carts/*"}
	moved, _ := json.Marshal(ruleInputs)
	if _, err := listenerRules.Update(ctx, engine.Request{ID: "lbrule/x/0", PhysicalID: lr.PhysicalID, Inputs: moved, Prior: lrIn}); err != nil {
		t.Fatalf("ListenerRule.Update: %v", err)
	}
	described, err := lbs.DescribeRules(ctx, &elb.DescribeRulesInput{RuleArns: []string{lr.PhysicalID}})
	if err != nil || len(described.Rules) != 1 || aws.ToString(described.Rules[0].Priority) != "20" {
		t.Errorf("the rule after update: %+v, %v", described, err)
	}
	if err := listenerRules.Delete(ctx, engine.Request{PhysicalID: lr.PhysicalID}); err != nil {
		t.Fatalf("ListenerRule.Delete: %v", err)
	}
	if err := targets.Delete(ctx, engine.Request{Prior: targetsIn}); err != nil {
		t.Fatalf("Targets.Delete: %v", err)
	}
	if err := groups.Delete(ctx, engine.Request{PhysicalID: group.PhysicalID}); err != nil {
		t.Fatalf("TargetGroup.Delete: %v", err)
	}
}

// TestSettingsEmulator deploys a function that traces, logs json, has tags of its own, a dead
// letter queue and async settings, checks lambda has them, and takes them off again.
func TestSettingsEmulator(t *testing.T) {
	cfg := awstest.Emulator(t)
	ctx := context.Background()
	ident, err := sts.NewFromConfig(cfg).GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		t.Fatal(err)
	}
	account := *ident.Account
	backend, err := state.Open(ctx, s3.NewFromConfig(cfg, func(o *s3.Options) { o.UsePathStyle = true }), account, awstest.Region)
	if err != nil {
		t.Fatal(err)
	}
	lambdaClient := lambda.NewFromConfig(cfg)
	e := &engine.Engine{
		Providers: map[string]engine.Provider{
			awsp.TypeLogGroup: awsp.LogGroup{Client: cloudwatchlogs.NewFromConfig(cfg)},
			awsp.TypeRole:     awsp.Role{Client: iam.NewFromConfig(cfg)},
			awsp.TypeFunction: awsp.Function{Client: lambdaClient, Wait: time.Minute, RetryEvery: 200 * time.Millisecond},
		},
		Concurrency: 4,
	}
	app := awstest.Name("settings")
	queue, err := sqs.NewFromConfig(cfg).CreateQueue(ctx, &sqs.CreateQueueInput{QueueName: aws.String(app + "-dead")})
	if err != nil {
		t.Fatal(err)
	}
	dead := "arn:aws:sqs:" + awstest.Region + ":" + account + ":" + app + "-dead"
	t.Cleanup(func() {
		_, _ = sqs.NewFromConfig(cfg).DeleteQueue(ctx, &sqs.DeleteQueueInput{QueueUrl: queue.QueueUrl})
	})

	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "src"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "src", "api.ts"), []byte("export const handler = async () => 'ok'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	retries := 0
	plain := config.Function{Runtime: config.RuntimeNode22, Handler: "src/api.handler", Memory: 128, Timeout: 5, Architecture: config.ArchARM64, EphemeralStorage: 512, LogRetention: 7}
	set := plain
	set.Tracing, set.Tags, set.LogFormat, set.ApplicationLogLevel = true, map[string]string{"tier": "web"}, config.LogFormatJSON, "WARN"
	set.DeadLetter = config.Value{Literal: dead}
	set.Async = &config.Async{MaximumRetryAttempts: &retries, OnFailure: config.Value{Literal: dead}}
	p := &config.Project{App: app, Stage: "dev", Region: awstest.Region, Functions: map[string]config.Function{"api": set}}
	deploy := func() {
		t.Helper()
		arc, err := build.Builder{Root: root, Stage: "dev"}.Build(ctx, "api", p.Functions["api"])
		if err != nil {
			t.Fatal(err)
		}
		data, _ := os.ReadFile(arc.Path)
		if err := backend.PutZip(ctx, app, "dev", arc.SHA256, data); err != nil {
			t.Fatal(err)
		}
		nodes, err := desired.Build(p, desired.Target{Account: account, Region: awstest.Region, Bucket: backend.Bucket, Archives: map[string]build.Archive{"api": arc}})
		if err != nil {
			t.Fatal(err)
		}
		apply(t, e, backend, app, nodes)
	}

	deploy()
	name := desired.FunctionName(p, "api")
	hasSettings(t, lambdaClient, name, app, dead)

	p.Functions["api"] = plain
	deploy()
	fn, err := lambdaClient.GetFunction(ctx, &lambda.GetFunctionInput{FunctionName: &name})
	if err != nil {
		t.Fatal(err)
	}
	if c := fn.Configuration; (c.TracingConfig != nil && c.TracingConfig.Mode == types.TracingModeActive) ||
		(c.DeadLetterConfig != nil && aws.ToString(c.DeadLetterConfig.TargetArn) != "") ||
		(c.LoggingConfig != nil && c.LoggingConfig.LogFormat == types.LogFormatJson) {
		t.Errorf("the settings are still there: tracing %+v, dead letter %+v, logging %+v", c.TracingConfig, c.DeadLetterConfig, c.LoggingConfig)
	}
	if _, err := lambdaClient.GetFunctionEventInvokeConfig(ctx, &lambda.GetFunctionEventInvokeConfigInput{FunctionName: &name}); !isNotFound(err) {
		t.Errorf("the async settings are still there: %v", err)
	}
	apply(t, e, backend, app, nil)
}

// hasSettings checks lambda has the settings TestSettingsEmulator deploys: tracing, json logs,
// the dead letter queue, the function's tags and its async settings.
func hasSettings(t *testing.T, lambdaClient *lambda.Client, name, app, dead string) {
	t.Helper()
	ctx := context.Background()
	fn, err := lambdaClient.GetFunction(ctx, &lambda.GetFunctionInput{FunctionName: &name})
	if err != nil {
		t.Fatal(err)
	}
	c := fn.Configuration
	if c.TracingConfig == nil || c.TracingConfig.Mode != types.TracingModeActive {
		t.Errorf("tracing = %+v", c.TracingConfig)
	}
	if c.LoggingConfig == nil || c.LoggingConfig.LogFormat != types.LogFormatJson || c.LoggingConfig.ApplicationLogLevel != types.ApplicationLogLevelWarn {
		t.Errorf("logging = %+v", c.LoggingConfig)
	}
	if c.DeadLetterConfig == nil || aws.ToString(c.DeadLetterConfig.TargetArn) != dead {
		t.Errorf("dead letter = %+v", c.DeadLetterConfig)
	}
	if fn.Tags["tier"] != "web" || fn.Tags[engine.TagApp] != app {
		t.Errorf("tags = %v", fn.Tags)
	}
	async, err := lambdaClient.GetFunctionEventInvokeConfig(ctx, &lambda.GetFunctionEventInvokeConfigInput{FunctionName: &name})
	if err != nil || aws.ToInt32(async.MaximumRetryAttempts) != 0 || async.DestinationConfig == nil || aws.ToString(async.DestinationConfig.OnFailure.Destination) != dead {
		t.Errorf("async = %+v, %v", async, err)
	}
}

// TestLogSubscriptionEmulator subscribes a function to a log group, finds the subscription, puts
// it again with another filter, and deletes it.
func TestLogSubscriptionEmulator(t *testing.T) {
	cfg := awstest.Emulator(t)
	ctx := context.Background()
	logs := cloudwatchlogs.NewFromConfig(cfg)
	name := awstest.Name("logsub")
	group := "/koldstart/" + name
	if _, err := logs.CreateLogGroup(ctx, &cloudwatchlogs.CreateLogGroupInput{LogGroupName: &group}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = logs.DeleteLogGroup(ctx, &cloudwatchlogs.DeleteLogGroupInput{LogGroupName: &group}) })
	subs := awsp.LogSubscription{Client: logs, Wait: 10 * time.Second}
	in := awsp.LogSubscriptionInputs{Name: name, LogGroup: group, Pattern: "", Function: emulatorFunction(name)}
	data, _ := json.Marshal(in)
	if _, err := subs.Create(ctx, engine.Request{Inputs: data}); err != nil {
		t.Fatalf("LogSubscription.Create: %v", err)
	}
	if _, found, err := subs.Read(ctx, engine.Request{Inputs: data}); err != nil || !found {
		t.Fatalf("LogSubscription.Read = %v, %v", found, err)
	}
	in.Pattern = "ERROR"
	changed, _ := json.Marshal(in)
	if _, err := subs.Update(ctx, engine.Request{PhysicalID: name, Inputs: changed, Prior: data}); err != nil {
		t.Fatalf("LogSubscription.Update: %v", err)
	}
	out, err := logs.DescribeSubscriptionFilters(ctx, &cloudwatchlogs.DescribeSubscriptionFiltersInput{LogGroupName: &group})
	if err != nil || len(out.SubscriptionFilters) != 1 || aws.ToString(out.SubscriptionFilters[0].FilterPattern) != "ERROR" {
		t.Errorf("filters = %+v, %v", out, err)
	}
	if err := subs.Delete(ctx, engine.Request{PhysicalID: name, Prior: changed}); err != nil {
		t.Fatalf("LogSubscription.Delete: %v", err)
	}
	if _, found, err := subs.Read(ctx, engine.Request{Inputs: changed}); err != nil || found {
		t.Errorf("after delete, Read = %v, %v", found, err)
	}
}
