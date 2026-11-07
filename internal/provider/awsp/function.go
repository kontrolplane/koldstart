package awsp

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
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
	// Layers and ImageConfig are left out of the inputs when a function has none, so a function
	// deployed before they were known keeps its hash.
	Layers      []string     `json:"layers,omitempty"`
	ImageConfig *ImageConfig `json:"imageConfig,omitempty"`
	// The settings below are left out the same way when a function does not set them.
	Tracing             string            `json:"tracing,omitempty"`
	KMSKey              string            `json:"kmsKey,omitempty"`
	Tags                map[string]string `json:"tags,omitempty"`
	DeadLetter          string            `json:"deadLetter,omitempty"`
	Async               *AsyncConfig      `json:"async,omitempty"`
	FileSystem          *FileSystemConfig `json:"fileSystem,omitempty"`
	LogFormat           string            `json:"logFormat,omitempty"`
	ApplicationLogLevel string            `json:"applicationLogLevel,omitempty"`
	SystemLogLevel      string            `json:"systemLogLevel,omitempty"`
}

// AsyncConfig is what lambda does with the events it is handed without waiting.
type AsyncConfig struct {
	MaximumRetryAttempts *int32 `json:"maximumRetryAttempts,omitempty"`
	MaximumEventAge      *int32 `json:"maximumEventAge,omitempty"`
	OnSuccess            string `json:"onSuccess,omitempty"`
	OnFailure            string `json:"onFailure,omitempty"`
}

// FileSystemConfig is an efs access point a function mounts.
type FileSystemConfig struct {
	AccessPoint string `json:"accessPoint"`
	MountPath   string `json:"mountPath"`
}

// tracing is the function's x-ray mode: PassThrough unless it traces.
func (in FunctionInputs) tracing() *types.TracingConfig {
	if in.Tracing == string(types.TracingModeActive) {
		return &types.TracingConfig{Mode: types.TracingModeActive}
	}
	return &types.TracingConfig{Mode: types.TracingModePassThrough}
}

// deadLetter is the function's dead letter target, an empty one to remove it.
func (in FunctionInputs) deadLetter() *types.DeadLetterConfig {
	return &types.DeadLetterConfig{TargetArn: &in.DeadLetter}
}

// kmsKey is the key of the function's variables, an empty one for lambda's own.
func (in FunctionInputs) kmsKey() *string {
	return &in.KMSKey
}

// fileSystems are the access points the function mounts, an empty list to unmount them.
func (in FunctionInputs) fileSystems() []types.FileSystemConfig {
	if in.FileSystem == nil {
		return []types.FileSystemConfig{}
	}
	return []types.FileSystemConfig{{Arn: &in.FileSystem.AccessPoint, LocalMountPath: &in.FileSystem.MountPath}}
}

// logging is where and how the function logs: text, or json with its levels.
func (in FunctionInputs) logging() *types.LoggingConfig {
	c := &types.LoggingConfig{LogGroup: &in.LogGroup, LogFormat: types.LogFormatText}
	if in.LogFormat == string(types.LogFormatJson) {
		c.LogFormat = types.LogFormatJson
		if in.ApplicationLogLevel != "" {
			c.ApplicationLogLevel = types.ApplicationLogLevel(in.ApplicationLogLevel)
		}
		if in.SystemLogLevel != "" {
			c.SystemLogLevel = types.SystemLogLevel(in.SystemLogLevel)
		}
	}
	return c
}

// tags are the tags of the function: its own, and koldstart's over them.
func (in FunctionInputs) tags(tags map[string]string) map[string]string {
	if len(in.Tags) == 0 {
		return tags
	}
	out := maps.Clone(in.Tags)
	maps.Copy(out, tags)
	return out
}

// Code is where a function's code is: its zip in the state bucket, or its container image.
type Code struct {
	Bucket   string `json:"bucket,omitempty"`
	Key      string `json:"key,omitempty"`
	ImageURI string `json:"imageUri,omitempty"`
	SHA256   string `json:"sha256"`
}

// ImageConfig is what a container image runs, in place of its CMD, ENTRYPOINT and WORKDIR.
type ImageConfig struct {
	Command          []string `json:"command,omitempty"`
	EntryPoint       []string `json:"entryPoint,omitempty"`
	WorkingDirectory string   `json:"workingDirectory,omitempty"`
}

// isImage reports whether the function runs a container image.
func (in FunctionInputs) isImage() bool { return in.Code.ImageURI != "" }

// code is the function's code as lambda takes it on create.
func (in FunctionInputs) code() *types.FunctionCode {
	if in.isImage() {
		return &types.FunctionCode{ImageUri: &in.Code.ImageURI}
	}
	return &types.FunctionCode{S3Bucket: &in.Code.Bucket, S3Key: &in.Code.Key}
}

// runtime, handler and packageType are what lambda takes for a zip, or for an image, which has
// neither runtime nor handler.
func (in FunctionInputs) runtime() types.Runtime {
	if in.isImage() {
		return ""
	}
	return types.Runtime(in.Runtime)
}

func (in FunctionInputs) handler() *string {
	if in.isImage() {
		return nil
	}
	return &in.Handler
}

func (in FunctionInputs) packageType() types.PackageType {
	if in.isImage() {
		return types.PackageTypeImage
	}
	return types.PackageTypeZip
}

// layers are the function's layers, an empty list to remove them all, or none for an image.
func (in FunctionInputs) layers() []string {
	if in.isImage() {
		return nil
	}
	if in.Layers == nil {
		return []string{}
	}
	return in.Layers
}

func (in FunctionInputs) imageConfig() *types.ImageConfig {
	if !in.isImage() {
		return nil
	}
	c := &types.ImageConfig{}
	if in.ImageConfig != nil {
		c.Command, c.EntryPoint = in.ImageConfig.Command, in.ImageConfig.EntryPoint
		if in.ImageConfig.WorkingDirectory != "" {
			c.WorkingDirectory = &in.ImageConfig.WorkingDirectory
		}
	}
	return c
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
			(awserr.Is(err, "InvalidParameterValueException") &&
				(strings.Contains(err.Error(), "cannot be assumed") || strings.Contains(err.Error(), "does not have permission")))
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
			FunctionName:      &in.Name,
			Runtime:           in.runtime(),
			Handler:           in.handler(),
			PackageType:       in.packageType(),
			ImageConfig:       in.imageConfig(),
			Layers:            in.Layers,
			Role:              &in.Role,
			MemorySize:        &in.Memory,
			Timeout:           &in.Timeout,
			Architectures:     architectures(in.Architecture),
			EphemeralStorage:  &types.EphemeralStorage{Size: &in.EphemeralStorage},
			Environment:       &types.Environment{Variables: in.Environment},
			Code:              in.code(),
			LoggingConfig:     in.logging(),
			VpcConfig:         vpcConfig(in.VPC),
			TracingConfig:     in.tracing(),
			KMSKeyArn:         optional(in.KMSKey),
			DeadLetterConfig:  in.createDeadLetter(),
			FileSystemConfigs: in.createFileSystems(),
			Tags:              in.tags(req.Tags),
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
	if err := p.async(ctx, in, nil); err != nil {
		return engine.Result{}, err
	}
	return functionResult(aws.ToString(out.FunctionArn), in.Name), nil
}

// createDeadLetter and createFileSystems are what a create sends: nothing when the function has
// none.
func (in FunctionInputs) createDeadLetter() *types.DeadLetterConfig {
	if in.DeadLetter == "" {
		return nil
	}
	return in.deadLetter()
}

func (in FunctionInputs) createFileSystems() []types.FileSystemConfig {
	if in.FileSystem == nil {
		return nil
	}
	return in.fileSystems()
}

// async puts how lambda handles the events it is handed without waiting, or removes it when the
// function no longer sets it.
func (p Function) async(ctx context.Context, in FunctionInputs, prior *FunctionInputs) error {
	if prior != nil && !changed(in.Async, prior.Async) {
		return nil
	}
	if in.Async == nil {
		if prior == nil {
			return nil
		}
		_, err := p.Client.DeleteFunctionEventInvokeConfig(ctx, &lambda.DeleteFunctionEventInvokeConfigInput{FunctionName: &in.Name})
		if err != nil && !awserr.Is(err, "ResourceNotFoundException") {
			return fmt.Errorf("removing the async settings of %s: %w", in.Name, err)
		}
		return nil
	}
	put := &lambda.PutFunctionEventInvokeConfigInput{
		FunctionName: &in.Name, MaximumRetryAttempts: in.Async.MaximumRetryAttempts, MaximumEventAgeInSeconds: in.Async.MaximumEventAge,
	}
	if in.Async.OnSuccess != "" || in.Async.OnFailure != "" {
		put.DestinationConfig = &types.DestinationConfig{}
		if in.Async.OnSuccess != "" {
			put.DestinationConfig.OnSuccess = &types.OnSuccess{Destination: &in.Async.OnSuccess}
		}
		if in.Async.OnFailure != "" {
			put.DestinationConfig.OnFailure = &types.OnFailure{Destination: &in.Async.OnFailure}
		}
	}
	err := p.retry(ctx, func() error {
		_, err := p.Client.PutFunctionEventInvokeConfig(ctx, put)
		return err
	})
	if err != nil {
		return fmt.Errorf("setting the async settings of %s: %w", in.Name, err)
	}
	return nil
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
	a.Async, b.Async = nil, nil
	a.Tags, b.Tags = nil, nil
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
	if err := p.async(ctx, in, prior); err != nil {
		return engine.Result{}, err
	}
	arn, err := p.arnOf(ctx, in.Name, req.Attributes)
	if err != nil {
		return engine.Result{}, err
	}
	if tags := in.tags(req.Tags); len(tags) > 0 {
		if _, err := p.Client.TagResource(ctx, &lambda.TagResourceInput{Resource: &arn, Tags: tags}); err != nil {
			return engine.Result{}, fmt.Errorf("tagging %s: %w", in.Name, err)
		}
	}
	return functionResult(arn, in.Name), nil
}

// updateCode points the function at its new zip, and waits for lambda to take it.
func (p Function) updateCode(ctx context.Context, in FunctionInputs) error {
	err := p.retry(ctx, func() error {
		update := &lambda.UpdateFunctionCodeInput{FunctionName: &in.Name, Architectures: architectures(in.Architecture)}
		if in.isImage() {
			update.ImageUri = &in.Code.ImageURI
		} else {
			update.S3Bucket, update.S3Key = &in.Code.Bucket, &in.Code.Key
		}
		_, err := p.Client.UpdateFunctionCode(ctx, update)
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
			FunctionName:      &in.Name,
			Runtime:           in.runtime(),
			Handler:           in.handler(),
			ImageConfig:       in.imageConfig(),
			Layers:            in.layers(),
			Role:              &in.Role,
			MemorySize:        &in.Memory,
			Timeout:           &in.Timeout,
			EphemeralStorage:  &types.EphemeralStorage{Size: &in.EphemeralStorage},
			Environment:       &types.Environment{Variables: in.Environment},
			LoggingConfig:     in.logging(),
			VpcConfig:         vpcConfig(in.VPC),
			TracingConfig:     in.tracing(),
			KMSKeyArn:         in.kmsKey(),
			DeadLetterConfig:  in.deadLetter(),
			FileSystemConfigs: in.fileSystems(),
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

// Replaces implements engine.Provider: a function cannot be renamed, or turned from a zip into a
// container image or back.
func (Function) Replaces(prior, desired json.RawMessage) bool {
	a, b := decodeBoth[FunctionInputs](prior, desired)
	return a.Name != b.Name || a.isImage() != b.isImage()
}
