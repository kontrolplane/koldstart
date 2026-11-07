// Package awsp holds the providers koldstart writes by hand: a function with its role, log group
// and url, the http api with its routes, integrations, authorizers and domain, the rest api with
// its resources, routes, authorizers, stage and deployment, api key and usage plan, and the
// schedules, mappings, subscriptions, notifications and permissions of its events. They call the
// aws apis directly, so a change aws can make in place takes one call.
package awsp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/kontrolplane/koldstart/internal/engine"
)

// attrArn is the attribute ref() reads an arn from, as CloudFormation names it.
const attrArn = "Arn"

// The types the providers here handle. They are koldstart's own names, so a resource of the same
// CloudFormation type in resources still goes to cloud control.
const (
	TypeLogGroup        = "koldstart::Logs::LogGroup"
	TypeRole            = "koldstart::IAM::Role"
	TypeFunction        = "koldstart::Lambda::Function"
	TypeURL             = "koldstart::Lambda::Url"
	TypePermission      = "koldstart::Lambda::Permission"
	TypeHTTPAPI         = "koldstart::ApiGatewayV2::Api"
	TypeIntegration     = "koldstart::ApiGatewayV2::Integration"
	TypeRoute           = "koldstart::ApiGatewayV2::Route"
	TypeAuthorizer      = "koldstart::ApiGatewayV2::Authorizer"
	TypeDomain          = "koldstart::ApiGatewayV2::DomainName"
	TypeSchedule        = "koldstart::Scheduler::Schedule"
	TypeMapping         = "koldstart::Lambda::EventSourceMapping"
	TypeSubscription    = "koldstart::SNS::Subscription"
	TypeNotification    = "koldstart::S3::BucketNotification"
	TypeRule            = "koldstart::Events::Rule"
	TypeTargetGroup     = "koldstart::ElasticLoadBalancingV2::TargetGroup"
	TypeTargets         = "koldstart::ElasticLoadBalancingV2::Targets"
	TypeListenerRule    = "koldstart::ElasticLoadBalancingV2::ListenerRule"
	TypeLogSubscription = "koldstart::Logs::SubscriptionFilter"
	TypeRESTAPI         = "koldstart::ApiGateway::RestApi"
	TypeRESTResource    = "koldstart::ApiGateway::Resource"
	TypeRESTRoute       = "koldstart::ApiGateway::Method"
	TypeRESTAuthorizer  = "koldstart::ApiGateway::Authorizer"
	TypeRESTStage       = "koldstart::ApiGateway::Stage"
	TypeRESTKey         = "koldstart::ApiGateway::ApiKey"
	TypeRESTUsagePlan   = "koldstart::ApiGateway::UsagePlan"
)

// decodeInputs reads the request's inputs into v.
func decodeInputs(data json.RawMessage, v any) error {
	if len(data) == 0 {
		return nil
	}
	if err := json.Unmarshal(data, v); err != nil {
		return fmt.Errorf("reading the inputs: %w", err)
	}
	return nil
}

// ours checks the resource's tags say this node created it.
func ours(name string, tags map[string]string, req engine.Request) error {
	if tags[engine.TagNode] != req.ID || tags[engine.TagStage] != req.Tags[engine.TagStage] || tags[engine.TagApp] != req.Tags[engine.TagApp] {
		return fmt.Errorf("%s %w: give it another name, or delete it", name, engine.ErrNotOurs)
	}
	return nil
}

// decodeBoth reads the inputs a Replaces compares. Inputs that do not decode compare as empty.
func decodeBoth[T any](prior, desired json.RawMessage) (before, after T) {
	_ = json.Unmarshal(prior, &before)
	_ = json.Unmarshal(desired, &after)
	return before, after
}

// retry calls fn until it succeeds, fails with an error retryable does not accept, or wait has
// passed, trying again every every. AWS refuses some calls for a while: a role iam has not spread
// yet, or a resource still busy with the call before.
func retry(ctx context.Context, wait, every time.Duration, retryable func(error) bool, fn func() error) error {
	deadline := time.Now().Add(wait)
	for {
		err := fn()
		if err == nil || !retryable(err) || time.Now().After(deadline) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(every):
		}
	}
}

// changed reports whether the json of a and b differ.
func changed(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) != string(y)
}

// Partition is the aws partition of a region, for arns.
func Partition(region string) string {
	switch {
	case strings.HasPrefix(region, "cn-"):
		return "aws-cn"
	case strings.HasPrefix(region, "us-gov-"):
		return "aws-us-gov"
	default:
		return "aws"
	}
}
