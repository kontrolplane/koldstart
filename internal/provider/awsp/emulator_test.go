package awsp_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/apigatewayv2"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
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
		if _, _, err := backend.PutZip(ctx, app, "dev", arc.SHA256, data); err != nil {
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
	if _, _, err := backend.PutZip(ctx, app, "dev", arc.SHA256, data); err != nil {
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
