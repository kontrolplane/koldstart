// Package awsp holds the providers koldstart writes by hand, for what every function needs: its
// role, log group, the function itself and its url. They call the aws apis directly, so a
// change that lambda can make in place takes one call.
package awsp

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/aws/smithy-go"

	"github.com/kontrolplane/koldstart/internal/engine"
)

// attrArn is the attribute ref() reads an arn from, as CloudFormation names it.
const attrArn = "Arn"

// The types the providers here handle. They are koldstart's own names, so a resource of the same
// CloudFormation type in resources still goes to cloud control.
const (
	TypeLogGroup     = "koldstart::Logs::LogGroup"
	TypeRole         = "koldstart::IAM::Role"
	TypeFunction     = "koldstart::Lambda::Function"
	TypeURL          = "koldstart::Lambda::Url"
	TypePermission   = "koldstart::Lambda::Permission"
	TypeHTTPAPI      = "koldstart::ApiGatewayV2::Api"
	TypeIntegration  = "koldstart::ApiGatewayV2::Integration"
	TypeRoute        = "koldstart::ApiGatewayV2::Route"
	TypeAuthorizer   = "koldstart::ApiGatewayV2::Authorizer"
	TypeDomain       = "koldstart::ApiGatewayV2::DomainName"
	TypeSchedule     = "koldstart::Scheduler::Schedule"
	TypeMapping      = "koldstart::Lambda::EventSourceMapping"
	TypeSubscription = "koldstart::SNS::Subscription"
	TypeNotification = "koldstart::S3::BucketNotification"
)

func errorCode(err error) string {
	if api, ok := errors.AsType[smithy.APIError](err); ok {
		return api.ErrorCode()
	}
	return ""
}

func isCode(err error, codes ...string) bool {
	return slices.Contains(codes, errorCode(err))
}

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
	if tags["koldstart:node"] != req.ID || tags["koldstart:stage"] != req.Tags["koldstart:stage"] || tags["koldstart:app"] != req.Tags["koldstart:app"] {
		return fmt.Errorf("%s %w: give it another name, or delete it", name, engine.ErrNotOurs)
	}
	return nil
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
