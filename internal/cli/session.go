package cli

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"slices"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials/ssocreds"
	"github.com/aws/aws-sdk-go-v2/credentials/stscreds"
	"github.com/aws/aws-sdk-go-v2/service/apigatewayv2"
	awscloudcontrol "github.com/aws/aws-sdk-go-v2/service/cloudcontrol"
	"github.com/aws/aws-sdk-go-v2/service/cloudformation"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	elb "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/scheduler"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"github.com/aws/aws-sdk-go-v2/service/sns"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/aws/smithy-go"

	"github.com/kontrolplane/koldstart/internal/engine"
	"github.com/kontrolplane/koldstart/internal/provider/awsp"
	"github.com/kontrolplane/koldstart/internal/provider/cloudcontrol"
	"github.com/kontrolplane/koldstart/internal/resolve"
	"github.com/kontrolplane/koldstart/internal/state"
)

// session is an aws account and region to work in, with its state bucket and the engine.
type session struct {
	cfg     aws.Config
	Account string
	Region  string
	// Caller is the arn of who the credentials belong to.
	Caller string
	S3     *s3.Client
	Lambda *lambda.Client
	Logs   *cloudwatchlogs.Client
	// CloudControl applies resources, with the schemas of their types.
	CloudControl *awscloudcontrol.Client
	Schemas      *cloudcontrol.Registry
	// Secrets reads ssm parameters and secrets manager secrets, each once per run.
	Secrets *resolve.Secrets
}

// awsConfig loads the shared aws config for the global flags, in region, or in the profile's
// region when region is empty.
func awsConfig(ctx context.Context, opts *Options, region string) (aws.Config, error) {
	var loaders []func(*awsconfig.LoadOptions) error
	if opts.Profile != "" {
		loaders = append(loaders, awsconfig.WithSharedConfigProfile(opts.Profile))
	}
	if region != "" {
		loaders = append(loaders, awsconfig.WithRegion(region))
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx, loaders...)
	if missing, ok := errors.AsType[awsconfig.SharedConfigProfileNotExistError](err); ok {
		return aws.Config{}, fmt.Errorf("no aws profile %q in the shared aws config: set it up with aws configure sso --profile %[1]s, or pass another --profile", missing.Profile)
	}
	if err != nil {
		return aws.Config{}, fmt.Errorf("loading the aws config: %w", err)
	}
	if cfg.Region == "" {
		return aws.Config{}, errors.New("no aws region: set region in the configuration or the aws profile, or pass --region")
	}
	return cfg, nil
}

// openSession connects to the account the credentials belong to, in region.
func openSession(ctx context.Context, opts *Options, region string) (*session, error) {
	cfg, err := awsConfig(ctx, opts, region)
	if err != nil {
		return nil, err
	}
	ident, err := sts.NewFromConfig(cfg).GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		return nil, accountError(err, cfg, opts.Profile)
	}
	s := newSession(cfg, aws.ToString(ident.Account))
	s.Caller = aws.ToString(ident.Arn)
	return s, nil
}

// The error codes of credentials aws refused, and of credentials that have expired.
var (
	refusedCodes = []string{"InvalidClientTokenId", "SignatureDoesNotMatch", "UnrecognizedClientException", "IncompleteSignature", "AuthFailure"}
	expiredCodes = []string{"ExpiredToken", "ExpiredTokenException", "RequestExpired", "TokenRefreshRequired"}
)

// accountError says what to do when sts could not say whose cfg's credentials are, by where they
// came from and why they failed: sign in again, fix the keys, set up a profile, or check the
// region. Where there are no credentials at all, the sdk's error is only logged.
func accountError(err error, cfg aws.Config, profile string) error {
	var sources []aws.CredentialSource
	if p, ok := cfg.Credentials.(aws.CredentialProviderSource); ok {
		sources = p.ProviderSources()
	}
	from := func(want ...aws.CredentialSource) bool {
		return slices.ContainsFunc(sources, func(s aws.CredentialSource) bool { return slices.Contains(want, s) })
	}
	withProfile := ""
	if profile != "" {
		withProfile = " --profile " + profile
	}
	var code string
	if apiErr, ok := errors.AsType[smithy.APIError](err); ok {
		code = apiErr.ErrorCode()
	}
	_, invalidToken := errors.AsType[*ssocreds.InvalidTokenError](err)
	_, offline := errors.AsType[*net.DNSError](err)

	switch {
	case offline:
		return fmt.Errorf("finding the aws account: %w\naws cannot be reached in %s: check the region, and that this machine is online", err, cfg.Region)
	case invalidToken || from(aws.CredentialSourceSSO, aws.CredentialSourceProfileSSO, aws.CredentialSourceSSOLegacy, aws.CredentialSourceProfileSSOLegacy):
		return fmt.Errorf("finding the aws account: %w\nthe sso session has expired or was never started: run aws sso login%s", err, withProfile)
	case from(aws.CredentialSourceLogin, aws.CredentialSourceProfileLogin):
		return fmt.Errorf("finding the aws account: %w\nthe aws login session has expired: run aws login%s", err, withProfile)
	case slices.Contains(expiredCodes, code):
		return fmt.Errorf("finding the aws account: %w\nthe aws credentials have expired: get new ones, or pass another --profile", err)
	case slices.Contains(refusedCodes, code) && from(aws.CredentialSourceEnvVars):
		return fmt.Errorf("finding the aws account: %w\naws refused the keys in AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY: check them, or unset them to use a profile", err)
	case slices.Contains(refusedCodes, code):
		return fmt.Errorf("finding the aws account: %w\naws refused the credentials: check the access keys of profile %s, or pass another --profile", err, cmp.Or(profile, "default"))
	case code == "" && (len(sources) == 0 || slices.Equal(sources, []aws.CredentialSource{aws.CredentialSourceIMDS})):
		slog.Debug("no aws credentials", "error", err)
		return errors.New("no aws credentials: pass --profile or set AWS_PROFILE, or set up a profile with aws configure sso")
	}
	return fmt.Errorf("finding the aws account: %w", err)
}

// newSession makes the clients for an account, in the config's region. With an endpoint of an
// emulator, which answers on one host, buckets go in the path rather than the host name. The
// endpoint is set by AWS_ENDPOINT_URL, or by endpoint_url in the profile.
func newSession(cfg aws.Config, account string) *session {
	return &session{
		cfg:     cfg,
		Account: account,
		Region:  cfg.Region,
		S3: s3.NewFromConfig(cfg, func(o *s3.Options) {
			o.UsePathStyle = cfg.BaseEndpoint != nil || os.Getenv("AWS_ENDPOINT_URL_S3") != ""
		}),
		Lambda:       lambda.NewFromConfig(cfg),
		Logs:         cloudwatchlogs.NewFromConfig(cfg),
		CloudControl: awscloudcontrol.NewFromConfig(cfg),
		Schemas:      &cloudcontrol.Registry{Client: cloudformation.NewFromConfig(cfg)},
		Secrets:      &resolve.Secrets{SSM: ssm.NewFromConfig(cfg), SecretsManager: secretsmanager.NewFromConfig(cfg)},
	}
}

// in returns the session in another region of the same account.
func (s *session) in(region string) *session {
	if region == s.Region {
		return s
	}
	cfg := s.cfg.Copy()
	cfg.Region = region
	other := newSession(cfg, s.Account)
	other.Caller = s.Caller
	return other
}

// engine returns an engine with the providers for this session.
func (s *session) engine(tags map[string]string) *engine.Engine {
	apigw := apigatewayv2.NewFromConfig(s.cfg)
	return &engine.Engine{
		Providers: map[string]engine.Provider{
			awsp.TypeLogGroup:        awsp.LogGroup{Client: s.Logs},
			awsp.TypeRole:            awsp.Role{Client: iam.NewFromConfig(s.cfg)},
			awsp.TypeFunction:        awsp.Function{Client: s.Lambda},
			awsp.TypeURL:             awsp.URL{Client: s.Lambda},
			awsp.TypePermission:      awsp.Permission{Client: s.Lambda},
			awsp.TypeHTTPAPI:         awsp.HTTPAPI{Client: apigw, Region: s.Region},
			awsp.TypeIntegration:     awsp.Integration{Client: apigw},
			awsp.TypeRoute:           awsp.Route{Client: apigw},
			awsp.TypeAuthorizer:      awsp.Authorizer{Client: apigw},
			awsp.TypeDomain:          awsp.Domain{Client: apigw},
			awsp.TypeSchedule:        awsp.Schedule{Client: scheduler.NewFromConfig(s.cfg)},
			awsp.TypeMapping:         awsp.Mapping{Client: s.Lambda},
			awsp.TypeSubscription:    awsp.Subscription{Client: sns.NewFromConfig(s.cfg)},
			awsp.TypeNotification:    awsp.Notification{Client: s.S3, Account: s.Account},
			awsp.TypeRule:            awsp.Rule{Client: eventbridge.NewFromConfig(s.cfg)},
			awsp.TypeTargetGroup:     awsp.TargetGroup{Client: elb.NewFromConfig(s.cfg)},
			awsp.TypeTargets:         awsp.Targets{Client: elb.NewFromConfig(s.cfg)},
			awsp.TypeListenerRule:    awsp.ListenerRule{Client: elb.NewFromConfig(s.cfg)},
			awsp.TypeLogSubscription: awsp.LogSubscription{Client: s.Logs},
		},
		Fallback:    cloudcontrol.Provider{Client: s.CloudControl, Schemas: s.Schemas},
		Secrets:     s.Secrets,
		Concurrency: 8,
		Tags:        tags,
	}
}

// stateIn is the state bucket of another region or account, for the outputs of a stage there.
// With a role it is read as the role, which koldstart assumes with this session's credentials,
// and the stage is in the role's account unless account says another; without one, it is read
// with this session's credentials, which the bucket's policy must let in. It is the bucket of
// the account, so one of another owner with its name is refused.
func (s *session) stateIn(_ context.Context, region, account, role string) (state.Backend, error) {
	if region == "" {
		region = s.Region
	}
	cfg := s.cfg.Copy()
	cfg.Region = region
	if role != "" {
		roleAccount, err := accountOf(role)
		if err != nil {
			return nil, err
		}
		if account == "" {
			account = roleAccount
		}
		cfg.Credentials = aws.NewCredentialsCache(stscreds.NewAssumeRoleProvider(sts.NewFromConfig(s.cfg), role, func(o *stscreds.AssumeRoleOptions) {
			o.RoleSessionName = "koldstart-outputs"
		}))
	}
	if account == "" {
		account = s.Account
	}
	other := newSession(cfg, account)
	return &state.S3{Client: other.S3, Bucket: state.BucketName(account, region), Account: account}, nil
}

// accountOf is the account of a role's arn, such as arn:aws:iam::123456789012:role/name.
func accountOf(role string) (string, error) {
	parts := strings.Split(role, ":")
	if len(parts) < 6 || parts[2] != "iam" || len(parts[4]) != 12 {
		return "", fmt.Errorf("%s is not the arn of a role", role)
	}
	return parts[4], nil
}

// backend returns the state bucket and whether it exists. With create, a bucket that does not
// exist yet is created, so it always does.
func (s *session) backend(ctx context.Context, create bool) (*state.S3, bool, error) {
	if create {
		b, err := state.Open(ctx, s.S3, s.Account, s.Region)
		return b, err == nil, err
	}
	return state.Find(ctx, s.S3, s.Account, s.Region)
}
