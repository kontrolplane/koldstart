package awsp

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sns"

	"github.com/kontrolplane/koldstart/internal/awserr"
	"github.com/kontrolplane/koldstart/internal/engine"
)

// SubscriptionInputs are the inputs of the subscription of a function to an sns topic.
type SubscriptionInputs struct {
	Topic        string `json:"topic"`
	Function     string `json:"function"`
	FilterPolicy string `json:"filterPolicy"`
}

// Subscription is the provider of sns subscriptions. Subscribing again with the same topic and
// function returns the subscription that exists, so a crashed deploy makes no second one.
type Subscription struct {
	Client *sns.Client
}

func subscriptionResult(arn string) engine.Result {
	return engine.Result{PhysicalID: arn, Attributes: map[string]string{"SubscriptionArn": arn}}
}

func (in SubscriptionInputs) subscribe(ctx context.Context, client *sns.Client) (string, error) {
	attrs := map[string]string{}
	if in.FilterPolicy != "" {
		attrs["FilterPolicy"] = in.FilterPolicy
	}
	out, err := client.Subscribe(ctx, &sns.SubscribeInput{
		TopicArn: &in.Topic, Protocol: aws.String("lambda"), Endpoint: &in.Function,
		Attributes: attrs, ReturnSubscriptionArn: true,
	})
	if err != nil {
		return "", fmt.Errorf("subscribing %s to %s: %w", in.Function, in.Topic, err)
	}
	return aws.ToString(out.SubscriptionArn), nil
}

// Create implements engine.Provider.
func (p Subscription) Create(ctx context.Context, req engine.Request) (engine.Result, error) {
	var in SubscriptionInputs
	if err := decodeInputs(req.Inputs, &in); err != nil {
		return engine.Result{}, err
	}
	arn, err := in.subscribe(ctx, p.Client)
	if err != nil {
		return engine.Result{}, err
	}
	return subscriptionResult(arn), nil
}

// Read implements engine.Provider: the subscription of the function among the topic's.
func (p Subscription) Read(ctx context.Context, req engine.Request) (engine.Result, bool, error) {
	var in SubscriptionInputs
	if err := decodeInputs(req.Inputs, &in); err != nil {
		return engine.Result{}, false, err
	}
	pages := sns.NewListSubscriptionsByTopicPaginator(p.Client, &sns.ListSubscriptionsByTopicInput{TopicArn: &in.Topic})
	for pages.HasMorePages() {
		page, err := pages.NextPage(ctx)
		if awserr.Is(err, "NotFound") {
			return engine.Result{}, false, nil
		}
		if err != nil {
			return engine.Result{}, false, fmt.Errorf("listing the subscriptions of %s: %w", in.Topic, err)
		}
		for _, s := range page.Subscriptions {
			if aws.ToString(s.Endpoint) == in.Function {
				return subscriptionResult(aws.ToString(s.SubscriptionArn)), true, nil
			}
		}
	}
	return engine.Result{}, false, nil
}

// Update implements engine.Provider: the filter policy is the one thing that changes in place.
func (p Subscription) Update(ctx context.Context, req engine.Request) (engine.Result, error) {
	var in SubscriptionInputs
	if err := decodeInputs(req.Inputs, &in); err != nil {
		return engine.Result{}, err
	}
	policy := in.FilterPolicy
	if policy == "" {
		policy = "{}"
	}
	_, err := p.Client.SetSubscriptionAttributes(ctx, &sns.SetSubscriptionAttributesInput{
		SubscriptionArn: &req.PhysicalID, AttributeName: aws.String("FilterPolicy"), AttributeValue: &policy,
	})
	if err != nil {
		return engine.Result{}, fmt.Errorf("updating the filter of %s: %w", req.PhysicalID, err)
	}
	return subscriptionResult(req.PhysicalID), nil
}

// Delete implements engine.Provider.
func (p Subscription) Delete(ctx context.Context, req engine.Request) error {
	_, err := p.Client.Unsubscribe(ctx, &sns.UnsubscribeInput{SubscriptionArn: &req.PhysicalID})
	if err != nil && !awserr.Is(err, "NotFound") {
		return fmt.Errorf("unsubscribing %s: %w", req.PhysicalID, err)
	}
	return nil
}

// Replaces implements engine.Provider.
func (Subscription) Replaces(prior, desired json.RawMessage) bool {
	a, b := decodeBoth[SubscriptionInputs](prior, desired)
	return a.Topic != b.Topic || a.Function != b.Function
}
