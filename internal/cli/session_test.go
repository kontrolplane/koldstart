package cli

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/sts"

	"github.com/kontrolplane/koldstart/internal/awstest"
	"github.com/kontrolplane/koldstart/internal/config"
	"github.com/kontrolplane/koldstart/internal/engine"
	"github.com/kontrolplane/koldstart/internal/resolve"
	"github.com/kontrolplane/koldstart/internal/state"
)

// TestPathStyle checks buckets go in the path with an endpoint of an emulator, from the
// environment or a profile's endpoint_url, and in the host name without one.
func TestPathStyle(t *testing.T) {
	tests := []struct {
		name     string
		endpoint *string
		s3Env    string
		want     bool
	}{
		{name: "aws", want: false},
		{name: "an endpoint", endpoint: aws.String("http://127.0.0.1:4566"), want: true},
		{name: "an endpoint for s3", s3Env: "http://127.0.0.1:4566", want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("AWS_ENDPOINT_URL_S3", tt.s3Env)
			s := newSession(aws.Config{Region: "eu-west-1", BaseEndpoint: tt.endpoint}, "123456789012")
			if got := s.S3.Options().UsePathStyle; got != tt.want {
				t.Errorf("UsePathStyle = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestAccountOf checks the account is read from a role's arn, and anything else refused.
func TestAccountOf(t *testing.T) {
	if got, err := accountOf("arn:aws:iam::210987654321:role/koldstart-outputs"); err != nil || got != "210987654321" {
		t.Errorf("accountOf = %q, %v", got, err)
	}
	for _, bad := range []string{"koldstart-outputs", "arn:aws:s3:::bucket", "arn:aws:iam::12:role/x"} {
		if _, err := accountOf(bad); err == nil {
			t.Errorf("accountOf(%q) took it", bad)
		}
	}
}

// TestStateInEmulator writes the outputs of a stage in another region and reads them back as
// another stage does: with its own credentials, and as a role it assumes.
func TestStateInEmulator(t *testing.T) {
	cfg := awstest.Emulator(t)
	ctx := context.Background()
	ident, err := sts.NewFromConfig(cfg).GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		t.Fatalf("GetCallerIdentity: %v", err)
	}
	account := aws.ToString(ident.Account)
	sess := newSession(cfg, account)
	const other = "us-east-1"
	there, err := state.Open(ctx, sess.in(other).S3, account, other)
	if err != nil {
		t.Fatalf("opening the state bucket in %s: %v", other, err)
	}
	app := awstest.Name("payments")
	st := state.New(app, "prod", other, account)
	st.Outputs = map[string]string{"apiUrl": "https://us.example.com"}
	if _, err := there.Write(ctx, st, ""); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = there.Delete(context.Background(), app, "prod") })

	role, err := iam.NewFromConfig(cfg).CreateRole(ctx, &iam.CreateRoleInput{
		RoleName:                 aws.String(app),
		AssumeRolePolicyDocument: aws.String(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"AWS":"arn:aws:iam::` + account + `:root"},"Action":"sts:AssumeRole"}]}`),
	})
	if err != nil {
		t.Fatalf("CreateRole: %v", err)
	}
	t.Cleanup(func() {
		_, _ = iam.NewFromConfig(cfg).DeleteRole(context.Background(), &iam.DeleteRoleInput{RoleName: aws.String(app)})
	})

	outputs := &resolve.Outputs{Backend: state.NewMemory(), Elsewhere: sess.stateIn}
	for _, ref := range []config.OutputRef{
		{App: app, Stage: "prod", Output: "apiUrl", Region: other},
		{App: app, Stage: "prod", Output: "apiUrl", Region: other, Role: aws.ToString(role.Role.Arn)},
	} {
		if got, err := outputs.Output(ctx, ref); err != nil || got != "https://us.example.com" {
			t.Errorf("the output %s = %q, %v", engine.DescribeOutput(ref), got, err)
		}
	}
	if _, err := outputs.Output(ctx, config.OutputRef{App: app, Stage: "prod", Output: "apiUrl", Region: "eu-central-1"}); err == nil {
		t.Error("an output of a region the stage is not in was read")
	}
}
