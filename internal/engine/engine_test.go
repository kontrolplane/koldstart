package engine

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/kontrolplane/koldstart/internal/config"
	"github.com/kontrolplane/koldstart/internal/state"
)

const fakeType = "Fake::Thing"

// stack is a small app: a log group, a role that refers to it, a function that refers to the
// role, and a table the function reads.
func stack(memory int) []Node {
	return []Node{
		{ID: "loggroup/api", Type: fakeType, Name: "/aws/lambda/orders-dev-api", Inputs: map[string]any{"retention": 30}},
		{ID: "role/api", Type: fakeType, Name: "orders-dev-api", Inputs: map[string]any{"logGroup": Ref("loggroup/api", "Arn")}},
		{ID: "resource/orders", Type: fakeType, Name: "orders-dev-orders", Inputs: map[string]any{"key": "id"}, DeletionPolicy: PolicyRetain},
		{
			ID: "function/api", Type: fakeType, Name: "orders-dev-api",
			Inputs: map[string]any{
				"memory": memory,
				"role":   Ref("role/api", "Arn"),
				"env":    map[string]any{"TABLE": Ref("resource/orders", "Name")},
			},
			After: []string{"loggroup/api"},
		},
	}
}

// harness is an engine on a fake provider and a memory backend.
type harness struct {
	t       *testing.T
	fake    *Fake
	backend *state.Memory
	engine  *Engine
}

func newHarness(t *testing.T) *harness {
	fake := NewFake()
	return &harness{
		t:       t,
		fake:    fake,
		backend: state.NewMemory(),
		engine:  &Engine{Providers: map[string]Provider{fakeType: fake}, Concurrency: 4},
	}
}

func (h *harness) read() (*state.State, string) {
	h.t.Helper()
	st, etag, err := h.backend.Read(context.Background(), "orders", "dev")
	if err != nil {
		h.t.Fatal(err)
	}
	if st == nil {
		st = state.New("orders", "dev", "eu-west-1", "000000000000")
	}
	return st, etag
}

// deploy plans and applies nodes, and returns the plan.
func (h *harness) deploy(nodes []Node) (*Plan, error) {
	h.t.Helper()
	st, etag := h.read()
	plan, err := h.engine.Plan(context.Background(), nodes, st)
	if err != nil {
		return plan, err
	}
	return plan, h.engine.Apply(context.Background(), plan, nodes, NewStore(h.backend, st, etag, "test"))
}

func actions(p *Plan) map[string]Action {
	out := map[string]Action{}
	for _, c := range p.Changes {
		out[c.ID] = c.Action
	}
	return out
}

// uniqueNames avoids the role and the function sharing a name in the one fake.
func uniqueNames(nodes []Node) []Node {
	for i := range nodes {
		nodes[i].Name = nodes[i].ID
	}
	return nodes
}

// TestCreateThenNoOp checks a first deploy creates everything in order, and a second changes
// nothing.
func TestCreateThenNoOp(t *testing.T) {
	h := newHarness(t)
	nodes := uniqueNames(stack(256))
	plan, err := h.deploy(nodes)
	if err != nil {
		t.Fatalf("deploy: %v", err)
	}
	for id, a := range actions(plan) {
		if a != Create {
			t.Errorf("%s: %s, want create", id, a)
		}
	}
	calls := h.fake.Calls()
	if slices.Index(calls, "create:role/api") > slices.Index(calls, "create:function/api") ||
		slices.Index(calls, "create:loggroup/api") > slices.Index(calls, "create:role/api") {
		t.Errorf("created out of order: %v", calls)
	}
	fn := h.fake.Resources()["function/api"]
	if fn.Inputs["role"] != "arn:fake:role/api" || fn.Inputs["env"].(map[string]any)["TABLE"] != "resource/orders" {
		t.Errorf("refs not resolved: %v", fn.Inputs)
	}
	if fn.Tags["koldstart:node"] != "function/api" || fn.Tags["koldstart:stage"] != "dev" {
		t.Errorf("tags = %v", fn.Tags)
	}

	h.fake.ResetCalls()
	plan, err = h.deploy(nodes)
	if err != nil {
		t.Fatalf("second deploy: %v", err)
	}
	if plan.HasChanges() || len(h.fake.Calls()) != 0 {
		t.Errorf("second deploy: %v, calls %v", actions(plan), h.fake.Calls())
	}
}

// TestUpdateReplaceAndRetain checks an in-place change is an update of that node alone, a
// change of a replace key replaces, and a retained resource is not replaced without leave.
func TestUpdateReplaceAndRetain(t *testing.T) {
	h := newHarness(t)
	h.fake.ReplaceKeys = []string{"key"}
	if _, err := h.deploy(uniqueNames(stack(256))); err != nil {
		t.Fatal(err)
	}

	plan, err := h.deploy(uniqueNames(stack(1024)))
	if err != nil {
		t.Fatal(err)
	}
	if got := actions(plan); got["function/api"] != Update || got["role/api"] != NoOp {
		t.Errorf("memory change: %v", got)
	}
	if c, _ := plan.Change("function/api"); !slices.Equal(c.Fields, []string{"memory"}) {
		t.Errorf("fields = %v, want memory", c.Fields)
	}

	nodes := uniqueNames(stack(1024))
	nodes[2].Inputs["key"] = "pk"
	if _, err := h.deploy(nodes); err == nil || !strings.Contains(err.Error(), "--allow-replace") {
		t.Fatalf("replacing a retained resource = %v, want an error naming --allow-replace", err)
	}
	h.engine.AllowReplace = true
	if _, err := h.deploy(nodes); err == nil || !strings.Contains(err.Error(), "give the new one another name") {
		t.Fatalf("replacing a retained resource under its own name = %v", err)
	}
	nodes[2].Name = "resource/orders-v2"
	plan, err = h.deploy(nodes)
	if err != nil {
		t.Fatal(err)
	}
	if got := actions(plan); got["resource/orders"] != Replace {
		t.Errorf("after the failed replace: %v, want the pending replace finished", got)
	}
	fake := h.fake.Resources()
	if fake["resource/orders-v2"].Inputs["key"] != "pk" || fake["resource/orders"].Inputs["key"] != "id" {
		t.Errorf("want the new table with pk and the old one kept with id: %v", fake)
	}
	st, _ := h.read()
	if st.Retained["resource/orders~resource/orders"] == nil || st.Resources["resource/orders"].PhysicalID != "resource/orders-v2" {
		t.Errorf("state after the replace: resources %v, retained %v", st.Resources["resource/orders"], st.Retained)
	}
}

// TestDeleteOrderAndRetain checks nodes gone from the configuration are deleted after what
// depends on them, a retained one is left in place, and coming back adopts it.
func TestDeleteOrderAndRetain(t *testing.T) {
	h := newHarness(t)
	if _, err := h.deploy(uniqueNames(stack(256))); err != nil {
		t.Fatal(err)
	}
	h.fake.ResetCalls()
	plan, err := h.deploy(nil)
	if err != nil {
		t.Fatalf("removing everything: %v", err)
	}
	if got := actions(plan); got["resource/orders"] != Keep || got["function/api"] != Delete {
		t.Errorf("remove: %v", got)
	}
	calls := h.fake.Calls()
	if slices.Index(calls, "delete:function/api") > slices.Index(calls, "delete:role/api") ||
		slices.Index(calls, "delete:role/api") > slices.Index(calls, "delete:loggroup/api") {
		t.Errorf("deleted out of order: %v", calls)
	}
	if _, ok := h.fake.Resources()["resource/orders"]; !ok || len(h.fake.Resources()) != 1 {
		t.Errorf("after remove the fake holds %v, want the retained table alone", slices.Collect(maps.Keys(h.fake.Resources())))
	}
	st, _ := h.read()
	if len(st.Resources) != 0 || st.Retained["resource/orders"] == nil {
		t.Errorf("state after remove: %+v, retained %+v", st.Resources, st.Retained)
	}

	plan, err = h.deploy(uniqueNames(stack(256)))
	if err != nil {
		t.Fatalf("deploying again: %v", err)
	}
	if got := actions(plan); got["resource/orders"] != Adopt || got["function/api"] != Create {
		t.Errorf("deploy after remove: %v", got)
	}
	if st, _ := h.read(); len(st.Retained) != 0 || st.Resources["resource/orders"] == nil {
		t.Errorf("the table was not adopted: %+v", st.Retained)
	}
}

// TestCrashAnywhereConverges kills the deploy after each create, update and delete in turn, as
// if the process died after aws did the work but before koldstart heard back, and checks the
// next deploy finishes the work with nothing made twice and nothing left over. Once a deploy
// makes fewer calls than the crash point, every point is covered. Each step deploys before
// first, without crashing, then after, which crashes and runs again.
func TestCrashAnywhereConverges(t *testing.T) {
	replaced := uniqueNames(stack(256))
	replaced[1].Inputs["logGroup"] = "elsewhere"
	steps := []struct {
		name        string
		before      []Node
		after       []Node
		replaceKeys []string
	}{
		{name: "replace", before: uniqueNames(stack(256)), after: replaced, replaceKeys: []string{"logGroup"}},
		{name: "first deploy", after: uniqueNames(stack(256))},
		{name: "update", before: uniqueNames(stack(256)), after: uniqueNames(stack(1024))},
		{name: "remove", before: uniqueNames(stack(256)), after: nil},
		{name: "remove a function", before: uniqueNames(stack(256)), after: uniqueNames(stack(256))[:3]},
	}
	for _, step := range steps {
		for crash := 1; ; crash++ {
			h := newHarness(t)
			h.fake.ReplaceKeys = step.replaceKeys
			if step.before != nil {
				if _, err := h.deploy(step.before); err != nil {
					t.Fatal(err)
				}
			}
			h.fake.CrashAfter = h.fake.mutated + crash
			_, err := h.deploy(step.after)
			if err == nil {
				t.Logf("%s: crashed at each of %d calls", step.name, crash-1)
				break
			}
			if !errors.Is(err, ErrCrash) {
				t.Fatalf("%s, crash %d: %v", step.name, crash, err)
			}
			h.fake.CrashAfter = 0
			if _, err := h.deploy(step.after); err != nil {
				t.Fatalf("%s, after a crash at call %d: %v", step.name, crash, err)
			}
			plan, err := h.deploy(step.after)
			if err != nil || plan.HasChanges() {
				t.Fatalf("%s, after a crash at call %d, a third deploy: %v, %v", step.name, crash, actions(plan), err)
			}
			checkConverged(t, fmt.Sprintf("%s, crash at call %d", step.name, crash), h, step.after)
		}
	}
}

// checkConverged checks the fake holds what nodes ask for, and the retained table, and the
// state agrees.
func checkConverged(t *testing.T, name string, h *harness, nodes []Node) {
	t.Helper()
	want := map[string]bool{"resource/orders": true}
	for _, n := range nodes {
		want[n.Name] = true
	}
	got := h.fake.Resources()
	for id := range got {
		if !want[id] {
			t.Errorf("%s: %s exists, and nothing asks for it", name, id)
		}
	}
	st, _ := h.read()
	for _, n := range nodes {
		if _, ok := got[n.Name]; !ok {
			t.Errorf("%s: %s does not exist", name, n.ID)
		}
		r := st.Resources[n.ID]
		if r == nil || r.Pending != nil || r.PhysicalID != n.Name {
			t.Errorf("%s: state of %s = %+v", name, n.ID, r)
		}
	}
	if fn, ok := got["function/api"]; ok && fn.Inputs["role"] != "arn:fake:role/api" {
		t.Errorf("%s: function inputs = %v", name, fn.Inputs)
	}
}

// TestUnresolvedNodeIsTold checks a node whose inputs cannot be resolved once what it depends
// on is applied is told as failed, though it never started, so progress counts it.
func TestUnresolvedNodeIsTold(t *testing.T) {
	tests := []struct {
		name      string
		attribute string
		wantErr   bool
	}{
		{name: "attribute there", attribute: "Arn"},
		{name: "attribute missing", attribute: "QueueArn", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)
			var events []Event
			h.engine.Concurrency = 1
			h.engine.OnEvent = func(ev Event) { events = append(events, ev) }
			nodes := []Node{
				{ID: "loggroup/api", Type: fakeType, Name: "loggroup", Inputs: map[string]any{"retention": 30}},
				{ID: "role/api", Type: fakeType, Name: "role", Inputs: map[string]any{"logGroup": Ref("loggroup/api", tt.attribute)}},
			}
			_, err := h.deploy(nodes)
			if (err != nil) != tt.wantErr {
				t.Fatalf("deploy = %v, want an error: %t", err, tt.wantErr)
			}
			var done *Event
			for i := range events {
				if events[i].ID == "role/api" && events[i].Done {
					done = &events[i]
				}
			}
			if done == nil {
				t.Fatalf("no done event for role/api in %+v", events)
			}
			if (done.Err != nil) != tt.wantErr || done.Action != Create {
				t.Errorf("done event = %+v, want action create and an error: %t", *done, tt.wantErr)
			}
		})
	}
}

// TestFailureKeepsWhatWasDone checks a failing node stops the deploy after its level, keeps
// what was done in the state, and the next deploy picks up from there.
func TestFailureKeepsWhatWasDone(t *testing.T) {
	h := newHarness(t)
	h.fake.Fail = map[string]error{"create:function/api": errors.New("throttled")}
	if _, err := h.deploy(uniqueNames(stack(256))); err == nil || !strings.Contains(err.Error(), "throttled") {
		t.Fatalf("deploy = %v, want the failure", err)
	}
	st, _ := h.read()
	if st.Resources["role/api"] == nil || st.Resources["role/api"].Pending != nil {
		t.Errorf("the role was not recorded: %+v", st.Resources["role/api"])
	}
	h.fake.Fail = nil
	plan, err := h.deploy(uniqueNames(stack(256)))
	if err != nil {
		t.Fatal(err)
	}
	if got := actions(plan); got["role/api"] != NoOp || got["function/api"] != Create {
		t.Errorf("deploy after the failure: %v", got)
	}
}

// TestPanicFailsTheNode checks a panic while a node is applied fails the deploy with an error,
// rather than end the program, and keeps what was done before it.
func TestPanicFailsTheNode(t *testing.T) {
	h := newHarness(t)
	h.engine.OnEvent = func(ev Event) {
		if ev.ID == "function/api" && !ev.Done {
			panic("boom")
		}
	}
	_, err := h.deploy(uniqueNames(stack(256)))
	if err == nil || !strings.Contains(err.Error(), "a bug in koldstart stopped it (boom)") {
		t.Fatalf("deploy = %v, want the panic as its error", err)
	}
	st, _ := h.read()
	if st.Resources["role/api"] == nil {
		t.Errorf("the role was not recorded: %v", slices.Sorted(maps.Keys(st.Resources)))
	}
}

// TestFencing checks a deploy whose state was written by another one in between stops.
func TestFencing(t *testing.T) {
	h := newHarness(t)
	st, etag := h.read()
	nodes := uniqueNames(stack(256))
	plan, err := h.engine.Plan(context.Background(), nodes, st)
	if err != nil {
		t.Fatal(err)
	}
	other := state.New("orders", "dev", "eu-west-1", "000000000000")
	if _, err := h.backend.Write(context.Background(), other, ""); err != nil {
		t.Fatal(err)
	}
	err = h.engine.Apply(context.Background(), plan, nodes, NewStore(h.backend, st, etag, "test"))
	if !errors.Is(err, state.ErrConflict) {
		t.Fatalf("Apply after another write = %v, want ErrConflict", err)
	}
}

type fakeSecrets map[string]string

func (f fakeSecrets) Resolve(_ context.Context, source, name, key string, _ bool) (string, error) {
	v, ok := f[source+":"+name+":"+key]
	if !ok {
		return "", fmt.Errorf("no secret %s", name)
	}
	return v, nil
}

// TestSecrets checks a secret reaches the provider, stays out of the state, and a new value
// is an update.
func TestSecrets(t *testing.T) {
	h := newHarness(t)
	secrets := fakeSecrets{"ssm:/orders/key:": "one"}
	h.engine.Secrets = secrets
	nodes := []Node{{ID: "function/api", Type: fakeType, Name: "api", Inputs: map[string]any{
		"env": map[string]any{"KEY": SecretValue(config.Secret{Source: "ssm", Name: "/orders/key"})},
	}}}
	if _, err := h.deploy(nodes); err != nil {
		t.Fatal(err)
	}
	if got := h.fake.Resources()["api"].Inputs["env"].(map[string]any)["KEY"]; got != "one" {
		t.Errorf("the provider got %v, want the secret's value", got)
	}
	st, _ := h.read()
	if strings.Contains(string(st.Resources["function/api"].Inputs), "one") {
		t.Errorf("the secret's value is in the state: %s", st.Resources["function/api"].Inputs)
	}

	plan, err := h.deploy(nodes)
	if err != nil || plan.HasChanges() {
		t.Fatalf("the same secret: %v, %v", actions(plan), err)
	}
	nodes[0].Inputs["memory"] = 256
	if _, err := h.deploy(nodes); err != nil {
		t.Fatal(err)
	}
	if got := h.fake.LastPrior["env"].(map[string]any)["KEY"]; got != "one" {
		t.Errorf("with the secret unchanged, the prior inputs hold %v, want its value", got)
	}

	secrets["ssm:/orders/key:"] = "two"
	plan, err = h.deploy(nodes)
	if err != nil || actions(plan)["function/api"] != Update {
		t.Fatalf("a new secret value: %v, %v", actions(plan), err)
	}
	if got := h.fake.Resources()["api"].Inputs["env"].(map[string]any)["KEY"]; got != "two" {
		t.Errorf("the provider got %v, want the new value", got)
	}
	if got := h.fake.LastPrior["env"].(map[string]any)["KEY"]; got != changedMarker {
		t.Errorf("with the secret changed, the prior inputs hold %q, want the changed marker", got)
	}

	missing := []Node{{ID: "function/other", Type: fakeType, Name: "other", Inputs: map[string]any{
		"env": map[string]any{"KEY": SecretValue(config.Secret{Source: "ssm", Name: "/missing"})},
	}}}
	if _, err := h.engine.Plan(context.Background(), missing, state.New("orders", "dev", "eu-west-1", "")); err == nil {
		t.Error("planning a new node with a missing secret: want an error")
	}
}

// TestStageSecrets checks a secret of kdst.param() is resolved by its full name under the app
// and stage, and its value reaches the provider.
func TestStageSecrets(t *testing.T) {
	p, err := config.Decode([]byte(`{"schemaVersion": 1, "app": "orders", "stage": "dev", "functions": {"api": {"environment": {
		"KEY": {"$koldstart": "secret", "source": "ssm", "name": "api-key", "scope": "stage", "key": "", "atRuntime": false}}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	h := newHarness(t)
	h.engine.Secrets = fakeSecrets{"ssm:/orders/dev/api-key:": "one"}
	nodes := []Node{{ID: "function/api", Type: fakeType, Name: "api", Inputs: map[string]any{
		"env": map[string]any{"KEY": SecretValue(*p.Functions["api"].Environment["KEY"].Secret)},
	}}}
	if _, err := h.deploy(nodes); err != nil {
		t.Fatal(err)
	}
	if got := h.fake.Resources()["api"].Inputs["env"].(map[string]any)["KEY"]; got != "one" {
		t.Errorf("the provider got %v, want the value of /orders/dev/api-key", got)
	}
}

// TestOrderRefuses checks a ref to a missing node and a cycle are refused before anything runs.
func TestOrderRefuses(t *testing.T) {
	missing := []Node{{ID: "a", Type: fakeType, Inputs: map[string]any{"x": Ref("b", "Arn")}}}
	if _, err := order(missing); err == nil || !strings.Contains(err.Error(), "not in the configuration") {
		t.Errorf("order with a missing node = %v", err)
	}
	cycle := []Node{
		{ID: "a", Type: fakeType, Inputs: map[string]any{"x": Ref("b", "Arn")}},
		{ID: "b", Type: fakeType, Inputs: map[string]any{"x": Ref("a", "Arn")}},
	}
	if _, err := order(cycle); err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Errorf("order with a cycle = %v", err)
	}
}

// TestStalePlan checks a plan made before the state changed is refused.
func TestStalePlan(t *testing.T) {
	h := newHarness(t)
	st, etag := h.read()
	nodes := uniqueNames(stack(256))
	plan, _ := h.engine.Plan(context.Background(), nodes, st)
	st.Serial = 7
	if err := h.engine.Apply(context.Background(), plan, nodes, NewStore(h.backend, st, etag, "test")); err == nil {
		t.Fatal("Apply of a stale plan: want an error")
	}
}

// TestJoin checks a join resolves once its refs do, is unknown before, and makes the node
// depend on what its refs name.
func TestJoin(t *testing.T) {
	h := newHarness(t)
	nodes := []Node{
		{ID: "resource/orders", Type: fakeType, Name: "orders", Inputs: map[string]any{}},
		{ID: "role/api", Type: fakeType, Name: "role", Inputs: map[string]any{"resource": Join(Ref("resource/orders", "Arn"), "/index/*")}},
	}
	plan, err := h.deploy(nodes)
	if err != nil {
		t.Fatal(err)
	}
	if c, _ := plan.Change("role/api"); !IsUnknown(c.After["resource"]) {
		t.Errorf("planned before the table exists: %v, want unknown", c.After["resource"])
	}
	if got := h.fake.Resources()["role"].Inputs["resource"]; got != "arn:fake:orders/index/*" {
		t.Errorf("applied = %v", got)
	}
	if deps := nodes[1].Deps(); !slices.Equal(deps, []string{"resource/orders"}) {
		t.Errorf("Deps = %v", deps)
	}
}

// TestOnlyCode checks a plan that changes nothing but function code is told apart.
func TestOnlyCode(t *testing.T) {
	plan := &Plan{Changes: []Change{
		{ID: "function/api", Action: Update, Fields: []string{"code"}},
		{ID: "role/api", Action: NoOp},
	}}
	if !plan.OnlyCode() {
		t.Error("OnlyCode = false for a code change")
	}
	plan.Changes[1].Action = Update
	if plan.OnlyCode() {
		t.Error("OnlyCode = true with a role update")
	}
}

// TestRefresh checks a plan without refresh misses what was changed and deleted outside
// koldstart, and one with it creates the deleted resource again, with what refers to it updated,
// and sets back what was changed from how it is.
func TestRefresh(t *testing.T) {
	h := newHarness(t)
	nodes := uniqueNames(stack(256))
	if _, err := h.deploy(nodes); err != nil {
		t.Fatalf("deploy: %v", err)
	}
	h.fake.Tamper("function/api", "memory", 1024)
	h.fake.Tamper("loggroup/api", "", nil)

	st, _ := h.read()
	plan, err := h.engine.Plan(context.Background(), nodes, st)
	if err != nil || plan.HasChanges() {
		t.Fatalf("a plan without refresh = %v, %v", actions(plan), err)
	}

	h.engine.Refresh = true
	h.fake.ResetCalls()
	plan, err = h.deploy(nodes)
	if err != nil {
		t.Fatalf("deploy with refresh: %v", err)
	}
	want := map[string]Action{"loggroup/api": Create, "role/api": Update, "function/api": Update, "resource/orders": NoOp}
	if got := actions(plan); !maps.Equal(got, want) {
		t.Errorf("plan = %v, want %v", got, want)
	}
	logs, _ := plan.Change("loggroup/api")
	fn, _ := plan.Change("function/api")
	if !logs.Gone || !slices.Equal(fn.Drift, []string{"memory"}) || len(fn.Fields) != 0 {
		t.Errorf("the log group gone %v, the function's drift %v and fields %v", logs.Gone, fn.Drift, fn.Fields)
	}
	if got := h.fake.Resources()["function/api"].Inputs["memory"]; got != float64(256) {
		t.Errorf("memory after the deploy = %v, want 256", got)
	}
	if _, ok := h.fake.Resources()["loggroup/api"]; !ok {
		t.Error("the log group was not created again")
	}
	if prior, ok := h.fake.Priors["function/api"]; !ok || prior != nil {
		t.Errorf("the function was updated from %v, want from how it is, with no prior", prior)
	}

	h.fake.ResetCalls()
	plan, err = h.deploy(nodes)
	if err != nil || plan.HasChanges() {
		t.Errorf("a deploy after = %v, %v", actions(plan), err)
	}
}

// cancelProvider cancels the deploy's context once each call has gone through, as ctrl+c
// pressed while aws did the work would.
type cancelProvider struct {
	*Fake
	cancel context.CancelFunc
}

// Create implements Provider.
func (p cancelProvider) Create(ctx context.Context, req Request) (Result, error) {
	defer p.cancel()
	return p.Fake.Create(ctx, req)
}

// Update implements Provider.
func (p cancelProvider) Update(ctx context.Context, req Request) (Result, error) {
	defer p.cancel()
	return p.Fake.Update(ctx, req)
}

// Delete implements Provider.
func (p cancelProvider) Delete(ctx context.Context, req Request) error {
	defer p.cancel()
	return p.Fake.Delete(ctx, req)
}

// TestCancelRecordsWhatWasDone checks a create, update or delete that went through before ctrl+c
// is in the state afterwards, with nothing left pending, so the next deploy plans nothing for it.
func TestCancelRecordsWhatWasDone(t *testing.T) {
	logGroup := func(retention int) []Node {
		return []Node{{ID: "loggroup/api", Type: fakeType, Name: "loggroup/api", Inputs: map[string]any{"retention": retention}}}
	}
	tests := []struct {
		name   string
		before []Node
		after  []Node
	}{
		{name: "create", after: logGroup(30)},
		{name: "update", before: logGroup(30), after: logGroup(60)},
		{name: "delete", before: logGroup(30)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)
			if tt.before != nil {
				if _, err := h.deploy(tt.before); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			e := &Engine{Providers: map[string]Provider{fakeType: cancelProvider{Fake: h.fake, cancel: cancel}}, Concurrency: 1}
			st, etag := h.read()
			plan, err := e.Plan(ctx, tt.after, st)
			if err != nil {
				t.Fatal(err)
			}
			_ = e.Apply(ctx, plan, tt.after, NewStore(h.backend, st, etag, "test"))
			if ctx.Err() == nil {
				t.Fatal("the provider did not cancel the context")
			}

			got, _ := h.read()
			r := got.Resources["loggroup/api"]
			switch {
			case tt.after == nil && r != nil:
				t.Errorf("the deleted log group is still in the state: %+v", r)
			case tt.after != nil && (r == nil || r.Pending != nil):
				t.Errorf("the log group in the state = %+v, want it recorded with nothing pending", r)
			}
			plan, err = h.deploy(tt.after)
			if err != nil || plan.HasChanges() {
				t.Errorf("the deploy after = %v, %v, want no changes", actions(plan), err)
			}
		})
	}
}
