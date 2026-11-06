package migrate

import (
	"context"
	"encoding/json"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/kontrolplane/koldstart/internal/build"
	"github.com/kontrolplane/koldstart/internal/config"
	"github.com/kontrolplane/koldstart/internal/desired"
	"github.com/kontrolplane/koldstart/internal/pkl"
	schema "github.com/kontrolplane/koldstart/pkl"
)

var update = flag.Bool("update", false, "write the expected files of the golden tests")

// golden runs fn on each case under testdata, a directory with a serverless.yml.
func golden(t *testing.T, fn func(t *testing.T, dir string, src []byte)) {
	t.Helper()
	dirs, err := filepath.Glob(filepath.Join("testdata", "*"))
	if err != nil {
		t.Fatal(err)
	}
	for _, dir := range dirs {
		t.Run(filepath.Base(dir), func(t *testing.T) {
			src, err := os.ReadFile(filepath.Join(dir, "serverless.yml"))
			if err != nil {
				t.Fatal(err)
			}
			fn(t, dir, src)
		})
	}
}

func compare(t *testing.T, path, got string) {
	t.Helper()
	if *update {
		if err := os.WriteFile(path, []byte(got), 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v: run go test ./internal/migrate -update to write it", err)
	}
	if got != string(want) {
		t.Errorf("%s differs; run go test ./internal/migrate -update to accept it. got:\n%s", path, got)
	}
}

// goldenName is the name the golden files are converted as.
const goldenName = "kdst"

// TestGolden converts each serverless.yml under testdata, and checks the koldstart.pkl and the
// todos are the ones beside it.
func TestGolden(t *testing.T) {
	golden(t, func(t *testing.T, dir string, src []byte) {
		res, err := Convert(src, dir, WithName(goldenName))
		if err != nil {
			t.Fatalf("Convert: %v", err)
		}
		compare(t, filepath.Join(dir, "koldstart.pkl"), res.Pkl)
		var todos strings.Builder
		if err := WriteTodos(&todos, res.Todos); err != nil {
			t.Fatal(err)
		}
		compare(t, filepath.Join(dir, "todos.txt"), todos.String())
	})
}

// evaluator returns the pkl koldstart would use. Outside ci it does not download one: without
// pkl installed, the tests that evaluate skip.
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

// TestGoldenEvaluates checks every converted file evaluates against the schema, for its stage and
// for prod, and passes the checks a deploy makes, so what migrate writes is valid even where it is
// not complete.
func TestGoldenEvaluates(t *testing.T) {
	ev := evaluator(t)
	golden(t, func(t *testing.T, dir string, src []byte) {
		res, err := Convert(src, dir, WithName(goldenName))
		if err != nil {
			t.Fatal(err)
		}
		for _, stage := range []string{res.Stage, "prod"} {
			problems, err := Check(context.Background(), ev, res.Pkl, stage)
			if err != nil {
				t.Fatalf("Check for %q: %v", stage, err)
			}
			for _, p := range problems {
				t.Errorf("a deploy of %q would refuse it: %s", stage, p)
			}
		}
	})
}

// TestConvertRefuses checks a serverless.yml for another cloud, or that is not yaml, is refused.
func TestConvertRefuses(t *testing.T) {
	for _, src := range []string{"provider:\n  name: azure\n", "- just\n- a list\n", "{nope"} {
		if _, err := Convert([]byte(src), "."); err == nil {
			t.Errorf("Convert(%q): want an error", src)
		}
	}
}

// TestWithName checks the file and the todos give commands by the name the binary was run as, and
// by koldstart without one.
func TestWithName(t *testing.T) {
	src := []byte("service: x\nprovider: {name: aws}\nplugins: [serverless-offline]\n")
	tests := []struct {
		name string
		opts []Option
		want string
	}{
		{name: "short", opts: []Option{WithName("kdst")}, want: "kdst"},
		{name: "long", opts: []Option{WithName("koldstart")}, want: "koldstart"},
		{name: "none", want: "koldstart"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, err := Convert(src, ".", tt.opts...)
			if err != nil {
				t.Fatal(err)
			}
			if want := "by " + tt.want + " migrate."; !strings.Contains(res.Pkl, want) {
				t.Errorf("the file says no %q:\n%s", want, res.Pkl)
			}
			if len(res.Todos) != 1 || !strings.HasPrefix(res.Todos[0].What, tt.want+" offline ") {
				t.Errorf("todos = %v, want one for %s offline", res.Todos, tt.want)
			}
		})
	}
}

// TestWriteTodos checks todos are listed under their kind's heading, the kinds that matter most
// first, in the order they came within a kind.
func TestWriteTodos(t *testing.T) {
	tests := []struct {
		name  string
		todos []Todo
		want  string
	}{
		{name: "none"},
		{
			name:  "one kind",
			todos: []Todo{{Kind: Review, Where: "a", What: "x"}, {Kind: Review, Where: "b", What: "y"}},
			want:  Review.String() + ":\n  a: x\n  b: y\n",
		},
		{
			name:  "by kind",
			todos: []Todo{{Kind: Drop, Where: "d", What: "x"}, {Kind: Add, Where: "a", What: "x"}, {Kind: Fix, Where: "f", What: "x"}},
			want:  Fix.String() + ":\n  f: x\n\n" + Add.String() + ":\n  a: x\n\n" + Drop.String() + ":\n  d: x\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got strings.Builder
			if err := WriteTodos(&got, tt.todos); err != nil {
				t.Fatal(err)
			}
			if got.String() != tt.want {
				t.Errorf("got:\n%s\nwant:\n%s", got.String(), tt.want)
			}
		})
	}
}

// TestText checks strings with serverless variables become pkl strings and expressions.
func TestText(t *testing.T) {
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte("service: orders\n"), &doc); err != nil {
		t.Fatal(err)
	}
	c := &converter{doc: doc.Content[0], region: "eu-west-1", resourceNames: map[string]string{}}
	tests := []struct {
		in, want string
		ok       bool
	}{
		{in: "plain", want: `"plain"`, ok: true},
		{in: `say "hi" \ there`, want: `"say \"hi\" \\ there"`, ok: true},
		{in: "${sls:stage}", want: "kdst.stage", ok: true},
		{in: "orders-${opt:stage, 'dev'}-table", want: `"orders-\(kdst.stage)-table"`, ok: true},
		{in: "${self:service}", want: "module.app", ok: true},
		{in: "${aws:region}", want: "module.region", ok: true},
		{in: "${ssm:/orders/key}", want: `kdst.ssm("/orders/key")`, ok: true},
		{in: "${ssm:/${self:service}/${sls:stage}/api-key}", want: `kdst.param("api-key")`, ok: true},
		{in: "${ssm:/orders/${opt:stage, 'dev'}/stripe/key~true}", want: `kdst.param("stripe/key")`, ok: true},
		{in: "${ssm:/aws/reference/secretsmanager/${self:service}/${self:provider.stage}/db}", want: `kdst.secret("db")`, ok: true},
		{in: "${ssm:/aws/reference/secretsmanager/orders/key}", want: `kdst.ssm("/aws/reference/secretsmanager/orders/key")`, ok: true},
		{in: "${ssm:/shared/${sls:stage}/key}", want: `kdst.ssm("/shared/\(kdst.stage)/key")`, ok: true},
		{in: "${ssm:/orders/dev/key}", want: `kdst.ssm("/orders/dev/key")`, ok: true},
		{in: "${ssm:/${self:service}/${sls:stage}//key}", want: `kdst.ssm("/\(module.app)/\(kdst.stage)//key")`, ok: true},
		{in: "${ssm:/${self:service}/${sls:stage}/key, 'none'}", want: `kdst.param("key")`, ok: true},
		{in: "${ssm:/${env:PREFIX}/key}", want: `kdst.ssm("/\(read("env:PREFIX"))/key")`, ok: true},
		{in: "${ssm:/${cf:shared-prod.Path}/key}", ok: false},
		{in: "${env:LEVEL, 'info'}", want: `read?("env:LEVEL") ?? "info"`, ok: true},
		{in: "${env:LEVEL}", want: `read("env:LEVEL")`, ok: true},
		{in: "${cf:payments-prod.ApiUrl}", want: `kdst.stageOutput("payments", "prod", "ApiUrl")`, ok: true},
		{in: "${cf(us-east-1):payments-prod.ApiUrl}", want: `(kdst.stageOutput("payments", "prod", "ApiUrl")) { region = "us-east-1" }`, ok: true},
		{in: "${cf.us-east-1:payments-prod.ApiUrl}", want: `(kdst.stageOutput("payments", "prod", "ApiUrl")) { region = "us-east-1" }`, ok: true},
		{in: "key-${ssm:/orders/key}", ok: false},
		{in: "${file(./x.json)}", ok: false},
		{in: "${self:custom.${sls:stage}.url}", ok: false},
		{in: "${broken", ok: false},
	}
	for _, tt := range tests {
		got := c.text(tt.in)
		if got.ok != tt.ok || (tt.ok && got.expr != tt.want) {
			t.Errorf("text(%q) = %+v, want %q, ok %v", tt.in, got, tt.want, tt.ok)
		}
	}
}

// TestLogicalID checks the logical ids the serverless framework gives functions.
func TestLogicalID(t *testing.T) {
	for in, want := range map[string]string{"api": "ApiLambdaFunction", "old_api": "OldUnderscoreapiLambdaFunction", "get-order": "GetDashorderLambdaFunction"} {
		if got := logicalID(in); got != want {
			t.Errorf("logicalID(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestIntrinsicsByStage loads the converted intrinsics for dev and prod, and checks each stage
// gets what its conditions and params say, with the joins planned for the account.
func TestIntrinsicsByStage(t *testing.T) {
	ev := evaluator(t)
	src, err := os.ReadFile(filepath.Join("testdata", "intrinsics", "koldstart.pkl"))
	if err != nil {
		t.Fatal(err)
	}
	path, cleanup, err := schema.Local(src)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	tests := []struct {
		stage    string
		domain   string
		alarms   bool
		recovery bool
	}{
		{stage: "dev", domain: "dev.shop.example.com"},
		{stage: "staging", domain: "dev.shop.example.com", alarms: true},
		{stage: "prod", domain: "shop.example.com", alarms: true, recovery: true},
	}
	for _, tt := range tests {
		t.Run(tt.stage, func(t *testing.T) {
			p, err := config.Load(context.Background(), ev, path, tt.stage)
			if err != nil {
				t.Fatal(err)
			}
			if got := p.Functions["api"].Environment["DOMAIN"].Literal; got != tt.domain {
				t.Errorf("DOMAIN = %q, want %q", got, tt.domain)
			}
			if _, ok := p.Resources["Alarms"]; ok != tt.alarms {
				t.Errorf("the Alarms topic: %v, want %v", ok, tt.alarms)
			}
			if _, ok := p.Resources["OrdersTable"].Properties["PointInTimeRecoverySpecification"]; ok != tt.recovery {
				t.Errorf("point in time recovery: %v, want %v", ok, tt.recovery)
			}
			nodes, err := desired.Build(p, desired.Target{Account: "123456789012", Region: "eu-west-1", Bucket: "b",
				Archives: map[string]build.Archive{"api": {Function: "api", SHA256: "x"}}})
			if err != nil {
				t.Fatal(err)
			}
			for _, n := range nodes {
				if n.ID != desired.ResourceID("Uploads") {
					continue
				}
				name := n.Inputs["properties"].(map[string]any)["BucketName"]
				if want := "shop-" + tt.stage + "-uploads-123456789012"; name != want {
					t.Errorf("BucketName = %v, want %s", name, want)
				}
			}
		})
	}
}

// TestShorthandsExact loads the converted shorthands, and checks each resource, whether written
// as a shorthand of kdst.pkl or in full, has the type, deletion policy and properties of
// serverless.yml, no more and no less.
func TestShorthandsExact(t *testing.T) {
	ev := evaluator(t)
	src, err := os.ReadFile(filepath.Join("testdata", "shorthands", "koldstart.pkl"))
	if err != nil {
		t.Fatal(err)
	}
	path, cleanup, err := schema.Local(src, "0.1.0")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	p, err := config.Load(context.Background(), ev, path, "dev")
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join("testdata", "shorthands", "serverless.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Resources struct {
			Resources map[string]struct {
				Type           string         `yaml:"Type"`
				DeletionPolicy string         `yaml:"DeletionPolicy"`
				Properties     map[string]any `yaml:"Properties"`
			} `yaml:"Resources"`
		} `yaml:"resources"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	if len(p.Resources) != len(doc.Resources.Resources) {
		t.Errorf("%d resources, want %d", len(p.Resources), len(doc.Resources.Resources))
	}
	for name, want := range doc.Resources.Resources {
		got := p.Resources[name]
		if want.Properties == nil {
			want.Properties = map[string]any{}
		}
		if got.Type != want.Type || got.DeletionPolicy != strings.ToLower(want.DeletionPolicy) || asJSON(t, got.Properties) != asJSON(t, want.Properties) {
			t.Errorf("%s = %s %q %s, want %s %q %s", name, got.Type, got.DeletionPolicy, asJSON(t, got.Properties),
				want.Type, strings.ToLower(want.DeletionPolicy), asJSON(t, want.Properties))
		}
	}
}

// asJSON is v as json, its keys sorted, for comparing what yaml and pkl give.
func asJSON(t *testing.T, v any) string {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// TestShorthand checks which properties of a resource make it a shorthand of kdst.pkl, with
// what amends it, and which keep it in full.
func TestShorthand(t *testing.T) {
	tests := []struct {
		name   string
		typ    string
		props  string
		helper string
		amends []string
		rest   int
	}{
		{name: "a topic without properties", typ: typeTopic, helper: "kdst.topic()"},
		{name: "a topic with a name", typ: typeTopic, props: "TopicName: alerts", helper: "kdst.topic()", rest: 1},
		{name: "a queue kept 14 days", typ: typeQueue, props: "MessageRetentionPeriod: 1209600\nVisibilityTimeout: 60", helper: "kdst.queue()", rest: 1},
		{name: "a queue kept 4 days", typ: typeQueue, props: "MessageRetentionPeriod: 345600"},
		{name: "a queue without properties", typ: typeQueue},
		{name: "a queue kept for a variable", typ: typeQueue, props: "MessageRetentionPeriod: ${self:custom.retention}"},
		{
			name:   "a table of a string key",
			typ:    typeTable,
			props:  tableYAML("id: S", "HASH: id") + "\nTableName: orders",
			helper: `kdst.table("id")`,
			rest:   1,
		},
		{
			name:   "a table of number keys",
			typ:    typeTable,
			props:  tableYAML("account: N\n  - at: B", "HASH: account\n  - RANGE: at"),
			helper: `kdst.table("account")`,
			amends: []string{`partitionKeyType = "N"`, `sortKey = "at"`, `sortKeyType = "B"`},
		},
		{name: "a table without point-in-time recovery", typ: typeTable, props: "BillingMode: PAY_PER_REQUEST\nAttributeDefinitions: [{AttributeName: id, AttributeType: S}]\nKeySchema: [{AttributeName: id, KeyType: HASH}]"},
		{name: "a table of an index's attribute", typ: typeTable, props: tableYAML("id: S\n  - email: S", "HASH: id")},
		{name: "a table of provisioned throughput", typ: typeTable, props: strings.Replace(tableYAML("id: S", "HASH: id"), "PAY_PER_REQUEST", "PROVISIONED", 1)},
		{name: "a table of its keys in another order", typ: typeTable, props: tableYAML("at: S\n  - account: S", "HASH: account\n  - RANGE: at")},
		{name: "a bucket of the defaults", typ: typeBucket, props: bucketYAML, helper: "kdst.bucket()"},
		{name: "a bucket of a kms key", typ: typeBucket, props: strings.Replace(bucketYAML, "AES256", "aws:kms", 1)},
		{name: "a bucket with a name", typ: typeBucket, props: "BucketName: exports"},
		{name: "another type", typ: "AWS::Kinesis::Stream", props: "ShardCount: 1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var props *yaml.Node
			if tt.props != "" {
				var doc yaml.Node
				if err := yaml.Unmarshal([]byte(tt.props), &doc); err != nil {
					t.Fatal(err)
				}
				props = doc.Content[0]
			}
			s, rest := shorthand(tt.typ, props)
			if s.helper != tt.helper || strings.Join(s.amends, "; ") != strings.Join(tt.amends, "; ") {
				t.Errorf("shorthand = %q %q, want %q %q", s.helper, s.amends, tt.helper, tt.amends)
			}
			if tt.helper == "" && rest != props {
				t.Error("the full form lost properties")
			}
			if tt.helper != "" && rest != nil && len(rest.Content)/2 != tt.rest {
				t.Errorf("%d properties left to amend, want %d", len(rest.Content)/2, tt.rest)
			}
		})
	}
}

// tableYAML is the properties of an on-demand table with point-in-time recovery, of the
// attributes, "name: type" each, and the keys, "role: name" each, as items of yaml lists.
func tableYAML(attributes, keys string) string {
	definitions := regexp.MustCompile(`(\w+): ([SNB])`).ReplaceAllString(attributes, "{AttributeName: $1, AttributeType: $2}")
	keySchema := regexp.MustCompile(`(HASH|RANGE): (\w+)`).ReplaceAllString(keys, "{AttributeName: $2, KeyType: $1}")
	return "BillingMode: PAY_PER_REQUEST\nAttributeDefinitions:\n  - " + definitions + "\nKeySchema:\n  - " + keySchema +
		"\nPointInTimeRecoverySpecification:\n  PointInTimeRecoveryEnabled: true"
}

// bucketYAML is the properties kdst.bucket() sets.
const bucketYAML = `PublicAccessBlockConfiguration:
  BlockPublicAcls: true
  BlockPublicPolicy: true
  IgnorePublicAcls: true
  RestrictPublicBuckets: true
BucketEncryption:
  ServerSideEncryptionConfiguration:
    - ServerSideEncryptionByDefault:
        SSEAlgorithm: AES256`

// TestSub checks a Fn::Sub becomes a pkl string when every part is one, and a kdst.join() when a
// ref or the account is among them.
func TestSub(t *testing.T) {
	tests := []struct {
		name string
		sub  string
		want string
	}{
		{name: "pseudo region", sub: `!Sub "arn:aws:s3:::${AWS::Region}-x"`, want: `"arn:aws:s3:::\(module.region)-x"`},
		{name: "a literal ${", sub: `!Sub "${!Literal}-${sls:stage}"`, want: `"${Literal}-\(kdst.stage)"`},
		{name: "an attribute", sub: `!Sub "${Orders.Arn}/index/*"`, want: `kdst.join(List(kdst.ref("Orders", "Arn"), "/index/*"))`},
		{name: "its own variables", sub: "!Sub [\"${A}-${B}\", {A: x, B: !Ref Orders}]", want: `kdst.join(List("x-", kdst.ref("Orders", "Identifier")))`},
		{name: "the account", sub: `!Sub "${AWS::AccountId}"`, want: `kdst.join(List(kdst.accountId))`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var doc yaml.Node
			src := "service: x\nprovider: {region: eu-west-1}\nresources: {Resources: {Orders: {Type: AWS::DynamoDB::Table}}}\nv: " + tt.sub + "\n"
			if err := yaml.Unmarshal([]byte(src), &doc); err != nil {
				t.Fatal(err)
			}
			c := &converter{doc: doc.Content[0], region: "eu-west-1", resourceNames: map[string]string{}}
			c.nameFunctions()
			c.nameResources()
			got := c.nodeValue(get(doc.Content[0], "v"))
			if !got.ok || got.expr != tt.want {
				t.Errorf("got %q (%s), want %q", got.expr, got.todo, tt.want)
			}
		})
	}
}
