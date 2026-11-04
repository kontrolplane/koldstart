package awsp

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/lambda"

	"github.com/kontrolplane/koldstart/internal/engine"
)

// PermissionInputs are the inputs of a permission node: a statement on a function's resource
// policy that lets a service invoke it.
type PermissionInputs struct {
	Function      string `json:"function"`
	StatementID   string `json:"statementId"`
	Action        string `json:"action"`
	Principal     string `json:"principal"`
	SourceArn     string `json:"sourceArn"`
	SourceAccount string `json:"sourceAccount"`
}

// Permission is the provider of the statements that let api gateway, sns, s3 and the scheduler
// invoke a function.
type Permission struct {
	Client *lambda.Client
}

func (in PermissionInputs) add() *lambda.AddPermissionInput {
	out := &lambda.AddPermissionInput{
		FunctionName: &in.Function, StatementId: &in.StatementID, Action: &in.Action, Principal: &in.Principal,
	}
	if in.SourceArn != "" {
		out.SourceArn = &in.SourceArn
	}
	if in.SourceAccount != "" {
		out.SourceAccount = &in.SourceAccount
	}
	return out
}

func permissionResult(in PermissionInputs) engine.Result {
	return engine.Result{PhysicalID: in.Function + "/" + in.StatementID, Attributes: map[string]string{"StatementId": in.StatementID}}
}

// Create implements engine.Provider.
func (p Permission) Create(ctx context.Context, req engine.Request) (engine.Result, error) {
	var in PermissionInputs
	if err := decodeInputs(req.Inputs, &in); err != nil {
		return engine.Result{}, err
	}
	_, err := p.Client.AddPermission(ctx, in.add())
	if isCode(err, "ResourceConflictException") {
		return engine.Result{}, engine.ErrExists
	}
	if err != nil {
		return engine.Result{}, fmt.Errorf("letting %s invoke %s: %w", in.Principal, in.Function, err)
	}
	return permissionResult(in), nil
}

// Read implements engine.Provider.
func (p Permission) Read(ctx context.Context, req engine.Request) (engine.Result, bool, error) {
	var in PermissionInputs
	if err := decodeInputs(req.Inputs, &in); err != nil {
		return engine.Result{}, false, err
	}
	out, err := p.Client.GetPolicy(ctx, &lambda.GetPolicyInput{FunctionName: &in.Function})
	if isCode(err, "ResourceNotFoundException") {
		return engine.Result{}, false, nil
	}
	if err != nil {
		return engine.Result{}, false, fmt.Errorf("reading the policy of %s: %w", in.Function, err)
	}
	var policy struct {
		Statement []struct {
			Sid string `json:"Sid"`
		} `json:"Statement"`
	}
	if err := json.Unmarshal([]byte(aws.ToString(out.Policy)), &policy); err != nil {
		return engine.Result{}, false, fmt.Errorf("reading the policy of %s: %w", in.Function, err)
	}
	for _, s := range policy.Statement {
		if s.Sid == in.StatementID {
			return permissionResult(in), true, nil
		}
	}
	return engine.Result{}, false, nil
}

// Update implements engine.Provider: a statement cannot be changed, so it is put again.
func (p Permission) Update(ctx context.Context, req engine.Request) (engine.Result, error) {
	if err := p.Delete(ctx, req); err != nil {
		return engine.Result{}, err
	}
	req.Prior = nil
	return p.Create(ctx, req)
}

// Delete implements engine.Provider.
func (p Permission) Delete(ctx context.Context, req engine.Request) error {
	var in PermissionInputs
	data := req.Prior
	if len(data) == 0 {
		data = req.Inputs
	}
	if err := decodeInputs(data, &in); err != nil {
		return err
	}
	_, err := p.Client.RemovePermission(ctx, &lambda.RemovePermissionInput{FunctionName: &in.Function, StatementId: &in.StatementID})
	if err != nil && !isCode(err, "ResourceNotFoundException") {
		return fmt.Errorf("removing %s from %s: %w", in.StatementID, in.Function, err)
	}
	return nil
}

// Replaces implements engine.Provider.
func (Permission) Replaces(prior, desired json.RawMessage) bool {
	var a, b PermissionInputs
	_ = json.Unmarshal(prior, &a)
	_ = json.Unmarshal(desired, &b)
	return a.Function != b.Function || a.StatementID != b.StatementID
}
