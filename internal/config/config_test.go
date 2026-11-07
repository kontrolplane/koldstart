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
	redrive := p.Resources["jobs"].Properties["RedrivePolicy"].(map[string]any)
	if r, ok := AsRef(redrive["deadLetterTargetArn"]); !ok || r.Resource != "dead" {
		t.Errorf("the dead letter target = %v, want a ref to dead", redrive["deadLetterTargetArn"])
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
			want: `kdst.ref("missing", "Arn") refers to no function or resource`,
		},
		{
			name: "an image with a runtime",
			change: func(p *Project) {
				fn := p.Functions["api"]
				fn.Image = &Image{Dockerfile: "Dockerfile", Context: "."}
				p.Functions["api"] = fn
			},
			want: "a function with an image has no runtime or handler",
		},
		{
			name: "neither runtime nor image",
			change: func(p *Project) {
				fn := p.Functions["api"]
				fn.Runtime, fn.Handler = "", ""
				p.Functions["api"] = fn
			},
			want: "a function needs a runtime and a handler, or an image",
		},
		{
			name: "layers on go",
			change: func(p *Project) {
				fn := p.Functions["worker"]
				fn.Layers = []Value{{Literal: "arn:aws:lambda:eu-west-1:1:layer:x:1"}}
				p.Functions["worker"] = fn
			},
			want: "a go function has no layers",
		},
		{
			name: "include on node",
			change: func(p *Project) {
				fn := p.Functions["api"]
				fn.Bundle.Include = []string{"src"}
				p.Functions["api"] = fn
			},
			want: "only python functions take it",
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
			want: `resources[dead]: kdst.ref("gone", "Arn")`,
		},
		{
			name: "name shared",
			change: func(p *Project) {
				p.Resources["api"] = Resource{Type: "AWS::SQS::Queue"}
			},
			want: `"api" is both a function and a resource`,
		},
		{
			name: "queue hides messages shorter than the timeout",
			change: func(p *Project) {
				fn := p.Functions["worker"]
				fn.Timeout = 60
				p.Functions["worker"] = fn
			},
			want: "set its VisibilityTimeout to at least 60",
		},
		{
			name: "bucket notifications twice",
			change: func(p *Project) {
				p.Resources["uploads"] = Resource{Type: "AWS::S3::Bucket", Properties: map[string]any{"NotificationConfiguration": map[string]any{}}}
				fn := p.Functions["worker"]
				fn.Events = append(fn.Events, Event{Type: EventBucket, Bucket: Value{Ref: &Ref{Resource: "uploads", Attribute: "BucketName"}}})
				p.Functions["worker"] = fn
			},
			want: "sets NotificationConfiguration",
		},
		{
			name: "dead letter target in s3",
			change: func(p *Project) {
				fn := p.Functions["api"]
				fn.DeadLetter = Value{Literal: "arn:aws:s3:::dead"}
				p.Functions["api"] = fn
			},
			want: "deadLetter: it is in s3, and takes sqs, sns alone",
		},
		{
			name: "destination by name",
			change: func(p *Project) {
				fn := p.Functions["api"]
				fn.Async = &Async{OnFailure: Value{Literal: "dead"}}
				p.Functions["api"] = fn
			},
			want: `async.onFailure: "dead" is not an arn`,
		},
		{
			name: "destination a table",
			change: func(p *Project) {
				fn := p.Functions["api"]
				fn.Async = &Async{OnSuccess: Value{Ref: &Ref{Resource: "orders", Attribute: "Arn"}}}
				p.Functions["api"] = fn
			},
			want: "async.onSuccess: it is in dynamodb, and takes sqs, sns, events, lambda alone",
		},
		{
			name: "efs without a vpc",
			change: func(p *Project) {
				fn := p.Functions["api"]
				fn.FileSystem = &FileSystem{AccessPoint: Value{Literal: "arn:aws:elasticfilesystem:eu-west-1:1:access-point/fsap-1"}, MountPath: "/mnt/x"}
				p.Functions["api"] = fn
			},
			want: "a function that mounts efs needs a vpc",
		},
		{
			name: "log levels of text logs",
			change: func(p *Project) {
				fn := p.Functions["api"]
				fn.LogFormat, fn.ApplicationLogLevel = LogFormatText, "WARN"
				p.Functions["api"] = fn
			},
			want: "the log levels apply to json logs alone",
		},
		{
			name: "iam and an authorizer",
			change: func(p *Project) {
				fn := p.Functions["api"]
				fn.Events[0].IAM = true
				p.Functions["api"] = fn
			},
			want: "a route takes iam or an authorizer, not both",
		},
		{
			name: "input and input path",
			change: func(p *Project) {
				fn := p.Functions["worker"]
				fn.Events = append(fn.Events, Event{Type: EventRule, Pattern: []byte(`{"source":["x"]}`), Input: "{}", InputPath: "$.detail"})
				p.Functions["worker"] = fn
			},
			want: "give one of input, inputPath and inputTransformer",
		},
		{
			name: "stream failures to a function",
			change: func(p *Project) {
				fn := p.Functions["worker"]
				fn.Events = append(fn.Events, Event{Type: EventStream, Stream: Value{Literal: "arn:aws:kinesis:eu-west-1:1:stream/x"},
					OnFailure: Value{Ref: &Ref{Resource: "api", Attribute: "Arn"}}})
				p.Functions["worker"] = fn
			},
			want: "onFailure: it is in lambda, and takes sqs, sns, s3 alone",
		},
		{
			name: "more provisioned than reserved",
			change: func(p *Project) {
				fn := p.Functions["worker"]
				ten := 10
				fn.ProvisionedConcurrency = &ten
				p.Functions["worker"] = fn
			},
			want: "provisionedConcurrency 10 is more than the reservedConcurrency of 5",
		},
		{
			name: "snapStart on node",
			change: func(p *Project) {
				fn := p.Functions["api"]
				fn.SnapStart = true
				p.Functions["api"] = fn
			},
			want: "lambda takes snapStart for python functions alone",
		},
		{
			name: "snapStart with provisioned concurrency",
			change: func(p *Project) {
				fn := p.Functions["api"]
				one := 1
				fn.Runtime, fn.SnapStart, fn.ProvisionedConcurrency = RuntimePy313, true, &one
				p.Functions["api"] = fn
			},
			want: "lambda takes snapStart or provisionedConcurrency, not both",
		},
		{
			name: "output of itself",
			change: func(p *Project) {
				p.Outputs["self"] = Value{Output: &OutputRef{App: "orders", Stage: "dev", Output: "table"}}
			},
			want: "use kdst.ref() instead",
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
		if _, err := Load(context.Background(), ev, "koldstart.pkl", stage); err == nil || !strings.Contains(err.Error(), "pass --stage") {
			t.Errorf("Load with stage %q = %v, want an error that says to pass --stage", stage, err)
		}
	}
}

// scoped is a project of app orders on stage dev with one secret, s, as pkl renders it, in a
// function's environment, a join, an output and a resource's properties.
func scoped(s string) []byte {
	return []byte(`{"schemaVersion": 1, "app": "orders", "stage": "dev",
		"functions": {"api": {"runtime": "nodejs22.x", "handler": "a.b",
			"environment": {"KEY": ` + s + `}, "layers": [{"$koldstart": "join", "parts": ["x", ` + s + `]}]}},
		"outputs": {"key": ` + s + `},
		"resources": {"db": {"type": "AWS::RDS::DBInstance", "properties": {"MasterUserPassword": ` + s + `}}}}`)
}

// TestDecodeScopes checks a secret under the app and stage gets its full name wherever it is,
// and one without a scope, or an absolute one, keeps its name.
func TestDecodeScopes(t *testing.T) {
	tests := []struct {
		name   string
		secret string
		want   Secret
	}{
		{
			name:   "kdst.param",
			secret: `{"$koldstart": "secret", "source": "ssm", "name": "api-key", "scope": "stage", "key": "", "atRuntime": false}`,
			want:   Secret{Source: SourceSSM, Name: "/orders/dev/api-key"},
		},
		{
			name:   "kdst.secret with a field",
			secret: `{"$koldstart": "secret", "source": "secretsmanager", "name": "db/main", "scope": "stage", "key": "password", "atRuntime": true}`,
			want:   Secret{Source: SourceSecretsManager, Name: "orders/dev/db/main", Key: "password", AtRuntime: true},
		},
		{
			name:   "kdst.ssm",
			secret: `{"$koldstart": "secret", "source": "ssm", "name": "/shared/key", "scope": "absolute", "key": "", "atRuntime": false}`,
			want:   Secret{Source: SourceSSM, Name: "/shared/key"},
		},
		{
			name:   "kdst.ssm without a slash",
			secret: `{"$koldstart": "secret", "source": "ssm", "name": "key", "scope": "absolute", "key": "", "atRuntime": false}`,
			want:   Secret{Source: SourceSSM, Name: "key"},
		},
		{
			name:   "a schema without scope",
			secret: `{"$koldstart": "secret", "source": "secretsmanager", "name": "db", "key": "", "atRuntime": false}`,
			want:   Secret{Source: SourceSecretsManager, Name: "db"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := Decode(scoped(tt.secret))
			if err != nil {
				t.Fatalf("Decode: %v", err)
			}
			fn := p.Functions["api"]
			for where, v := range map[string]Value{"environment": fn.Environment["KEY"], "join": fn.Layers[0].Join[1], "outputs": p.Outputs["key"]} {
				if v.Secret == nil || *v.Secret != tt.want {
					t.Errorf("%s: %+v, want %+v", where, v.Secret, tt.want)
				}
			}
			prop := p.Resources["db"].Properties["MasterUserPassword"].(map[string]any)
			if _, ok := prop["scope"]; ok || prop["name"] != tt.want.Name {
				t.Errorf("properties: %v, want the name %s and no scope", prop, tt.want.Name)
			}
		})
	}
}

// TestDecodeScopeRefuses checks a name under the app and stage with a leading /, an empty
// segment or .. is refused, naming the helper, where it is and what to use instead.
func TestDecodeScopeRefuses(t *testing.T) {
	tests := []struct {
		name   string
		source string
		secret string
		want   []string
	}{
		{name: "leading slash", source: "ssm", secret: "/orders/api-key", want: []string{`functions.api.environment.KEY: kdst.param("/orders/api-key")`, "after /orders/dev/", "drop the leading /", `kdst.ssm("/orders/api-key")`}},
		{name: "leading slash of a secret", source: "secretsmanager", secret: "/db", want: []string{`kdst.secret("/db")`, "after orders/dev/", `kdst.secretsmanager("/db")`}},
		{name: "empty segment", source: "ssm", secret: "a//b", want: []string{`kdst.param("a//b")`, "no empty segments or ..", "kdst.ssm()"}},
		{name: "dot dot", source: "secretsmanager", secret: "../prod/db", want: []string{`kdst.secret("../prod/db")`, "kdst.secretsmanager()"}},
		{name: "trailing slash", source: "ssm", secret: "a/", want: []string{`kdst.param("a/")`}},
		{name: "empty", source: "ssm", secret: "", want: []string{`kdst.param("")`}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := `{"$koldstart": "secret", "source": "` + tt.source + `", "name": "` + tt.secret + `", "scope": "stage", "key": "", "atRuntime": false}`
			_, err := Decode(scoped(s))
			if err == nil {
				t.Fatal("Decode: want an error")
			}
			for _, want := range tt.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("Decode = %v, want %q in it", err, want)
				}
			}
		})
	}
	if _, err := Decode(scoped(`{"$koldstart": "secret", "source": "ssm", "name": "a", "scope": "nope"}`)); err == nil || !strings.Contains(err.Error(), "absolute or stage") {
		t.Errorf("an unknown scope: Decode = %v, want an error", err)
	}
}
