package cli

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/apigatewayv2"
	awscloudcontrol "github.com/aws/aws-sdk-go-v2/service/cloudcontrol"
	"github.com/aws/aws-sdk-go-v2/service/cloudformation"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/scheduler"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"github.com/aws/aws-sdk-go-v2/service/sns"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	"github.com/aws/aws-sdk-go-v2/service/sts"

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
	S3      *s3.Client
	Lambda  *lambda.Client
	Logs    *cloudwatchlogs.Client
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
	if err != nil {
		return aws.Config{}, fmt.Errorf("loading the aws config: %w", err)
	}
	if cfg.Region == "" {
		return aws.Config{}, errors.New("no region: set region in the configuration, or pass --region")
	}
	return cfg, nil
}

// openSession connects to the account the credentials belong to, in region. With an endpoint of
// an emulator, which answers on one host, buckets go in the path rather than the host name.
func openSession(ctx context.Context, opts *Options, region string) (*session, error) {
	cfg, err := awsConfig(ctx, opts, region)
	if err != nil {
		return nil, err
	}
	ident, err := sts.NewFromConfig(cfg).GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		return nil, fmt.Errorf("finding the aws account: are there credentials? %w", err)
	}
	return &session{
		cfg:     cfg,
		Account: aws.ToString(ident.Account),
		Region:  cfg.Region,
		S3: s3.NewFromConfig(cfg, func(o *s3.Options) {
			o.UsePathStyle = os.Getenv("AWS_ENDPOINT_URL") != "" || os.Getenv("AWS_ENDPOINT_URL_S3") != ""
		}),
		Lambda:       lambda.NewFromConfig(cfg),
		Logs:         cloudwatchlogs.NewFromConfig(cfg),
		CloudControl: awscloudcontrol.NewFromConfig(cfg),
		Schemas:      &cloudcontrol.Registry{Client: cloudformation.NewFromConfig(cfg)},
		Secrets:      &resolve.Secrets{SSM: ssm.NewFromConfig(cfg), SecretsManager: secretsmanager.NewFromConfig(cfg)},
	}, nil
}

// engine returns an engine with the providers for this session.
func (s *session) engine(tags map[string]string) *engine.Engine {
	apigw := apigatewayv2.NewFromConfig(s.cfg)
	return &engine.Engine{
		Providers: map[string]engine.Provider{
			awsp.TypeLogGroup:     awsp.LogGroup{Client: s.Logs},
			awsp.TypeRole:         awsp.Role{Client: iam.NewFromConfig(s.cfg)},
			awsp.TypeFunction:     awsp.Function{Client: s.Lambda},
			awsp.TypeURL:          awsp.URL{Client: s.Lambda},
			awsp.TypePermission:   awsp.Permission{Client: s.Lambda},
			awsp.TypeHTTPAPI:      awsp.HTTPAPI{Client: apigw, Region: s.Region},
			awsp.TypeIntegration:  awsp.Integration{Client: apigw},
			awsp.TypeRoute:        awsp.Route{Client: apigw},
			awsp.TypeAuthorizer:   awsp.Authorizer{Client: apigw},
			awsp.TypeDomain:       awsp.Domain{Client: apigw},
			awsp.TypeSchedule:     awsp.Schedule{Client: scheduler.NewFromConfig(s.cfg)},
			awsp.TypeMapping:      awsp.Mapping{Client: s.Lambda},
			awsp.TypeSubscription: awsp.Subscription{Client: sns.NewFromConfig(s.cfg)},
			awsp.TypeNotification: awsp.Notification{Client: s.S3, Account: s.Account},
		},
		Fallback:    cloudcontrol.Provider{Client: s.CloudControl, Schemas: s.Schemas},
		Secrets:     s.Secrets,
		Concurrency: 8,
		Tags:        tags,
	}
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
