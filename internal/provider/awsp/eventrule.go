package awsp

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge/types"

	"github.com/kontrolplane/koldstart/internal/awserr"
	"github.com/kontrolplane/koldstart/internal/engine"
)

// ruleTarget is the id of the one target of a rule koldstart makes.
const ruleTarget = "koldstart"

// RuleInputs are the inputs of an eventbridge rule that sends what matches its pattern to a
// function.
type RuleInputs struct {
	Name     string `json:"name"`
	Bus      string `json:"bus"`
	Pattern  string `json:"pattern"`
	Function string `json:"function"`
	Input    string `json:"input"`
	Enabled  bool   `json:"enabled"`
}

// Rule is the provider of eventbridge rules.
type Rule struct {
	Client *eventbridge.Client
}

func (in RuleInputs) state() types.RuleState {
	if in.Enabled {
		return types.RuleStateEnabled
	}
	return types.RuleStateDisabled
}

func ruleResult(arn, name string) engine.Result {
	return engine.Result{PhysicalID: name, Attributes: map[string]string{attrArn: arn, "Name": name}}
}

// put creates the rule, or brings it in line, and points it at the function.
func (p Rule) put(ctx context.Context, in RuleInputs, tags map[string]string) (engine.Result, error) {
	ruleTags := make([]types.Tag, 0, len(tags))
	for k, v := range tags {
		ruleTags = append(ruleTags, types.Tag{Key: aws.String(k), Value: aws.String(v)})
	}
	out, err := p.Client.PutRule(ctx, &eventbridge.PutRuleInput{
		Name: &in.Name, EventBusName: &in.Bus, EventPattern: &in.Pattern, State: in.state(), Tags: ruleTags,
	})
	if err != nil {
		return engine.Result{}, fmt.Errorf("putting the rule %s: %w", in.Name, err)
	}
	target := types.Target{Id: aws.String(ruleTarget), Arn: &in.Function}
	if in.Input != "" {
		target.Input = &in.Input
	}
	failed, err := p.Client.PutTargets(ctx, &eventbridge.PutTargetsInput{Rule: &in.Name, EventBusName: &in.Bus, Targets: []types.Target{target}})
	if err != nil {
		return engine.Result{}, fmt.Errorf("pointing the rule %s at %s: %w", in.Name, in.Function, err)
	}
	if failed.FailedEntryCount > 0 && len(failed.FailedEntries) > 0 {
		return engine.Result{}, fmt.Errorf("pointing the rule %s at %s: %s", in.Name, in.Function, aws.ToString(failed.FailedEntries[0].ErrorMessage))
	}
	return ruleResult(aws.ToString(out.RuleArn), in.Name), nil
}

// Create implements engine.Provider. PutRule takes over a rule of the name that exists.
func (p Rule) Create(ctx context.Context, req engine.Request) (engine.Result, error) {
	var in RuleInputs
	if err := decodeInputs(req.Inputs, &in); err != nil {
		return engine.Result{}, err
	}
	return p.put(ctx, in, req.Tags)
}

// Read implements engine.Provider.
func (p Rule) Read(ctx context.Context, req engine.Request) (engine.Result, bool, error) {
	var in RuleInputs
	if err := decodeInputs(req.Inputs, &in); err != nil {
		return engine.Result{}, false, err
	}
	out, err := p.Client.DescribeRule(ctx, &eventbridge.DescribeRuleInput{Name: &in.Name, EventBusName: &in.Bus})
	if awserr.Is(err, "ResourceNotFoundException") {
		return engine.Result{}, false, nil
	}
	if err != nil {
		return engine.Result{}, false, fmt.Errorf("reading the rule %s: %w", in.Name, err)
	}
	return ruleResult(aws.ToString(out.Arn), in.Name), true, nil
}

// Update implements engine.Provider.
func (p Rule) Update(ctx context.Context, req engine.Request) (engine.Result, error) {
	var in RuleInputs
	if err := decodeInputs(req.Inputs, &in); err != nil {
		return engine.Result{}, err
	}
	return p.put(ctx, in, nil)
}

// Delete implements engine.Provider: its target first, as a rule with targets is not deleted.
func (p Rule) Delete(ctx context.Context, req engine.Request) error {
	var in RuleInputs
	if err := decodeInputs(req.Prior, &in); err != nil {
		return err
	}
	_, err := p.Client.RemoveTargets(ctx, &eventbridge.RemoveTargetsInput{Rule: &in.Name, EventBusName: &in.Bus, Ids: []string{ruleTarget}})
	if err != nil && !awserr.Is(err, "ResourceNotFoundException") {
		return fmt.Errorf("removing the target of the rule %s: %w", in.Name, err)
	}
	_, err = p.Client.DeleteRule(ctx, &eventbridge.DeleteRuleInput{Name: &in.Name, EventBusName: &in.Bus})
	if err != nil && !awserr.Is(err, "ResourceNotFoundException") {
		return fmt.Errorf("deleting the rule %s: %w", in.Name, err)
	}
	return nil
}

// Replaces implements engine.Provider: a rule is named, on its bus.
func (Rule) Replaces(prior, desired json.RawMessage) bool {
	a, b := decodeBoth[RuleInputs](prior, desired)
	return a.Name != b.Name || a.Bus != b.Bus
}
