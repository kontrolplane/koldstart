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
		Tags: req.Tags,
	}
	if len(in.Filters) > 0 {
		create.FilterCriteria = in.filters()
	}
	if in.StartingPosition != "" {
		create.StartingPosition = types.EventSourcePosition(in.StartingPosition)
	}
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
	err := p.retry(ctx, func() error {
		_, err := p.Client.UpdateEventSourceMapping(ctx, &lambda.UpdateEventSourceMappingInput{
			UUID: &req.PhysicalID, FunctionName: &in.Function, BatchSize: &in.BatchSize,
			MaximumBatchingWindowInSeconds: &in.MaximumBatchingWindow, FunctionResponseTypes: in.responseTypes(),
			FilterCriteria: in.filters(),
		})
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
