package awsp

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"

	"github.com/kontrolplane/koldstart/internal/engine"
)

// LogGroupInputs are the inputs of a log group node.
type LogGroupInputs struct {
	Name      string `json:"name"`
	Retention int32  `json:"retention"`
}

// LogGroup is the provider of a function's log group, which koldstart creates before the
// function so lambda never makes one of its own that keeps logs forever.
type LogGroup struct {
	Client *cloudwatchlogs.Client
}

// Create implements engine.Provider.
func (p LogGroup) Create(ctx context.Context, req engine.Request) (engine.Result, error) {
	var in LogGroupInputs
	if err := decodeInputs(req.Inputs, &in); err != nil {
		return engine.Result{}, err
	}
	_, err := p.Client.CreateLogGroup(ctx, &cloudwatchlogs.CreateLogGroupInput{LogGroupName: &in.Name, Tags: req.Tags})
	if isCode(err, "ResourceAlreadyExistsException") {
		return engine.Result{}, engine.ErrExists
	}
	if err != nil {
		return engine.Result{}, fmt.Errorf("creating the log group %s: %w", in.Name, err)
	}
	if err := p.retention(ctx, in); err != nil {
		return engine.Result{}, err
	}
	return p.result(ctx, in.Name)
}

func (p LogGroup) retention(ctx context.Context, in LogGroupInputs) error {
	if in.Retention == 0 {
		_, err := p.Client.DeleteRetentionPolicy(ctx, &cloudwatchlogs.DeleteRetentionPolicyInput{LogGroupName: &in.Name})
		return err
	}
	_, err := p.Client.PutRetentionPolicy(ctx, &cloudwatchlogs.PutRetentionPolicyInput{LogGroupName: &in.Name, RetentionInDays: &in.Retention})
	if err != nil {
		return fmt.Errorf("setting the retention of %s: %w", in.Name, err)
	}
	return nil
}

func (p LogGroup) find(ctx context.Context, name string) (string, bool, error) {
	out, err := p.Client.DescribeLogGroups(ctx, &cloudwatchlogs.DescribeLogGroupsInput{LogGroupNamePrefix: &name})
	if err != nil {
		return "", false, fmt.Errorf("finding the log group %s: %w", name, err)
	}
	for _, g := range out.LogGroups {
		if aws.ToString(g.LogGroupName) == name {
			return aws.ToString(g.Arn), true, nil
		}
	}
	return "", false, nil
}

func (p LogGroup) result(ctx context.Context, name string) (engine.Result, error) {
	arn, ok, err := p.find(ctx, name)
	if err != nil {
		return engine.Result{}, err
	}
	if !ok {
		return engine.Result{}, fmt.Errorf("the log group %s is gone", name)
	}
	return engine.Result{PhysicalID: name, Attributes: map[string]string{attrArn: arn, "LogGroupName": name}}, nil
}

// Read implements engine.Provider.
func (p LogGroup) Read(ctx context.Context, req engine.Request) (engine.Result, bool, error) {
	arn, ok, err := p.find(ctx, req.PhysicalID)
	if err != nil || !ok {
		return engine.Result{}, false, err
	}
	tags, err := p.Client.ListTagsForResource(ctx, &cloudwatchlogs.ListTagsForResourceInput{ResourceArn: aws.String(trimWildcard(arn))})
	if err != nil {
		return engine.Result{}, false, fmt.Errorf("reading the tags of %s: %w", req.PhysicalID, err)
	}
	if err := ours(req.PhysicalID, tags.Tags, req); err != nil {
		return engine.Result{}, false, err
	}
	return engine.Result{PhysicalID: req.PhysicalID, Attributes: map[string]string{attrArn: arn, "LogGroupName": req.PhysicalID}}, true, nil
}

// Update implements engine.Provider.
func (p LogGroup) Update(ctx context.Context, req engine.Request) (engine.Result, error) {
	var in LogGroupInputs
	if err := decodeInputs(req.Inputs, &in); err != nil {
		return engine.Result{}, err
	}
	if err := p.retention(ctx, in); err != nil {
		return engine.Result{}, err
	}
	res, err := p.result(ctx, in.Name)
	if err != nil {
		return res, err
	}
	if _, err := p.Client.TagResource(ctx, &cloudwatchlogs.TagResourceInput{ResourceArn: aws.String(trimWildcard(res.Attributes[attrArn])), Tags: maps.Clone(req.Tags)}); err != nil {
		return res, fmt.Errorf("tagging %s: %w", in.Name, err)
	}
	return res, nil
}

// Delete implements engine.Provider.
func (p LogGroup) Delete(ctx context.Context, req engine.Request) error {
	_, err := p.Client.DeleteLogGroup(ctx, &cloudwatchlogs.DeleteLogGroupInput{LogGroupName: &req.PhysicalID})
	if err != nil && !isCode(err, "ResourceNotFoundException") {
		return fmt.Errorf("deleting the log group %s: %w", req.PhysicalID, err)
	}
	return nil
}

// Replaces implements engine.Provider: a log group cannot be renamed.
func (LogGroup) Replaces(prior, desired json.RawMessage) bool {
	var a, b LogGroupInputs
	_ = json.Unmarshal(prior, &a)
	_ = json.Unmarshal(desired, &b)
	return a.Name != b.Name
}

// trimWildcard turns the arn DescribeLogGroups returns, which ends in :*, into the one the
// tagging apis take.
func trimWildcard(arn string) string {
	if len(arn) > 2 && arn[len(arn)-2:] == ":*" {
		return arn[:len(arn)-2]
	}
	return arn
}
