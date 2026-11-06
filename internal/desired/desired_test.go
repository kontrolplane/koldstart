package desired

import (
	"encoding/json"
	"maps"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/kontrolplane/koldstart/internal/build"
	"github.com/kontrolplane/koldstart/internal/config"
	"github.com/kontrolplane/koldstart/internal/engine"
	"github.com/kontrolplane/koldstart/internal/state"
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
	if len(got) != 16 {
		t.Errorf("%d nodes, want 16: %v", len(got), slices.Sorted(maps.Keys(got)))
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
	if got["resource/orders"].DeletionPolicy != engine.PolicyRetain || got["resource/jobs"].DeletionPolicy != "" {
		t.Errorf("deletion policies: table %q, queue %q", got["resource/orders"].DeletionPolicy, got["resource/jobs"].DeletionPolicy)
	}
	if js(t, got["resource/jobs"].Inputs["properties"].(map[string]any)["RedrivePolicy"].(map[string]any)["deadLetterTargetArn"]) != js(t, engine.Ref("resource/dead", "Arn")) {
		t.Errorf("a ref inside properties was not turned into a node ref: %v", got["resource/jobs"].Inputs)
	}
}

// TestEvents checks the nodes of each kind of event: the http api and its routes, a mapping
// after the role that may read the queue, schedules with their role, subscriptions, and one
// notification node for every function a bucket invokes.
func TestEvents(t *testing.T) {
	p := project(t)
	enabled := false
	worker := p.Functions["worker"]
	worker.Events = append(worker.Events,
		config.Event{Type: config.EventSchedule, Expression: "rate(5 minutes)", Timezone: "UTC", Enabled: &enabled},
		config.Event{Type: config.EventTopic, Topic: config.Value{Literal: "arn:aws:sns:eu-west-1:123456789012:alerts"}},
		config.Event{Type: config.EventBucket, Bucket: config.Value{Literal: "uploads"}, Prefix: "in/"},
	)
	p.Functions["worker"] = worker
	api := p.Functions["api"]
	api.Events = append(api.Events, config.Event{Type: config.EventBucket, Bucket: config.Value{Literal: "uploads"}, Suffix: ".png"})
	p.Functions["api"] = api

	nodes, err := Build(p, target())
	if err != nil {
		t.Fatal(err)
	}
	got := byID(nodes)

	route := got["route/GET /orders/{id}"]
	if route.Inputs["authorizationType"] != "JWT" || js(t, route.Inputs["authorizer"]) != js(t, engine.Ref("authorizer/users", "AuthorizerId")) {
		t.Errorf("the authorized route = %v", route.Inputs)
	}
	if got["route/POST /orders"].Inputs["authorizationType"] != "NONE" {
		t.Errorf("the open route = %v", got["route/POST /orders"].Inputs)
	}
	if !strings.Contains(js(t, got["permission/api/httpapi"].Inputs["sourceArn"]), "execute-api:eu-west-1:123456789012:") {
		t.Errorf("the api's permission = %v", got["permission/api/httpapi"].Inputs)
	}
	if got["httpapi"].Inputs["throttling"] == nil {
		t.Error("the http api lost its throttling")
	}

	mapping := got["mapping/worker/jobs"]
	if !slices.Contains(mapping.Deps(), "role/worker") || js(t, mapping.Inputs["source"]) != js(t, engine.Ref("resource/jobs", "Arn")) {
		t.Errorf("the mapping = %v, deps %v", mapping.Inputs, mapping.Deps())
	}
	if policy := js(t, got["role/worker"].Inputs["policy"]); !strings.Contains(policy, "sqs:ReceiveMessage") {
		t.Errorf("the worker may not read its queue: %s", policy)
	}

	schedule := got["schedule/worker/0"]
	if schedule.Inputs["enabled"] != false || js(t, schedule.Inputs["role"]) != js(t, engine.Ref("schedulerrole/worker", "Arn")) {
		t.Errorf("the schedule = %v", schedule.Inputs)
	}
	if !strings.Contains(js(t, got["schedulerrole/worker"].Inputs), "scheduler.amazonaws.com") {
		t.Errorf("the scheduler role = %v", got["schedulerrole/worker"].Inputs)
	}

	if sub := got["subscription/worker/alerts"]; !slices.Contains(sub.Deps(), "permission/worker/topic-alerts") {
		t.Errorf("the subscription comes before its permission: %v", sub.Deps())
	}

	n, ok := got["notification/uploads"]
	if !ok {
		t.Fatalf("no notification node for uploads: %v", slices.Sorted(maps.Keys(got)))
	}
	functions := n.Inputs["functions"].([]any)
	if len(functions) != 2 || !slices.Contains(n.Deps(), "permission/api/bucket-uploads") || !slices.Contains(n.Deps(), "permission/worker/bucket-uploads") {
		t.Errorf("the notification = %v, deps %v", n.Inputs, n.Deps())
	}
	if !strings.HasPrefix(functions[0].(map[string]any)["id"].(string), "koldstart-orders-dev-") {
		t.Errorf("a notification id does not start with the stage's prefix: %v", functions[0])
	}

	before := slices.Sorted(maps.Keys(got))
	worker.Events = slices.Delete(worker.Events, 2, 3)
	p.Functions["worker"] = worker
	nodes, err = Build(p, target())
	if err != nil {
		t.Fatal(err)
	}
	after := byID(nodes)
	for _, id := range []string{"mapping/worker/jobs", "schedule/worker/0", "permission/worker/bucket-uploads"} {
		if _, ok := after[id]; !ok {
			t.Errorf("removing the topic event renamed %s: before %v", id, before)
		}
	}

	if a, b := got["permission/api/httpapi"].Inputs["statementId"], got["permission/api/bucket-uploads"].Inputs["statementId"]; a == b {
		t.Errorf("two permissions share the statement id %v", a)
	}
}

// TestStageSecrets checks the secrets kdst.param() and kdst.secret() give reach the function's
// inputs, as the plan shows them, and its role by their full names under the app and stage.
func TestStageSecrets(t *testing.T) {
	tests := []struct {
		name     string
		secret   string
		describe string
		arn      string
	}{
		{
			name:     "kdst.param",
			secret:   `{"$koldstart": "secret", "source": "ssm", "name": "api-key", "scope": "stage", "key": "", "atRuntime": true}`,
			describe: "ssm:/orders/dev/api-key",
			arn:      "arn:aws:ssm:eu-west-1:123456789012:parameter/orders/dev/api-key",
		},
		{
			name:     "kdst.secret",
			secret:   `{"$koldstart": "secret", "source": "secretsmanager", "name": "db", "scope": "stage", "key": "password", "atRuntime": true}`,
			describe: "secretsmanager:orders/dev/db#password",
			arn:      "arn:aws:secretsmanager:eu-west-1:123456789012:secret:orders/dev/db-??????",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := os.ReadFile("../config/testdata/orders.json")
			if err != nil {
				t.Fatal(err)
			}
			data = []byte(strings.Replace(string(data), `"LOG_LEVEL": "info"`, `"LOG_LEVEL": "info", "SECRET": `+tt.secret, 1))
			p, err := config.Decode(data)
			if err != nil {
				t.Fatal(err)
			}
			nodes, err := Build(p, target())
			if err != nil {
				t.Fatal(err)
			}
			got := byID(nodes)
			env := got["function/api"].Inputs["environment"].(map[string]any)
			if d := engine.Describe(env["SECRET"]); d != tt.describe {
				t.Errorf("SECRET is %s, want %s", d, tt.describe)
			}
			if policy := js(t, got["role/api"].Inputs["policy"]); !strings.Contains(policy, `"`+tt.arn+`"`) {
				t.Errorf("the policy lacks %s: %s", tt.arn, policy)
			}
		})
	}
}

// TestRuntimeSecrets checks a secret the function reads itself is allowed on its role, and a field
// of it is named in a variable beside it.
func TestRuntimeSecrets(t *testing.T) {
	p := project(t)
	api := p.Functions["api"]
	api.Environment["DB_PASSWORD"] = config.Value{Secret: &config.Secret{Source: config.SourceSecretsManager, Name: "db", Key: "password", AtRuntime: true}}
	api.Environment["TOKEN"] = config.Value{Secret: &config.Secret{Source: config.SourceSSM, Name: "/orders/token", AtRuntime: true}}
	p.Functions["api"] = api
	nodes, err := Build(p, target())
	if err != nil {
		t.Fatal(err)
	}
	got := byID(nodes)
	policy := js(t, got["role/api"].Inputs["policy"])
	for _, want := range []string{
		`"arn:aws:secretsmanager:eu-west-1:123456789012:secret:db-??????"`,
		`"arn:aws:ssm:eu-west-1:123456789012:parameter/orders/token"`,
		`"secretsmanager:GetSecretValue"`, `"ssm:GetParameter"`,
	} {
		if !strings.Contains(policy, want) {
			t.Errorf("the policy lacks %s: %s", want, policy)
		}
	}
	if strings.Contains(policy, "/orders/api-key") {
		t.Errorf("a secret read while deploying is granted to the function: %s", policy)
	}
	env := got["function/api"].Inputs["environment"].(map[string]any)
	if env["DB_PASSWORD_KEY"] != "password" || env["TOKEN_KEY"] != nil {
		t.Errorf("env = %v, want DB_PASSWORD_KEY alone", env)
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

// TestTriggers checks the nodes that invoke a function are found in a state: its routes,
// mappings, schedules, subscriptions and the notifications of its buckets, and none of another function.
func TestTriggers(t *testing.T) {
	p := project(t)
	worker := p.Functions["worker"]
	worker.Events = append(worker.Events,
		config.Event{Type: config.EventSchedule, Expression: "rate(5 minutes)"},
		config.Event{Type: config.EventTopic, Topic: config.Value{Literal: "arn:aws:sns:eu-west-1:123456789012:alerts"}},
		config.Event{Type: config.EventBucket, Bucket: config.Value{Literal: "uploads"}},
	)
	p.Functions["worker"] = worker
	nodes, err := Build(p, target())
	if err != nil {
		t.Fatal(err)
	}
	resources := map[string]*state.Resource{}
	for _, n := range nodes {
		resources[n.ID] = &state.Resource{Type: n.Type, Dependencies: n.Deps()}
	}
	tests := []struct {
		function string
		want     []string
	}{
		{function: "worker", want: []string{"mapping/worker/jobs", "notification/uploads", "schedule/worker/0", "subscription/worker/alerts"}},
		{function: "api", want: []string{"route/GET /orders/{id}", "route/POST /orders"}},
		{function: "missing"},
	}
	for _, tt := range tests {
		t.Run(tt.function, func(t *testing.T) {
			if got := Triggers(resources, tt.function); !slices.Equal(got, tt.want) {
				t.Errorf("Triggers = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestGroup checks every node of a stage with events falls under the function or resource it
// belongs to, or the http api.
func TestGroup(t *testing.T) {
	p := project(t)
	worker := p.Functions["worker"]
	worker.Events = append(worker.Events,
		config.Event{Type: config.EventSchedule, Expression: "rate(5 minutes)"},
		config.Event{Type: config.EventBucket, Bucket: config.Value{Ref: &config.Ref{Resource: "orders"}}},
	)
	p.Functions["worker"] = worker
	nodes, err := Build(p, target())
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range nodes {
		kind, name := Group(n.ID, n.Deps())
		switch {
		case kind == GroupFunction && p.Functions[name].Runtime == "":
			t.Errorf("%s belongs to %q, which is not a function", n.ID, name)
		case kind == GroupResource && p.Resources[name].Type == "":
			t.Errorf("%s belongs to %q, which is not a resource", n.ID, name)
		case kind == "":
			t.Errorf("%s belongs to nothing", n.ID)
		}
	}
	if kind, name := Group("route/GET /orders/{id}", []string{"httpapi", "integration/api"}); kind != GroupFunction || name != "api" {
		t.Errorf("a route belongs to %s %q, want the function api", kind, name)
	}
}

// TestJoinAndPseudo checks a join becomes one the engine joins once its refs are known, and the
// account's values are filled in for the stage's account and region, in an environment and
// inside a resource's properties.
func TestJoinAndPseudo(t *testing.T) {
	p := project(t)
	api := p.Functions["api"]
	api.Environment = map[string]config.Value{
		"BUCKET":  {Join: []config.Value{{Literal: "arn:"}, {Pseudo: config.PseudoPartition}, {Literal: ":s3:::"}, {Ref: &config.Ref{Resource: "orders", Attribute: "Arn"}}}},
		"ACCOUNT": {Pseudo: config.PseudoAccountID},
	}
	p.Functions["api"] = api
	p.Resources["topic"] = config.Resource{Type: "AWS::SNS::Topic", Properties: map[string]any{
		"TopicName": map[string]any{"$koldstart": "join", "parts": []any{"orders-", map[string]any{"$koldstart": "pseudo", "name": "accountId"}}},
	}}
	nodes, err := Build(p, target())
	if err != nil {
		t.Fatal(err)
	}
	got := byID(nodes)
	env := got[FunctionID("api")].Inputs["environment"].(map[string]any)
	if env["ACCOUNT"] != "123456789012" {
		t.Errorf("ACCOUNT = %v, want the account", env["ACCOUNT"])
	}
	want := engine.Join("arn:", "aws", ":s3:::", engine.Ref(ResourceID("orders"), "Arn"))
	if !reflect.DeepEqual(env["BUCKET"], want) {
		t.Errorf("BUCKET = %v, want %v", env["BUCKET"], want)
	}
	props := got[ResourceID("topic")].Inputs["properties"].(map[string]any)
	if props["TopicName"] != "orders-123456789012" {
		t.Errorf("TopicName = %v", props["TopicName"])
	}
}

// TestImageAndLayers checks an image function's node has its image as its code and no runtime or
// handler, a function with layers has them, and one without has no layers at all, so a function
// deployed before layers keeps its hash.
func TestImageAndLayers(t *testing.T) {
	p := project(t)
	p.Functions["img"] = config.Function{Image: &config.Image{Command: []string{"app.handler"}}, Memory: 512, Timeout: 10, Architecture: config.ArchARM64}
	api := p.Functions["api"]
	api.Layers = []config.Value{{Literal: "arn:aws:lambda:eu-west-1:123456789012:layer:shared:3"}}
	p.Functions["api"] = api
	tgt := target()
	tgt.ImageRegistry = "123456789012.dkr.ecr.eu-west-1.amazonaws.com"
	tgt.Archives["img"] = build.Archive{Function: "img", SHA256: strings.Repeat("c", 64), Image: true}
	nodes, err := Build(p, tgt)
	if err != nil {
		t.Fatal(err)
	}
	got := byID(nodes)
	img := got[FunctionID("img")].Inputs
	if _, ok := img["runtime"]; ok {
		t.Error("an image function has a runtime")
	}
	code := img["code"].(map[string]any)
	if want := "123456789012.dkr.ecr.eu-west-1.amazonaws.com/koldstart/orders:dev-img-" + strings.Repeat("c", 32); code["imageUri"] != want {
		t.Errorf("imageUri = %v, want %s", code["imageUri"], want)
	}
	if !reflect.DeepEqual(img["imageConfig"], map[string]any{"command": []string{"app.handler"}}) {
		t.Errorf("imageConfig = %v", img["imageConfig"])
	}
	if layers := got[FunctionID("api")].Inputs["layers"].([]any); len(layers) != 1 {
		t.Errorf("layers = %v", layers)
	}
	if _, ok := got[FunctionID("worker")].Inputs["layers"]; ok {
		t.Error("a function without layers has a layers input")
	}
}

// TestMoreEvents checks the nodes of an eventbridge rule, websocket routes and a load balancer
// rule: each with its permission, the load balancer's registration after it, the websocket's
// callback url and the right to post to connections, and a route's own request limit.
func TestMoreEvents(t *testing.T) {
	p := project(t)
	api := p.Functions["api"]
	api.Events = append(api.Events,
		config.Event{Type: config.EventRule, Pattern: json.RawMessage(`{"source":["shop"]}`), Bus: config.Value{Literal: "shop"}},
		config.Event{Type: config.EventWebSocket, Route: "$connect"},
		config.Event{Type: config.EventWebSocket, Route: "sendMessage"},
		config.Event{Type: config.EventALB, Listener: config.Value{Literal: "arn:aws:elasticloadbalancing:eu-west-1:1:listener/app/x/y/z"}, Priority: 10, Paths: []string{"/orders/*"}},
		config.Event{Type: config.EventHTTPRoute, Method: "GET", Path: "/slow", Throttling: &config.Throttling{Rate: 5, Burst: 10}},
	)
	p.Functions["api"] = api
	nodes, err := Build(p, target())
	if err != nil {
		t.Fatal(err)
	}
	got := byID(nodes)
	for _, id := range []string{"rule/api/0", "permission/api/rule-0", WebSocketID, "wsintegration/api", "wsroute/$connect", "wsroute/sendMessage",
		"permission/api/websocket", "targetgroup/api/0", "permission/api/alb-0", "lbtarget/api/0", "lbrule/api/0"} {
		if _, ok := got[id]; !ok {
			t.Errorf("no node %s", id)
		}
	}
	if rule := got["rule/api/0"].Inputs; rule["pattern"] != `{"source":["shop"]}` || rule["bus"] != "shop" {
		t.Errorf("rule inputs = %v", rule)
	}
	if after := got["lbtarget/api/0"].After; !slices.Contains(after, "permission/api/alb-0") {
		t.Errorf("the function is registered before the load balancer may invoke it: %v", after)
	}
	if name := got["targetgroup/api/0"].Inputs["name"].(string); len(name) > 32 {
		t.Errorf("target group name %q is longer than 32", name)
	}
	if ws := got[WebSocketID].Inputs; ws["protocol"] != "WEBSOCKET" || ws["stage"] != "dev" {
		t.Errorf("websocket api inputs = %v", ws)
	}
	if _, ok := got[FunctionID("api")].Inputs["environment"].(map[string]any)["WEBSOCKET_CALLBACK_URL"]; !ok {
		t.Error("a websocket function has no WEBSOCKET_CALLBACK_URL")
	}
	if _, ok := got[FunctionID("worker")].Inputs["environment"].(map[string]any)["WEBSOCKET_CALLBACK_URL"]; ok {
		t.Error("a function without websocket routes has WEBSOCKET_CALLBACK_URL")
	}
	policy, _ := json.Marshal(got[RoleID("api")].Inputs["policy"])
	if !strings.Contains(string(policy), "execute-api:ManageConnections") {
		t.Error("a websocket function may not post to its connections")
	}
	if th := got["route/GET /slow"].Inputs["throttling"]; !reflect.DeepEqual(th, map[string]any{"rate": 5.0, "burst": 10}) {
		t.Errorf("route throttling = %v", th)
	}
	if _, ok := got["route/GET /orders/{id}"].Inputs["throttling"]; ok {
		t.Error("a route without throttling has it as an input, which changes its hash")
	}
	for _, n := range nodes {
		if kind, _ := Group(n.ID, n.Deps()); kind == "" {
			t.Errorf("%s belongs to nothing", n.ID)
		}
	}
	resources := map[string]*state.Resource{}
	for _, n := range nodes {
		resources[n.ID] = &state.Resource{Dependencies: n.Deps()}
	}
	triggers := Triggers(resources, "api")
	for _, want := range []string{"rule/api/0", "wsroute/$connect", "lbrule/api/0"} {
		if !slices.Contains(triggers, want) {
			t.Errorf("Triggers has no %s: %v", want, triggers)
		}
	}
}
