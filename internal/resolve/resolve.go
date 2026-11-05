// Package resolve reads what a configuration refers to outside it while deploying: the secrets in
// ssm parameter store and secrets manager, and the outputs of other apps' stages.
package resolve

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"github.com/aws/aws-sdk-go-v2/service/ssm"

	"github.com/kontrolplane/koldstart/internal/awserr"
	"github.com/kontrolplane/koldstart/internal/config"
	"github.com/kontrolplane/koldstart/internal/engine"
	"github.com/kontrolplane/koldstart/internal/state"
)

// SSMAPI is the part of the ssm client secrets use.
type SSMAPI interface {
	GetParameter(context.Context, *ssm.GetParameterInput, ...func(*ssm.Options)) (*ssm.GetParameterOutput, error)
}

// SecretsManagerAPI is the part of the secrets manager client secrets use.
type SecretsManagerAPI interface {
	GetSecretValue(context.Context, *secretsmanager.GetSecretValueInput, ...func(*secretsmanager.Options)) (*secretsmanager.GetSecretValueOutput, error)
	DescribeSecret(context.Context, *secretsmanager.DescribeSecretInput, ...func(*secretsmanager.Options)) (*secretsmanager.DescribeSecretOutput, error)
}

// Secrets reads secrets, each once per run. A secret read at runtime resolves to its arn, which
// the function reads it by.
type Secrets struct {
	SSM            SSMAPI
	SecretsManager SecretsManagerAPI

	mu    sync.Mutex
	cache map[string]string
}

// Resolve implements engine.Secrets.
func (s *Secrets) Resolve(ctx context.Context, source, name, key string, atRuntime bool) (string, error) {
	cacheKey := fmt.Sprintf("%s\x00%s\x00%s\x00%t", source, name, key, atRuntime)
	s.mu.Lock()
	defer s.mu.Unlock()
	if v, ok := s.cache[cacheKey]; ok {
		return v, nil
	}
	var v string
	var err error
	switch source {
	case config.SourceSSM:
		v, err = s.parameter(ctx, name, atRuntime)
	case config.SourceSecretsManager:
		v, err = s.secret(ctx, name, key, atRuntime)
	default:
		err = fmt.Errorf("no secret source %q", source)
	}
	if err != nil {
		return "", err
	}
	if s.cache == nil {
		s.cache = map[string]string{}
	}
	s.cache[cacheKey] = v
	return v, nil
}

func (s *Secrets) parameter(ctx context.Context, name string, atRuntime bool) (string, error) {
	out, err := s.SSM.GetParameter(ctx, &ssm.GetParameterInput{Name: &name, WithDecryption: aws.Bool(!atRuntime)})
	if awserr.Is(err, "ParameterNotFound") {
		return "", fmt.Errorf("no ssm parameter %s: create it, or check its name and the region", name)
	}
	if err != nil {
		return "", fmt.Errorf("reading the ssm parameter %s: %w", name, err)
	}
	if atRuntime {
		return aws.ToString(out.Parameter.ARN), nil
	}
	return aws.ToString(out.Parameter.Value), nil
}

func (s *Secrets) secret(ctx context.Context, name, key string, atRuntime bool) (string, error) {
	if atRuntime {
		out, err := s.SecretsManager.DescribeSecret(ctx, &secretsmanager.DescribeSecretInput{SecretId: &name})
		if awserr.Is(err, "ResourceNotFoundException") {
			return "", fmt.Errorf("no secret %s in secrets manager: create it, or check its name and the region", name)
		}
		if err != nil {
			return "", fmt.Errorf("reading the secret %s: %w", name, err)
		}
		return aws.ToString(out.ARN), nil
	}
	out, err := s.SecretsManager.GetSecretValue(ctx, &secretsmanager.GetSecretValueInput{SecretId: &name})
	if awserr.Is(err, "ResourceNotFoundException") {
		return "", fmt.Errorf("no secret %s in secrets manager: create it, or check its name and the region", name)
	}
	if err != nil {
		return "", fmt.Errorf("reading the secret %s: %w", name, err)
	}
	value := aws.ToString(out.SecretString)
	if key == "" {
		return value, nil
	}
	return field(name, value, key)
}

// field takes one key of a json secret: a string as it is, anything else as json.
func field(name, value, key string) (string, error) {
	var fields map[string]any
	if err := json.Unmarshal([]byte(value), &fields); err != nil {
		return "", fmt.Errorf("the secret %s is not a json object, so it has no field %s", name, key)
	}
	v, ok := fields[key]
	if !ok {
		return "", fmt.Errorf("the secret %s has no field %s; it has %s", name, key, list(slices.Sorted(maps.Keys(fields))))
	}
	if s, ok := v.(string); ok {
		return s, nil
	}
	data, err := json.Marshal(v)
	return string(data), err
}

// list names things, or says there are none.
func list(names []string) string {
	if len(names) == 0 {
		return "none"
	}
	return strings.Join(names, ", ")
}

// Outputs reads the outputs of other stages from their states, each state once per run: from
// Backend, the bucket of this stage's account and region, or from the one Elsewhere opens for a
// stage in another region or account.
type Outputs struct {
	Backend   state.Backend
	Elsewhere func(ctx context.Context, region, account, role string) (state.Backend, error)

	mu     sync.Mutex
	states map[string]*state.State
}

// Output implements engine.Outputs.
func (o *Outputs) Output(ctx context.Context, ref config.OutputRef) (string, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	key := engine.DescribeOutput(config.OutputRef{App: ref.App, Stage: ref.Stage, Region: ref.Region, Account: ref.Account, Role: ref.Role})
	st, ok := o.states[key]
	if !ok {
		backend, err := o.backend(ctx, ref)
		if err != nil {
			return "", fmt.Errorf("reading the outputs of %s: %w", key, err)
		}
		st, _, err = backend.Read(ctx, ref.App, ref.Stage)
		if err != nil {
			return "", fmt.Errorf("reading the outputs of %s: %w", key, err)
		}
		if o.states == nil {
			o.states = map[string]*state.State{}
		}
		o.states[key] = st
	}
	stage := ref.App + "/" + ref.Stage
	if st == nil {
		where := "this account and region"
		if ref.Region != "" || ref.Account != "" || ref.Role != "" {
			where = "the account and region it names"
		}
		return "", fmt.Errorf("%s is not deployed in %s: deploy it first", stage, where)
	}
	v, ok := st.Outputs[ref.Output]
	if !ok {
		return "", fmt.Errorf("%s has no output %s; it has %s", stage, ref.Output, list(slices.Sorted(maps.Keys(st.Outputs))))
	}
	return v, nil
}

// backend is the state bucket of the stage an output is of.
func (o *Outputs) backend(ctx context.Context, ref config.OutputRef) (state.Backend, error) {
	if ref.Region == "" && ref.Account == "" && ref.Role == "" {
		return o.Backend, nil
	}
	if o.Elsewhere == nil {
		return nil, errors.New("no way to read a state in another region or account")
	}
	return o.Elsewhere(ctx, ref.Region, ref.Account, ref.Role)
}
