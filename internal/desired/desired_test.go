package desired

import (
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/kontrolplane/koldstart/internal/build"
	"github.com/kontrolplane/koldstart/internal/config"
	"github.com/kontrolplane/koldstart/internal/engine"
)

func project(t *testing.T) *config.Project {
	t.Helper()
	data, err := os.ReadFile("../config/testdata/orders.json")
	if err != nil {
		t.Fatal(err)
	}
	p, err := config.Decode(data)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func target() Target {
	return Target{
		Account: "123456789012", Region: "eu-west-1", Bucket: "koldstart-state-123456789012-eu-west-1",
		Archives: map[string]build.Archive{"api": {SHA256: "aaa"}, "worker": {SHA256: "bbb"}},
	}
}

func byID(nodes []engine.Node) map[string]engine.Node {
	out := map[string]engine.Node{}
	for _, n := range nodes {
		out[n.ID] = n
	}
	return out
}

// js returns v as json, for comparing inputs without caring about go types.
func js(t *testing.T, v any) string {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// TestBuild checks the nodes of the fixture: their ids, names, refs and policies.
func TestBuild(t *testing.T) {
	nodes, err := Build(project(t), target())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	got := byID(nodes)
	for _, id := range []string{
		"loggroup/api", "role/api", "function/api", "loggroup/worker", "role/worker", "function/worker",
		"resource/orders", "resource/jobs", "resource/dead",
	} {
		if _, ok := got[id]; !ok {
			t.Errorf("no node %s", id)
		}
	}
	if len(got) != 9 {
		t.Errorf("%d nodes, want 9", len(got))
	}

	api := got["function/api"]
	if api.Name != "orders-dev-api" || got["role/api"].Name != "orders-dev-api-eu-west-1" || got["loggroup/api"].Name != "/aws/lambda/orders-dev-api" {
		t.Errorf("names: %s, %s, %s", api.Name, got["role/api"].Name, got["loggroup/api"].Name)
	}
	env := api.Inputs["environment"].(map[string]any)
	if js(t, env["TABLE_NAME"]) != js(t, engine.Ref("resource/orders", "TableName")) {
		t.Errorf("TABLE_NAME = %v", env["TABLE_NAME"])
	}
	if js(t, env["ORDERS_ARN"]) != js(t, engine.Ref("resource/orders", "Arn")) || env["ORDERS_NAME"] == nil {
		t.Errorf("link did not add ORDERS_ARN and ORDERS_NAME: %v", env)
	}
	if !strings.Contains(js(t, env["API_KEY"]), `"$koldstart":"secret"`) || !strings.Contains(js(t, env["PAYMENTS_URL"]), `"$koldstart":"output"`) {
		t.Errorf("secret or output lost: %v, %v", env["API_KEY"], env["PAYMENTS_URL"])
	}
	code := api.Inputs["code"].(map[string]any)
	if code["key"] != "zips/orders/dev/aaa.zip" || code["bucket"] != target().Bucket {
		t.Errorf("code = %v", code)
	}
	if deps := api.Deps(); !slices.Equal(deps, []string{"loggroup/api", "resource/orders", "role/api"}) {
		t.Errorf("api deps = %v", deps)
	}

	policy := js(t, got["role/api"].Inputs["policy"])
	for _, want := range []string{`"dynamodb:GetItem"`, `"/index/*"`, `"logs:PutLogEvents"`} {
		if !strings.Contains(policy, want) {
			t.Errorf("the api's policy lacks %s: %s", want, policy)
		}
	}
	if strings.Contains(policy, "dynamodb:PutItem") {
		t.Errorf("a read link grants writes: %s", policy)
	}
	if !strings.Contains(js(t, got["role/worker"].Inputs["policy"]), "dynamodb:PutItem") {
		t.Error("the worker's own permissions are not in its policy")
	}

	if got["function/worker"].Inputs["handler"] != "bootstrap" {
		t.Errorf("a go handler = %v, want bootstrap", got["function/worker"].Inputs["handler"])
	}
	if got["resource/orders"].DeletionPolicy != engine.Retain || got["resource/jobs"].DeletionPolicy != "" {
		t.Errorf("deletion policies: table %q, queue %q", got["resource/orders"].DeletionPolicy, got["resource/jobs"].DeletionPolicy)
	}
	if js(t, got["resource/jobs"].Inputs["properties"].(map[string]any)["RedrivePolicy"].(map[string]any)["deadLetterTargetArn"]) != js(t, engine.Ref("resource/dead", "Arn")) {
		t.Errorf("a ref inside properties was not turned into a node ref: %v", got["resource/jobs"].Inputs)
	}
}

// TestBuildRefuses checks a link to a type link does not know, and a function that was not built.
func TestBuildRefuses(t *testing.T) {
	p := project(t)
	p.Resources["topic"] = config.Resource{Type: "AWS::Events::EventBus"}
	fn := p.Functions["api"]
	fn.Link["topic"] = "write"
	if _, err := Build(p, target()); err == nil || !strings.Contains(err.Error(), "use permissions") && !strings.Contains(err.Error(), "permissions instead") {
		t.Errorf("Build with a link to an event bus = %v", err)
	}

	tg := target()
	delete(tg.Archives, "worker")
	if _, err := Build(project(t), tg); err == nil || !strings.Contains(err.Error(), "functions[worker]") {
		t.Errorf("Build without the worker's zip = %v", err)
	}
}

// TestName checks a name that fits is kept, and a long one is cut to the limit with a hash
// that keeps two long names apart.
func TestName(t *testing.T) {
	if got := Name(64, "orders", "dev", "api"); got != "orders-dev-api" {
		t.Errorf("Name = %q", got)
	}
	long := strings.Repeat("a", 30)
	a := Name(64, long, "pr-1234", long+"x")
	b := Name(64, long, "pr-1234", long+"y")
	if len(a) != 64 || len(b) != 64 || a == b {
		t.Errorf("long names: %q (%d), %q (%d)", a, len(a), b, len(b))
	}
	if a != Name(64, long, "pr-1234", long+"x") {
		t.Error("the same long name gives another result")
	}
}
