package awsp

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/apigateway"
	"github.com/aws/aws-sdk-go-v2/service/apigateway/types"

	"github.com/kontrolplane/koldstart/internal/awserr"
	"github.com/kontrolplane/koldstart/internal/engine"
)

// RESTStageClient is the part of the api gateway client the stage of the rest api uses.
type RESTStageClient interface {
	CreateDeployment(context.Context, *apigateway.CreateDeploymentInput, ...func(*apigateway.Options)) (*apigateway.CreateDeploymentOutput, error)
	DeleteDeployment(context.Context, *apigateway.DeleteDeploymentInput, ...func(*apigateway.Options)) (*apigateway.DeleteDeploymentOutput, error)
	CreateStage(context.Context, *apigateway.CreateStageInput, ...func(*apigateway.Options)) (*apigateway.CreateStageOutput, error)
	GetStage(context.Context, *apigateway.GetStageInput, ...func(*apigateway.Options)) (*apigateway.GetStageOutput, error)
	UpdateStage(context.Context, *apigateway.UpdateStageInput, ...func(*apigateway.Options)) (*apigateway.UpdateStageOutput, error)
	DeleteStage(context.Context, *apigateway.DeleteStageInput, ...func(*apigateway.Options)) (*apigateway.DeleteStageOutput, error)
	TagResource(context.Context, *apigateway.TagResourceInput, ...func(*apigateway.Options)) (*apigateway.TagResourceOutput, error)
}

// RESTStageInputs are the inputs of the stage of the rest api, named after the stage, and the
// deployment it serves: Deployment is a hash of the api's routes and authorizers, and a new one
// makes a new deployment.
type RESTStageInputs struct {
	API        string      `json:"api"`
	Stage      string      `json:"stage"`
	Deployment string      `json:"deployment"`
	Throttling *Throttling `json:"throttling,omitempty"`
}

// RESTStage is the provider of the stage of the rest api with its deployment, a snapshot of the
// api's routes, which a change to them does not touch. A deployment cannot change, so a new
// hash makes another, points the stage at it, and deletes the one before, which api gateway
// refuses while a stage points at it.
type RESTStage struct {
	Client RESTStageClient
	Region string
}

func restStageResult(stage, deployment string) engine.Result {
	return engine.Result{PhysicalID: stage, Attributes: map[string]string{"StageName": stage, "DeploymentId": deployment}}
}

// deploy makes a deployment of the api as it is now.
func (p RESTStage) deploy(ctx context.Context, in RESTStageInputs) (string, error) {
	var out *apigateway.CreateDeploymentOutput
	err := call(ctx, func() (err error) {
		out, err = p.Client.CreateDeployment(ctx, &apigateway.CreateDeploymentInput{RestApiId: &in.API, Description: aws.String("koldstart " + in.Deployment)})
		return err
	})
	if err != nil {
		return "", fmt.Errorf("deploying the rest api %s: %w", in.API, err)
	}
	return aws.ToString(out.Id), nil
}

// throttle sets the stage-wide request limit, or removes the one the stage had.
func (p RESTStage) throttle(ctx context.Context, in RESTStageInputs, prior *RESTStageInputs) error {
	var ops []types.PatchOperation
	switch {
	case in.Throttling != nil:
		ops = []types.PatchOperation{
			replace("/*/*/throttling/rateLimit", strconv.FormatFloat(in.Throttling.Rate, 'f', -1, 64)),
			replace("/*/*/throttling/burstLimit", strconv.Itoa(int(in.Throttling.Burst))),
		}
	case prior != nil && prior.Throttling != nil:
		ops = []types.PatchOperation{{Op: types.OpRemove, Path: aws.String("/*/*")}}
	default:
		return nil
	}
	err := call(ctx, func() error {
		_, err := p.Client.UpdateStage(ctx, &apigateway.UpdateStageInput{RestApiId: &in.API, StageName: &in.Stage, PatchOperations: ops})
		return err
	})
	if err != nil {
		return fmt.Errorf("setting the request limit of the stage %s of the rest api: %w", in.Stage, err)
	}
	return nil
}

// Create implements engine.Provider: a deployment, and the stage that serves it.
func (p RESTStage) Create(ctx context.Context, req engine.Request) (engine.Result, error) {
	var in RESTStageInputs
	if err := decodeInputs(req.Inputs, &in); err != nil {
		return engine.Result{}, err
	}
	deployment, err := p.deploy(ctx, in)
	if err != nil {
		return engine.Result{}, err
	}
	err = call(ctx, func() error {
		_, err := p.Client.CreateStage(ctx, &apigateway.CreateStageInput{RestApiId: &in.API, StageName: &in.Stage, DeploymentId: &deployment, Tags: req.Tags})
		return err
	})
	if awserr.Is(err, codeConflict) {
		p.forget(ctx, in.API, deployment)
		return engine.Result{}, engine.ErrExists
	}
	if err != nil {
		return engine.Result{}, fmt.Errorf("creating the stage %s of the rest api: %w", in.Stage, err)
	}
	if err := p.throttle(ctx, in, nil); err != nil {
		return engine.Result{}, err
	}
	return restStageResult(in.Stage, deployment), nil
}

// forget deletes a deployment no stage serves any longer. One that is left behind costs nothing
// and goes with the api, so a failure is logged and not returned.
func (p RESTStage) forget(ctx context.Context, api, deployment string) {
	if deployment == "" {
		return
	}
	_, err := p.Client.DeleteDeployment(ctx, &apigateway.DeleteDeploymentInput{RestApiId: &api, DeploymentId: &deployment})
	if err != nil && !awserr.Is(err, codeNotFound) {
		slog.Debug("deleting a deployment of the rest api", "api", api, "deployment", deployment, "error", err)
	}
}

// Read implements engine.Provider: by its name, unique in the api.
func (p RESTStage) Read(ctx context.Context, req engine.Request) (engine.Result, bool, error) {
	var in RESTStageInputs
	if err := decodeInputs(req.Inputs, &in); err != nil {
		return engine.Result{}, false, err
	}
	out, err := p.Client.GetStage(ctx, &apigateway.GetStageInput{RestApiId: &in.API, StageName: &in.Stage})
	if awserr.Is(err, codeNotFound) {
		return engine.Result{}, false, nil
	}
	if err != nil {
		return engine.Result{}, false, fmt.Errorf("reading the stage %s of the rest api: %w", in.Stage, err)
	}
	return restStageResult(in.Stage, aws.ToString(out.DeploymentId)), true, nil
}

// Update implements engine.Provider: a new deployment when the routes changed, or the one the
// stage serves is not known, as after a crash, and the request limit and tags.
func (p RESTStage) Update(ctx context.Context, req engine.Request) (engine.Result, error) {
	var in RESTStageInputs
	if err := decodeInputs(req.Inputs, &in); err != nil {
		return engine.Result{}, err
	}
	var prior *RESTStageInputs
	if len(req.Prior) > 0 {
		prior = &RESTStageInputs{}
		if err := decodeInputs(req.Prior, prior); err != nil {
			return engine.Result{}, err
		}
	}
	deployment := req.Attributes["DeploymentId"]
	if prior == nil || prior.Deployment != in.Deployment || deployment == "" {
		next, err := p.deploy(ctx, in)
		if err != nil {
			return engine.Result{}, err
		}
		err = call(ctx, func() error {
			_, err := p.Client.UpdateStage(ctx, &apigateway.UpdateStageInput{
				RestApiId: &in.API, StageName: &in.Stage, PatchOperations: []types.PatchOperation{replace("/deploymentId", next)},
			})
			return err
		})
		if err != nil {
			p.forget(ctx, in.API, next)
			return engine.Result{}, fmt.Errorf("pointing the stage %s of the rest api at its new deployment: %w", in.Stage, err)
		}
		p.forget(ctx, in.API, deployment)
		deployment = next
	}
	if err := p.throttle(ctx, in, prior); err != nil {
		return engine.Result{}, err
	}
	arn := "arn:" + Partition(p.Region) + ":apigateway:" + p.Region + "::/restapis/" + in.API + "/stages/" + in.Stage
	if _, err := p.Client.TagResource(ctx, &apigateway.TagResourceInput{ResourceArn: &arn, Tags: req.Tags}); err != nil {
		return engine.Result{}, fmt.Errorf("tagging the stage %s of the rest api: %w", in.Stage, err)
	}
	return restStageResult(in.Stage, deployment), nil
}

// Delete implements engine.Provider: the stage, then its deployment.
func (p RESTStage) Delete(ctx context.Context, req engine.Request) error {
	var in RESTStageInputs
	if err := decodeInputs(req.Prior, &in); err != nil {
		return err
	}
	err := call(ctx, func() error {
		_, err := p.Client.DeleteStage(ctx, &apigateway.DeleteStageInput{RestApiId: &in.API, StageName: &in.Stage})
		return err
	})
	if err != nil && !awserr.Is(err, codeNotFound) {
		return fmt.Errorf("deleting the stage %s of the rest api: %w", in.Stage, err)
	}
	p.forget(ctx, in.API, req.Attributes["DeploymentId"])
	return nil
}

// Replaces implements engine.Provider: a stage belongs to its api, by its name.
func (RESTStage) Replaces(prior, desired json.RawMessage) bool {
	a, b := decodeBoth[RESTStageInputs](prior, desired)
	return a.API != b.API || a.Stage != b.Stage
}

// RESTKeyClient is the part of the api gateway client the api key of the rest api uses.
type RESTKeyClient interface {
	CreateApiKey(context.Context, *apigateway.CreateApiKeyInput, ...func(*apigateway.Options)) (*apigateway.CreateApiKeyOutput, error)
	GetApiKeys(context.Context, *apigateway.GetApiKeysInput, ...func(*apigateway.Options)) (*apigateway.GetApiKeysOutput, error)
	UpdateApiKey(context.Context, *apigateway.UpdateApiKeyInput, ...func(*apigateway.Options)) (*apigateway.UpdateApiKeyOutput, error)
	DeleteApiKey(context.Context, *apigateway.DeleteApiKeyInput, ...func(*apigateway.Options)) (*apigateway.DeleteApiKeyOutput, error)
	TagResource(context.Context, *apigateway.TagResourceInput, ...func(*apigateway.Options)) (*apigateway.TagResourceOutput, error)
}

// RESTKeyInputs are the inputs of the api key of the private routes of the rest api, whose value
// api gateway makes and koldstart never reads.
type RESTKeyInputs struct {
	Name string `json:"name"`
}

// RESTKey is the provider of the api key of the rest api's private routes. Keys have no unique
// name, so one a crashed deploy made is found by its name and tags.
type RESTKey struct {
	Client RESTKeyClient
	Region string
}

func restKeyResult(id string) engine.Result {
	return engine.Result{PhysicalID: id, Attributes: map[string]string{"APIKeyId": id}}
}

// Create implements engine.Provider.
func (p RESTKey) Create(ctx context.Context, req engine.Request) (engine.Result, error) {
	var in RESTKeyInputs
	if err := decodeInputs(req.Inputs, &in); err != nil {
		return engine.Result{}, err
	}
	var out *apigateway.CreateApiKeyOutput
	err := call(ctx, func() (err error) {
		out, err = p.Client.CreateApiKey(ctx, &apigateway.CreateApiKeyInput{Name: &in.Name, Enabled: true, Tags: req.Tags})
		return err
	})
	if err != nil {
		return engine.Result{}, fmt.Errorf("creating the api key %s: %w", in.Name, err)
	}
	return restKeyResult(aws.ToString(out.Id)), nil
}

// Read implements engine.Provider: by id, or after a crash by its name and tags.
func (p RESTKey) Read(ctx context.Context, req engine.Request) (engine.Result, bool, error) {
	var in RESTKeyInputs
	if err := decodeInputs(req.Inputs, &in); err != nil {
		return engine.Result{}, false, err
	}
	var position *string
	for {
		out, err := p.Client.GetApiKeys(ctx, &apigateway.GetApiKeysInput{NameQuery: &in.Name, Limit: aws.Int32(500), Position: position})
		if err != nil {
			return engine.Result{}, false, fmt.Errorf("listing the api keys: %w", err)
		}
		for _, k := range out.Items {
			if aws.ToString(k.Id) == req.PhysicalID || (aws.ToString(k.Name) == in.Name && ours(in.Name, k.Tags, req) == nil) {
				return restKeyResult(aws.ToString(k.Id)), true, nil
			}
		}
		if out.Position == nil {
			return engine.Result{}, false, nil
		}
		position = out.Position
	}
}

// Update implements engine.Provider: its name and tags.
func (p RESTKey) Update(ctx context.Context, req engine.Request) (engine.Result, error) {
	var in RESTKeyInputs
	if err := decodeInputs(req.Inputs, &in); err != nil {
		return engine.Result{}, err
	}
	err := call(ctx, func() error {
		_, err := p.Client.UpdateApiKey(ctx, &apigateway.UpdateApiKeyInput{ApiKey: &req.PhysicalID, PatchOperations: []types.PatchOperation{replace("/name", in.Name)}})
		return err
	})
	if err != nil {
		return engine.Result{}, fmt.Errorf("updating the api key %s: %w", in.Name, err)
	}
	arn := "arn:" + Partition(p.Region) + ":apigateway:" + p.Region + "::/apikeys/" + req.PhysicalID
	if _, err := p.Client.TagResource(ctx, &apigateway.TagResourceInput{ResourceArn: &arn, Tags: req.Tags}); err != nil {
		return engine.Result{}, fmt.Errorf("tagging the api key %s: %w", in.Name, err)
	}
	return restKeyResult(req.PhysicalID), nil
}

// Delete implements engine.Provider.
func (p RESTKey) Delete(ctx context.Context, req engine.Request) error {
	err := call(ctx, func() error {
		_, err := p.Client.DeleteApiKey(ctx, &apigateway.DeleteApiKeyInput{ApiKey: &req.PhysicalID})
		return err
	})
	if err != nil && !awserr.Is(err, codeNotFound) {
		return fmt.Errorf("deleting the api key %s: %w", req.PhysicalID, err)
	}
	return nil
}

// Replaces implements engine.Provider: a key changes in place.
func (RESTKey) Replaces(_, _ json.RawMessage) bool { return false }

// RESTUsagePlanClient is the part of the api gateway client the usage plan of the rest api uses.
type RESTUsagePlanClient interface {
	CreateUsagePlan(context.Context, *apigateway.CreateUsagePlanInput, ...func(*apigateway.Options)) (*apigateway.CreateUsagePlanOutput, error)
	GetUsagePlans(context.Context, *apigateway.GetUsagePlansInput, ...func(*apigateway.Options)) (*apigateway.GetUsagePlansOutput, error)
	UpdateUsagePlan(context.Context, *apigateway.UpdateUsagePlanInput, ...func(*apigateway.Options)) (*apigateway.UpdateUsagePlanOutput, error)
	DeleteUsagePlan(context.Context, *apigateway.DeleteUsagePlanInput, ...func(*apigateway.Options)) (*apigateway.DeleteUsagePlanOutput, error)
	CreateUsagePlanKey(context.Context, *apigateway.CreateUsagePlanKeyInput, ...func(*apigateway.Options)) (*apigateway.CreateUsagePlanKeyOutput, error)
	DeleteUsagePlanKey(context.Context, *apigateway.DeleteUsagePlanKeyInput, ...func(*apigateway.Options)) (*apigateway.DeleteUsagePlanKeyOutput, error)
}

// RESTUsagePlanInputs are the inputs of the usage plan that lets the api key into the stage of
// the rest api, without a quota or a limit of its own.
type RESTUsagePlanInputs struct {
	Name  string `json:"name"`
	API   string `json:"api"`
	Stage string `json:"stage"`
	Key   string `json:"key"`
}

// apiStage is the stage of the plan as its patches name it, <api>:<stage>.
func (in RESTUsagePlanInputs) apiStage() string { return in.API + ":" + in.Stage }

// RESTUsagePlan is the provider of the usage plan of the rest api's private routes, with the api
// key in it. Usage plans have no unique name, so one a crashed deploy made is found by its name
// and tags.
type RESTUsagePlan struct {
	Client RESTUsagePlanClient
}

func restUsagePlanResult(id string) engine.Result {
	return engine.Result{PhysicalID: id, Attributes: map[string]string{"UsagePlanId": id}}
}

// addKey puts the api key in the plan; one in it already is no error.
func (p RESTUsagePlan) addKey(ctx context.Context, plan, key string) error {
	err := call(ctx, func() error {
		_, err := p.Client.CreateUsagePlanKey(ctx, &apigateway.CreateUsagePlanKeyInput{UsagePlanId: &plan, KeyId: &key, KeyType: aws.String("API_KEY")})
		return err
	})
	if err != nil && !awserr.Is(err, codeConflict) {
		return fmt.Errorf("putting the api key %s in the usage plan %s: %w", key, plan, err)
	}
	return nil
}

// Create implements engine.Provider.
func (p RESTUsagePlan) Create(ctx context.Context, req engine.Request) (engine.Result, error) {
	var in RESTUsagePlanInputs
	if err := decodeInputs(req.Inputs, &in); err != nil {
		return engine.Result{}, err
	}
	var out *apigateway.CreateUsagePlanOutput
	err := call(ctx, func() (err error) {
		out, err = p.Client.CreateUsagePlan(ctx, &apigateway.CreateUsagePlanInput{
			Name: &in.Name, ApiStages: []types.ApiStage{{ApiId: &in.API, Stage: &in.Stage}}, Tags: req.Tags,
		})
		return err
	})
	if err != nil {
		return engine.Result{}, fmt.Errorf("creating the usage plan %s: %w", in.Name, err)
	}
	id := aws.ToString(out.Id)
	if err := p.addKey(ctx, id, in.Key); err != nil {
		return engine.Result{}, err
	}
	return restUsagePlanResult(id), nil
}

// Read implements engine.Provider: by id, or after a crash by its name and tags.
func (p RESTUsagePlan) Read(ctx context.Context, req engine.Request) (engine.Result, bool, error) {
	var in RESTUsagePlanInputs
	if err := decodeInputs(req.Inputs, &in); err != nil {
		return engine.Result{}, false, err
	}
	var position *string
	for {
		out, err := p.Client.GetUsagePlans(ctx, &apigateway.GetUsagePlansInput{Limit: aws.Int32(500), Position: position})
		if err != nil {
			return engine.Result{}, false, fmt.Errorf("listing the usage plans: %w", err)
		}
		for _, u := range out.Items {
			if aws.ToString(u.Id) == req.PhysicalID || (aws.ToString(u.Name) == in.Name && ours(in.Name, u.Tags, req) == nil) {
				return restUsagePlanResult(aws.ToString(u.Id)), true, nil
			}
		}
		if out.Position == nil {
			return engine.Result{}, false, nil
		}
		position = out.Position
	}
}

// Update implements engine.Provider: its name, its stage and its key.
func (p RESTUsagePlan) Update(ctx context.Context, req engine.Request) (engine.Result, error) {
	prior, in := decodeBoth[RESTUsagePlanInputs](req.Prior, req.Inputs)
	id := req.PhysicalID
	ops := []types.PatchOperation{replace("/name", in.Name)}
	if prior.apiStage() != in.apiStage() {
		ops = append(ops, types.PatchOperation{Op: types.OpAdd, Path: aws.String("/apiStages"), Value: aws.String(in.apiStage())})
		if prior.API != "" {
			ops = append(ops, types.PatchOperation{Op: types.OpRemove, Path: aws.String("/apiStages"), Value: aws.String(prior.apiStage())})
		}
	}
	err := call(ctx, func() error {
		_, err := p.Client.UpdateUsagePlan(ctx, &apigateway.UpdateUsagePlanInput{UsagePlanId: &id, PatchOperations: ops})
		return err
	})
	if err != nil {
		return engine.Result{}, fmt.Errorf("updating the usage plan %s: %w", in.Name, err)
	}
	if prior.Key != in.Key && prior.Key != "" {
		_, err := p.Client.DeleteUsagePlanKey(ctx, &apigateway.DeleteUsagePlanKeyInput{UsagePlanId: &id, KeyId: &prior.Key})
		if err != nil && !awserr.Is(err, codeNotFound) {
			return engine.Result{}, fmt.Errorf("taking the api key %s out of the usage plan %s: %w", prior.Key, in.Name, err)
		}
	}
	if err := p.addKey(ctx, id, in.Key); err != nil {
		return engine.Result{}, err
	}
	return restUsagePlanResult(id), nil
}

// Delete implements engine.Provider: its stage out of it first, which api gateway asks of a plan
// it deletes.
func (p RESTUsagePlan) Delete(ctx context.Context, req engine.Request) error {
	var in RESTUsagePlanInputs
	if err := decodeInputs(req.Prior, &in); err != nil {
		return err
	}
	id := req.PhysicalID
	err := call(ctx, func() error {
		_, err := p.Client.UpdateUsagePlan(ctx, &apigateway.UpdateUsagePlanInput{UsagePlanId: &id, PatchOperations: []types.PatchOperation{
			{Op: types.OpRemove, Path: aws.String("/apiStages"), Value: aws.String(in.apiStage())},
		}})
		return err
	})
	if err != nil && !awserr.Is(err, codeNotFound) && !awserr.Is(err, codeBadRequest) {
		return fmt.Errorf("taking the stage out of the usage plan %s: %w", in.Name, err)
	}
	err = call(ctx, func() error {
		_, err := p.Client.DeleteUsagePlan(ctx, &apigateway.DeleteUsagePlanInput{UsagePlanId: &id})
		return err
	})
	if err != nil && !awserr.Is(err, codeNotFound) {
		return fmt.Errorf("deleting the usage plan %s: %w", in.Name, err)
	}
	return nil
}

// Replaces implements engine.Provider: a usage plan changes in place.
func (RESTUsagePlan) Replaces(_, _ json.RawMessage) bool { return false }
