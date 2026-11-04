package config

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func readFixture(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile("testdata/orders.json")
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// TestDecode checks the fixture decodes, values included.
func TestDecode(t *testing.T) {
	p, err := Decode(readFixture(t))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if p.App != "orders" || p.Stage != "dev" || len(p.Functions) != 2 || len(p.Resources) != 3 {
		t.Fatalf("Decode = %+v", p)
	}
	env := p.Functions["api"].Environment
	if r := env["TABLE_NAME"].Ref; r == nil || *r != (Ref{Resource: "orders", Attribute: "TableName"}) {
		t.Errorf("TABLE_NAME = %+v, want ref(orders, TableName)", env["TABLE_NAME"])
	}
	if s := env["API_KEY"].Secret; s == nil || s.Source != SourceSSM || s.Name != "/orders/api-key" {
		t.Errorf("API_KEY = %+v, want the ssm secret", env["API_KEY"])
	}
	if o := env["PAYMENTS_URL"].Output; o == nil || o.App != "payments" {
		t.Errorf("PAYMENTS_URL = %+v, want output(payments, dev, apiUrl)", env["PAYMENTS_URL"])
	}
	if env["LOG_LEVEL"].Literal != "info" {
		t.Errorf("LOG_LEVEL = %+v, want info", env["LOG_LEVEL"])
	}
	if !p.Functions["api"].IsNode() || !p.Functions["worker"].IsGo() {
		t.Error("runtimes are not told apart")
	}
	if got := Refs(p.Resources["jobs"].Properties); len(got) != 1 || got[0].Resource != "dead" {
		t.Errorf("Refs(jobs) = %v, want the ref to dead", got)
	}
	if err := p.Validate(); err != nil {
		t.Errorf("Validate: %v", err)
	}
}

// TestValueRoundTrip checks a value is written back the way pkl renders it.
func TestValueRoundTrip(t *testing.T) {
	values := []Value{
		{Literal: "plain"},
		{Ref: &Ref{Resource: "orders", Attribute: "Arn"}},
		{Secret: &Secret{Source: SourceSecretsManager, Name: "db", Key: "password", AtRuntime: true}},
		{Output: &OutputRef{App: "payments", Stage: "prod", Output: "apiUrl"}},
	}
	for _, v := range values {
		data, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("Marshal(%+v): %v", v, err)
		}
		var got Value
		if err := json.Unmarshal(data, &got); err != nil {
			t.Fatalf("Unmarshal(%s): %v", data, err)
		}
		again, _ := json.Marshal(got)
		if string(again) != string(data) {
			t.Errorf("round trip of %s gave %s", data, again)
		}
	}
	var v Value
	if err := json.Unmarshal([]byte(`{"$koldstart":"nope"}`), &v); err == nil {
		t.Error("an unknown kind: want an error")
	}
	if err := json.Unmarshal([]byte(`12`), &v); err == nil {
		t.Error("a number: want an error")
	}
}

// TestDecodeRefuses checks a newer schema, a file that amends nothing, and an unknown property
// are refused.
func TestDecodeRefuses(t *testing.T) {
	tests := []struct {
		name string
		data string
		want string
	}{
		{name: "newer schema", data: `{"schemaVersion": 99}`, want: "upgrade koldstart"},
		{name: "not amended", data: `{"app": "orders"}`, want: "must amend"},
		{name: "unknown property", data: `{"schemaVersion": 1, "app": "orders", "nope": 1}`, want: "nope"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Decode([]byte(tt.data))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("Decode = %v, want an error with %q", err, tt.want)
			}
		})
	}
}

// TestValidate checks each kind of dangling name is reported, all at once.
func TestValidate(t *testing.T) {
	tests := []struct {
		name   string
		change func(p *Project)
		want   string
	}{
		{
			name: "ref to nothing",
			change: func(p *Project) {
				p.Functions["api"].Environment["X"] = Value{Ref: &Ref{Resource: "missing", Attribute: "Arn"}}
			},
			want: `ref("missing", "Arn") refers to no function or resource`,
		},
		{
			name: "link to a function",
			change: func(p *Project) {
				p.Functions["api"].Link["worker"] = "read"
			},
			want: `"worker" is not a resource`,
		},
		{
			name: "missing authorizer",
			change: func(p *Project) {
				p.HTTP.Authorizers = nil
			},
			want: `no authorizer "users"`,
		},
		{
			name: "duplicate route",
			change: func(p *Project) {
				fn := p.Functions["worker"]
				fn.Events = append(fn.Events, Event{Type: EventHTTPRoute, Method: "post", Path: "/orders"})
				p.Functions["worker"] = fn
			},
			want: "POST /orders is also a route of",
		},
		{
			name: "ref inside resource properties",
			change: func(p *Project) {
				p.Resources["dead"] = Resource{Type: "AWS::SQS::Queue", Properties: map[string]any{
					"X": map[string]any{marker: kindRef, "resource": "gone", "attribute": "Arn"},
				}}
			},
			want: `resources[dead]: ref("gone", "Arn")`,
		},
		{
			name: "name shared",
			change: func(p *Project) {
				p.Resources["api"] = Resource{Type: "AWS::SQS::Queue"}
			},
			want: `"api" is both a function and a resource`,
		},
		{
			name: "output of itself",
			change: func(p *Project) {
				p.Outputs["self"] = Value{Output: &OutputRef{App: "orders", Stage: "dev", Output: "table"}}
			},
			want: "use ref() instead",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := Decode(readFixture(t))
			if err != nil {
				t.Fatal(err)
			}
			tt.change(p)
			err = p.Validate()
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("Validate = %v, want an error with %q", err, tt.want)
			}
		})
	}
}

type fakeEvaluator struct {
	out   []byte
	props map[string]string
}

func (f *fakeEvaluator) EvalJSON(_ context.Context, _ string, props map[string]string) ([]byte, error) {
	f.props = props
	return f.out, nil
}

// TestLoad checks the stage is passed to pkl, checked, and must come back unchanged.
func TestLoad(t *testing.T) {
	ev := &fakeEvaluator{out: readFixture(t)}
	if _, err := Load(context.Background(), ev, "koldstart.pkl", "dev"); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if ev.props["stage"] != "dev" {
		t.Errorf("stage passed to pkl = %q, want dev", ev.props["stage"])
	}
	if _, err := Load(context.Background(), ev, "koldstart.pkl", "prod"); err == nil || !strings.Contains(err.Error(), "leave stage to --stage") {
		t.Errorf("Load with a stage the file overrides = %v", err)
	}
	for _, stage := range []string{"Prod", "1st", "a_b", strings.Repeat("a", 17), ""} {
		if _, err := Load(context.Background(), ev, "koldstart.pkl", stage); err == nil {
			t.Errorf("Load with stage %q: want an error", stage)
		}
	}
}
