package awsp_test

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/apigateway"
	"github.com/aws/aws-sdk-go-v2/service/apigateway/types"
	"github.com/aws/smithy-go"
)

// fakeAPIGateway keeps rest apis, api keys and usage plans in memory, and refuses what api
// gateway refuses: a resource or method that exists, a second parameter after one path, an
// authorizer a method names, a deployment a stage serves, a deployment of an api without methods,
// and a usage plan with a stage in it.
type fakeAPIGateway struct {
	mu    sync.Mutex
	next  int
	apis  map[string]*fakeRestAPI
	keys  map[string]*types.ApiKey
	plans map[string]*fakePlan
	// calls are the calls made, such as DeleteDeployment.
	calls []string
}

// fakeRestAPI is one rest api the fake holds.
type fakeRestAPI struct {
	name        string
	tags        map[string]string
	root        string
	resources   map[string]*types.Resource
	methods     map[string]*fakeMethod
	authorizers map[string]*types.Authorizer
	validators  map[string]string
	// deployments are the routes each deployment took a snapshot of, as GET /orders.
	deployments map[string][]string
	stages      map[string]*types.Stage
}

// fakeMethod is a method and its integration.
type fakeMethod struct {
	put         apigateway.PutMethodInput
	integration *apigateway.PutIntegrationInput
}

// fakePlan is a usage plan, its stages as api:stage, and its keys.
type fakePlan struct {
	name   string
	tags   map[string]string
	stages []string
	keys   []string
}

func newFakeAPIGateway() *fakeAPIGateway {
	return &fakeAPIGateway{apis: map[string]*fakeRestAPI{}, keys: map[string]*types.ApiKey{}, plans: map[string]*fakePlan{}}
}

func apiError(code, format string, args ...any) error {
	return &smithy.GenericAPIError{Code: code, Message: fmt.Sprintf(format, args...)}
}

func (f *fakeAPIGateway) id(prefix string) string {
	f.next++
	return prefix + strconv.Itoa(f.next)
}

// lock takes the lock and records the call.
func (f *fakeAPIGateway) lock(name string) func() {
	f.mu.Lock()
	f.calls = append(f.calls, name)
	return f.mu.Unlock
}

func (f *fakeAPIGateway) api(id *string) (*fakeRestAPI, error) {
	a := f.apis[aws.ToString(id)]
	if a == nil {
		return nil, apiError("NotFoundException", "Invalid API identifier specified")
	}
	return a, nil
}

// snapshot is what a deployment of an api serves now: each method with an integration, by its
// method and path, sorted.
func (a *fakeRestAPI) snapshot() []string {
	var out []string
	for _, m := range a.methods {
		if m.integration != nil {
			out = append(out, aws.ToString(m.put.HttpMethod)+" "+aws.ToString(a.resources[aws.ToString(m.put.ResourceId)].Path))
		}
	}
	slices.Sort(out)
	return out
}

// served is what the stage of an api serves, nil when it has no such stage.
func (f *fakeAPIGateway) served(api, stage string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	a := f.apis[api]
	if a == nil || a.stages[stage] == nil {
		return nil
	}
	return a.deployments[aws.ToString(a.stages[stage].DeploymentId)]
}

func (f *fakeAPIGateway) only() (string, *fakeRestAPI) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for id, a := range f.apis {
		return id, a
	}
	return "", nil
}

func (f *fakeAPIGateway) CreateRestApi(_ context.Context, in *apigateway.CreateRestApiInput, _ ...func(*apigateway.Options)) (*apigateway.CreateRestApiOutput, error) {
	defer f.lock("CreateRestApi")()
	id, root := f.id("api"), f.id("root")
	f.apis[id] = &fakeRestAPI{
		name: aws.ToString(in.Name), tags: in.Tags, root: root,
		resources:   map[string]*types.Resource{root: {Id: aws.String(root), Path: aws.String("/")}},
		methods:     map[string]*fakeMethod{},
		authorizers: map[string]*types.Authorizer{}, validators: map[string]string{},
		deployments: map[string][]string{}, stages: map[string]*types.Stage{},
	}
	return &apigateway.CreateRestApiOutput{Id: &id, RootResourceId: &root, Name: in.Name}, nil
}

func (f *fakeAPIGateway) GetRestApis(_ context.Context, _ *apigateway.GetRestApisInput, _ ...func(*apigateway.Options)) (*apigateway.GetRestApisOutput, error) {
	defer f.lock("GetRestApis")()
	out := &apigateway.GetRestApisOutput{}
	for _, id := range slices.Sorted(maps.Keys(f.apis)) {
		a := f.apis[id]
		out.Items = append(out.Items, types.RestApi{Id: aws.String(id), Name: aws.String(a.name), Tags: a.tags, RootResourceId: aws.String(a.root)})
	}
	return out, nil
}

func (f *fakeAPIGateway) UpdateRestApi(_ context.Context, in *apigateway.UpdateRestApiInput, _ ...func(*apigateway.Options)) (*apigateway.UpdateRestApiOutput, error) {
	defer f.lock("UpdateRestApi")()
	a, err := f.api(in.RestApiId)
	if err != nil {
		return nil, err
	}
	for _, op := range in.PatchOperations {
		if aws.ToString(op.Path) == "/name" {
			a.name = aws.ToString(op.Value)
		}
	}
	return &apigateway.UpdateRestApiOutput{Id: in.RestApiId, RootResourceId: aws.String(a.root)}, nil
}

func (f *fakeAPIGateway) DeleteRestApi(_ context.Context, in *apigateway.DeleteRestApiInput, _ ...func(*apigateway.Options)) (*apigateway.DeleteRestApiOutput, error) {
	defer f.lock("DeleteRestApi")()
	if _, err := f.api(in.RestApiId); err != nil {
		return nil, err
	}
	delete(f.apis, aws.ToString(in.RestApiId))
	return &apigateway.DeleteRestApiOutput{}, nil
}

func (f *fakeAPIGateway) CreateRequestValidator(_ context.Context, in *apigateway.CreateRequestValidatorInput, _ ...func(*apigateway.Options)) (*apigateway.CreateRequestValidatorOutput, error) {
	defer f.lock("CreateRequestValidator")()
	a, err := f.api(in.RestApiId)
	if err != nil {
		return nil, err
	}
	id := f.id("validator")
	a.validators[id] = aws.ToString(in.Name)
	return &apigateway.CreateRequestValidatorOutput{Id: &id, Name: in.Name}, nil
}

func (f *fakeAPIGateway) GetRequestValidators(_ context.Context, in *apigateway.GetRequestValidatorsInput, _ ...func(*apigateway.Options)) (*apigateway.GetRequestValidatorsOutput, error) {
	defer f.lock("GetRequestValidators")()
	a, err := f.api(in.RestApiId)
	if err != nil {
		return nil, err
	}
	out := &apigateway.GetRequestValidatorsOutput{}
	for id, name := range a.validators {
		out.Items = append(out.Items, types.RequestValidator{Id: aws.String(id), Name: aws.String(name)})
	}
	return out, nil
}

func (f *fakeAPIGateway) TagResource(_ context.Context, _ *apigateway.TagResourceInput, _ ...func(*apigateway.Options)) (*apigateway.TagResourceOutput, error) {
	defer f.lock("TagResource")()
	return &apigateway.TagResourceOutput{}, nil
}

func (f *fakeAPIGateway) CreateResource(_ context.Context, in *apigateway.CreateResourceInput, _ ...func(*apigateway.Options)) (*apigateway.CreateResourceOutput, error) {
	defer f.lock("CreateResource")()
	a, err := f.api(in.RestApiId)
	if err != nil {
		return nil, err
	}
	parent := a.resources[aws.ToString(in.ParentId)]
	if parent == nil {
		return nil, apiError("NotFoundException", "Invalid Resource identifier specified")
	}
	part := aws.ToString(in.PathPart)
	for _, r := range a.resources {
		if aws.ToString(r.ParentId) != aws.ToString(in.ParentId) {
			continue
		}
		if aws.ToString(r.PathPart) == part {
			return nil, apiError("ConflictException", "Another resource with the same parent already has this name: %s", part)
		}
		if strings.HasPrefix(part, "{") && strings.HasPrefix(aws.ToString(r.PathPart), "{") {
			return nil, apiError("BadRequestException", "A sibling (%s) of this resource already has a variable path part -- only one is allowed", aws.ToString(r.PathPart))
		}
	}
	id := f.id("res")
	path := strings.TrimSuffix(aws.ToString(parent.Path), "/") + "/" + part
	a.resources[id] = &types.Resource{Id: aws.String(id), ParentId: in.ParentId, PathPart: in.PathPart, Path: aws.String(path)}
	return &apigateway.CreateResourceOutput{Id: aws.String(id), Path: aws.String(path)}, nil
}

func (f *fakeAPIGateway) GetResources(_ context.Context, in *apigateway.GetResourcesInput, _ ...func(*apigateway.Options)) (*apigateway.GetResourcesOutput, error) {
	defer f.lock("GetResources")()
	a, err := f.api(in.RestApiId)
	if err != nil {
		return nil, err
	}
	out := &apigateway.GetResourcesOutput{}
	for _, id := range slices.Sorted(maps.Keys(a.resources)) {
		out.Items = append(out.Items, *a.resources[id])
	}
	return out, nil
}

// deleteResource deletes a resource, what is under it, and their methods.
func (a *fakeRestAPI) deleteResource(id string) {
	for child, r := range a.resources {
		if aws.ToString(r.ParentId) == id {
			a.deleteResource(child)
		}
	}
	for key := range a.methods {
		if strings.HasPrefix(key, id+" ") {
			delete(a.methods, key)
		}
	}
	delete(a.resources, id)
}

func (f *fakeAPIGateway) DeleteResource(_ context.Context, in *apigateway.DeleteResourceInput, _ ...func(*apigateway.Options)) (*apigateway.DeleteResourceOutput, error) {
	defer f.lock("DeleteResource")()
	a, err := f.api(in.RestApiId)
	if err != nil {
		return nil, err
	}
	if a.resources[aws.ToString(in.ResourceId)] == nil {
		return nil, apiError("NotFoundException", "Invalid Resource identifier specified")
	}
	a.deleteResource(aws.ToString(in.ResourceId))
	return &apigateway.DeleteResourceOutput{}, nil
}

func (f *fakeAPIGateway) PutMethod(_ context.Context, in *apigateway.PutMethodInput, _ ...func(*apigateway.Options)) (*apigateway.PutMethodOutput, error) {
	defer f.lock("PutMethod")()
	a, err := f.api(in.RestApiId)
	if err != nil {
		return nil, err
	}
	if a.resources[aws.ToString(in.ResourceId)] == nil {
		return nil, apiError("NotFoundException", "Invalid Resource identifier specified")
	}
	key := aws.ToString(in.ResourceId) + " " + aws.ToString(in.HttpMethod)
	if a.methods[key] != nil {
		return nil, apiError("ConflictException", "Method already exists for this resource")
	}
	if id := aws.ToString(in.AuthorizerId); id != "" && a.authorizers[id] == nil {
		return nil, apiError("BadRequestException", "Invalid authorizer ID specified")
	}
	a.methods[key] = &fakeMethod{put: *in}
	return &apigateway.PutMethodOutput{}, nil
}

func (f *fakeAPIGateway) method(apiID, resource, method *string) (*fakeMethod, error) {
	a, err := f.api(apiID)
	if err != nil {
		return nil, err
	}
	m := a.methods[aws.ToString(resource)+" "+aws.ToString(method)]
	if m == nil {
		return nil, apiError("NotFoundException", "Invalid Method identifier specified")
	}
	return m, nil
}

func (f *fakeAPIGateway) GetMethod(_ context.Context, in *apigateway.GetMethodInput, _ ...func(*apigateway.Options)) (*apigateway.GetMethodOutput, error) {
	defer f.lock("GetMethod")()
	m, err := f.method(in.RestApiId, in.ResourceId, in.HttpMethod)
	if err != nil {
		return nil, err
	}
	return &apigateway.GetMethodOutput{HttpMethod: m.put.HttpMethod, AuthorizationType: m.put.AuthorizationType}, nil
}

func (f *fakeAPIGateway) DeleteMethod(_ context.Context, in *apigateway.DeleteMethodInput, _ ...func(*apigateway.Options)) (*apigateway.DeleteMethodOutput, error) {
	defer f.lock("DeleteMethod")()
	if _, err := f.method(in.RestApiId, in.ResourceId, in.HttpMethod); err != nil {
		return nil, err
	}
	delete(f.apis[aws.ToString(in.RestApiId)].methods, aws.ToString(in.ResourceId)+" "+aws.ToString(in.HttpMethod))
	return &apigateway.DeleteMethodOutput{}, nil
}

func (f *fakeAPIGateway) PutIntegration(_ context.Context, in *apigateway.PutIntegrationInput, _ ...func(*apigateway.Options)) (*apigateway.PutIntegrationOutput, error) {
	defer f.lock("PutIntegration")()
	m, err := f.method(in.RestApiId, in.ResourceId, in.HttpMethod)
	if err != nil {
		return nil, err
	}
	m.integration = in
	return &apigateway.PutIntegrationOutput{}, nil
}

func (f *fakeAPIGateway) CreateAuthorizer(_ context.Context, in *apigateway.CreateAuthorizerInput, _ ...func(*apigateway.Options)) (*apigateway.CreateAuthorizerOutput, error) {
	defer f.lock("CreateAuthorizer")()
	a, err := f.api(in.RestApiId)
	if err != nil {
		return nil, err
	}
	id := f.id("auth")
	a.authorizers[id] = &types.Authorizer{
		Id: aws.String(id), Name: in.Name, Type: in.Type, AuthorizerUri: in.AuthorizerUri, IdentitySource: in.IdentitySource,
		IdentityValidationExpression: in.IdentityValidationExpression, AuthorizerResultTtlInSeconds: in.AuthorizerResultTtlInSeconds,
		ProviderARNs: in.ProviderARNs,
	}
	return &apigateway.CreateAuthorizerOutput{Id: aws.String(id)}, nil
}

func (f *fakeAPIGateway) GetAuthorizers(_ context.Context, in *apigateway.GetAuthorizersInput, _ ...func(*apigateway.Options)) (*apigateway.GetAuthorizersOutput, error) {
	defer f.lock("GetAuthorizers")()
	a, err := f.api(in.RestApiId)
	if err != nil {
		return nil, err
	}
	out := &apigateway.GetAuthorizersOutput{}
	for _, id := range slices.Sorted(maps.Keys(a.authorizers)) {
		out.Items = append(out.Items, *a.authorizers[id])
	}
	return out, nil
}

func (f *fakeAPIGateway) UpdateAuthorizer(_ context.Context, in *apigateway.UpdateAuthorizerInput, _ ...func(*apigateway.Options)) (*apigateway.UpdateAuthorizerOutput, error) {
	defer f.lock("UpdateAuthorizer")()
	a, err := f.api(in.RestApiId)
	if err != nil {
		return nil, err
	}
	auth := a.authorizers[aws.ToString(in.AuthorizerId)]
	if auth == nil {
		return nil, apiError("NotFoundException", "Invalid Authorizer identifier specified")
	}
	for _, op := range in.PatchOperations {
		value := op.Value
		switch aws.ToString(op.Path) {
		case "/name":
			auth.Name = value
		case "/type":
			auth.Type = types.AuthorizerType(aws.ToString(value))
		case "/identitySource":
			auth.IdentitySource = value
		case "/identityValidationExpression":
			auth.IdentityValidationExpression = value
		case "/authorizerUri":
			auth.AuthorizerUri = value
		case "/authorizerResultTtlInSeconds":
			ttl, _ := strconv.Atoi(aws.ToString(value))
			auth.AuthorizerResultTtlInSeconds = aws.Int32(int32(ttl))
		case "/providerARNs":
			if op.Op == types.OpAdd {
				auth.ProviderARNs = append(auth.ProviderARNs, aws.ToString(value))
			} else {
				auth.ProviderARNs = slices.DeleteFunc(auth.ProviderARNs, func(s string) bool { return s == aws.ToString(value) })
			}
		}
	}
	return &apigateway.UpdateAuthorizerOutput{}, nil
}

func (f *fakeAPIGateway) DeleteAuthorizer(_ context.Context, in *apigateway.DeleteAuthorizerInput, _ ...func(*apigateway.Options)) (*apigateway.DeleteAuthorizerOutput, error) {
	defer f.lock("DeleteAuthorizer")()
	a, err := f.api(in.RestApiId)
	if err != nil {
		return nil, err
	}
	id := aws.ToString(in.AuthorizerId)
	if a.authorizers[id] == nil {
		return nil, apiError("NotFoundException", "Invalid Authorizer identifier specified")
	}
	for key, m := range a.methods {
		if aws.ToString(m.put.AuthorizerId) == id {
			return nil, apiError("ConflictException", "Cannot delete authorizer, is referenced in method: %s", key)
		}
	}
	delete(a.authorizers, id)
	return &apigateway.DeleteAuthorizerOutput{}, nil
}

func (f *fakeAPIGateway) CreateDeployment(_ context.Context, in *apigateway.CreateDeploymentInput, _ ...func(*apigateway.Options)) (*apigateway.CreateDeploymentOutput, error) {
	defer f.lock("CreateDeployment")()
	a, err := f.api(in.RestApiId)
	if err != nil {
		return nil, err
	}
	routes := a.snapshot()
	if len(routes) == 0 {
		return nil, apiError("BadRequestException", "The REST API doesn't contain any methods")
	}
	id := f.id("deployment")
	a.deployments[id] = routes
	return &apigateway.CreateDeploymentOutput{Id: aws.String(id)}, nil
}

func (f *fakeAPIGateway) DeleteDeployment(_ context.Context, in *apigateway.DeleteDeploymentInput, _ ...func(*apigateway.Options)) (*apigateway.DeleteDeploymentOutput, error) {
	defer f.lock("DeleteDeployment")()
	a, err := f.api(in.RestApiId)
	if err != nil {
		return nil, err
	}
	id := aws.ToString(in.DeploymentId)
	if _, ok := a.deployments[id]; !ok {
		return nil, apiError("NotFoundException", "Invalid Deployment identifier specified")
	}
	for _, s := range a.stages {
		if aws.ToString(s.DeploymentId) == id {
			return nil, apiError("BadRequestException", "Active stages pointing to this deployment must be moved or deleted")
		}
	}
	delete(a.deployments, id)
	return &apigateway.DeleteDeploymentOutput{}, nil
}

func (f *fakeAPIGateway) CreateStage(_ context.Context, in *apigateway.CreateStageInput, _ ...func(*apigateway.Options)) (*apigateway.CreateStageOutput, error) {
	defer f.lock("CreateStage")()
	a, err := f.api(in.RestApiId)
	if err != nil {
		return nil, err
	}
	name := aws.ToString(in.StageName)
	if a.stages[name] != nil {
		return nil, apiError("ConflictException", "Stage already exists")
	}
	if _, ok := a.deployments[aws.ToString(in.DeploymentId)]; !ok {
		return nil, apiError("BadRequestException", "Invalid deployment identifier specified")
	}
	a.stages[name] = &types.Stage{StageName: in.StageName, DeploymentId: in.DeploymentId, Tags: in.Tags, MethodSettings: map[string]types.MethodSetting{}}
	return &apigateway.CreateStageOutput{StageName: in.StageName, DeploymentId: in.DeploymentId}, nil
}

func (f *fakeAPIGateway) stage(apiID, name *string) (*fakeRestAPI, *types.Stage, error) {
	a, err := f.api(apiID)
	if err != nil {
		return nil, nil, err
	}
	s := a.stages[aws.ToString(name)]
	if s == nil {
		return nil, nil, apiError("NotFoundException", "Invalid Stage identifier specified")
	}
	return a, s, nil
}

func (f *fakeAPIGateway) GetStage(_ context.Context, in *apigateway.GetStageInput, _ ...func(*apigateway.Options)) (*apigateway.GetStageOutput, error) {
	defer f.lock("GetStage")()
	_, s, err := f.stage(in.RestApiId, in.StageName)
	if err != nil {
		return nil, err
	}
	return &apigateway.GetStageOutput{StageName: s.StageName, DeploymentId: s.DeploymentId, MethodSettings: s.MethodSettings}, nil
}

func (f *fakeAPIGateway) UpdateStage(_ context.Context, in *apigateway.UpdateStageInput, _ ...func(*apigateway.Options)) (*apigateway.UpdateStageOutput, error) {
	defer f.lock("UpdateStage")()
	a, s, err := f.stage(in.RestApiId, in.StageName)
	if err != nil {
		return nil, err
	}
	for _, op := range in.PatchOperations {
		path, value := aws.ToString(op.Path), aws.ToString(op.Value)
		switch {
		case path == "/deploymentId":
			if _, ok := a.deployments[value]; !ok {
				return nil, apiError("BadRequestException", "Invalid deployment identifier specified")
			}
			s.DeploymentId = aws.String(value)
		case path == "/*/*" && op.Op == types.OpRemove:
			delete(s.MethodSettings, "*/*")
		case strings.HasPrefix(path, "/*/*/throttling/"):
			setting := s.MethodSettings["*/*"]
			number, _ := strconv.ParseFloat(value, 64)
			if strings.HasSuffix(path, "rateLimit") {
				setting.ThrottlingRateLimit = number
			} else {
				setting.ThrottlingBurstLimit = int32(number)
			}
			s.MethodSettings["*/*"] = setting
		}
	}
	return &apigateway.UpdateStageOutput{}, nil
}

func (f *fakeAPIGateway) DeleteStage(_ context.Context, in *apigateway.DeleteStageInput, _ ...func(*apigateway.Options)) (*apigateway.DeleteStageOutput, error) {
	defer f.lock("DeleteStage")()
	a, _, err := f.stage(in.RestApiId, in.StageName)
	if err != nil {
		return nil, err
	}
	delete(a.stages, aws.ToString(in.StageName))
	return &apigateway.DeleteStageOutput{}, nil
}

func (f *fakeAPIGateway) CreateApiKey(_ context.Context, in *apigateway.CreateApiKeyInput, _ ...func(*apigateway.Options)) (*apigateway.CreateApiKeyOutput, error) {
	defer f.lock("CreateApiKey")()
	id := f.id("key")
	f.keys[id] = &types.ApiKey{Id: aws.String(id), Name: in.Name, Enabled: in.Enabled, Tags: in.Tags}
	return &apigateway.CreateApiKeyOutput{Id: aws.String(id)}, nil
}

func (f *fakeAPIGateway) GetApiKeys(_ context.Context, in *apigateway.GetApiKeysInput, _ ...func(*apigateway.Options)) (*apigateway.GetApiKeysOutput, error) {
	defer f.lock("GetApiKeys")()
	out := &apigateway.GetApiKeysOutput{}
	for _, id := range slices.Sorted(maps.Keys(f.keys)) {
		if k := f.keys[id]; strings.HasPrefix(aws.ToString(k.Name), aws.ToString(in.NameQuery)) {
			out.Items = append(out.Items, *k)
		}
	}
	return out, nil
}

func (f *fakeAPIGateway) UpdateApiKey(_ context.Context, in *apigateway.UpdateApiKeyInput, _ ...func(*apigateway.Options)) (*apigateway.UpdateApiKeyOutput, error) {
	defer f.lock("UpdateApiKey")()
	k := f.keys[aws.ToString(in.ApiKey)]
	if k == nil {
		return nil, apiError("NotFoundException", "Invalid API Key identifier specified")
	}
	for _, op := range in.PatchOperations {
		if aws.ToString(op.Path) == "/name" {
			k.Name = op.Value
		}
	}
	return &apigateway.UpdateApiKeyOutput{}, nil
}

func (f *fakeAPIGateway) DeleteApiKey(_ context.Context, in *apigateway.DeleteApiKeyInput, _ ...func(*apigateway.Options)) (*apigateway.DeleteApiKeyOutput, error) {
	defer f.lock("DeleteApiKey")()
	id := aws.ToString(in.ApiKey)
	if f.keys[id] == nil {
		return nil, apiError("NotFoundException", "Invalid API Key identifier specified")
	}
	delete(f.keys, id)
	for _, p := range f.plans {
		p.keys = slices.DeleteFunc(p.keys, func(k string) bool { return k == id })
	}
	return &apigateway.DeleteApiKeyOutput{}, nil
}

func (f *fakeAPIGateway) CreateUsagePlan(_ context.Context, in *apigateway.CreateUsagePlanInput, _ ...func(*apigateway.Options)) (*apigateway.CreateUsagePlanOutput, error) {
	defer f.lock("CreateUsagePlan")()
	p := &fakePlan{name: aws.ToString(in.Name), tags: in.Tags}
	for _, s := range in.ApiStages {
		if _, _, err := f.stage(s.ApiId, s.Stage); err != nil {
			return nil, apiError("BadRequestException", "API Stage not found: %s:%s", aws.ToString(s.ApiId), aws.ToString(s.Stage))
		}
		p.stages = append(p.stages, aws.ToString(s.ApiId)+":"+aws.ToString(s.Stage))
	}
	id := f.id("plan")
	f.plans[id] = p
	return &apigateway.CreateUsagePlanOutput{Id: aws.String(id)}, nil
}

func (f *fakeAPIGateway) GetUsagePlans(_ context.Context, _ *apigateway.GetUsagePlansInput, _ ...func(*apigateway.Options)) (*apigateway.GetUsagePlansOutput, error) {
	defer f.lock("GetUsagePlans")()
	out := &apigateway.GetUsagePlansOutput{}
	for _, id := range slices.Sorted(maps.Keys(f.plans)) {
		out.Items = append(out.Items, types.UsagePlan{Id: aws.String(id), Name: aws.String(f.plans[id].name), Tags: f.plans[id].tags})
	}
	return out, nil
}

func (f *fakeAPIGateway) UpdateUsagePlan(_ context.Context, in *apigateway.UpdateUsagePlanInput, _ ...func(*apigateway.Options)) (*apigateway.UpdateUsagePlanOutput, error) {
	defer f.lock("UpdateUsagePlan")()
	p := f.plans[aws.ToString(in.UsagePlanId)]
	if p == nil {
		return nil, apiError("NotFoundException", "Invalid Usage Plan ID specified")
	}
	for _, op := range in.PatchOperations {
		value := aws.ToString(op.Value)
		switch {
		case aws.ToString(op.Path) == "/name":
			p.name = value
		case op.Op == types.OpAdd:
			p.stages = append(p.stages, value)
		case op.Op == types.OpRemove:
			if !slices.Contains(p.stages, value) {
				return nil, apiError("BadRequestException", "Invalid API Stage: %s", value)
			}
			p.stages = slices.DeleteFunc(p.stages, func(s string) bool { return s == value })
		}
	}
	return &apigateway.UpdateUsagePlanOutput{}, nil
}

func (f *fakeAPIGateway) DeleteUsagePlan(_ context.Context, in *apigateway.DeleteUsagePlanInput, _ ...func(*apigateway.Options)) (*apigateway.DeleteUsagePlanOutput, error) {
	defer f.lock("DeleteUsagePlan")()
	id := aws.ToString(in.UsagePlanId)
	p := f.plans[id]
	if p == nil {
		return nil, apiError("NotFoundException", "Invalid Usage Plan ID specified")
	}
	if len(p.stages) > 0 {
		return nil, apiError("BadRequestException", "Cannot delete Usage Plan %s as it has API Stages associated with it", id)
	}
	delete(f.plans, id)
	return &apigateway.DeleteUsagePlanOutput{}, nil
}

func (f *fakeAPIGateway) CreateUsagePlanKey(_ context.Context, in *apigateway.CreateUsagePlanKeyInput, _ ...func(*apigateway.Options)) (*apigateway.CreateUsagePlanKeyOutput, error) {
	defer f.lock("CreateUsagePlanKey")()
	p := f.plans[aws.ToString(in.UsagePlanId)]
	if p == nil || f.keys[aws.ToString(in.KeyId)] == nil {
		return nil, apiError("NotFoundException", "Invalid Usage Plan or API Key specified")
	}
	if slices.Contains(p.keys, aws.ToString(in.KeyId)) {
		return nil, apiError("ConflictException", "Usage Plan Key already exists")
	}
	p.keys = append(p.keys, aws.ToString(in.KeyId))
	return &apigateway.CreateUsagePlanKeyOutput{Id: in.KeyId}, nil
}

func (f *fakeAPIGateway) DeleteUsagePlanKey(_ context.Context, in *apigateway.DeleteUsagePlanKeyInput, _ ...func(*apigateway.Options)) (*apigateway.DeleteUsagePlanKeyOutput, error) {
	defer f.lock("DeleteUsagePlanKey")()
	p := f.plans[aws.ToString(in.UsagePlanId)]
	if p == nil || !slices.Contains(p.keys, aws.ToString(in.KeyId)) {
		return nil, apiError("NotFoundException", "Invalid Usage Plan Key specified")
	}
	p.keys = slices.DeleteFunc(p.keys, func(k string) bool { return k == aws.ToString(in.KeyId) })
	return &apigateway.DeleteUsagePlanKeyOutput{}, nil
}
