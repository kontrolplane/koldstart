package cloudcontrol

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"strings"
	"sync"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudcontrol"
	"github.com/aws/aws-sdk-go-v2/service/cloudcontrol/types"

	"github.com/kontrolplane/koldstart/internal/engine"
)

// fake is cloud control as aws runs it, for one type of table: named by TableName, with an Arn
// aws sets, client tokens that return the request they started, and a request that is in
// progress the first time it is looked at.
type fake struct {
	mu       sync.Mutex
	tables   map[string]map[string]any
	requests map[string]*types.ProgressEvent
	byClient map[string]string
	looked   map[string]bool
	patches  []string
	creates  int
	failNext types.HandlerErrorCode
}

func newFake() *fake {
	return &fake{
		tables: map[string]map[string]any{}, requests: map[string]*types.ProgressEvent{},
		byClient: map[string]string{}, looked: map[string]bool{},
	}
}

func (f *fake) request(op types.Operation, id string, code types.HandlerErrorCode) string {
	token := fmt.Sprintf("request-%d", len(f.requests)+1)
	status := types.OperationStatusSuccess
	if code != "" {
		status = types.OperationStatusFailed
	}
	f.requests[token] = &types.ProgressEvent{RequestToken: aws.String(token), Operation: op, Identifier: aws.String(id), OperationStatus: status, ErrorCode: code, StatusMessage: aws.String(string(code))}
	return token
}

func (f *fake) CreateResource(_ context.Context, in *cloudcontrol.CreateResourceInput, _ ...func(*cloudcontrol.Options)) (*cloudcontrol.CreateResourceOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if in.ClientToken != nil {
		if token, ok := f.byClient[*in.ClientToken]; ok {
			return &cloudcontrol.CreateResourceOutput{ProgressEvent: f.requests[token]}, nil
		}
	}
	var props map[string]any
	_ = json.Unmarshal([]byte(*in.DesiredState), &props)
	name, _ := props["TableName"].(string)
	var token string
	switch {
	case f.failNext != "":
		token = f.request(types.OperationCreate, name, f.failNext)
		f.failNext = ""
	case f.tables[name] != nil:
		token = f.request(types.OperationCreate, name, types.HandlerErrorCodeAlreadyExists)
	default:
		props["Arn"] = "arn:aws:dynamodb:eu-west-1:123456789012:table/" + name
		f.tables[name] = props
		f.creates++
		token = f.request(types.OperationCreate, name, "")
	}
	if in.ClientToken != nil {
		f.byClient[*in.ClientToken] = token
	}
	return &cloudcontrol.CreateResourceOutput{ProgressEvent: f.requests[token]}, nil
}

func (f *fake) GetResource(_ context.Context, in *cloudcontrol.GetResourceInput, _ ...func(*cloudcontrol.Options)) (*cloudcontrol.GetResourceOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	props, ok := f.tables[*in.Identifier]
	if !ok {
		return nil, &types.ResourceNotFoundException{Message: aws.String("not found")}
	}
	data, _ := json.Marshal(props)
	return &cloudcontrol.GetResourceOutput{ResourceDescription: &types.ResourceDescription{Identifier: in.Identifier, Properties: aws.String(string(data))}}, nil
}

func (f *fake) UpdateResource(_ context.Context, in *cloudcontrol.UpdateResourceInput, _ ...func(*cloudcontrol.Options)) (*cloudcontrol.UpdateResourceOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	props, ok := f.tables[*in.Identifier]
	if !ok {
		return nil, &types.ResourceNotFoundException{Message: aws.String("not found")}
	}
	f.patches = append(f.patches, *in.PatchDocument)
	var ops []Op
	_ = json.Unmarshal([]byte(*in.PatchDocument), &ops)
	for _, op := range ops {
		key := strings.TrimPrefix(op.Path, "/")
		if op.Op == "remove" {
			delete(props, key)
		} else {
			props[key] = op.Value
		}
	}
	return &cloudcontrol.UpdateResourceOutput{ProgressEvent: f.requests[f.request(types.OperationUpdate, *in.Identifier, "")]}, nil
}

func (f *fake) DeleteResource(_ context.Context, in *cloudcontrol.DeleteResourceInput, _ ...func(*cloudcontrol.Options)) (*cloudcontrol.DeleteResourceOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.tables[*in.Identifier]; !ok {
		return nil, &types.ResourceNotFoundException{Message: aws.String("not found")}
	}
	delete(f.tables, *in.Identifier)
	return &cloudcontrol.DeleteResourceOutput{ProgressEvent: f.requests[f.request(types.OperationDelete, *in.Identifier, "")]}, nil
}

func (f *fake) GetResourceRequestStatus(_ context.Context, in *cloudcontrol.GetResourceRequestStatusInput, _ ...func(*cloudcontrol.Options)) (*cloudcontrol.GetResourceRequestStatusOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	e, ok := f.requests[*in.RequestToken]
	if !ok {
		return nil, &types.RequestTokenNotFoundException{Message: aws.String("no request")}
	}
	if !f.looked[*in.RequestToken] {
		f.looked[*in.RequestToken] = true
		inProgress := *e
		inProgress.OperationStatus = types.OperationStatusInProgress
		return &cloudcontrol.GetResourceRequestStatusOutput{ProgressEvent: &inProgress}, nil
	}
	return &cloudcontrol.GetResourceRequestStatusOutput{ProgressEvent: e}, nil
}

type fakeSchemas map[string]*Schema

func (f fakeSchemas) Schema(_ context.Context, typ string) (*Schema, error) {
	if s, ok := f[typ]; ok {
		return s, nil
	}
	return nil, errors.New("no schema")
}

const tableType = "AWS::DynamoDB::Table"

func table(name string, extra map[string]any) json.RawMessage {
	props := map[string]any{"TableName": name, "BillingMode": "PAY_PER_REQUEST", "KeySchema": []any{map[string]any{"AttributeName": "id", "KeyType": "HASH"}}}
	maps.Copy(props, extra)
	data, _ := json.Marshal(Inputs{Type: tableType, Properties: props})
	return data
}

func provider(f *fake) Provider {
	return Provider{
		Client:  f,
		Schemas: fakeSchemas{tableType: {CreateOnly: []string{"TableName", "KeySchema"}, ReadOnly: []string{"Arn", "StreamArn"}, Provisionable: true}},
		Poll:    1,
	}
}

// TestCreate checks a create waits for its request, records its token, and returns the
// properties aws set as attributes.
func TestCreate(t *testing.T) {
	f := newFake()
	var recorded string
	res, err := provider(f).Create(context.Background(), engine.Request{
		ID: "resource/orders", Inputs: table("orders", nil), ClientToken: "koldstart-1",
		Progress: func(token string) error { recorded = token; return nil },
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if res.PhysicalID != "orders" || res.Attributes["Arn"] != "arn:aws:dynamodb:eu-west-1:123456789012:table/orders" || res.Attributes["TableName"] != "orders" {
		t.Errorf("Create = %+v", res)
	}
	if !strings.Contains(res.Attributes["KeySchema"], `"AttributeName":"id"`) {
		t.Errorf("a list property is not kept as json: %q", res.Attributes["KeySchema"])
	}
	if recorded == "" {
		t.Error("the request token was not recorded before waiting")
	}
}

// TestCreateAfterCrash checks a create retried after a deploy died, before it could record the
// request, makes no second resource; and one retried after the first failed starts over.
func TestCreateAfterCrash(t *testing.T) {
	f := newFake()
	p := provider(f)
	died := errors.New("died")
	_, err := p.Create(context.Background(), engine.Request{
		ID: "resource/orders", Inputs: table("orders", nil), ClientToken: "koldstart-1",
		Progress: func(string) error { return died },
	})
	if !errors.Is(err, died) {
		t.Fatalf("Create with a dying deploy = %v", err)
	}
	res, err := p.Create(context.Background(), engine.Request{ID: "resource/orders", Inputs: table("orders", nil), ClientToken: "koldstart-1"})
	if err != nil || res.PhysicalID != "orders" || f.creates != 1 {
		t.Fatalf("the retry = %+v, %v, after %d creates", res, err, f.creates)
	}

	f.failNext = types.HandlerErrorCodeThrottling
	var token string
	_, err = p.Create(context.Background(), engine.Request{
		ID: "resource/jobs", Inputs: table("jobs", nil), ClientToken: "koldstart-2",
		Progress: func(t string) error { token = t; return nil },
	})
	if err == nil {
		t.Fatal("a create that failed: want an error")
	}
	if _, err := p.Create(context.Background(), engine.Request{ID: "resource/jobs", Inputs: table("jobs", nil), ClientToken: "koldstart-2", Token: token}); err != nil {
		t.Fatalf("a retry after a failure: %v", err)
	}
	if f.creates != 2 {
		t.Errorf("%d creates, want the failed one tried again", f.creates)
	}
}

// TestCreateExists checks a resource that exists already, not by koldstart, says how to take it over.
func TestCreateExists(t *testing.T) {
	f := newFake()
	f.tables["orders"] = map[string]any{"TableName": "orders"}
	_, err := provider(f).Create(context.Background(), engine.Request{ID: "resource/orders", Inputs: table("orders", nil), ClientToken: "koldstart-1"})
	if err == nil || !strings.Contains(err.Error(), "kdst import orders") {
		t.Fatalf("Create of a table that exists = %v", err)
	}
}

// TestReadByToken checks a resource a crashed deploy left as a request token is found.
func TestReadByToken(t *testing.T) {
	f := newFake()
	p := provider(f)
	var token string
	_, _ = p.Create(context.Background(), engine.Request{ID: "resource/orders", Inputs: table("orders", nil), Progress: func(t string) error { token = t; return errors.New("died") }})
	res, ok, err := p.Read(context.Background(), engine.Request{ID: "resource/orders", Inputs: table("orders", nil), Token: token})
	if err != nil || !ok || res.PhysicalID != "orders" {
		t.Fatalf("Read by token = %+v, %v, %v", res, ok, err)
	}
	if _, ok, _ := p.Read(context.Background(), engine.Request{ID: "resource/x", Inputs: table("x", nil)}); ok {
		t.Error("Read of nothing found something")
	}
}

// TestUpdate checks an update patches what changed alone, never what aws sets, and removes what
// the configuration dropped.
func TestUpdate(t *testing.T) {
	f := newFake()
	p := provider(f)
	if _, err := p.Create(context.Background(), engine.Request{ID: "resource/orders", Inputs: table("orders", map[string]any{"Tags": []any{"a"}})}); err != nil {
		t.Fatal(err)
	}
	prior := table("orders", map[string]any{"Tags": []any{"a"}, "Arn": "set by aws"})
	desired := table("orders", map[string]any{"TimeToLiveSpecification": map[string]any{"Enabled": true}, "BillingMode": "PROVISIONED"})
	if _, err := p.Update(context.Background(), engine.Request{ID: "resource/orders", PhysicalID: "orders", Prior: prior, Inputs: desired}); err != nil {
		t.Fatal(err)
	}
	if len(f.patches) != 1 {
		t.Fatalf("%d patches", len(f.patches))
	}
	var ops []Op
	_ = json.Unmarshal([]byte(f.patches[0]), &ops)
	got := map[string]string{}
	for _, op := range ops {
		got[op.Path] = op.Op
	}
	want := map[string]string{"/TimeToLiveSpecification": "add", "/Tags": "remove", "/BillingMode": "replace"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("patch = %s, want %v", f.patches[0], want)
	}

	if _, err := p.Update(context.Background(), engine.Request{ID: "resource/orders", PhysicalID: "orders", Prior: desired, Inputs: desired}); err != nil || len(f.patches) != 1 {
		t.Errorf("an update without changes sent a patch: %v, %v", f.patches, err)
	}
}

// TestUpdateAdopting checks taking over a resource patches only what the configuration sets.
func TestUpdateAdopting(t *testing.T) {
	f := newFake()
	f.tables["orders"] = map[string]any{"TableName": "orders", "BillingMode": "PROVISIONED", "Tags": []any{"theirs"}}
	if _, err := provider(f).Update(context.Background(), engine.Request{ID: "resource/orders", PhysicalID: "orders", Inputs: table("orders", nil)}); err != nil {
		t.Fatal(err)
	}
	if len(f.patches) != 1 || strings.Contains(f.patches[0], "remove") {
		t.Errorf("adopting patched %v, want no removes", f.patches)
	}
}

// TestReplaces checks a create only property needs a new resource, and the rest does not.
func TestReplaces(t *testing.T) {
	p := provider(newFake())
	if p.Replaces(table("orders", nil), table("orders", map[string]any{"BillingMode": "PROVISIONED"})) {
		t.Error("a billing mode change replaces the table")
	}
	if !p.Replaces(table("orders", nil), table("orders-v2", nil)) {
		t.Error("a new table name does not replace the table")
	}
	other, _ := json.Marshal(Inputs{Type: "AWS::SQS::Queue"})
	if !p.Replaces(table("orders", nil), other) {
		t.Error("a new type does not replace the resource")
	}
	if (Provider{Client: newFake()}).Replaces(table("orders", nil), table("orders-v2", nil)) {
		t.Error("without schemas, a change replaces")
	}
}

// TestDelete checks a delete waits, and one of something gone is fine.
func TestDelete(t *testing.T) {
	f := newFake()
	p := provider(f)
	if _, err := p.Create(context.Background(), engine.Request{ID: "resource/orders", Inputs: table("orders", nil)}); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := p.Delete(context.Background(), engine.Request{ID: "resource/orders", PhysicalID: "orders", Prior: table("orders", nil)}); err != nil {
			t.Fatalf("Delete: %v", err)
		}
	}
	if len(f.tables) != 0 {
		t.Errorf("the table is still there: %v", f.tables)
	}
}

// TestParseSchema checks create only and read only properties are read by their top level.
func TestParseSchema(t *testing.T) {
	s, err := ParseSchema(`{"createOnlyProperties": ["/properties/TableName", "/properties/KeySchema/0/AttributeName", "/properties/KeySchema"], "readOnlyProperties": ["/properties/Arn"]}`)
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(s.CreateOnly) != "[TableName KeySchema]" || fmt.Sprint(s.ReadOnly) != "[Arn]" {
		t.Errorf("ParseSchema = %+v", s)
	}
}

// TestPatchEscapes checks keys with / and ~ are escaped in the pointer.
func TestPatchEscapes(t *testing.T) {
	ops := Patch(map[string]any{}, map[string]any{"a/b~c": 1.0}, nil)
	if len(ops) != 1 || ops[0].Path != "/a~1b~0c" {
		t.Errorf("Patch = %+v", ops)
	}
}
