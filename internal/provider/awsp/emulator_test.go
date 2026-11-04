package awsp_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	"github.com/aws/aws-sdk-go-v2/service/lambda/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
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
