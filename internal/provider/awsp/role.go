package awsp

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/iam/types"

	"github.com/kontrolplane/koldstart/internal/engine"
)

// RoleInputs are the inputs of a role node. The policies are json documents as values, which
// may hold refs until they are resolved.
type RoleInputs struct {
	Name             string   `json:"name"`
	AssumeRolePolicy any      `json:"assumeRolePolicy"`
	Policy           any      `json:"policy"`
	ManagedPolicies  []string `json:"managedPolicies"`
}

// policyName is the name of the inline policy koldstart puts on a role.
const policyName = "koldstart"

// Role is the provider of a function's execution role.
type Role struct {
	Client *iam.Client
}

func document(v any) (string, error) {
	data, err := json.Marshal(v)
	return string(data), err
}

func iamTags(tags map[string]string) []types.Tag {
	out := make([]types.Tag, 0, len(tags))
	for _, k := range slices.Sorted(maps.Keys(tags)) {
		out = append(out, types.Tag{Key: aws.String(k), Value: aws.String(tags[k])})
	}
	return out
}

// Create implements engine.Provider.
func (p Role) Create(ctx context.Context, req engine.Request) (engine.Result, error) {
	var in RoleInputs
	if err := decodeInputs(req.Inputs, &in); err != nil {
		return engine.Result{}, err
	}
	trust, err := document(in.AssumeRolePolicy)
	if err != nil {
		return engine.Result{}, err
	}
	out, err := p.Client.CreateRole(ctx, &iam.CreateRoleInput{RoleName: &in.Name, AssumeRolePolicyDocument: &trust, Tags: iamTags(req.Tags)})
	if isCode(err, "EntityAlreadyExists") {
		return engine.Result{}, engine.ErrExists
	}
	if err != nil {
		return engine.Result{}, fmt.Errorf("creating the role %s: %w", in.Name, err)
	}
	if err := p.policies(ctx, in, nil); err != nil {
		return engine.Result{}, err
	}
	return roleResult(out.Role), nil
}

func roleResult(r *types.Role) engine.Result {
	return engine.Result{PhysicalID: aws.ToString(r.RoleName), Attributes: map[string]string{attrArn: aws.ToString(r.Arn), "RoleName": aws.ToString(r.RoleName)}}
}

// policies puts the inline policy and attaches and detaches managed ones to match in.
func (p Role) policies(ctx context.Context, in RoleInputs, prior []string) error {
	if in.Policy != nil {
		doc, err := document(in.Policy)
		if err != nil {
			return err
		}
		if _, err := p.Client.PutRolePolicy(ctx, &iam.PutRolePolicyInput{RoleName: &in.Name, PolicyName: aws.String(policyName), PolicyDocument: &doc}); err != nil {
			return fmt.Errorf("putting the policy of %s: %w", in.Name, err)
		}
	} else {
		_, err := p.Client.DeleteRolePolicy(ctx, &iam.DeleteRolePolicyInput{RoleName: &in.Name, PolicyName: aws.String(policyName)})
		if err != nil && !isCode(err, "NoSuchEntity") {
			return fmt.Errorf("deleting the policy of %s: %w", in.Name, err)
		}
	}
	for _, arn := range in.ManagedPolicies {
		if slices.Contains(prior, arn) {
			continue
		}
		if _, err := p.Client.AttachRolePolicy(ctx, &iam.AttachRolePolicyInput{RoleName: &in.Name, PolicyArn: aws.String(arn)}); err != nil {
			return fmt.Errorf("attaching %s to %s: %w", arn, in.Name, err)
		}
	}
	for _, arn := range prior {
		if slices.Contains(in.ManagedPolicies, arn) {
			continue
		}
		_, err := p.Client.DetachRolePolicy(ctx, &iam.DetachRolePolicyInput{RoleName: &in.Name, PolicyArn: aws.String(arn)})
		if err != nil && !isCode(err, "NoSuchEntity") {
			return fmt.Errorf("detaching %s from %s: %w", arn, in.Name, err)
		}
	}
	return nil
}

// Read implements engine.Provider.
func (p Role) Read(ctx context.Context, req engine.Request) (engine.Result, bool, error) {
	out, err := p.Client.GetRole(ctx, &iam.GetRoleInput{RoleName: &req.PhysicalID})
	if isCode(err, "NoSuchEntity") {
		return engine.Result{}, false, nil
	}
	if err != nil {
		return engine.Result{}, false, fmt.Errorf("reading the role %s: %w", req.PhysicalID, err)
	}
	tags := map[string]string{}
	for _, t := range out.Role.Tags {
		tags[aws.ToString(t.Key)] = aws.ToString(t.Value)
	}
	if err := ours(req.PhysicalID, tags, req); err != nil {
		return engine.Result{}, false, err
	}
	return roleResult(out.Role), true, nil
}

// Update implements engine.Provider. Without prior inputs it is taking over a role a crashed
// deploy made, and detaches whatever is attached that should not be.
func (p Role) Update(ctx context.Context, req engine.Request) (engine.Result, error) {
	var in, prior RoleInputs
	if err := decodeInputs(req.Inputs, &in); err != nil {
		return engine.Result{}, err
	}
	if err := decodeInputs(req.Prior, &prior); err != nil {
		return engine.Result{}, err
	}
	if len(req.Prior) == 0 {
		attached, err := p.Client.ListAttachedRolePolicies(ctx, &iam.ListAttachedRolePoliciesInput{RoleName: &in.Name})
		if err != nil {
			return engine.Result{}, fmt.Errorf("listing the policies of %s: %w", in.Name, err)
		}
		for _, a := range attached.AttachedPolicies {
			prior.ManagedPolicies = append(prior.ManagedPolicies, aws.ToString(a.PolicyArn))
		}
	}
	if changed(in.AssumeRolePolicy, prior.AssumeRolePolicy) {
		trust, err := document(in.AssumeRolePolicy)
		if err != nil {
			return engine.Result{}, err
		}
		if _, err := p.Client.UpdateAssumeRolePolicy(ctx, &iam.UpdateAssumeRolePolicyInput{RoleName: &in.Name, PolicyDocument: &trust}); err != nil {
			return engine.Result{}, fmt.Errorf("updating who may assume %s: %w", in.Name, err)
		}
	}
	if err := p.policies(ctx, in, prior.ManagedPolicies); err != nil {
		return engine.Result{}, err
	}
	if _, err := p.Client.TagRole(ctx, &iam.TagRoleInput{RoleName: &in.Name, Tags: iamTags(req.Tags)}); err != nil {
		return engine.Result{}, fmt.Errorf("tagging %s: %w", in.Name, err)
	}
	out, err := p.Client.GetRole(ctx, &iam.GetRoleInput{RoleName: &in.Name})
	if err != nil {
		return engine.Result{}, fmt.Errorf("reading the role %s: %w", in.Name, err)
	}
	return roleResult(out.Role), nil
}

// Delete implements engine.Provider: the policies first, as iam requires.
func (p Role) Delete(ctx context.Context, req engine.Request) error {
	name := req.PhysicalID
	attached, err := p.Client.ListAttachedRolePolicies(ctx, &iam.ListAttachedRolePoliciesInput{RoleName: &name})
	if isCode(err, "NoSuchEntity") {
		return nil
	}
	if err != nil {
		return fmt.Errorf("listing the policies of %s: %w", name, err)
	}
	for _, a := range attached.AttachedPolicies {
		if _, err := p.Client.DetachRolePolicy(ctx, &iam.DetachRolePolicyInput{RoleName: &name, PolicyArn: a.PolicyArn}); err != nil && !isCode(err, "NoSuchEntity") {
			return fmt.Errorf("detaching %s from %s: %w", aws.ToString(a.PolicyArn), name, err)
		}
	}
	inline, err := p.Client.ListRolePolicies(ctx, &iam.ListRolePoliciesInput{RoleName: &name})
	if err != nil && !isCode(err, "NoSuchEntity") {
		return fmt.Errorf("listing the policies of %s: %w", name, err)
	}
	if inline != nil {
		for _, policy := range inline.PolicyNames {
			if _, err := p.Client.DeleteRolePolicy(ctx, &iam.DeleteRolePolicyInput{RoleName: &name, PolicyName: aws.String(policy)}); err != nil && !isCode(err, "NoSuchEntity") {
				return fmt.Errorf("deleting the policy %s of %s: %w", policy, name, err)
			}
		}
	}
	if _, err := p.Client.DeleteRole(ctx, &iam.DeleteRoleInput{RoleName: &name}); err != nil && !isCode(err, "NoSuchEntity") {
		return fmt.Errorf("deleting the role %s: %w", name, err)
	}
	return nil
}

// Replaces implements engine.Provider: a role cannot be renamed.
func (Role) Replaces(prior, desired json.RawMessage) bool {
	var a, b RoleInputs
	_ = json.Unmarshal(prior, &a)
	_ = json.Unmarshal(desired, &b)
	return a.Name != b.Name
}
