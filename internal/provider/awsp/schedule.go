package awsp

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/scheduler"
	"github.com/aws/aws-sdk-go-v2/service/scheduler/types"

	"github.com/kontrolplane/koldstart/internal/awserr"
	"github.com/kontrolplane/koldstart/internal/engine"
)

// ScheduleInputs are the inputs of an eventbridge scheduler schedule that invokes a function.
type ScheduleInputs struct {
	Name       string `json:"name"`
	Expression string `json:"expression"`
	Timezone   string `json:"timezone"`
	Function   string `json:"function"`
	Role       string `json:"role"`
	Input      string `json:"input"`
	Enabled    bool   `json:"enabled"`
}

// Schedule is the provider of schedules.
type Schedule struct {
	Client *scheduler.Client
	// Wait is how long a schedule is retried while iam spreads its new role.
	Wait time.Duration
}

func (in ScheduleInputs) target() *types.Target {
	t := &types.Target{Arn: &in.Function, RoleArn: &in.Role}
	if in.Input != "" {
		t.Input = &in.Input
	}
	return t
}

func (in ScheduleInputs) state() types.ScheduleState {
	if in.Enabled {
		return types.ScheduleStateEnabled
	}
	return types.ScheduleStateDisabled
}

// retry calls fn until the scheduler may assume the role, which iam takes a few seconds to spread.
func (p Schedule) retry(ctx context.Context, fn func() error) error {
	retryable := func(err error) bool {
		return awserr.Is(err, "ValidationException") && strings.Contains(err.Error(), "assume")
	}
	return retry(ctx, cmp.Or(p.Wait, 2*time.Minute), 2*time.Second, retryable, fn)
}

func scheduleResult(name, arn string) engine.Result {
	return engine.Result{PhysicalID: name, Attributes: map[string]string{attrArn: arn, "Name": name}}
}

// Create implements engine.Provider.
func (p Schedule) Create(ctx context.Context, req engine.Request) (engine.Result, error) {
	var in ScheduleInputs
	if err := decodeInputs(req.Inputs, &in); err != nil {
		return engine.Result{}, err
	}
	var out *scheduler.CreateScheduleOutput
	err := p.retry(ctx, func() error {
		var err error
		out, err = p.Client.CreateSchedule(ctx, &scheduler.CreateScheduleInput{
			Name: &in.Name, ScheduleExpression: &in.Expression, ScheduleExpressionTimezone: &in.Timezone,
			Target: in.target(), State: in.state(),
			FlexibleTimeWindow: &types.FlexibleTimeWindow{Mode: types.FlexibleTimeWindowModeOff},
		})
		return err
	})
	if awserr.Is(err, "ConflictException") {
		return engine.Result{}, engine.ErrExists
	}
	if err != nil {
		return engine.Result{}, fmt.Errorf("creating the schedule %s: %w", in.Name, err)
	}
	return scheduleResult(in.Name, aws.ToString(out.ScheduleArn)), nil
}

// Read implements engine.Provider. Schedules carry no tags, so one with the name is taken as ours.
func (p Schedule) Read(ctx context.Context, req engine.Request) (engine.Result, bool, error) {
	out, err := p.Client.GetSchedule(ctx, &scheduler.GetScheduleInput{Name: &req.PhysicalID})
	if awserr.Is(err, "ResourceNotFoundException") {
		return engine.Result{}, false, nil
	}
	if err != nil {
		return engine.Result{}, false, fmt.Errorf("reading the schedule %s: %w", req.PhysicalID, err)
	}
	return scheduleResult(req.PhysicalID, aws.ToString(out.Arn)), true, nil
}

// Update implements engine.Provider.
func (p Schedule) Update(ctx context.Context, req engine.Request) (engine.Result, error) {
	var in ScheduleInputs
	if err := decodeInputs(req.Inputs, &in); err != nil {
		return engine.Result{}, err
	}
	var out *scheduler.UpdateScheduleOutput
	err := p.retry(ctx, func() error {
		var err error
		out, err = p.Client.UpdateSchedule(ctx, &scheduler.UpdateScheduleInput{
			Name: &in.Name, ScheduleExpression: &in.Expression, ScheduleExpressionTimezone: &in.Timezone,
			Target: in.target(), State: in.state(),
			FlexibleTimeWindow: &types.FlexibleTimeWindow{Mode: types.FlexibleTimeWindowModeOff},
		})
		return err
	})
	if err != nil {
		return engine.Result{}, fmt.Errorf("updating the schedule %s: %w", in.Name, err)
	}
	return scheduleResult(in.Name, aws.ToString(out.ScheduleArn)), nil
}

// Delete implements engine.Provider.
func (p Schedule) Delete(ctx context.Context, req engine.Request) error {
	_, err := p.Client.DeleteSchedule(ctx, &scheduler.DeleteScheduleInput{Name: &req.PhysicalID})
	if err != nil && !awserr.Is(err, "ResourceNotFoundException") {
		return fmt.Errorf("deleting the schedule %s: %w", req.PhysicalID, err)
	}
	return nil
}

// Replaces implements engine.Provider: a schedule cannot be renamed.
func (Schedule) Replaces(prior, desired json.RawMessage) bool {
	a, b := decodeBoth[ScheduleInputs](prior, desired)
	return a.Name != b.Name
}
