package migrate

import (
	"context"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kontrolplane/koldstart/internal/pkl"
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

// TestGolden converts each serverless.yml under testdata, and checks the koldstart.pkl and the
// todos are the ones beside it.
func TestGolden(t *testing.T) {
	golden(t, func(t *testing.T, dir string, src []byte) {
		res, err := Convert(src, "0.1.0")
		if err != nil {
			t.Fatalf("Convert: %v", err)
		}
		compare(t, filepath.Join(dir, "koldstart.pkl"), res.Pkl)
		var todos strings.Builder
		for _, todo := range res.Todos {
			todos.WriteString(todo.String() + "\n")
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

// TestGoldenEvaluates checks every converted file evaluates against the schema, and passes the
// checks a deploy makes, so what migrate writes is valid even where it is not complete.
func TestGoldenEvaluates(t *testing.T) {
	ev := evaluator(t)
	golden(t, func(t *testing.T, dir string, src []byte) {
		res, err := Convert(src, "0.1.0")
		if err != nil {
			t.Fatal(err)
		}
		problems, err := Check(context.Background(), ev, res.Pkl, "0.1.0", res.Stage)
		if err != nil {
			t.Fatalf("Check: %v", err)
		}
		for _, p := range problems {
			t.Errorf("a deploy would refuse it: %s", p)
		}
	})
}

// TestConvertRefuses checks a serverless.yml for another cloud, or that is not yaml, is refused.
func TestConvertRefuses(t *testing.T) {
	for _, src := range []string{"provider:\n  name: azure\n", "- just\n- a list\n", "{nope"} {
		if _, err := Convert([]byte(src), "0.1.0"); err == nil {
			t.Errorf("Convert(%q): want an error", src)
		}
	}
}

// TestText checks strings with serverless variables become pkl strings and expressions.
func TestText(t *testing.T) {
	c := &converter{region: "eu-west-1", resourceNames: map[string]string{}}
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
		{in: "${env:LEVEL, 'info'}", want: `read?("env:LEVEL") ?? "info"`, ok: true},
		{in: "${env:LEVEL}", want: `read("env:LEVEL")`, ok: true},
		{in: "${cf:payments-prod.ApiUrl}", want: `kdst.stageOutput("payments", "prod", "ApiUrl")`, ok: true},
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
