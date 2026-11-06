//go:build e2e

// Package e2e deploys examples/full-featured to a real aws account and works with it, for what
// no emulator gets right. Run it with go test -tags e2e ./test/e2e/..., with credentials for a
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
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	ssmtypes "github.com/aws/aws-sdk-go-v2/service/ssm/types"
	"github.com/aws/aws-sdk-go-v2/service/sts"

	"github.com/kontrolplane/koldstart/internal/state"
)

// The example the tests deploy, and the app it is.
const (
	example = "../../examples/full-featured"
	app     = "shop"
)

// stalePrefix starts the stages the tests deploy, and staleAfter is when the sweep removes one
// a run left behind.
const (
	stalePrefix = "e2e-"
	staleAfter  = 6 * time.Hour
)

// e2eConfig amends the example so a remove deletes what holds data too: an e2e stage keeps
// nothing.
const e2eConfig = `amends "koldstart.pkl"

resources {
  ["orders"] { deletionPolicy = "delete" }
  ["jobs"] { deletionPolicy = "delete" }
}
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

// TestFullFeatured deploys the example, calls its api, checks the worker stored what the api
// queued, deploys a change of code through the fast path, and removes the stage, leaving nothing.
func TestFullFeatured(t *testing.T) {
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

	status, body := request(t, http.MethodPost, api+"/orders", key, `{"item": "socks"}`)
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

	report := filepath.Join(p.dir, "src", "report.ts")
	data, err := os.ReadFile(report)
	if err != nil {
		t.Fatal(err)
	}
	write(t, report, strings.Replace(string(data), `msg: "orders so far"`, `msg: "orders until now"`, 1))
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
