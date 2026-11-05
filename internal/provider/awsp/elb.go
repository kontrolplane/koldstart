package awsp

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"

	"github.com/aws/aws-sdk-go-v2/aws"
	elb "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	"github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2/types"

	"github.com/kontrolplane/koldstart/internal/awserr"
	"github.com/kontrolplane/koldstart/internal/engine"
)

// TargetGroupInputs are the inputs of a target group a function is the target of. Its name is
// at most 32 long.
type TargetGroupInputs struct {
	Name              string `json:"name"`
	MultiValueHeaders bool   `json:"multiValueHeaders"`
}

// TargetGroup is the provider of the target groups that hand a load balancer's requests to
// functions.
type TargetGroup struct {
	Client *elb.Client
}

func targetGroupResult(arn, name string) engine.Result {
	return engine.Result{PhysicalID: arn, Attributes: map[string]string{attrArn: arn, "TargetGroupName": name}}
}

// multiValue sets whether the function gets headers and query parameters as lists.
func (p TargetGroup) multiValue(ctx context.Context, arn string, on bool) error {
	_, err := p.Client.ModifyTargetGroupAttributes(ctx, &elb.ModifyTargetGroupAttributesInput{
		TargetGroupArn: &arn,
		Attributes:     []types.TargetGroupAttribute{{Key: aws.String("lambda.multi_value_headers.enabled"), Value: aws.String(strconv.FormatBool(on))}},
	})
	if err != nil {
		return fmt.Errorf("setting the attributes of the target group %s: %w", arn, err)
	}
	return nil
}

// Create implements engine.Provider: the group, without its target, which Targets registers
// once lambda allows the load balancer to invoke the function.
func (p TargetGroup) Create(ctx context.Context, req engine.Request) (engine.Result, error) {
	var in TargetGroupInputs
	if err := decodeInputs(req.Inputs, &in); err != nil {
		return engine.Result{}, err
	}
	var tags []types.Tag
	for k, v := range req.Tags {
		tags = append(tags, types.Tag{Key: aws.String(k), Value: aws.String(v)})
	}
	out, err := p.Client.CreateTargetGroup(ctx, &elb.CreateTargetGroupInput{Name: &in.Name, TargetType: types.TargetTypeEnumLambda, Tags: tags})
	if awserr.Is(err, "DuplicateTargetGroupName") {
		return engine.Result{}, engine.ErrExists
	}
	if err != nil {
		return engine.Result{}, fmt.Errorf("creating the target group %s: %w", in.Name, err)
	}
	if len(out.TargetGroups) == 0 {
		return engine.Result{}, fmt.Errorf("creating the target group %s: no group came back", in.Name)
	}
	arn := aws.ToString(out.TargetGroups[0].TargetGroupArn)
	if in.MultiValueHeaders {
		if err := p.multiValue(ctx, arn, true); err != nil {
			return engine.Result{}, err
		}
	}
	return targetGroupResult(arn, in.Name), nil
}

// Read implements engine.Provider: by its arn, or by its name, which is unique in the region.
func (p TargetGroup) Read(ctx context.Context, req engine.Request) (engine.Result, bool, error) {
	var in TargetGroupInputs
	if err := decodeInputs(req.Inputs, &in); err != nil {
		return engine.Result{}, false, err
	}
	out, err := p.Client.DescribeTargetGroups(ctx, &elb.DescribeTargetGroupsInput{Names: []string{in.Name}})
	if awserr.Is(err, "TargetGroupNotFound") {
		return engine.Result{}, false, nil
	}
	if err != nil {
		return engine.Result{}, false, fmt.Errorf("reading the target group %s: %w", in.Name, err)
	}
	if len(out.TargetGroups) == 0 {
		return engine.Result{}, false, nil
	}
	return targetGroupResult(aws.ToString(out.TargetGroups[0].TargetGroupArn), in.Name), true, nil
}

// Update implements engine.Provider.
func (p TargetGroup) Update(ctx context.Context, req engine.Request) (engine.Result, error) {
	var in TargetGroupInputs
	if err := decodeInputs(req.Inputs, &in); err != nil {
		return engine.Result{}, err
	}
	if err := p.multiValue(ctx, req.PhysicalID, in.MultiValueHeaders); err != nil {
		return engine.Result{}, err
	}
	return targetGroupResult(req.PhysicalID, in.Name), nil
}

// Delete implements engine.Provider.
func (p TargetGroup) Delete(ctx context.Context, req engine.Request) error {
	_, err := p.Client.DeleteTargetGroup(ctx, &elb.DeleteTargetGroupInput{TargetGroupArn: &req.PhysicalID})
	if err != nil && !awserr.Is(err, "TargetGroupNotFound") {
		return fmt.Errorf("deleting the target group %s: %w", req.PhysicalID, err)
	}
	return nil
}

// Replaces implements engine.Provider: a target group is named.
func (TargetGroup) Replaces(prior, desired json.RawMessage) bool {
	a, b := decodeBoth[TargetGroupInputs](prior, desired)
	return a.Name != b.Name
}

// TargetsInputs register a function as the target of a target group.
type TargetsInputs struct {
	TargetGroup string `json:"targetGroup"`
	Function    string `json:"function"`
}

// Targets is the provider of a function registered in a target group.
type Targets struct {
	Client *elb.Client
}

func (in TargetsInputs) register(ctx context.Context, client *elb.Client) error {
	_, err := client.RegisterTargets(ctx, &elb.RegisterTargetsInput{TargetGroupArn: &in.TargetGroup, Targets: []types.TargetDescription{{Id: &in.Function}}})
	if err != nil {
		return fmt.Errorf("registering %s in %s: %w", in.Function, in.TargetGroup, err)
	}
	return nil
}

func targetsResult(in TargetsInputs) engine.Result {
	return engine.Result{PhysicalID: in.TargetGroup + "|" + in.Function}
}

// Create implements engine.Provider.
func (p Targets) Create(ctx context.Context, req engine.Request) (engine.Result, error) {
	var in TargetsInputs
	if err := decodeInputs(req.Inputs, &in); err != nil {
		return engine.Result{}, err
	}
	return targetsResult(in), in.register(ctx, p.Client)
}

// Read implements engine.Provider: whether the function is a target of the group.
func (p Targets) Read(ctx context.Context, req engine.Request) (engine.Result, bool, error) {
	var in TargetsInputs
	if err := decodeInputs(req.Inputs, &in); err != nil {
		return engine.Result{}, false, err
	}
	out, err := p.Client.DescribeTargetHealth(ctx, &elb.DescribeTargetHealthInput{TargetGroupArn: &in.TargetGroup})
	if awserr.Is(err, "TargetGroupNotFound") {
		return engine.Result{}, false, nil
	}
	if err != nil {
		return engine.Result{}, false, fmt.Errorf("reading the targets of %s: %w", in.TargetGroup, err)
	}
	found := slices.ContainsFunc(out.TargetHealthDescriptions, func(d types.TargetHealthDescription) bool {
		return d.Target != nil && aws.ToString(d.Target.Id) == in.Function
	})
	return targetsResult(in), found, nil
}

// Update implements engine.Provider: the function the group targets changes.
func (p Targets) Update(ctx context.Context, req engine.Request) (engine.Result, error) {
	var in, prior TargetsInputs
	if err := decodeInputs(req.Inputs, &in); err != nil {
		return engine.Result{}, err
	}
	if err := in.register(ctx, p.Client); err != nil {
		return engine.Result{}, err
	}
	if decodeInputs(req.Prior, &prior) == nil && prior.Function != "" && prior.Function != in.Function {
		if err := p.deregister(ctx, prior); err != nil {
			return engine.Result{}, err
		}
	}
	return targetsResult(in), nil
}

func (p Targets) deregister(ctx context.Context, in TargetsInputs) error {
	_, err := p.Client.DeregisterTargets(ctx, &elb.DeregisterTargetsInput{TargetGroupArn: &in.TargetGroup, Targets: []types.TargetDescription{{Id: &in.Function}}})
	if err != nil && !awserr.Is(err, "TargetGroupNotFound", "InvalidTarget") {
		return fmt.Errorf("deregistering %s from %s: %w", in.Function, in.TargetGroup, err)
	}
	return nil
}

// Delete implements engine.Provider.
func (p Targets) Delete(ctx context.Context, req engine.Request) error {
	var in TargetsInputs
	if err := decodeInputs(req.Prior, &in); err != nil {
		return err
	}
	return p.deregister(ctx, in)
}

// Replaces implements engine.Provider: a registration belongs to its group.
func (Targets) Replaces(prior, desired json.RawMessage) bool {
	a, b := decodeBoth[TargetsInputs](prior, desired)
	return a.TargetGroup != b.TargetGroup
}

// ListenerRuleInputs are the inputs of a rule of a load balancer's listener that forwards what
// it matches to a target group.
type ListenerRuleInputs struct {
	Listener    string   `json:"listener"`
	Priority    int32    `json:"priority"`
	TargetGroup string   `json:"targetGroup"`
	Paths       []string `json:"paths"`
	Hosts       []string `json:"hosts"`
	Methods     []string `json:"methods"`
}

// ListenerRule is the provider of listener rules.
type ListenerRule struct {
	Client *elb.Client
}

func (in ListenerRuleInputs) conditions() []types.RuleCondition {
	var out []types.RuleCondition
	if len(in.Paths) > 0 {
		out = append(out, types.RuleCondition{Field: aws.String("path-pattern"), PathPatternConfig: &types.PathPatternConditionConfig{Values: in.Paths}})
	}
	if len(in.Hosts) > 0 {
		out = append(out, types.RuleCondition{Field: aws.String("host-header"), HostHeaderConfig: &types.HostHeaderConditionConfig{Values: in.Hosts}})
	}
	if len(in.Methods) > 0 {
		out = append(out, types.RuleCondition{Field: aws.String("http-request-method"), HttpRequestMethodConfig: &types.HttpRequestMethodConditionConfig{Values: in.Methods}})
	}
	return out
}

func (in ListenerRuleInputs) actions() []types.Action {
	return []types.Action{{Type: types.ActionTypeEnumForward, TargetGroupArn: &in.TargetGroup}}
}

func listenerRuleResult(arn string) engine.Result {
	return engine.Result{PhysicalID: arn, Attributes: map[string]string{attrArn: arn}}
}

// Create implements engine.Provider.
func (p ListenerRule) Create(ctx context.Context, req engine.Request) (engine.Result, error) {
	var in ListenerRuleInputs
	if err := decodeInputs(req.Inputs, &in); err != nil {
		return engine.Result{}, err
	}
	var tags []types.Tag
	for k, v := range req.Tags {
		tags = append(tags, types.Tag{Key: aws.String(k), Value: aws.String(v)})
	}
	out, err := p.Client.CreateRule(ctx, &elb.CreateRuleInput{
		ListenerArn: &in.Listener, Priority: &in.Priority, Conditions: in.conditions(), Actions: in.actions(), Tags: tags,
	})
	if awserr.Is(err, "PriorityInUse") {
		return engine.Result{}, engine.ErrExists
	}
	if err != nil {
		return engine.Result{}, fmt.Errorf("creating the rule of priority %d on %s: %w", in.Priority, in.Listener, err)
	}
	if len(out.Rules) == 0 {
		return engine.Result{}, fmt.Errorf("creating the rule of priority %d: no rule came back", in.Priority)
	}
	return listenerRuleResult(aws.ToString(out.Rules[0].RuleArn)), nil
}

// Read implements engine.Provider: by its arn, or the rule of its priority on the listener.
func (p ListenerRule) Read(ctx context.Context, req engine.Request) (engine.Result, bool, error) {
	var in ListenerRuleInputs
	if err := decodeInputs(req.Inputs, &in); err != nil {
		return engine.Result{}, false, err
	}
	out, err := p.Client.DescribeRules(ctx, &elb.DescribeRulesInput{ListenerArn: &in.Listener})
	if awserr.Is(err, "ListenerNotFound") {
		return engine.Result{}, false, nil
	}
	if err != nil {
		return engine.Result{}, false, fmt.Errorf("reading the rules of %s: %w", in.Listener, err)
	}
	priority := strconv.Itoa(int(in.Priority))
	for _, r := range out.Rules {
		if aws.ToString(r.RuleArn) == req.PhysicalID || aws.ToString(r.Priority) == priority {
			return listenerRuleResult(aws.ToString(r.RuleArn)), true, nil
		}
	}
	return engine.Result{}, false, nil
}

// Update implements engine.Provider: its conditions and target, and its priority.
func (p ListenerRule) Update(ctx context.Context, req engine.Request) (engine.Result, error) {
	var in, prior ListenerRuleInputs
	if err := decodeInputs(req.Inputs, &in); err != nil {
		return engine.Result{}, err
	}
	_, err := p.Client.ModifyRule(ctx, &elb.ModifyRuleInput{RuleArn: &req.PhysicalID, Conditions: in.conditions(), Actions: in.actions()})
	if err != nil {
		return engine.Result{}, fmt.Errorf("updating the rule %s: %w", req.PhysicalID, err)
	}
	if decodeInputs(req.Prior, &prior) == nil && prior.Priority != in.Priority {
		_, err := p.Client.SetRulePriorities(ctx, &elb.SetRulePrioritiesInput{
			RulePriorities: []types.RulePriorityPair{{RuleArn: &req.PhysicalID, Priority: &in.Priority}},
		})
		if err != nil {
			return engine.Result{}, fmt.Errorf("moving the rule %s to priority %d: %w", req.PhysicalID, in.Priority, err)
		}
	}
	return listenerRuleResult(req.PhysicalID), nil
}

// Delete implements engine.Provider.
func (p ListenerRule) Delete(ctx context.Context, req engine.Request) error {
	_, err := p.Client.DeleteRule(ctx, &elb.DeleteRuleInput{RuleArn: &req.PhysicalID})
	if err != nil && !awserr.Is(err, "RuleNotFound") {
		return fmt.Errorf("deleting the rule %s: %w", req.PhysicalID, err)
	}
	return nil
}

// Replaces implements engine.Provider: a rule belongs to its listener.
func (ListenerRule) Replaces(prior, desired json.RawMessage) bool {
	a, b := decodeBoth[ListenerRuleInputs](prior, desired)
	return a.Listener != b.Listener
}
