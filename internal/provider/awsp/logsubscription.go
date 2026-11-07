package awsp

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"

	"github.com/kontrolplane/koldstart/internal/awserr"
	"github.com/kontrolplane/koldstart/internal/engine"
)

// LogSubscriptionInputs are the inputs of a subscription filter that sends the lines of a log
// group that match its pattern to a function.
type LogSubscriptionInputs struct {
	Name     string `json:"name"`
	LogGroup string `json:"logGroup"`
	Pattern  string `json:"pattern"`
	Function string `json:"function"`
}

// LogSubscription is the provider of subscription filters. Putting one of the same name replaces
// it, so a crashed deploy makes no second one.
type LogSubscription struct {
	Client *cloudwatchlogs.Client
	// Wait is how long a put is retried while cloudwatch logs may not invoke the function yet.
	Wait time.Duration
}

func logSubscriptionResult(name string) engine.Result {
	return engine.Result{PhysicalID: name, Attributes: map[string]string{"FilterName": name}}
}

// put creates the filter or brings it in line, once the permission that lets cloudwatch logs
// invoke the function has spread.
func (p LogSubscription) put(ctx context.Context, in LogSubscriptionInputs) (engine.Result, error) {
	retryable := func(err error) bool {
		return awserr.Is(err, "InvalidParameterException") && strings.Contains(err.Error(), "permission") ||
			awserr.Is(err, "OperationAbortedException")
	}
	err := retry(ctx, cmp.Or(p.Wait, 2*time.Minute), 2*time.Second, retryable, func() error {
		_, err := p.Client.PutSubscriptionFilter(ctx, &cloudwatchlogs.PutSubscriptionFilterInput{
			LogGroupName: &in.LogGroup, FilterName: &in.Name, FilterPattern: &in.Pattern, DestinationArn: &in.Function,
		})
		return err
	})
	if err != nil {
		return engine.Result{}, fmt.Errorf("subscribing %s to the log group %s: %w", in.Function, in.LogGroup, err)
	}
	return logSubscriptionResult(in.Name), nil
}

// Create implements engine.Provider.
func (p LogSubscription) Create(ctx context.Context, req engine.Request) (engine.Result, error) {
	var in LogSubscriptionInputs
	if err := decodeInputs(req.Inputs, &in); err != nil {
		return engine.Result{}, err
	}
	return p.put(ctx, in)
}

// Read implements engine.Provider: the filter of the name among the log group's.
func (p LogSubscription) Read(ctx context.Context, req engine.Request) (engine.Result, bool, error) {
	var in LogSubscriptionInputs
	if err := decodeInputs(req.Inputs, &in); err != nil {
		return engine.Result{}, false, err
	}
	out, err := p.Client.DescribeSubscriptionFilters(ctx, &cloudwatchlogs.DescribeSubscriptionFiltersInput{
		LogGroupName: &in.LogGroup, FilterNamePrefix: &in.Name,
	})
	if awserr.Is(err, "ResourceNotFoundException") {
		return engine.Result{}, false, nil
	}
	if err != nil {
		return engine.Result{}, false, fmt.Errorf("reading the subscriptions of the log group %s: %w", in.LogGroup, err)
	}
	for _, f := range out.SubscriptionFilters {
		if aws.ToString(f.FilterName) == in.Name {
			return logSubscriptionResult(in.Name), true, nil
		}
	}
	return engine.Result{}, false, nil
}

// Update implements engine.Provider.
func (p LogSubscription) Update(ctx context.Context, req engine.Request) (engine.Result, error) {
	var in LogSubscriptionInputs
	if err := decodeInputs(req.Inputs, &in); err != nil {
		return engine.Result{}, err
	}
	return p.put(ctx, in)
}

// Delete implements engine.Provider.
func (p LogSubscription) Delete(ctx context.Context, req engine.Request) error {
	var in LogSubscriptionInputs
	if err := decodeInputs(req.Prior, &in); err != nil {
		return err
	}
	_, err := p.Client.DeleteSubscriptionFilter(ctx, &cloudwatchlogs.DeleteSubscriptionFilterInput{LogGroupName: &in.LogGroup, FilterName: &in.Name})
	if err != nil && !awserr.Is(err, "ResourceNotFoundException") {
		return fmt.Errorf("deleting the subscription %s of the log group %s: %w", in.Name, in.LogGroup, err)
	}
	return nil
}

// Replaces implements engine.Provider: a filter is named, in its log group.
func (LogSubscription) Replaces(prior, desired json.RawMessage) bool {
	a, b := decodeBoth[LogSubscriptionInputs](prior, desired)
	return a.Name != b.Name || a.LogGroup != b.LogGroup
}
