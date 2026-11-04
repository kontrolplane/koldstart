package awsp

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	"github.com/aws/aws-sdk-go-v2/service/lambda/types"

	"github.com/kontrolplane/koldstart/internal/engine"
)

// URLInputs are the inputs of a function url node.
type URLInputs struct {
	Function string   `json:"function"`
	Auth     string   `json:"auth"` // NONE or AWS_IAM
	CORS     *URLCORS `json:"cors"`
}

// URLCORS is the cross-origin configuration of a function url.
type URLCORS struct {
	Origins     []string `json:"origins"`
	Methods     []string `json:"methods"`
	Headers     []string `json:"headers"`
	Expose      []string `json:"expose"`
	Credentials bool     `json:"credentials"`
	MaxAge      int32    `json:"maxAge"`
}

// The statements a public url puts on its function. Since october 2025 a public url needs both:
// one to call the url, one to invoke the function through it.
const (
	statementURL    = "koldstart-url"
	statementInvoke = "koldstart-url-invoke"
)

// URL is the provider of function urls.
type URL struct {
	Client *lambda.Client
}

func (u URLInputs) cors() *types.Cors {
	if u.CORS == nil {
		return &types.Cors{}
	}
	return &types.Cors{
		AllowOrigins: u.CORS.Origins, AllowMethods: u.CORS.Methods, AllowHeaders: u.CORS.Headers,
		ExposeHeaders: u.CORS.Expose, AllowCredentials: &u.CORS.Credentials, MaxAge: &u.CORS.MaxAge,
	}
}

func urlResult(out string, function string) engine.Result {
	return engine.Result{PhysicalID: function, Attributes: map[string]string{"FunctionUrl": out}}
}

// Create implements engine.Provider.
func (p URL) Create(ctx context.Context, req engine.Request) (engine.Result, error) {
	var in URLInputs
	if err := decodeInputs(req.Inputs, &in); err != nil {
		return engine.Result{}, err
	}
	out, err := p.Client.CreateFunctionUrlConfig(ctx, &lambda.CreateFunctionUrlConfigInput{
		FunctionName: &in.Function, AuthType: types.FunctionUrlAuthType(in.Auth), Cors: in.cors(),
	})
	if isCode(err, "ResourceConflictException") {
		return engine.Result{}, engine.ErrExists
	}
	if err != nil {
		return engine.Result{}, fmt.Errorf("creating the url of %s: %w", in.Function, err)
	}
	if err := p.permissions(ctx, in); err != nil {
		return engine.Result{}, err
	}
	return urlResult(aws.ToString(out.FunctionUrl), in.Function), nil
}

// permissions lets anyone call a public url, and takes that away again from one with iam auth.
func (p URL) permissions(ctx context.Context, in URLInputs) error {
	if in.Auth != string(types.FunctionUrlAuthTypeNone) {
		return p.removePermissions(ctx, in.Function)
	}
	statements := []*lambda.AddPermissionInput{
		{FunctionName: &in.Function, StatementId: aws.String(statementURL), Action: aws.String("lambda:InvokeFunctionUrl"), Principal: aws.String("*"), FunctionUrlAuthType: types.FunctionUrlAuthTypeNone},
		{FunctionName: &in.Function, StatementId: aws.String(statementInvoke), Action: aws.String("lambda:InvokeFunction"), Principal: aws.String("*"), InvokedViaFunctionUrl: aws.Bool(true)},
	}
	for _, s := range statements {
		_, err := p.Client.AddPermission(ctx, s)
		if err != nil && !isCode(err, "ResourceConflictException") {
			return fmt.Errorf("making the url of %s public: %w", in.Function, err)
		}
	}
	return nil
}

func (p URL) removePermissions(ctx context.Context, function string) error {
	for _, id := range []string{statementURL, statementInvoke} {
		_, err := p.Client.RemovePermission(ctx, &lambda.RemovePermissionInput{FunctionName: &function, StatementId: aws.String(id)})
		if err != nil && !isCode(err, "ResourceNotFoundException") {
			return fmt.Errorf("removing %s from %s: %w", id, function, err)
		}
	}
	return nil
}

// Read implements engine.Provider. A url has no tags of its own; its function's say whose it is.
func (p URL) Read(ctx context.Context, req engine.Request) (engine.Result, bool, error) {
	out, err := p.Client.GetFunctionUrlConfig(ctx, &lambda.GetFunctionUrlConfigInput{FunctionName: &req.PhysicalID})
	if isCode(err, "ResourceNotFoundException") {
		return engine.Result{}, false, nil
	}
	if err != nil {
		return engine.Result{}, false, fmt.Errorf("reading the url of %s: %w", req.PhysicalID, err)
	}
	return urlResult(aws.ToString(out.FunctionUrl), req.PhysicalID), true, nil
}

// Update implements engine.Provider.
func (p URL) Update(ctx context.Context, req engine.Request) (engine.Result, error) {
	var in URLInputs
	if err := decodeInputs(req.Inputs, &in); err != nil {
		return engine.Result{}, err
	}
	out, err := p.Client.UpdateFunctionUrlConfig(ctx, &lambda.UpdateFunctionUrlConfigInput{
		FunctionName: &in.Function, AuthType: types.FunctionUrlAuthType(in.Auth), Cors: in.cors(),
	})
	if err != nil {
		return engine.Result{}, fmt.Errorf("updating the url of %s: %w", in.Function, err)
	}
	if err := p.permissions(ctx, in); err != nil {
		return engine.Result{}, err
	}
	return urlResult(aws.ToString(out.FunctionUrl), in.Function), nil
}

// Delete implements engine.Provider.
func (p URL) Delete(ctx context.Context, req engine.Request) error {
	if err := p.removePermissions(ctx, req.PhysicalID); err != nil {
		return err
	}
	_, err := p.Client.DeleteFunctionUrlConfig(ctx, &lambda.DeleteFunctionUrlConfigInput{FunctionName: &req.PhysicalID})
	if err != nil && !isCode(err, "ResourceNotFoundException") {
		return fmt.Errorf("deleting the url of %s: %w", req.PhysicalID, err)
	}
	return nil
}

// Replaces implements engine.Provider: a url belongs to its function.
func (URL) Replaces(prior, desired json.RawMessage) bool {
	var a, b URLInputs
	_ = json.Unmarshal(prior, &a)
	_ = json.Unmarshal(desired, &b)
	return a.Function != b.Function
}
