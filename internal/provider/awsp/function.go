package awsp

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	"github.com/aws/aws-sdk-go-v2/service/lambda/types"

	"github.com/kontrolplane/koldstart/internal/awserr"
	"github.com/kontrolplane/koldstart/internal/config"
	"github.com/kontrolplane/koldstart/internal/engine"
)

// FunctionInputs are the inputs of a function node.
type FunctionInputs struct {
	Name                string            `json:"name"`
	Runtime             string            `json:"runtime"`
	Handler             string            `json:"handler"`
	Memory              int32             `json:"memory"`
	Timeout             int32             `json:"timeout"`
	Architecture        string            `json:"architecture"`
	EphemeralStorage    int32             `json:"ephemeralStorage"`
	ReservedConcurrency *int32            `json:"reservedConcurrency"`
	Environment         map[string]string `json:"environment"`
	Role                string            `json:"role"`
	LogGroup            string            `json:"logGroup"`
	Code                Code              `json:"code"`
	VPC                 *VPCConfig        `json:"vpc"`
}

// Code is where a function's zip is.
type Code struct {
	Bucket string `json:"bucket"`
	Key    string `json:"key"`
	SHA256 string `json:"sha256"`
}

// VPCConfig puts a function in subnets.
type VPCConfig struct {
	SubnetIDs        []string `json:"subnetIds"`
	SecurityGroupIDs []string `json:"securityGroupIds"`
}

// Function is the provider of lambda functions.
type Function struct {
	Client *lambda.Client
	// Wait is how long to wait for a function to become active or finish updating, five minutes
	// when it is not set, and RetryEvery how often a call lambda refuses for now is tried again.
	Wait       time.Duration
	RetryEvery time.Duration
}

func (p Function) wait() time.Duration {
	return cmp.Or(p.Wait, 5*time.Minute)
}

// retry calls fn while lambda refuses it for now: a role iam has not spread yet, or an update
// while the one before is still in progress.
func (p Function) retry(ctx context.Context, fn func() error) error {
	retryable := func(err error) bool {
		return awserr.Is(err, "ResourceConflictException", "TooManyRequestsException") ||
			(awserr.Is(err, "InvalidParameterValueException") && strings.Contains(err.Error(), "cannot be assumed"))
	}
	return retry(ctx, p.wait(), cmp.Or(p.RetryEvery, 2*time.Second), retryable, fn)
}

func architectures(a string) []types.Architecture {
	if a == config.ArchX8664 {
		return []types.Architecture{types.ArchitectureX8664}
	}
	return []types.Architecture{types.ArchitectureArm64}
}

func vpcConfig(v *VPCConfig) *types.VpcConfig {
	if v == nil {
		return &types.VpcConfig{SubnetIds: []string{}, SecurityGroupIds: []string{}}
	}
	return &types.VpcConfig{SubnetIds: v.SubnetIDs, SecurityGroupIds: v.SecurityGroupIDs}
}

// Create implements engine.Provider.
func (p Function) Create(ctx context.Context, req engine.Request) (engine.Result, error) {
	var in FunctionInputs
	if err := decodeInputs(req.Inputs, &in); err != nil {
		return engine.Result{}, err
	}
	var out *lambda.CreateFunctionOutput
	err := p.retry(ctx, func() error {
		var err error
		out, err = p.Client.CreateFunction(ctx, &lambda.CreateFunctionInput{
			FunctionName:     &in.Name,
			Runtime:          types.Runtime(in.Runtime),
			Handler:          &in.Handler,
			Role:             &in.Role,
			MemorySize:       &in.Memory,
			Timeout:          &in.Timeout,
			Architectures:    architectures(in.Architecture),
			EphemeralStorage: &types.EphemeralStorage{Size: &in.EphemeralStorage},
			Environment:      &types.Environment{Variables: in.Environment},
			Code:             &types.FunctionCode{S3Bucket: &in.Code.Bucket, S3Key: &in.Code.Key},
			LoggingConfig:    &types.LoggingConfig{LogGroup: &in.LogGroup, LogFormat: types.LogFormatText},
			VpcConfig:        vpcConfig(in.VPC),
			Tags:             req.Tags,
		})
		if awserr.Is(err, "ResourceConflictException") && strings.Contains(err.Error(), "already exist") {
			return engine.ErrExists
		}
		return err
	})
	if errors.Is(err, engine.ErrExists) {
		return engine.Result{}, engine.ErrExists
	}
	if err != nil {
		return engine.Result{}, fmt.Errorf("creating the function %s: %w", in.Name, err)
	}
	if err := p.waitActive(ctx, in.Name); err != nil {
		return engine.Result{}, err
	}
	if err := p.concurrency(ctx, in, nil); err != nil {
		return engine.Result{}, err
	}
	return functionResult(aws.ToString(out.FunctionArn), in.Name), nil
}

func functionResult(arn, name string) engine.Result {
	return engine.Result{PhysicalID: name, Attributes: map[string]string{attrArn: arn, "FunctionName": name}}
}

func (p Function) waitActive(ctx context.Context, name string) error {
	err := lambda.NewFunctionActiveV2Waiter(p.Client).Wait(ctx, &lambda.GetFunctionInput{FunctionName: &name}, p.wait())
	if err != nil {
		return fmt.Errorf("waiting for %s to become active: %w", name, err)
	}
	return nil
}

func (p Function) waitUpdated(ctx context.Context, name string) error {
	err := lambda.NewFunctionUpdatedV2Waiter(p.Client).Wait(ctx, &lambda.GetFunctionInput{FunctionName: &name}, p.wait())
	if err != nil {
		return fmt.Errorf("waiting for the update of %s: %w", name, err)
	}
	return nil
}

func (p Function) concurrency(ctx context.Context, in FunctionInputs, prior *FunctionInputs) error {
	if prior != nil && !changed(in.ReservedConcurrency, prior.ReservedConcurrency) {
		return nil
	}
	if in.ReservedConcurrency == nil {
		if prior == nil {
			return nil
		}
		_, err := p.Client.DeleteFunctionConcurrency(ctx, &lambda.DeleteFunctionConcurrencyInput{FunctionName: &in.Name})
		if err != nil && !awserr.Is(err, "ResourceNotFoundException") {
			return fmt.Errorf("removing the reserved concurrency of %s: %w", in.Name, err)
		}
		return nil
	}
	_, err := p.Client.PutFunctionConcurrency(ctx, &lambda.PutFunctionConcurrencyInput{FunctionName: &in.Name, ReservedConcurrentExecutions: in.ReservedConcurrency})
	if err != nil {
		return fmt.Errorf("reserving concurrency for %s: %w", in.Name, err)
	}
	return nil
}

// Read implements engine.Provider.
func (p Function) Read(ctx context.Context, req engine.Request) (engine.Result, bool, error) {
	out, err := p.Client.GetFunction(ctx, &lambda.GetFunctionInput{FunctionName: &req.PhysicalID})
	if awserr.Is(err, "ResourceNotFoundException") {
		return engine.Result{}, false, nil
	}
	if err != nil {
		return engine.Result{}, false, fmt.Errorf("reading the function %s: %w", req.PhysicalID, err)
	}
	if err := ours(req.PhysicalID, out.Tags, req); err != nil {
		return engine.Result{}, false, err
	}
	return functionResult(aws.ToString(out.Configuration.FunctionArn), req.PhysicalID), true, nil
}

// configChanged reports whether anything UpdateFunctionConfiguration sets differs.
func configChanged(a, b FunctionInputs) bool {
	a.Code, b.Code = Code{}, Code{}
	a.Architecture, b.Architecture = "", ""
	a.ReservedConcurrency, b.ReservedConcurrency = nil, nil
	return changed(a, b)
}

// Update implements engine.Provider. A change of the code alone is one UpdateFunctionCode;
// the configuration is only sent when it changed.
func (p Function) Update(ctx context.Context, req engine.Request) (engine.Result, error) {
	var in FunctionInputs
	if err := decodeInputs(req.Inputs, &in); err != nil {
		return engine.Result{}, err
	}
	var prior *FunctionInputs
	if len(req.Prior) > 0 {
		prior = &FunctionInputs{}
		if err := decodeInputs(req.Prior, prior); err != nil {
			return engine.Result{}, err
		}
	}
	if prior == nil || in.Code != prior.Code || in.Architecture != prior.Architecture {
		if err := p.updateCode(ctx, in); err != nil {
			return engine.Result{}, err
		}
	}
	if prior == nil || configChanged(in, *prior) {
		if err := p.updateConfiguration(ctx, in); err != nil {
			return engine.Result{}, err
		}
	}
	if err := p.concurrency(ctx, in, prior); err != nil {
		return engine.Result{}, err
	}
	arn, err := p.arnOf(ctx, in.Name, req.Attributes)
	if err != nil {
		return engine.Result{}, err
	}
	if len(req.Tags) > 0 {
		if _, err := p.Client.TagResource(ctx, &lambda.TagResourceInput{Resource: &arn, Tags: req.Tags}); err != nil {
			return engine.Result{}, fmt.Errorf("tagging %s: %w", in.Name, err)
		}
	}
	return functionResult(arn, in.Name), nil
}

// updateCode points the function at its new zip, and waits for lambda to take it.
func (p Function) updateCode(ctx context.Context, in FunctionInputs) error {
	err := p.retry(ctx, func() error {
		_, err := p.Client.UpdateFunctionCode(ctx, &lambda.UpdateFunctionCodeInput{
			FunctionName: &in.Name, S3Bucket: &in.Code.Bucket, S3Key: &in.Code.Key, Architectures: architectures(in.Architecture),
		})
		return err
	})
	if err != nil {
		return fmt.Errorf("updating the code of %s: %w", in.Name, err)
	}
	return p.waitUpdated(ctx, in.Name)
}

// updateConfiguration sends everything but the code and the concurrency, and waits for it.
func (p Function) updateConfiguration(ctx context.Context, in FunctionInputs) error {
	err := p.retry(ctx, func() error {
		_, err := p.Client.UpdateFunctionConfiguration(ctx, &lambda.UpdateFunctionConfigurationInput{
			FunctionName:     &in.Name,
			Runtime:          types.Runtime(in.Runtime),
			Handler:          &in.Handler,
			Role:             &in.Role,
			MemorySize:       &in.Memory,
			Timeout:          &in.Timeout,
			EphemeralStorage: &types.EphemeralStorage{Size: &in.EphemeralStorage},
			Environment:      &types.Environment{Variables: in.Environment},
			LoggingConfig:    &types.LoggingConfig{LogGroup: &in.LogGroup, LogFormat: types.LogFormatText},
			VpcConfig:        vpcConfig(in.VPC),
		})
		return err
	})
	if err != nil {
		return fmt.Errorf("updating the configuration of %s: %w", in.Name, err)
	}
	return p.waitUpdated(ctx, in.Name)
}

// arnOf is the function's arn: the one the state holds, or else lambda's.
func (p Function) arnOf(ctx context.Context, name string, attributes map[string]string) (string, error) {
	if arn := attributes[attrArn]; arn != "" {
		return arn, nil
	}
	out, err := p.Client.GetFunction(ctx, &lambda.GetFunctionInput{FunctionName: &name})
	if err != nil {
		return "", fmt.Errorf("reading the function %s: %w", name, err)
	}
	return aws.ToString(out.Configuration.FunctionArn), nil
}

// Delete implements engine.Provider.
func (p Function) Delete(ctx context.Context, req engine.Request) error {
	_, err := p.Client.DeleteFunction(ctx, &lambda.DeleteFunctionInput{FunctionName: &req.PhysicalID})
	if err != nil && !awserr.Is(err, "ResourceNotFoundException") {
		return fmt.Errorf("deleting the function %s: %w", req.PhysicalID, err)
	}
	return nil
}

// Replaces implements engine.Provider: a function cannot be renamed.
func (Function) Replaces(prior, desired json.RawMessage) bool {
	a, b := decodeBoth[FunctionInputs](prior, desired)
	return a.Name != b.Name
}
