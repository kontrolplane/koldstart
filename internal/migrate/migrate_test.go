package migrate

import (
	"context"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
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
		res, err := Convert(src, "0.1.0", dir, WithName(goldenName))
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
		res, err := Convert(src, "0.1.0", dir, WithName(goldenName))
		if err != nil {
			t.Fatal(err)
		}
		for _, stage := range []string{res.Stage, "prod"} {
			problems, err := Check(context.Background(), ev, res.Pkl, "0.1.0", stage)
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
		if _, err := Convert([]byte(src), "0.1.0", "."); err == nil {
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
			res, err := Convert(src, "0.1.0", ".", tt.opts...)
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
	path, cleanup, err := schema.Local(src, "0.1.0")
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
