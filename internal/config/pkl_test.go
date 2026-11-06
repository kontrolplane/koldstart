package config_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kontrolplane/koldstart/internal/config"
	"github.com/kontrolplane/koldstart/internal/pkl"
)

// evaluator returns the pkl koldstart would use, to run the schema in pkl/ through the real
// binary. Outside ci it does not download one: without pkl installed, the tests skip.
func evaluator(t *testing.T) pkl.Evaluator {
	t.Helper()
	if os.Getenv("CI") == "" && os.Getenv(pkl.EnvBinary) == "" {
		if _, err := exec.LookPath("pkl"); err != nil {
			t.Skip("no pkl on the PATH")
		}
	}
	binary, err := pkl.Find(context.Background())
	if err != nil {
		t.Fatalf("finding pkl: %v", err)
	}
	return pkl.Evaluator{Binary: binary}
}

// TestSchema evaluates the example project against the schema, in its pkl project, whose
// koldstart dependency is pkl/ as a local one, for two stages, and checks the json decodes,
// unknown fields refused, into what the file says.
func TestSchema(t *testing.T) {
	ev := evaluator(t)
	path := filepath.Join("testdata", "orders", "koldstart.pkl")
	for _, stage := range []string{"dev", "prod"} {
		p, err := config.Load(context.Background(), ev, path, stage)
		if err != nil {
			t.Fatalf("Load for %s: %v", stage, err)
		}
		api := p.Functions["api"]
		wantMemory := 256
		if stage == "prod" {
			wantMemory = 1024
		}
		if api.Memory != wantMemory || api.Timeout != 10 {
			t.Errorf("%s: api memory %d, timeout %d", stage, api.Memory, api.Timeout)
		}
		if r := api.Environment["TABLE_NAME"].Ref; r == nil || r.Resource != "orders" {
			t.Errorf("%s: TABLE_NAME = %+v", stage, api.Environment["TABLE_NAME"])
		}
		if s := api.Environment["DB_PASSWORD"].Secret; s == nil || s.Key != "password" || !s.AtRuntime {
			t.Errorf("%s: DB_PASSWORD = %+v", stage, api.Environment["DB_PASSWORD"])
		}
		if o := api.Environment["PAYMENTS_URL"].Output; o == nil || o.Stage != stage {
			t.Errorf("%s: PAYMENTS_URL = %+v, want the output of payments/%s", stage, api.Environment["PAYMENTS_URL"], stage)
		}
		if len(api.Events) != 2 || api.Events[0].Type != config.EventHTTPRoute || api.Events[0].Authorizer != "users" {
			t.Errorf("%s: api events = %+v", stage, api.Events)
		}
		worker := p.Functions["worker"]
		if worker.Memory != 1024 || worker.Timeout != 6 || worker.Architecture != config.ArchARM64 || worker.LogRetention != 30 {
			t.Errorf("%s: worker defaults = %+v", stage, worker)
		}
		if q := worker.Events[0]; q.Type != config.EventQueue || q.MaximumBatchingWindow != 2 || !q.ReportBatchItemFailures {
			t.Errorf("%s: queue event = %+v", stage, q)
		}
		if p.HTTP == nil || p.HTTP.Authorizers["users"].Type != "jwt" || p.HTTP.Throttling.Burst != 200 {
			t.Errorf("%s: http = %+v", stage, p.HTTP)
		}
		redrive, _ := p.Resources["jobs"].Properties["RedrivePolicy"].(map[string]any)
		if r, ok := config.AsRef(redrive["deadLetterTargetArn"]); !ok || r.Resource != "dead" {
			t.Errorf("%s: the dead letter target = %v", stage, redrive["deadLetterTargetArn"])
		}
	}
}

// TestStageFile checks a stage's own file, which amends the main one, changes what it sets and
// keeps the rest, an output of a stage elsewhere among it.
func TestStageFile(t *testing.T) {
	ev := evaluator(t)
	p, err := config.Load(context.Background(), ev, filepath.Join("testdata", "orders", "koldstart.prod.pkl"), "prod")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if p.Region != "us-east-1" || p.Functions["api"].Memory != 2048 || p.Functions["api"].Handler != "src/api.handler" || len(p.Functions) != 2 {
		t.Errorf("the prod file gave region %s, api %+v, %d functions", p.Region, p.Functions["api"], len(p.Functions))
	}
	if p.Functions["api"].Environment["PAYMENTS_URL"].Output.Stage != "prod" {
		t.Errorf("the stage did not reach the main file: %+v", p.Functions["api"].Environment["PAYMENTS_URL"])
	}
	if o := p.Functions["api"].Environment["LEDGER_URL"].Output; o == nil || o.Region != "us-east-1" || o.Role != "arn:aws:iam::210987654321:role/koldstart-outputs" || o.Account != "" {
		t.Errorf("LEDGER_URL = %+v, want the output of ledger/prod in us-east-1 as the role", p.Functions["api"].Environment["LEDGER_URL"])
	}
}

// TestStageSecrets checks kdst.param() and kdst.secret() load as the full names of the app and
// the stage evaluated, a field and readAtRuntime kept, and kdst.ssm() as it is.
func TestStageSecrets(t *testing.T) {
	ev := evaluator(t)
	dir := t.TempDir()
	schema, err := filepath.Abs(filepath.Join("..", "..", "pkl"))
	if err != nil {
		t.Fatal(err)
	}
	body := `amends "` + filepath.Join(schema, "Project.pkl") + `"

import "` + filepath.Join(schema, "kdst.pkl") + `"

app = "shop"

functions {
  ["api"] {
    runtime = "nodejs22.x"
    handler = "src/api.handler"
    environment {
      ["API_KEY"] = kdst.param("api-key")
      ["DB_PASSWORD"] = kdst.secret("db").field("password").readAtRuntime()
      ["SHARED"] = kdst.ssm("shared-key")
    }
  }
}
`
	path := filepath.Join(dir, "koldstart.pkl")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		stage string
		want  map[string]config.Secret
	}{
		{stage: "dev", want: map[string]config.Secret{
			"API_KEY":     {Source: config.SourceSSM, Name: "/shop/dev/api-key"},
			"DB_PASSWORD": {Source: config.SourceSecretsManager, Name: "shop/dev/db", Key: "password", AtRuntime: true},
			"SHARED":      {Source: config.SourceSSM, Name: "shared-key"},
		}},
		{stage: "prod", want: map[string]config.Secret{
			"API_KEY":     {Source: config.SourceSSM, Name: "/shop/prod/api-key"},
			"DB_PASSWORD": {Source: config.SourceSecretsManager, Name: "shop/prod/db", Key: "password", AtRuntime: true},
			"SHARED":      {Source: config.SourceSSM, Name: "shared-key"},
		}},
	}
	for _, tt := range tests {
		t.Run(tt.stage, func(t *testing.T) {
			p, err := config.Load(context.Background(), ev, path, tt.stage)
			if err != nil {
				t.Fatal(err)
			}
			env := p.Functions["api"].Environment
			for key, want := range tt.want {
				if s := env[key].Secret; s == nil || *s != want {
					t.Errorf("%s = %+v, want %+v", key, s, want)
				}
			}
		})
	}
}

// TestLoadInfers checks functions that set neither runtime nor handler evaluate, and load with
// what the files beside the configuration, or in the root LoadIn is given, say.
func TestLoadInfers(t *testing.T) {
	ev := evaluator(t)
	schema, err := filepath.Abs(filepath.Join("..", "..", "pkl", "Project.pkl"))
	if err != nil {
		t.Fatal(err)
	}
	body := `amends "` + schema + `"

app = "shop"

functions {
  ["api"] { memory = 256 }
  ["worker"] {}
  ["report"] { runtime = "nodejs24.x" }
}
`
	root := t.TempDir()
	files := map[string]string{
		"koldstart.pkl":      body,
		"src/api.ts":         "export const handler = async () => 1\n",
		"src/report.ts":      "export const handler = async () => 1\n",
		"cmd/worker/main.go": "package main\n",
	}
	for name, content := range files {
		file := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(file), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	elsewhere := filepath.Join(t.TempDir(), "koldstart.pkl")
	if err := os.WriteFile(elsewhere, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	want := map[string][2]string{
		"api":    {config.RuntimeNode22, "src/api.handler"},
		"worker": {config.RuntimeGo, "./cmd/worker"},
		"report": {config.RuntimeNode24, "src/report.handler"},
	}
	tests := []struct {
		name string
		load func() (*config.Project, error)
	}{
		{name: "beside", load: func() (*config.Project, error) {
			return config.Load(context.Background(), ev, filepath.Join(root, "koldstart.pkl"), "dev")
		}},
		{name: "in root", load: func() (*config.Project, error) {
			return config.LoadIn(context.Background(), ev, elsewhere, root, "dev")
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := tt.load()
			if err != nil {
				t.Fatal(err)
			}
			for name, w := range want {
				if fn := p.Functions[name]; fn.Runtime != w[0] || fn.Handler != w[1] {
					t.Errorf("%s: runtime %q, handler %q, want %q, %q", name, fn.Runtime, fn.Handler, w[0], w[1])
				}
			}
		})
	}
	if _, err := config.Load(context.Background(), ev, elsewhere, "dev"); err == nil || !strings.Contains(err.Error(), "function api: no src/api.ts, src/api.py or cmd/api/ here") {
		t.Errorf("Load without the files = %v, want an error that says what is looked for", err)
	}
}

// header starts a koldstart.pkl that amends the schema in pkl/ and imports kdst.pkl.
func header(t *testing.T) string {
	t.Helper()
	schema, err := filepath.Abs(filepath.Join("..", "..", "pkl"))
	if err != nil {
		t.Fatal(err)
	}
	return "amends \"" + filepath.Join(schema, "Project.pkl") + "\"\n\nimport \"" + filepath.Join(schema, "kdst.pkl") + "\"\n\napp = \"shop\"\n"
}

// render writes body after header to a file of dir named after name, and returns the json pkl
// renders for it.
func render(t *testing.T, ev pkl.Evaluator, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, strings.NewReplacer(" ", "-", "/", "-").Replace(name)+".pkl")
	if err := os.WriteFile(path, []byte(header(t)+body+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := ev.EvalJSON(context.Background(), path, map[string]string{"stage": "dev"})
	if err != nil {
		t.Fatalf("evaluating %s: %v", name, err)
	}
	return string(out)
}

// TestEventHelpers checks each event helper of kdst.pkl, and one amended, renders the json of
// the event written out in full.
func TestEventHelpers(t *testing.T) {
	ev := evaluator(t)
	tests := []struct {
		name  string
		short string
		long  string
	}{
		{name: "get", short: `kdst.get("/orders/{id}")`, long: `new kdst.HttpRoute { method = "GET"; path = "/orders/{id}" }`},
		{name: "post", short: `kdst.post("/orders")`, long: `new kdst.HttpRoute { method = "POST"; path = "/orders" }`},
		{name: "put", short: `kdst.put("/orders/{id}")`, long: `new kdst.HttpRoute { method = "PUT"; path = "/orders/{id}" }`},
		{name: "patch", short: `kdst.patch("/orders/{id}")`, long: `new kdst.HttpRoute { method = "PATCH"; path = "/orders/{id}" }`},
		{name: "del", short: `kdst.del("/orders/{id}")`, long: `new kdst.HttpRoute { method = "DELETE"; path = "/orders/{id}" }`},
		{name: "head", short: `kdst.head("/orders")`, long: `new kdst.HttpRoute { method = "HEAD"; path = "/orders" }`},
		{name: "options", short: `kdst.options("/orders")`, long: `new kdst.HttpRoute { method = "OPTIONS"; path = "/orders" }`},
		{name: "any", short: `kdst.any("$default")`, long: `new kdst.HttpRoute { method = "ANY"; path = "$default" }`},
		{
			name:  "get amended",
			short: `(kdst.get("/orders")) { authorizer = "users"; scopes { "orders:read" } }`,
			long:  `new kdst.HttpRoute { method = "GET"; path = "/orders"; authorizer = "users"; scopes { "orders:read" } }`,
		},
		{name: "schedule", short: `kdst.schedule("rate(1 hour)")`, long: `new kdst.Schedule { expression = "rate(1 hour)" }`},
		{
			name:  "schedule amended",
			short: `(kdst.schedule("cron(0 9 ? * MON-FRI *)")) { timezone = "Europe/Amsterdam"; enabled = false }`,
			long:  `new kdst.Schedule { expression = "cron(0 9 ? * MON-FRI *)"; timezone = "Europe/Amsterdam"; enabled = false }`,
		},
		{name: "queue by name", short: `kdst.fromQueue("jobs")`, long: `new kdst.Queue { queue = kdst.ref("jobs", "Arn") }`},
		{
			name:  "queue by arn",
			short: `kdst.fromQueue("arn:aws:sqs:eu-west-1:123456789012:jobs")`,
			long:  `new kdst.Queue { queue = "arn:aws:sqs:eu-west-1:123456789012:jobs" }`,
		},
		{
			name:  "queue by an output",
			short: `kdst.fromQueue(kdst.stageOutput("billing", "dev", "jobsArn"))`,
			long:  `new kdst.Queue { queue = kdst.stageOutput("billing", "dev", "jobsArn") }`,
		},
		{
			name:  "queue amended",
			short: `(kdst.fromQueue("jobs")) { batchSize = 5; maximumBatchingWindow = 2.s }`,
			long:  `new kdst.Queue { queue = kdst.ref("jobs", "Arn"); batchSize = 5; maximumBatchingWindow = 2.s }`,
		},
		{name: "topic by name", short: `kdst.fromTopic("alerts")`, long: `new kdst.Topic { topic = kdst.ref("alerts", "TopicArn") }`},
		{
			name:  "topic by arn",
			short: `kdst.fromTopic("arn:aws:sns:eu-west-1:123456789012:billing")`,
			long:  `new kdst.Topic { topic = "arn:aws:sns:eu-west-1:123456789012:billing" }`,
		},
		{name: "stream by name", short: `kdst.fromStream("clicks")`, long: `new kdst.Stream { stream = kdst.ref("clicks", "Arn") }`},
		{name: "table stream by name", short: `kdst.fromTableStream("orders")`, long: `new kdst.Stream { stream = kdst.ref("orders", "StreamArn") }`},
		{
			name:  "table stream by arn",
			short: `(kdst.fromTableStream("arn:aws:dynamodb:eu-west-1:123456789012:table/orders/stream/2026-01-01T00:00:00.000")) { startingPosition = "TRIM_HORIZON" }`,
			long:  `new kdst.Stream { stream = "arn:aws:dynamodb:eu-west-1:123456789012:table/orders/stream/2026-01-01T00:00:00.000"; startingPosition = "TRIM_HORIZON" }`,
		},
		{name: "bucket by name", short: `kdst.fromBucket("uploads")`, long: `new kdst.Bucket { bucket = kdst.ref("uploads", "BucketName") }`},
		{name: "bucket by arn", short: `kdst.fromBucket("arn:aws:s3:::archive")`, long: `new kdst.Bucket { bucket = "archive" }`},
		{
			name:  "bucket amended",
			short: `(kdst.fromBucket("uploads")) { events { "s3:ObjectCreated:*" }; prefix = "incoming/" }`,
			long:  `new kdst.Bucket { bucket = kdst.ref("uploads", "BucketName"); events { "s3:ObjectCreated:*" }; prefix = "incoming/" }`,
		},
		{
			name:  "eventbridge",
			short: `(kdst.fromEventBridge(new Dynamic { source { "orders" } })) { bus = "orders" }`,
			long:  `new kdst.EventRule { pattern = new Dynamic { source { "orders" } }; bus = "orders" }`,
		},
		{name: "websocket", short: `kdst.fromWebSocket("$connect")`, long: `new kdst.WebSocketRoute { route = "$connect" }`},
	}
	dir := t.TempDir()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			events := func(event string) string {
				return `functions { ["api"] { runtime = "nodejs22.x"; handler = "src/api.handler"; events { ` + event + ` } } }`
			}
			short := render(t, ev, dir, tt.name+"-short", events(tt.short))
			long := render(t, ev, dir, tt.name+"-long", events(tt.long))
			if short != long {
				t.Errorf("%s renders\n%s\nwant\n%s", tt.short, short, long)
			}
		})
	}
}

// TestResourceShorthands checks each resource shorthand of kdst.pkl, and ones amended, render
// the json of the resource written out in full.
func TestResourceShorthands(t *testing.T) {
	ev := evaluator(t)
	bucketProperties := `PublicAccessBlockConfiguration { BlockPublicAcls = true; BlockPublicPolicy = true; IgnorePublicAcls = true; RestrictPublicBuckets = true }
      BucketEncryption { ServerSideEncryptionConfiguration { new { ServerSideEncryptionByDefault { SSEAlgorithm = "AES256" } } } }`
	tests := []struct {
		name  string
		short string
		long  string
	}{
		{
			name:  "table",
			short: `kdst.table("id")`,
			long: `new { type = "AWS::DynamoDB::Table"; properties {
      BillingMode = "PAY_PER_REQUEST"
      AttributeDefinitions { new { AttributeName = "id"; AttributeType = "S" } }
      KeySchema { new { AttributeName = "id"; KeyType = "HASH" } }
      PointInTimeRecoverySpecification { PointInTimeRecoveryEnabled = true }
    } }`,
		},
		{
			name:  "table with a sort key",
			short: `(kdst.table("customer")) { sortKey = "createdAt"; sortKeyType = "N" }`,
			long: `new { type = "AWS::DynamoDB::Table"; properties {
      BillingMode = "PAY_PER_REQUEST"
      AttributeDefinitions { new { AttributeName = "customer"; AttributeType = "S" }; new { AttributeName = "createdAt"; AttributeType = "N" } }
      KeySchema { new { AttributeName = "customer"; KeyType = "HASH" }; new { AttributeName = "createdAt"; KeyType = "RANGE" } }
      PointInTimeRecoverySpecification { PointInTimeRecoveryEnabled = true }
    } }`,
		},
		{
			name:  "table amended",
			short: `(kdst.table("id")) { partitionKeyType = "N"; deletionPolicy = "delete"; properties { TableClass = "STANDARD_INFREQUENT_ACCESS" } }`,
			long: `new { type = "AWS::DynamoDB::Table"; deletionPolicy = "delete"; properties {
      BillingMode = "PAY_PER_REQUEST"
      AttributeDefinitions { new { AttributeName = "id"; AttributeType = "N" } }
      KeySchema { new { AttributeName = "id"; KeyType = "HASH" } }
      PointInTimeRecoverySpecification { PointInTimeRecoveryEnabled = true }
      TableClass = "STANDARD_INFREQUENT_ACCESS"
    } }`,
		},
		{name: "queue", short: `kdst.queue()`, long: `new { type = "AWS::SQS::Queue"; properties { MessageRetentionPeriod = 1209600 } }`},
		{
			name:  "queue amended",
			short: `(kdst.queue()) { properties { VisibilityTimeout = 60; MessageRetentionPeriod = 3600 } }`,
			long:  `new { type = "AWS::SQS::Queue"; properties { MessageRetentionPeriod = 3600; VisibilityTimeout = 60 } }`,
		},
		{name: "bucket", short: `kdst.bucket()`, long: `new { type = "AWS::S3::Bucket"; properties { ` + bucketProperties + ` } }`},
		{
			name:  "bucket amended",
			short: `(kdst.bucket()) { deletionPolicy = "delete"; properties { VersioningConfiguration { Status = "Enabled" } } }`,
			long:  `new { type = "AWS::S3::Bucket"; deletionPolicy = "delete"; properties { ` + bucketProperties + `; VersioningConfiguration { Status = "Enabled" } } }`,
		},
		{name: "topic", short: `kdst.topic()`, long: `new { type = "AWS::SNS::Topic" }`},
		{
			name:  "topic amended",
			short: `(kdst.topic()) { properties { DisplayName = "alerts" } }`,
			long:  `new { type = "AWS::SNS::Topic"; properties { DisplayName = "alerts" } }`,
		},
	}
	dir := t.TempDir()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			short := render(t, ev, dir, tt.name+"-short", `resources { ["r"] = `+tt.short+` }`)
			long := render(t, ev, dir, tt.name+"-long", `resources { ["r"] = `+tt.long+` }`)
			if short != long {
				t.Errorf("%s renders\n%s\nwant\n%s", tt.short, short, long)
			}
		})
	}
}

// TestFunctionDefault checks the default of functions applies to every function, and a function
// that sets the same property keeps its own.
func TestFunctionDefault(t *testing.T) {
	ev := evaluator(t)
	dir := t.TempDir()
	body := `functions {
  default { memory = 256; timeout = 10.s }
  ["api"] { runtime = "nodejs22.x"; handler = "src/api.handler" }
  ["worker"] { runtime = "nodejs22.x"; handler = "src/worker.handler"; memory = 2048 }
}`
	path := filepath.Join(dir, "koldstart.pkl")
	if err := os.WriteFile(path, []byte(header(t)+body+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	p, err := config.Load(context.Background(), ev, path, "dev")
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		function string
		memory   int
		timeout  int
	}{
		{function: "api", memory: 256, timeout: 10},
		{function: "worker", memory: 2048, timeout: 10},
	}
	for _, tt := range tests {
		if fn := p.Functions[tt.function]; fn.Memory != tt.memory || fn.Timeout != tt.timeout {
			t.Errorf("%s: memory %d, timeout %d, want %d and %d", tt.function, fn.Memory, fn.Timeout, tt.memory, tt.timeout)
		}
	}
}

// TestSchemaRefuses checks the schema reports mistakes, pointing at the line.
func TestSchemaRefuses(t *testing.T) {
	ev := evaluator(t)
	tests := []struct {
		name string
		body string
		want string
	}{
		{name: "memory out of range", body: `functions { ["api"] { runtime = "nodejs22.x"; handler = "src/api.handler"; memory = 64 } }`, want: "isBetween(128, 10240)"},
		{name: "unknown runtime", body: `functions { ["api"] { runtime = "ruby3.3"; handler = "x.y" } }`, want: "ruby3.3"},
		{name: "misspelled property", body: `functions { ["api"] { runtime = "nodejs22.x"; handlr = "x.y" } }`, want: "handlr"},
		{name: "url without auth", body: `functions { ["api"] { runtime = "nodejs22.x"; handler = "x.y"; url = new {} } }`, want: "auth"},
		{name: "stage set", body: `stage = "prod"`, want: "fixed"},
		{name: "output of a short account", body: `functions { ["api"] { runtime = "nodejs22.x"; handler = "x.y"; environment { ["X"] = new StageOutput { app = "a"; stage = "b"; output = "c"; account = "1234" } } } }`, want: `\d{12}`},
		{name: "param with a leading slash", body: `functions { ["api"] { runtime = "nodejs22.x"; handler = "x.y"; environment { ["X"] = kdst.param("/orders/key") } } }`, want: `kdst.param("/orders/key"): the name goes after /<app>/<stage>/: drop the leading /, or use kdst.ssm("/orders/key")`},
		{name: "secret with dot dot", body: `functions { ["api"] { runtime = "nodejs22.x"; handler = "x.y"; environment { ["X"] = kdst.secret("../prod/db") } } }`, want: `kdst.secret("../prod/db"): the name goes after <app>/<stage>/ and has no empty segments or ..`},
		{name: "bucket by another arn", body: `functions { ["api"] { runtime = "nodejs22.x"; handler = "x.y"; events { kdst.fromBucket("arn:aws:sqs:eu-west-1:123456789012:jobs") } } }`, want: "not the arn of a bucket"},
		{name: "table of an unknown key type", body: `resources { ["orders"] = (kdst.table("id")) { partitionKeyType = "X" } }`, want: "partitionKeyType"},
		{name: "secret with an empty segment", body: `functions { ["api"] { runtime = "nodejs22.x"; handler = "x.y"; environment { ["X"] = kdst.secret("db//main").field("password") } } }`, want: "kdst.secretsmanager() for the full name"},
	}
	dir := t.TempDir()
	schema, err := filepath.Abs(filepath.Join("..", "..", "pkl", "Project.pkl"))
	if err != nil {
		t.Fatal(err)
	}
	kdst := filepath.Join(filepath.Dir(schema), "kdst.pkl")
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(dir, strings.ReplaceAll(tt.name, " ", "-")+".pkl")
			body := "amends \"" + schema + "\"\n\nimport \"" + kdst + "\"\n\napp = \"orders\"\n" + tt.body + "\n"
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := config.Load(context.Background(), ev, path, "dev")
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("Load = %v, want an error with %q", err, tt.want)
			}
		})
	}
}
