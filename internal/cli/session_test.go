package cli

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials/ssocreds"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/aws/smithy-go"

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
	tests := []struct {
		role string
		want string
	}{
		{role: "arn:aws:iam::210987654321:role/koldstart-outputs", want: "210987654321"},
		{role: "koldstart-outputs"},
		{role: "arn:aws:s3:::bucket"},
		{role: "arn:aws:iam::12:role/x"},
	}
	for _, tt := range tests {
		t.Run(tt.role, func(t *testing.T) {
			got, err := accountOf(tt.role)
			if tt.want == "" {
				if err == nil {
					t.Errorf("accountOf(%q) = %q, want an error", tt.role, got)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Errorf("accountOf(%q) = %q, %v, want %q", tt.role, got, err, tt.want)
			}
		})
	}
}

// sourced is credentials that say where they came from, as the sdk's do.
type sourced []aws.CredentialSource

func (sourced) Retrieve(context.Context) (aws.Credentials, error) {
	return aws.Credentials{}, nil
}

func (s sourced) ProviderSources() []aws.CredentialSource {
	return s
}

// TestAccountError checks a failure to find the account says what to do, from where the
// credentials came from and why they failed.
func TestAccountError(t *testing.T) {
	refused := &smithy.GenericAPIError{Code: "InvalidClientTokenId", Message: "The security token included in the request is invalid."}
	tests := []struct {
		name    string
		err     error
		sources sourced
		profile string
		want    string
	}{
		{
			name:    "sso session never started",
			err:     fmt.Errorf("get credentials: %w", fs.ErrNotExist),
			sources: sourced{aws.CredentialSourceProfileSSO, aws.CredentialSourceSSO},
			profile: "sandbox",
			want:    "run aws sso login --profile sandbox",
		},
		{
			name: "sso token expired",
			err:  &ssocreds.InvalidTokenError{},
			want: "run aws sso login",
		},
		{
			name:    "aws login",
			err:     errors.New("get credentials: expired"),
			sources: sourced{aws.CredentialSourceProfileLogin},
			profile: "dev",
			want:    "run aws login --profile dev",
		},
		{
			name:    "keys in the environment",
			err:     refused,
			sources: sourced{aws.CredentialSourceEnvVars},
			want:    "AWS_ACCESS_KEY_ID",
		},
		{
			name:    "keys in a profile",
			err:     refused,
			sources: sourced{aws.CredentialSourceProfile},
			profile: "ci",
			want:    "check the access keys of profile ci",
		},
		{
			name:    "expired",
			err:     &smithy.GenericAPIError{Code: "ExpiredToken"},
			sources: sourced{aws.CredentialSourceEnvVars},
			want:    "have expired",
		},
		{
			name:    "no region",
			err:     fmt.Errorf("request send failed: %w", &net.DNSError{Err: "no such host", Name: "sts.nowhere-1.amazonaws.com"}),
			sources: sourced{aws.CredentialSourceProfile},
			want:    "check the region",
		},
		{
			name:    "no credentials",
			err:     errors.New("no EC2 IMDS role found"),
			sources: sourced{aws.CredentialSourceIMDS},
			want:    "no aws credentials: pass --profile",
		},
		{
			name:    "something else",
			err:     errors.New("connection reset"),
			sources: sourced{aws.CredentialSourceProfile},
			want:    "finding the aws account: connection reset",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := accountError(tt.err, aws.Config{Region: "nowhere-1", Credentials: tt.sources}, tt.profile)
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("accountError = %q, want it to contain %q", err, tt.want)
			}
		})
	}
}

// TestAWSConfigErrors checks a profile that is not in the shared aws config, and a missing region,
// say what to do.
func TestAWSConfigErrors(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "config")
	if err := os.WriteFile(file, []byte("[profile noregion]\naws_access_key_id = AKIAEXAMPLE\naws_secret_access_key = secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AWS_CONFIG_FILE", file)
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", filepath.Join(dir, "credentials"))
	t.Setenv("AWS_REGION", "")
	t.Setenv("AWS_DEFAULT_REGION", "")
	t.Setenv("AWS_PROFILE", "")

	tests := []struct {
		name    string
		profile string
		want    string
	}{
		{name: "missing profile", profile: "nope", want: `no aws profile "nope" in the shared aws config: set it up with aws configure sso --profile nope, or pass another --profile`},
		{name: "no region", profile: "noregion", want: "no aws region: set region in the configuration or the aws profile, or pass --region"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := awsConfig(context.Background(), &Options{Profile: tt.profile}, "")
			if err == nil || err.Error() != tt.want {
				t.Errorf("awsConfig = %v, want %q", err, tt.want)
			}
		})
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
