package awsp

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	"github.com/aws/aws-sdk-go-v2/service/lambda/types"

	"github.com/kontrolplane/koldstart/internal/awserr"
	"github.com/kontrolplane/koldstart/internal/engine"
)

// MappingInputs are the inputs of an event source mapping from a queue or a stream.
type MappingInputs struct {
	Function                string   `json:"function"`
	Source                  string   `json:"source"`
	BatchSize               int32    `json:"batchSize"`
	MaximumBatchingWindow   int32    `json:"maximumBatchingWindow"`
	ReportBatchItemFailures bool     `json:"reportBatchItemFailures"`
	Filters                 []string `json:"filters"`
	StartingPosition        string   `json:"startingPosition"`
	// The settings below are left out when they are lambda's default, so a mapping deployed
	// before they were known keeps its hash.
	Enabled                    *bool  `json:"enabled,omitempty"`
	MaximumConcurrency         *int32 `json:"maximumConcurrency,omitempty"`
	MaximumRetryAttempts       *int32 `json:"maximumRetryAttempts,omitempty"`
	MaximumRecordAge           *int32 `json:"maximumRecordAge,omitempty"`
	BisectBatchOnFunctionError bool   `json:"bisectBatchOnFunctionError,omitempty"`
	ParallelizationFactor      int32  `json:"parallelizationFactor,omitempty"`
	TumblingWindow             int32  `json:"tumblingWindow,omitempty"`
	OnFailure                  string `json:"onFailure,omitempty"`
}

// enabled reports whether lambda reads the source: unless the mapping is turned off.
func (in MappingInputs) enabled() *bool {
	return aws.Bool(in.Enabled == nil || *in.Enabled)
}

// isStream reports whether the mapping reads a stream, which takes the settings of failures.
func (in MappingInputs) isStream() bool { return in.StartingPosition != "" }

// scaling is how many functions read a queue at once, or nil for lambda's default. An update
// sends an empty one to go back to it.
func (in MappingInputs) scaling(update bool) *types.ScalingConfig {
	if in.MaximumConcurrency == nil {
		if update && !in.isStream() {
			return &types.ScalingConfig{}
		}
		return nil
	}
	return &types.ScalingConfig{MaximumConcurrency: in.MaximumConcurrency}
}

// streamSettings are a stream's failure and parallel settings, with lambda's defaults for what
// is not set, so an update takes back what was.
func (in MappingInputs) streamSettings() (retries, age *int32, bisect *bool, parallel, window *int32, dest *types.DestinationConfig) {
	retries, age = aws.Int32(-1), aws.Int32(-1)
	if in.MaximumRetryAttempts != nil {
		retries = in.MaximumRetryAttempts
	}
	if in.MaximumRecordAge != nil {
		age = in.MaximumRecordAge
	}
	parallel = aws.Int32(max(in.ParallelizationFactor, 1))
	dest = &types.DestinationConfig{OnFailure: &types.OnFailure{Destination: aws.String(in.OnFailure)}}
	return retries, age, aws.Bool(in.BisectBatchOnFunctionError), parallel, aws.Int32(in.TumblingWindow), dest
}

// Mapping is the provider of event source mappings. A mapping has no name: one a crashed deploy
// made is found by its function and source, of which there is one mapping each.
type Mapping struct {
	Client *lambda.Client
	// Wait is how long a call is retried while a mapping is busy or iam spreads a policy.
	Wait time.Duration
}

func (in MappingInputs) responseTypes() []types.FunctionResponseType {
	if in.ReportBatchItemFailures {
		return []types.FunctionResponseType{types.FunctionResponseTypeReportBatchItemFailures}
	}
	return []types.FunctionResponseType{}
}

func (in MappingInputs) filters() *types.FilterCriteria {
	out := &types.FilterCriteria{Filters: []types.Filter{}}
	for _, f := range in.Filters {
		out.Filters = append(out.Filters, types.Filter{Pattern: aws.String(f)})
	}
	return out
}

// retry calls fn while lambda says the mapping is busy, or that the role may not read the source
// yet, which happens until iam spreads the role's new policy.
func (p Mapping) retry(ctx context.Context, fn func() error) error {
	retryable := func(err error) bool {
		return awserr.Is(err, "ResourceInUseException", "TooManyRequestsException") ||
			(awserr.Is(err, "InvalidParameterValueException") && strings.Contains(err.Error(), "permission"))
	}
	return retry(ctx, cmp.Or(p.Wait, 2*time.Minute), 2*time.Second, retryable, fn)
}

func mappingResult(id string) engine.Result {
	return engine.Result{PhysicalID: id, Attributes: map[string]string{"Id": id}}
}

// Create implements engine.Provider.
func (p Mapping) Create(ctx context.Context, req engine.Request) (engine.Result, error) {
	var in MappingInputs
	if err := decodeInputs(req.Inputs, &in); err != nil {
		return engine.Result{}, err
	}
	create := &lambda.CreateEventSourceMappingInput{
		FunctionName: &in.Function, EventSourceArn: &in.Source, BatchSize: &in.BatchSize,
		MaximumBatchingWindowInSeconds: &in.MaximumBatchingWindow, FunctionResponseTypes: in.responseTypes(),
		Enabled: in.enabled(), Tags: req.Tags,
	}
	if len(in.Filters) > 0 {
		create.FilterCriteria = in.filters()
	}
	if in.StartingPosition != "" {
		create.StartingPosition = types.EventSourcePosition(in.StartingPosition)
		create.MaximumRetryAttempts, create.MaximumRecordAgeInSeconds, create.BisectBatchOnFunctionError,
			create.ParallelizationFactor, create.TumblingWindowInSeconds, create.DestinationConfig = in.streamSettings()
		if in.OnFailure == "" {
			create.DestinationConfig = nil
		}
	}
	create.ScalingConfig = in.scaling(false)
	var out *lambda.CreateEventSourceMappingOutput
	err := p.retry(ctx, func() error {
		var err error
		out, err = p.Client.CreateEventSourceMapping(ctx, create)
		return err
	})
	if awserr.Is(err, "ResourceConflictException") {
		return engine.Result{}, engine.ErrExists
	}
	if err != nil {
		return engine.Result{}, fmt.Errorf("mapping %s to %s: %w", in.Source, in.Function, err)
	}
	return mappingResult(aws.ToString(out.UUID)), nil
}

// Read implements engine.Provider.
func (p Mapping) Read(ctx context.Context, req engine.Request) (engine.Result, bool, error) {
	var in MappingInputs
	if err := decodeInputs(req.Inputs, &in); err != nil {
		return engine.Result{}, false, err
	}
	if req.PhysicalID != "" {
		out, err := p.Client.GetEventSourceMapping(ctx, &lambda.GetEventSourceMappingInput{UUID: &req.PhysicalID})
		if err == nil {
			return mappingResult(aws.ToString(out.UUID)), true, nil
		}
		if !awserr.Is(err, "ResourceNotFoundException") {
			return engine.Result{}, false, fmt.Errorf("reading the mapping %s: %w", req.PhysicalID, err)
		}
	}
	out, err := p.Client.ListEventSourceMappings(ctx, &lambda.ListEventSourceMappingsInput{FunctionName: &in.Function, EventSourceArn: &in.Source})
	if awserr.Is(err, "ResourceNotFoundException") {
		return engine.Result{}, false, nil
	}
	if err != nil {
		return engine.Result{}, false, fmt.Errorf("listing the mappings of %s: %w", in.Function, err)
	}
	if len(out.EventSourceMappings) == 0 {
		return engine.Result{}, false, nil
	}
	return mappingResult(aws.ToString(out.EventSourceMappings[0].UUID)), true, nil
}

// Update implements engine.Provider.
func (p Mapping) Update(ctx context.Context, req engine.Request) (engine.Result, error) {
	var in MappingInputs
	if err := decodeInputs(req.Inputs, &in); err != nil {
		return engine.Result{}, err
	}
	update := &lambda.UpdateEventSourceMappingInput{
		UUID: &req.PhysicalID, FunctionName: &in.Function, BatchSize: &in.BatchSize,
		MaximumBatchingWindowInSeconds: &in.MaximumBatchingWindow, FunctionResponseTypes: in.responseTypes(),
		FilterCriteria: in.filters(), ScalingConfig: in.scaling(true), Enabled: in.enabled(),
	}
	if in.isStream() {
		update.MaximumRetryAttempts, update.MaximumRecordAgeInSeconds, update.BisectBatchOnFunctionError,
			update.ParallelizationFactor, update.TumblingWindowInSeconds, update.DestinationConfig = in.streamSettings()
	}
	err := p.retry(ctx, func() error {
		_, err := p.Client.UpdateEventSourceMapping(ctx, update)
		return err
	})
	if err != nil {
		return engine.Result{}, fmt.Errorf("updating the mapping of %s to %s: %w", in.Source, in.Function, err)
	}
	return mappingResult(req.PhysicalID), nil
}

// Delete implements engine.Provider.
func (p Mapping) Delete(ctx context.Context, req engine.Request) error {
	err := p.retry(ctx, func() error {
		_, err := p.Client.DeleteEventSourceMapping(ctx, &lambda.DeleteEventSourceMappingInput{UUID: &req.PhysicalID})
		return err
	})
	if err != nil && !awserr.Is(err, "ResourceNotFoundException") {
		return fmt.Errorf("deleting the mapping %s: %w", req.PhysicalID, err)
	}
	return nil
}

// Replaces implements engine.Provider: the source and where a stream starts cannot change.
func (Mapping) Replaces(prior, desired json.RawMessage) bool {
	a, b := decodeBoth[MappingInputs](prior, desired)
	return a.Source != b.Source || a.StartingPosition != b.StartingPosition || a.Function != b.Function
}
