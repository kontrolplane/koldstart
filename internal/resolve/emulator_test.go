package resolve_test

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	ssmtypes "github.com/aws/aws-sdk-go-v2/service/ssm/types"

	"github.com/kontrolplane/koldstart/internal/awstest"
	"github.com/kontrolplane/koldstart/internal/config"
	"github.com/kontrolplane/koldstart/internal/resolve"
)

// TestSecretsEmulator reads a secure string parameter and a field of a json secret from the
// emulator, and their arns for reading at runtime.
func TestSecretsEmulator(t *testing.T) {
	cfg := awstest.Emulator(t)
	ctx := context.Background()
	param := "/" + awstest.Name("orders") + "/api-key"
	secret := awstest.Name("db")
	ssmClient, smClient := ssm.NewFromConfig(cfg), secretsmanager.NewFromConfig(cfg)
	if _, err := ssmClient.PutParameter(ctx, &ssm.PutParameterInput{Name: &param, Value: aws.String("k3y"), Type: ssmtypes.ParameterTypeSecureString}); err != nil {
		t.Fatal(err)
	}
	if _, err := smClient.CreateSecret(ctx, &secretsmanager.CreateSecretInput{Name: &secret, SecretString: aws.String(`{"password": "pa55"}`)}); err != nil {
		t.Fatal(err)
	}
	s := &resolve.Secrets{SSM: ssmClient, SecretsManager: smClient}
	if got, err := s.Resolve(ctx, config.SourceSSM, param, "", false); err != nil || got != "k3y" {
		t.Errorf("the parameter = %q, %v", got, err)
	}
	if got, err := s.Resolve(ctx, config.SourceSecretsManager, secret, "password", false); err != nil || got != "pa55" {
		t.Errorf("the secret's field = %q, %v", got, err)
	}
	for _, source := range []struct{ source, name string }{{config.SourceSSM, param}, {config.SourceSecretsManager, secret}} {
		if got, err := s.Resolve(ctx, source.source, source.name, "", true); err != nil || len(got) < 4 || got[:4] != "arn:" {
			t.Errorf("the arn of %s = %q, %v", source.name, got, err)
		}
	}
}
