//go:build e2e

// Package e2e deploys examples/reference-application to a real aws account and works with it, for
// what no emulator gets right. Run it with go test -tags e2e ./test/e2e/..., with credentials for a
// sandbox account: every run deploys a stage of its own, e2e-<run>, and removes it.
package e2e

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/apigateway"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	lambdatypes "github.com/aws/aws-sdk-go-v2/service/lambda/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	ssmtypes "github.com/aws/aws-sdk-go-v2/service/ssm/types"
	"github.com/aws/aws-sdk-go-v2/service/sts"

	"github.com/kontrolplane/koldstart/internal/state"
)

// The example the tests deploy, and the app it is.
const (
	example = "../../examples/reference-application"
	app     = "shop"
)

// stalePrefix starts the stages the tests deploy, and staleAfter is when the sweep removes one
// a run left behind.
const (
	stalePrefix = "e2e-"
	staleAfter  = 6 * time.Hour
)

// e2eConfig amends the example so a remove deletes what holds data too, as an e2e stage keeps
// nothing, and adds what only aws itself can check:
//
//   - audit, written by the test, subscribed to the worker's log group, found through a ref so it
//     waits for the group, with provisioned concurrency, so it has a live alias its events
//     invoke, async invocations sent to the failures queue once they fail, tracing and json logs
//   - a route of the api that takes only requests signed with sigv4
//   - legacy, written by the test, with a private route of the rest api, which takes only
//     requests with the stage's api key
//   - a limit on how many workers read the jobs queue at once
const e2eConfig = `amends "koldstart.pkl"

import "@koldstart/kdst.pkl"

functions {
  ["api"] {
    events {
      (kdst.get("/admin/stats")) { iam = true }
    }
  }
  ["worker"] {
    events = new {
      (kdst.fromQueue("jobs")) { maximumConcurrency = 2 }
    }
  }
  ["legacy"] {
    events {
      (kdst.rest("GET", "/legacy/{id}")) { private = true }
    }
  }
  ["audit"] {
    memory = 256
    provisionedConcurrency = 1
    tracing = true
    logFormat = "json"
    async = new {
      maximumRetryAttempts = 0
      onFailure = kdst.ref("failures", "Arn")
    }
    events {
      (kdst.fromLogs(kdst.join(List("/aws/lambda/", kdst.ref("worker", "FunctionName"))))) {
        filter = "stored an order"
      }
    }
  }
}

resources {
  ["orders"] { deletionPolicy = "delete" }
  ["jobs"] { deletionPolicy = "delete" }
  ["failures"] = (kdst.queue()) {
    deletionPolicy = "delete"
    properties { QueueName = "shop-\(kdst.stage)-failures" }
  }
}

outputs {
  ["failuresUrl"] = kdst.ref("failures", "QueueUrl")
}
`

// auditSource is the audit function: it counts the lines of the worker's log it is sent, and
// fails when an event asks it to, for its async failures to reach the failures queue. version
// changes what it logs, so a deploy of it publishes a new version.
func auditSource(version string) string {
	return `import { gunzipSync } from "node:zlib";

export const handler = async (event: { fail?: boolean; awslogs?: { data: string } }) => {
  if (event.fail) {
    throw new Error("asked to fail");
  }
  const lines = event.awslogs ? JSON.parse(gunzipSync(Buffer.from(event.awslogs.data, "base64")).toString()).logEvents.length : 0;
  console.log(JSON.stringify({ msg: "audited", lines, version: "` + version + `" }));
  return { lines };
};
`
}

// legacySource is the legacy function: it answers with what of the event of payload 1.0 it was
// handed, the route's resource and the path's id.
const legacySource = `export const handler = async (event: { resource: string; pathParameters: { id: string } }) => ({
  statusCode: 200,
  body: JSON.stringify({ resource: event.resource, id: event.pathParameters.id }),
});
`

// project is a copy of the example, with the kdst built from this tree to run in it.
type project struct {
	t     *testing.T
	dir   string
	kdst  string
	stage string
	cfg   aws.Config
}

// newProject builds kdst, copies the example with its node packages installed and the schema of
// this tree in place of the released package, and checks there are credentials.
func newProject(t *testing.T, stage string) *project {
	t.Helper()
	ctx := context.Background()
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		t.Fatalf("loading the aws config: %v", err)
	}
	if _, err := sts.NewFromConfig(cfg).GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{}); err != nil {
		t.Fatalf("e2e tests need credentials for a sandbox account: %v", err)
	}
	tmp := t.TempDir()
	p := &project{t: t, dir: filepath.Join(tmp, "shop"), kdst: filepath.Join(tmp, "kdst"), stage: stage, cfg: cfg}
	run(t, "../..", "go", "build", "-ldflags", "-X github.com/kontrolplane/koldstart/internal/version.buildVersion=dev", "-o", p.kdst, "./cmd/koldstart")
	copyDir(t, example, p.dir)
	run(t, p.dir, "npm", "ci", "--no-audit", "--no-fund")
	p.useLocalSchema()
	write(t, filepath.Join(p.dir, "e2e.pkl"), e2eConfig)
	write(t, filepath.Join(p.dir, "src", "audit.ts"), auditSource("1"))
	write(t, filepath.Join(p.dir, "src", "legacy.ts"), legacySource)
	return p
}

// useLocalSchema points the example's PklProject at the schema of this tree, which is what a
// release of it would package, with kdst upgrade: as a dev build, kdst depends on the schema it
// embeds, extracted into its cache.
func (p *project) useLocalSchema() {
	out := run(p.t, p.dir, p.kdst, "upgrade")
	if !strings.Contains(out, "→ dev (") {
		p.t.Fatalf("kdst upgrade did not point the example at its own schema:\n%s", out)
	}
}

// kdstRun runs kdst on the stage, and returns what it wrote. It fails the test when kdst fails.
func (p *project) kdstRun(args ...string) string {
	p.t.Helper()
	return run(p.t, p.dir, p.kdst, append(args, "--stage", p.stage, "--config", "e2e.pkl")...)
}

// kdstTry runs kdst on the stage, and logs what it wrote, whether it fails or not.
func (p *project) kdstTry(args ...string) {
	p.t.Helper()
	cmd := exec.Command(p.kdst, append(args, "--stage", p.stage, "--config", "e2e.pkl")...)
	cmd.Dir = p.dir
	out, err := cmd.CombinedOutput()
	p.t.Logf("kdst %s: %v\n%s", strings.Join(args, " "), err, out)
}

// outputs reads the stage's outputs.
func (p *project) outputs() map[string]string {
	p.t.Helper()
	out := map[string]string{}
	for line := range strings.SplitSeq(p.kdstRun("outputs"), "\n") {
		if k, v, ok := strings.Cut(strings.TrimSpace(line), " = "); ok {
			out[k] = v
		}
	}
	return out
}

// putAPIKey puts the parameter the api reads its key from, and removes it when the test ends.
func (p *project) putAPIKey() string {
	p.t.Helper()
	ctx := context.Background()
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	key := hex.EncodeToString(b)
	name := "/" + app + "/" + p.stage + "/api-key"
	client := ssm.NewFromConfig(p.cfg)
	if _, err := client.PutParameter(ctx, &ssm.PutParameterInput{Name: &name, Value: &key, Type: ssmtypes.ParameterTypeSecureString, Overwrite: aws.Bool(true)}); err != nil {
		p.t.Fatalf("putting %s: %v", name, err)
	}
	p.t.Cleanup(func() {
		_, _ = client.DeleteParameter(context.Background(), &ssm.DeleteParameterInput{Name: &name})
	})
	return key
}

// TestReferenceApplication deploys the reference application, calls its api, checks the worker
// stored what the api queued, that a route of iam refuses an unsigned request, that the private
// route of the rest api takes only requests with its key, and what audit brings: its settings, its
// alias, its subscription to the worker's log and its async failures. It deploys a change of code
// through the fast path, and removes the stage, leaving nothing.
func TestReferenceApplication(t *testing.T) {
	p := newProject(t, stageName())
	key := p.putAPIKey()
	t.Cleanup(func() {
		if t.Failed() {
			p.kdstTry("remove", "--yes")
		}
	})

	t.Log(p.kdstRun("deploy", "--yes"))
	api := p.outputs()["httpApiUrl"]
	if api == "" {
		t.Fatal("no httpApiUrl output")
	}

	status, body := request(t, http.MethodPost, api+"/orders", key, `{"customerId": "end-to-end-test", "lines": [{"sku": "socks", "quantity": 1}]}`)
	if status != http.StatusAccepted {
		t.Fatalf("POST /orders: %d %s", status, body)
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(body), &created); err != nil || created.ID == "" {
		t.Fatalf("POST /orders returned %s", body)
	}
	if status, _ := request(t, http.MethodGet, api+"/orders/"+created.ID, "wrong", ""); status != http.StatusUnauthorized {
		t.Errorf("GET with a wrong key: %d, want 401", status)
	}
	eventually(t, 2*time.Minute, "the worker to store the order", func() bool {
		status, body := request(t, http.MethodGet, api+"/orders/"+created.ID, key, "")
		return status == http.StatusOK && strings.Contains(body, "socks")
	})
	eventually(t, 2*time.Minute, "the worker's log to say so", func() bool {
		return strings.Contains(p.kdstRun("logs", "-f", "worker", "--since", "15m"), "stored an order")
	})
	if status, _ := request(t, http.MethodGet, api+"/admin/stats", key, ""); status != http.StatusForbidden {
		t.Errorf("GET /admin/stats unsigned: %d, want 403 from a route that takes only signed requests", status)
	}
	p.checkRest()
	p.checkAudit()

	report := filepath.Join(p.dir, "src", "report.ts")
	data, err := os.ReadFile(report)
	if err != nil {
		t.Fatal(err)
	}
	write(t, report, strings.Replace(string(data), `"orders so far"`, `"orders until now"`, 1))
	out := p.kdstRun("deploy", "--yes")
	if !strings.Contains(out, "only code changes") || !strings.Contains(out, "~   function/report") || strings.Count(out, "\n  ~") != 1 {
		t.Fatalf("a change of code did not take the fast path:\n%s", out)
	}
	if out := p.kdstRun("invoke", "-f", "report"); !regexp.MustCompile(`"count":\s*1`).MatchString(out) {
		t.Errorf("invoking report: %s, want a count of 1", out)
	}

	out = p.kdstRun("remove", "--yes")
	if !strings.Contains(out, "removed "+app+"/"+p.stage) || strings.Contains(out, "left in place") {
		t.Fatalf("remove:\n%s", out)
	}
	eventually(t, 3*time.Minute, "no tagged resources to be left", func() bool {
		left := tagged(t, p.cfg, p.stage)
		if len(left) > 0 {
			t.Logf("still tagged with %s: %v", p.stage, left)
		}
		return len(left) == 0
	})
}

// checkRest checks the private route of the rest api refuses a request without the stage's api
// key, and hands legacy the event of payload 1.0 of one with it, once the stage's deployment
// serves it.
func (p *project) checkRest() {
	t := p.t
	t.Helper()
	outputs := p.outputs()
	url, id := outputs["restApiUrl"], outputs["restApiKeyId"]
	if url == "" || id == "" {
		t.Fatalf("no restApiUrl or restApiKeyId output: %v", outputs)
	}
	key, err := apigateway.NewFromConfig(p.cfg).GetApiKey(context.Background(), &apigateway.GetApiKeyInput{ApiKey: &id, IncludeValue: aws.Bool(true)})
	if err != nil {
		t.Fatalf("reading the api key %s: %v", id, err)
	}
	eventually(t, 2*time.Minute, "the rest api to serve the private route", func() bool {
		status, body := request(t, http.MethodGet, url+"/legacy/7", aws.ToString(key.Value), "")
		return status == http.StatusOK && strings.Contains(body, `"resource":"/legacy/{id}"`) && strings.Contains(body, `"id":"7"`)
	})
	if status, _ := request(t, http.MethodGet, url+"/legacy/7", "", ""); status != http.StatusForbidden {
		t.Errorf("GET /legacy/7 without the key: %d, want 403", status)
	}
}

// checkAudit checks what audit brings that only aws can: its settings in lambda, its live alias
// with provisioned concurrency, the subscription to the worker's log, an async failure reaching
// the failures queue, the worker's queue read by at most two at once, and a change of its code
// publishing a version the alias moves to.
func (p *project) checkAudit() {
	t := p.t
	t.Helper()
	ctx := context.Background()
	fns := lambda.NewFromConfig(p.cfg)
	name := app + "-" + p.stage + "-audit"
	conf, err := fns.GetFunctionConfiguration(ctx, &lambda.GetFunctionConfigurationInput{FunctionName: &name})
	if err != nil {
		t.Fatalf("reading audit: %v", err)
	}
	if conf.TracingConfig == nil || conf.TracingConfig.Mode != lambdatypes.TracingModeActive {
		t.Errorf("audit's tracing = %+v, want Active", conf.TracingConfig)
	}
	if conf.LoggingConfig == nil || conf.LoggingConfig.LogFormat != lambdatypes.LogFormatJson {
		t.Errorf("audit's logging = %+v, want JSON", conf.LoggingConfig)
	}
	first := aliasVersion(t, fns, name)
	if pc, err := fns.GetProvisionedConcurrencyConfig(ctx, &lambda.GetProvisionedConcurrencyConfigInput{FunctionName: &name, Qualifier: aws.String("live")}); err != nil || aws.ToInt32(pc.RequestedProvisionedConcurrentExecutions) != 1 {
		t.Errorf("audit's provisioned concurrency = %+v, %v, want 1 on live", pc, err)
	}
	async, err := fns.GetFunctionEventInvokeConfig(ctx, &lambda.GetFunctionEventInvokeConfigInput{FunctionName: &name})
	if err != nil || aws.ToInt32(async.MaximumRetryAttempts) != 0 || async.DestinationConfig == nil || async.DestinationConfig.OnFailure == nil {
		t.Errorf("audit's async settings = %+v, %v", async, err)
	}
	mappings, err := fns.ListEventSourceMappings(ctx, &lambda.ListEventSourceMappingsInput{FunctionName: aws.String(app + "-" + p.stage + "-worker")})
	if err != nil || len(mappings.EventSourceMappings) != 1 || mappings.EventSourceMappings[0].ScalingConfig == nil || aws.ToInt32(mappings.EventSourceMappings[0].ScalingConfig.MaximumConcurrency) != 2 {
		t.Errorf("the worker's mapping = %+v, %v, want at most 2 at once", mappings, err)
	}

	eventually(t, 3*time.Minute, "audit to be sent the worker's line", func() bool {
		return strings.Contains(p.kdstRun("logs", "-f", "audit", "--since", "15m"), "audited")
	})

	if _, err := fns.Invoke(ctx, &lambda.InvokeInput{FunctionName: aws.String(name + ":live"), InvocationType: lambdatypes.InvocationTypeEvent, Payload: []byte(`{"fail": true}`)}); err != nil {
		t.Fatalf("invoking audit async: %v", err)
	}
	queues := sqs.NewFromConfig(p.cfg)
	url := p.outputs()["failuresUrl"]
	if url == "" {
		t.Fatal("no failuresUrl output")
	}
	eventually(t, 3*time.Minute, "audit's async failure to reach the failures queue", func() bool {
		out, err := queues.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{QueueUrl: &url, WaitTimeSeconds: 5})
		return err == nil && len(out.Messages) > 0 && strings.Contains(aws.ToString(out.Messages[0].Body), "asked to fail")
	})

	write(t, filepath.Join(p.dir, "src", "audit.ts"), auditSource("2"))
	p.kdstRun("deploy", "function", "-f", "audit")
	if next := aliasVersion(t, fns, name); next == first {
		t.Errorf("a change of audit's code left live at version %s", first)
	}
}

// aliasVersion is the version the live alias of function points at.
func aliasVersion(t *testing.T, fns *lambda.Client, function string) string {
	t.Helper()
	out, err := fns.GetAlias(context.Background(), &lambda.GetAliasInput{FunctionName: &function, Name: aws.String("live")})
	if err != nil {
		t.Fatalf("reading the live alias of %s: %v", function, err)
	}
	return aws.ToString(out.FunctionVersion)
}

// TestSweep removes the e2e stages that runs left behind, older than staleAfter, when
// KOLDSTART_E2E_SWEEP is set.
func TestSweep(t *testing.T) {
	if os.Getenv("KOLDSTART_E2E_SWEEP") == "" {
		t.Skip("set KOLDSTART_E2E_SWEEP to remove the stages earlier runs left")
	}
	p := newProject(t, "")
	ctx := context.Background()
	ident, err := sts.NewFromConfig(p.cfg).GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		t.Fatal(err)
	}
	bucket := state.BucketName(aws.ToString(ident.Account), p.cfg.Region)
	client := s3.NewFromConfig(p.cfg)
	prefix := app + "/" + stalePrefix
	pages := s3.NewListObjectsV2Paginator(client, &s3.ListObjectsV2Input{Bucket: &bucket, Prefix: &prefix})
	for pages.HasMorePages() {
		page, err := pages.NextPage(ctx)
		if err != nil {
			t.Skipf("no state bucket to sweep: %v", err)
		}
		for _, o := range page.Contents {
			key := aws.ToString(o.Key)
			if !strings.HasSuffix(key, "/state.json") || time.Since(aws.ToTime(o.LastModified)) < staleAfter {
				continue
			}
			p.stage = strings.TrimSuffix(strings.TrimPrefix(key, app+"/"), "/state.json")
			t.Logf("removing %s, last deployed %s", p.stage, aws.ToTime(o.LastModified))
			p.kdstTry("unlock", "--yes")
			p.kdstTry("remove", "--yes")
		}
	}
}

// stageName is the stage of this run: e2e- and the run of the ci job, or a random one.
func stageName() string {
	if id := os.Getenv("GITHUB_RUN_ID"); id != "" {
		return stalePrefix + id + "-" + os.Getenv("GITHUB_RUN_ATTEMPT")
	}
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return stalePrefix + hex.EncodeToString(b)
}

func request(t *testing.T, method, url, key, body string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("x-api-key", key)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Logf("%s %s: %v", method, url, err)
		return 0, ""
	}
	defer func() { _ = resp.Body.Close() }()
	data, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(data)
}

// eventually checks ok every five seconds until it holds, and fails the test when it has not
// within d.
func eventually(t *testing.T, d time.Duration, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("waited %s for %s", d, what)
		}
		time.Sleep(5 * time.Second)
	}
}

// tagged lists the arns of what is still tagged with the stage.
func tagged(t *testing.T, cfg aws.Config, stage string) []string {
	t.Helper()
	out := run(t, ".", "aws", "resourcegroupstaggingapi", "get-resources", "--region", cfg.Region,
		"--tag-filters", "Key=koldstart:stage,Values="+stage, "--query", "ResourceTagMappingList[].ResourceARN", "--output", "text")
	return strings.Fields(out)
}

// run runs a command in dir, and fails the test with its output when it fails.
func run(t *testing.T, dir, name string, args ...string) string {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, out.String())
	}
	return out.String()
}

func write(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}

// copyDir copies the example, without what npm and koldstart put in it.
func copyDir(t *testing.T, from, to string) {
	t.Helper()
	err := filepath.WalkDir(from, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(from, path)
		if d.IsDir() && (d.Name() == "node_modules" || rel == filepath.Join(".koldstart", "build") || rel == filepath.Join(".koldstart", "local")) {
			return filepath.SkipDir
		}
		if d.IsDir() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		write(t, filepath.Join(to, rel), string(data))
		return nil
	})
	if err != nil {
		t.Fatalf("copying the example: %v", err)
	}
}
