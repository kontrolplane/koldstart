package awsp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"

	"github.com/kontrolplane/koldstart/internal/engine"
)

// NotificationInputs are the inputs of the notifications of one bucket: every function of the
// stage that the bucket invokes.
type NotificationInputs struct {
	Bucket string `json:"bucket"`
	// Prefix starts the id of every notification of the stage, so they are told from others.
	Prefix    string                 `json:"prefix"`
	Functions []NotificationFunction `json:"functions"`
}

// NotificationFunction is one function a bucket invokes.
type NotificationFunction struct {
	ID       string   `json:"id"`
	Function string   `json:"function"`
	Events   []string `json:"events"`
	Prefix   string   `json:"prefix"`
	Suffix   string   `json:"suffix"`
}

// Notification is the provider of bucket notifications. A bucket has one notification
// configuration, so this provider owns the stage's part of it: it puts its own functions in, and
// leaves every other notification as it found it.
type Notification struct {
	Client  *s3.Client
	Account string
}

func (in NotificationInputs) configurations() []types.LambdaFunctionConfiguration {
	out := make([]types.LambdaFunctionConfiguration, 0, len(in.Functions))
	for _, f := range in.Functions {
		events := f.Events
		if len(events) == 0 {
			events = []string{"s3:ObjectCreated:*"}
		}
		c := types.LambdaFunctionConfiguration{Id: aws.String(f.ID), LambdaFunctionArn: aws.String(f.Function)}
		for _, e := range events {
			c.Events = append(c.Events, types.Event(e))
		}
		var rules []types.FilterRule
		if f.Prefix != "" {
			rules = append(rules, types.FilterRule{Name: types.FilterRuleNamePrefix, Value: aws.String(f.Prefix)})
		}
		if f.Suffix != "" {
			rules = append(rules, types.FilterRule{Name: types.FilterRuleNameSuffix, Value: aws.String(f.Suffix)})
		}
		if rules != nil {
			c.Filter = &types.NotificationConfigurationFilter{Key: &types.S3KeyFilter{FilterRules: rules}}
		}
		out = append(out, c)
	}
	return out
}

// put replaces the stage's notifications on the bucket with in's, keeping the rest.
func (p Notification) put(ctx context.Context, bucket, prefix string, ours []types.LambdaFunctionConfiguration) error {
	current, err := p.Client.GetBucketNotificationConfiguration(ctx, &s3.GetBucketNotificationConfigurationInput{Bucket: &bucket, ExpectedBucketOwner: &p.Account})
	if err != nil {
		return fmt.Errorf("reading the notifications of %s: %w", bucket, err)
	}
	config := &types.NotificationConfiguration{
		TopicConfigurations:          current.TopicConfigurations,
		QueueConfigurations:          current.QueueConfigurations,
		EventBridgeConfiguration:     current.EventBridgeConfiguration,
		LambdaFunctionConfigurations: ours,
	}
	for _, c := range current.LambdaFunctionConfigurations {
		if !strings.HasPrefix(aws.ToString(c.Id), prefix) {
			config.LambdaFunctionConfigurations = append(config.LambdaFunctionConfigurations, c)
		}
	}
	_, err = p.Client.PutBucketNotificationConfiguration(ctx, &s3.PutBucketNotificationConfigurationInput{
		Bucket: &bucket, NotificationConfiguration: config, ExpectedBucketOwner: &p.Account,
	})
	if err != nil {
		return fmt.Errorf("putting the notifications of %s: %w", bucket, err)
	}
	return nil
}

func notificationResult(bucket string) engine.Result {
	return engine.Result{PhysicalID: bucket, Attributes: map[string]string{"Bucket": bucket}}
}

// Create implements engine.Provider.
func (p Notification) Create(ctx context.Context, req engine.Request) (engine.Result, error) {
	return p.Update(ctx, req)
}

// Read implements engine.Provider: the bucket's notifications exist as long as the bucket does.
func (p Notification) Read(ctx context.Context, req engine.Request) (engine.Result, bool, error) {
	var in NotificationInputs
	if err := decodeInputs(req.Inputs, &in); err != nil {
		return engine.Result{}, false, err
	}
	_, err := p.Client.GetBucketNotificationConfiguration(ctx, &s3.GetBucketNotificationConfigurationInput{Bucket: &in.Bucket, ExpectedBucketOwner: &p.Account})
	if isCode(err, "NoSuchBucket") {
		return engine.Result{}, false, nil
	}
	if err != nil {
		return engine.Result{}, false, fmt.Errorf("reading the notifications of %s: %w", in.Bucket, err)
	}
	return notificationResult(in.Bucket), true, nil
}

// Update implements engine.Provider.
func (p Notification) Update(ctx context.Context, req engine.Request) (engine.Result, error) {
	var in NotificationInputs
	if err := decodeInputs(req.Inputs, &in); err != nil {
		return engine.Result{}, err
	}
	if err := p.put(ctx, in.Bucket, in.Prefix, in.configurations()); err != nil {
		return engine.Result{}, err
	}
	return notificationResult(in.Bucket), nil
}

// Delete implements engine.Provider: the stage's notifications go, the rest stay.
func (p Notification) Delete(ctx context.Context, req engine.Request) error {
	var in NotificationInputs
	if err := decodeInputs(req.Prior, &in); err != nil {
		return err
	}
	err := p.put(ctx, in.Bucket, in.Prefix, nil)
	if isCode(err, "NoSuchBucket") {
		return nil
	}
	return err
}

// Replaces implements engine.Provider.
func (Notification) Replaces(prior, desired json.RawMessage) bool {
	var a, b NotificationInputs
	_ = json.Unmarshal(prior, &a)
	_ = json.Unmarshal(desired, &b)
	return a.Bucket != b.Bucket
}
