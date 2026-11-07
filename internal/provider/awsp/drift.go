package awsp

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	"github.com/aws/aws-sdk-go-v2/service/lambda/types"

	"github.com/kontrolplane/koldstart/internal/engine"
)

// Drift implements engine.Differ: the inputs of the function lambda has otherwise now. The code
// of a zip is compared by its hash; an image only by its uri, as lambda hashes it its own way.
func (p Function) Drift(ctx context.Context, req engine.Request) ([]string, error) {
	var in FunctionInputs
	if err := decodeInputs(req.Inputs, &in); err != nil {
		return nil, err
	}
	out, err := p.Client.GetFunction(ctx, &lambda.GetFunctionInput{FunctionName: &req.PhysicalID})
	if err != nil {
		return nil, fmt.Errorf("reading the function %s: %w", req.PhysicalID, err)
	}
	return engine.ChangedKeys(asCompared(in), asCompared(liveInputs(in, out))), nil
}

// liveInputs are the inputs lambda says a function has, with what lambda does not return, such
// as where its zip came from and its async settings, taken from in. Its tags are the ones of in
// that lambda has, as lambda's include koldstart's own.
func liveInputs(in FunctionInputs, out *lambda.GetFunctionOutput) FunctionInputs {
	c := out.Configuration
	live := FunctionInputs{
		Name: in.Name, Runtime: string(c.Runtime), Handler: aws.ToString(c.Handler),
		Memory: aws.ToInt32(c.MemorySize), Timeout: aws.ToInt32(c.Timeout), Role: aws.ToString(c.Role),
		Code: in.Code, Async: in.Async, KMSKey: aws.ToString(c.KMSKeyArn),
	}
	if c.TracingConfig != nil && c.TracingConfig.Mode == types.TracingModeActive {
		live.Tracing = string(types.TracingModeActive)
	}
	if c.DeadLetterConfig != nil {
		live.DeadLetter = aws.ToString(c.DeadLetterConfig.TargetArn)
	}
	for _, fs := range c.FileSystemConfigs {
		live.FileSystem = &FileSystemConfig{AccessPoint: aws.ToString(fs.Arn), MountPath: aws.ToString(fs.LocalMountPath)}
	}
	for k := range in.Tags {
		if v, ok := out.Tags[k]; ok {
			if live.Tags == nil {
				live.Tags = map[string]string{}
			}
			live.Tags[k] = v
		}
	}
	if in.isImage() {
		live.Runtime, live.Handler = in.Runtime, in.Handler
		if out.Code != nil {
			live.Code.ImageURI = aws.ToString(out.Code.ImageUri)
		}
	} else if sum, err := base64.StdEncoding.DecodeString(aws.ToString(c.CodeSha256)); err == nil {
		live.Code.SHA256 = hex.EncodeToString(sum)
	}
	if len(c.Architectures) > 0 {
		live.Architecture = string(c.Architectures[0])
	}
	if c.EphemeralStorage != nil {
		live.EphemeralStorage = aws.ToInt32(c.EphemeralStorage.Size)
	}
	if c.Environment != nil {
		live.Environment = c.Environment.Variables
	}
	if c.LoggingConfig != nil {
		live.LogGroup = aws.ToString(c.LoggingConfig.LogGroup)
		if c.LoggingConfig.LogFormat == types.LogFormatJson {
			live.LogFormat = string(types.LogFormatJson)
			live.ApplicationLogLevel = level(in.ApplicationLogLevel, string(c.LoggingConfig.ApplicationLogLevel))
			live.SystemLogLevel = level(in.SystemLogLevel, string(c.LoggingConfig.SystemLogLevel))
		}
	}
	if c.VpcConfig != nil && len(c.VpcConfig.SubnetIds) > 0 {
		live.VPC = &VPCConfig{SubnetIDs: c.VpcConfig.SubnetIds, SecurityGroupIDs: c.VpcConfig.SecurityGroupIds}
	}
	for _, l := range c.Layers {
		live.Layers = append(live.Layers, aws.ToString(l.Arn))
	}
	if c.ImageConfigResponse != nil && c.ImageConfigResponse.ImageConfig != nil {
		ic := c.ImageConfigResponse.ImageConfig
		live.ImageConfig = &ImageConfig{Command: ic.Command, EntryPoint: ic.EntryPoint, WorkingDirectory: aws.ToString(ic.WorkingDirectory)}
	}
	if out.Concurrency != nil {
		live.ReservedConcurrency = out.Concurrency.ReservedConcurrentExecutions
	}
	return live
}

// level is the log level lambda has, empty when it is INFO, lambda's default, and the function
// sets none.
func level(want, live string) string {
	if want == "" && live == "INFO" {
		return ""
	}
	return live
}

// asCompared is a function's inputs as a map, with an empty environment, list of layers, image
// configuration and vpc the same as none.
func asCompared(in FunctionInputs) map[string]any {
	if len(in.Environment) == 0 {
		in.Environment = nil
	}
	if len(in.Layers) == 0 {
		in.Layers = nil
	}
	if in.ImageConfig != nil && len(in.ImageConfig.Command) == 0 && len(in.ImageConfig.EntryPoint) == 0 && in.ImageConfig.WorkingDirectory == "" {
		in.ImageConfig = nil
	}
	if in.VPC != nil && len(in.VPC.SubnetIDs) == 0 {
		in.VPC = nil
	}
	data, _ := json.Marshal(in)
	var out map[string]any
	_ = json.Unmarshal(data, &out)
	return out
}
