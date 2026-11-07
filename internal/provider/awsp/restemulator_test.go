package awsp_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/apigateway"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
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

// TestRESTEmulator deploys a function with rest routes to the emulator, one of them private, and
// calls them: the function gets the event of payload 1.0, the private route refuses a request
// without the api key and takes one with it, and a route removed by the next deploy is gone from
// the stage. The emulator lets a deployment a stage serves be deleted, which aws refuses; the
// fake of TestRESTDeploy does as aws does.
func TestRESTEmulator(t *testing.T) {
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
	rest := apigateway.NewFromConfig(cfg)
	e := &engine.Engine{
		Providers: map[string]engine.Provider{
			awsp.TypeLogGroup:       awsp.LogGroup{Client: cloudwatchlogs.NewFromConfig(cfg)},
			awsp.TypeRole:           awsp.Role{Client: iam.NewFromConfig(cfg)},
			awsp.TypeFunction:       awsp.Function{Client: lambdaClient, Wait: time.Minute, RetryEvery: 200 * time.Millisecond},
			awsp.TypePermission:     awsp.Permission{Client: lambdaClient},
			awsp.TypeRESTAPI:        awsp.RESTAPI{Client: rest, Region: awstest.Region},
			awsp.TypeRESTResource:   awsp.RESTResource{Client: rest},
			awsp.TypeRESTRoute:      awsp.RESTRoute{Client: rest},
			awsp.TypeRESTAuthorizer: awsp.RESTAuthorizer{Client: rest},
			awsp.TypeRESTStage:      awsp.RESTStage{Client: rest, Region: awstest.Region},
			awsp.TypeRESTKey:        awsp.RESTKey{Client: rest, Region: awstest.Region},
			awsp.TypeRESTUsagePlan:  awsp.RESTUsagePlan{Client: rest},
		},
		Concurrency: 4,
	}

	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "src"), 0o750); err != nil {
		t.Fatal(err)
	}
	source := "export const handler = async (e) => ({ statusCode: 200, body: JSON.stringify({ " +
		"resource: e.resource, method: e.httpMethod, id: e.pathParameters?.id ?? null, version: e.version ?? null }) })\n"
	if err := os.WriteFile(filepath.Join(root, "src", "api.ts"), []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	app := awstest.Name("rest")
	private := config.Event{Type: config.EventRESTRoute, Method: "POST", Path: "/orders", Private: true}
	fn := config.Function{
		Runtime: config.RuntimeNode22, Handler: "src/api.handler", Memory: 128, Timeout: 5,
		Architecture: config.ArchARM64, EphemeralStorage: 512, LogRetention: 7,
		Events: []config.Event{{Type: config.EventRESTRoute, Method: "GET", Path: "/orders/{id}"}, private},
	}
	arc, err := build.Builder{Root: root, Stage: "dev"}.Build(ctx, "api", fn)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(arc.Path)
	if err := backend.PutZip(ctx, app, "dev", arc.SHA256, data); err != nil {
		t.Fatal(err)
	}
	deploy := func(fn config.Function) {
		t.Helper()
		p := &config.Project{App: app, Stage: "dev", Region: awstest.Region, Functions: map[string]config.Function{"api": fn}}
		nodes, err := desired.Build(p, desired.Target{Account: account, Region: awstest.Region, Bucket: backend.Bucket, Archives: map[string]build.Archive{"api": arc}})
		if err != nil {
			t.Fatal(err)
		}
		apply(t, e, backend, app, nodes)
	}
	deploy(fn)
	st, _, _ := backend.Read(ctx, app, "dev")
	url := st.Resources[desired.RESTAPIID].Attributes["ApiEndpoint"] + "/dev"
	key, err := rest.GetApiKey(ctx, &apigateway.GetApiKeyInput{ApiKey: aws.String(st.Resources[desired.RESTKeyID].PhysicalID), IncludeValue: aws.Bool(true)})
	if err != nil {
		t.Fatal(err)
	}
	call := func(method, path, apiKey string) (int, string) {
		t.Helper()
		req, err := http.NewRequest(method, url+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		if apiKey != "" {
			req.Header.Set("x-api-key", apiKey)
		}
		resp, err := awstest.Do(t, req)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		return resp.StatusCode, string(body)
	}

	status, body := call(http.MethodGet, "/orders/7", "")
	var ev struct {
		Resource, Method, ID string
		Version              *string
	}
	if err := json.Unmarshal([]byte(body), &ev); status != http.StatusOK || err != nil || ev.Resource != "/orders/{id}" || ev.Method != "GET" || ev.ID != "7" || ev.Version != nil {
		t.Errorf("GET /orders/7 = %d %s, want the event of payload 1.0", status, body)
	}
	if status, _ := call(http.MethodPost, "/orders", ""); status != http.StatusForbidden {
		t.Errorf("POST /orders without the key = %d, want 403", status)
	}
	if status, body := call(http.MethodPost, "/orders", aws.ToString(key.Value)); status != http.StatusOK {
		t.Errorf("POST /orders with the key = %d %s", status, body)
	}

	fn.Events = fn.Events[:1]
	deploy(fn)
	if status, _ := call(http.MethodPost, "/orders", aws.ToString(key.Value)); status == http.StatusOK {
		t.Errorf("POST /orders is still served after its route was removed")
	}
	if status, _ := call(http.MethodGet, "/orders/8", ""); status != http.StatusOK {
		t.Errorf("GET /orders/8 after the next deploy = %d", status)
	}

	apply(t, e, backend, app, nil)
	if _, err := rest.GetRestApi(ctx, &apigateway.GetRestApiInput{RestApiId: aws.String(st.Resources[desired.RESTAPIID].PhysicalID)}); err == nil {
		t.Error("the rest api is still there after remove")
	}
}
