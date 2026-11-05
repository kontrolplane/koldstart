package resolve

import (
	"context"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	smtypes "github.com/aws/aws-sdk-go-v2/service/secretsmanager/types"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	ssmtypes "github.com/aws/aws-sdk-go-v2/service/ssm/types"

	"github.com/kontrolplane/koldstart/internal/config"
	"github.com/kontrolplane/koldstart/internal/state"
)

// fakeSSM holds parameters by name, and counts the reads.
type fakeSSM struct {
	params map[string]string
	reads  int
}

func (f *fakeSSM) GetParameter(_ context.Context, in *ssm.GetParameterInput, _ ...func(*ssm.Options)) (*ssm.GetParameterOutput, error) {
	f.reads++
	v, ok := f.params[*in.Name]
	if !ok {
		return nil, &ssmtypes.ParameterNotFound{}
	}
	return &ssm.GetParameterOutput{Parameter: &ssmtypes.Parameter{
		Name: in.Name, Value: aws.String(v), ARN: aws.String("arn:aws:ssm:eu-west-1:123456789012:parameter" + *in.Name),
	}}, nil
}

// fakeSecretsManager holds secrets by name.
type fakeSecretsManager map[string]string

func (f fakeSecretsManager) GetSecretValue(_ context.Context, in *secretsmanager.GetSecretValueInput, _ ...func(*secretsmanager.Options)) (*secretsmanager.GetSecretValueOutput, error) {
	v, ok := f[*in.SecretId]
	if !ok {
		return nil, &smtypes.ResourceNotFoundException{}
	}
	return &secretsmanager.GetSecretValueOutput{SecretString: aws.String(v)}, nil
}

func (f fakeSecretsManager) DescribeSecret(_ context.Context, in *secretsmanager.DescribeSecretInput, _ ...func(*secretsmanager.Options)) (*secretsmanager.DescribeSecretOutput, error) {
	if _, ok := f[*in.SecretId]; !ok {
		return nil, &smtypes.ResourceNotFoundException{}
	}
	return &secretsmanager.DescribeSecretOutput{ARN: aws.String("arn:aws:secretsmanager:eu-west-1:123456789012:secret:" + *in.SecretId + "-AbCdEf")}, nil
}

// TestSecrets checks each source resolves to its value, or to its arn at runtime, a field is
// taken from a json secret, and a secret is read once however often it is used.
func TestSecrets(t *testing.T) {
	params := &fakeSSM{params: map[string]string{"/orders/api-key": "k3y"}}
	s := &Secrets{SSM: params, SecretsManager: fakeSecretsManager{
		"db":    `{"username": "app", "password": "pa55", "port": 5432}`,
		"plain": "just a string",
	}}
	ctx := context.Background()
	tests := []struct {
		source, name, key string
		atRuntime         bool
		want              string
	}{
		{source: config.SourceSSM, name: "/orders/api-key", want: "k3y"},
		{source: config.SourceSSM, name: "/orders/api-key", atRuntime: true, want: "arn:aws:ssm:eu-west-1:123456789012:parameter/orders/api-key"},
		{source: config.SourceSecretsManager, name: "plain", want: "just a string"},
		{source: config.SourceSecretsManager, name: "db", key: "password", want: "pa55"},
		{source: config.SourceSecretsManager, name: "db", key: "port", want: "5432"},
		{source: config.SourceSecretsManager, name: "db", key: "password", atRuntime: true, want: "arn:aws:secretsmanager:eu-west-1:123456789012:secret:db-AbCdEf"},
	}
	for _, tt := range tests {
		got, err := s.Resolve(ctx, tt.source, tt.name, tt.key, tt.atRuntime)
		if err != nil || got != tt.want {
			t.Errorf("Resolve(%s, %s, %q, %v) = %q, %v, want %q", tt.source, tt.name, tt.key, tt.atRuntime, got, err, tt.want)
		}
	}
	if _, err := s.Resolve(ctx, config.SourceSSM, "/orders/api-key", "", false); err != nil || params.reads != 2 {
		t.Errorf("a second read of the same parameter went to ssm: %d reads, %v", params.reads, err)
	}

	failures := []struct {
		source, name, key string
		want              string
	}{
		{source: config.SourceSSM, name: "/missing", want: "no ssm parameter /missing"},
		{source: config.SourceSecretsManager, name: "missing", want: "no secret missing"},
		{source: config.SourceSecretsManager, name: "db", key: "token", want: "it has password, port, username"},
		{source: config.SourceSecretsManager, name: "plain", key: "token", want: "not a json object"},
	}
	for _, tt := range failures {
		if _, err := s.Resolve(ctx, tt.source, tt.name, tt.key, false); err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("Resolve(%s, %s, %q) = %v, want an error with %q", tt.source, tt.name, tt.key, err, tt.want)
		}
	}
}

// TestOutputs checks an output of another stage is read from its state, and a stage that is not
// deployed, or an output it does not have, says so.
func TestOutputs(t *testing.T) {
	backend := state.NewMemory()
	payments := state.New("payments", "prod", "eu-west-1", "123456789012")
	payments.Outputs = map[string]string{"apiUrl": "https://pay.example.com"}
	if _, err := backend.Write(context.Background(), payments, ""); err != nil {
		t.Fatal(err)
	}
	o := &Outputs{Backend: backend}
	ctx := context.Background()
	if got, err := o.Output(ctx, "payments", "prod", "apiUrl"); err != nil || got != "https://pay.example.com" {
		t.Errorf("Output = %q, %v", got, err)
	}
	if _, err := o.Output(ctx, "payments", "prod", "nope"); err == nil || !strings.Contains(err.Error(), "it has apiUrl") {
		t.Errorf("a missing output = %v", err)
	}
	if _, err := o.Output(ctx, "payments", "dev", "apiUrl"); err == nil || !strings.Contains(err.Error(), "not deployed") {
		t.Errorf("a stage not deployed = %v", err)
	}
}
