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

// TestSchema evaluates the example project against the schema, for two stages, and checks the
// json decodes, unknown fields refused, into what the file says.
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
// keeps the rest.
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
		{name: "unknown runtime", body: `functions { ["api"] { runtime = "python3.13"; handler = "x.y" } }`, want: "python3.13"},
		{name: "misspelled property", body: `functions { ["api"] { runtime = "nodejs22.x"; handlr = "x.y" } }`, want: "handlr"},
		{name: "url without auth", body: `functions { ["api"] { runtime = "nodejs22.x"; handler = "x.y"; url = new {} } }`, want: "auth"},
		{name: "stage set", body: `stage = "prod"`, want: "fixed"},
	}
	dir := t.TempDir()
	schema, err := filepath.Abs(filepath.Join("..", "..", "pkl", "Project.pkl"))
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(dir, strings.ReplaceAll(tt.name, " ", "-")+".pkl")
			body := "amends \"" + schema + "\"\n\napp = \"orders\"\n" + tt.body + "\n"
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
