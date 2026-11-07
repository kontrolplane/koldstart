package awsp

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/apigateway"
	"github.com/aws/aws-sdk-go-v2/service/apigateway/types"

	"github.com/kontrolplane/koldstart/internal/awserr"
	"github.com/kontrolplane/koldstart/internal/engine"
)

// The errors of api gateway the rest api's providers tell apart.
const (
	codeNotFound    = "NotFoundException"
	codeConflict    = "ConflictException"
	codeTooMany     = "TooManyRequestsException"
	codeBadRequest  = "BadRequestException"
	validatorParams = "koldstart-parameters"
)

// busy reports whether api gateway refused a call for now: too many calls, or another change
// to the same api still being made, as when routes are made at once.
func busy(err error) bool {
	return awserr.Is(err, codeTooMany) || (awserr.Is(err, codeConflict) && strings.Contains(strings.ToLower(err.Error()), "concurrent"))
}

// The longest a call api gateway refuses for now is tried again, and how often.
const (
	busyWait  = 3 * time.Minute
	busyEvery = 2 * time.Second
)

// call calls fn until api gateway takes it, or busyWait has passed.
func call(ctx context.Context, fn func() error) error {
	return retry(ctx, busyWait, busyEvery, busy, fn)
}

// replace is a patch operation that sets path to value.
func replace(path, value string) types.PatchOperation {
	return types.PatchOperation{Op: types.OpReplace, Path: aws.String(path), Value: aws.String(value)}
}

// RESTAPIClient is the part of the api gateway client the rest api uses.
type RESTAPIClient interface {
	CreateRestApi(context.Context, *apigateway.CreateRestApiInput, ...func(*apigateway.Options)) (*apigateway.CreateRestApiOutput, error)
	GetRestApis(context.Context, *apigateway.GetRestApisInput, ...func(*apigateway.Options)) (*apigateway.GetRestApisOutput, error)
	UpdateRestApi(context.Context, *apigateway.UpdateRestApiInput, ...func(*apigateway.Options)) (*apigateway.UpdateRestApiOutput, error)
	DeleteRestApi(context.Context, *apigateway.DeleteRestApiInput, ...func(*apigateway.Options)) (*apigateway.DeleteRestApiOutput, error)
	CreateRequestValidator(context.Context, *apigateway.CreateRequestValidatorInput, ...func(*apigateway.Options)) (*apigateway.CreateRequestValidatorOutput, error)
	GetRequestValidators(context.Context, *apigateway.GetRequestValidatorsInput, ...func(*apigateway.Options)) (*apigateway.GetRequestValidatorsOutput, error)
	TagResource(context.Context, *apigateway.TagResourceInput, ...func(*apigateway.Options)) (*apigateway.TagResourceOutput, error)
}

// RESTAPIInputs are the inputs of the stage's rest api, a regional one.
type RESTAPIInputs struct {
	Name string `json:"name"`
}

// RESTAPI is the provider of the rest api all rest routes of a stage share, with a validator of
// request parameters for the routes that require some. Rest apis have no unique name, so one a
// crashed deploy made is found by its name and tags.
type RESTAPI struct {
	Client RESTAPIClient
	Region string
}

// restAPIResult is what the state holds of a rest api: its id, its root resource, which the
// first segments of paths hang from, its validator of parameters, and the url its stages are
// under.
func (p RESTAPI) restAPIResult(id, root, validator string) engine.Result {
	suffix := "amazonaws.com"
	if strings.HasPrefix(p.Region, "cn-") {
		suffix = "amazonaws.com.cn"
	}
	attrs := map[string]string{"RestApiId": id, "RootResourceId": root, "ApiEndpoint": "https://" + id + ".execute-api." + p.Region + "." + suffix}
	if validator != "" {
		attrs["ParametersValidatorId"] = validator
	}
	return engine.Result{PhysicalID: id, Attributes: attrs}
}

// Create implements engine.Provider.
func (p RESTAPI) Create(ctx context.Context, req engine.Request) (engine.Result, error) {
	var in RESTAPIInputs
	if err := decodeInputs(req.Inputs, &in); err != nil {
		return engine.Result{}, err
	}
	var api *apigateway.CreateRestApiOutput
	err := call(ctx, func() (err error) {
		api, err = p.Client.CreateRestApi(ctx, &apigateway.CreateRestApiInput{
			Name: &in.Name, Tags: req.Tags, ApiKeySource: types.ApiKeySourceTypeHeader,
			EndpointConfiguration: &types.EndpointConfiguration{Types: []types.EndpointType{types.EndpointTypeRegional}},
		})
		return err
	})
	if err != nil {
		return engine.Result{}, fmt.Errorf("creating the rest api %s: %w", in.Name, err)
	}
	id := aws.ToString(api.Id)
	validator, err := p.validator(ctx, id)
	if err != nil {
		return engine.Result{}, err
	}
	return p.restAPIResult(id, aws.ToString(api.RootResourceId), validator), nil
}

// validator returns the api's validator of request parameters, made when it has none.
func (p RESTAPI) validator(ctx context.Context, id string) (string, error) {
	found, err := p.findValidator(ctx, id)
	if err != nil || found != "" {
		return found, err
	}
	var out *apigateway.CreateRequestValidatorOutput
	err = call(ctx, func() (err error) {
		out, err = p.Client.CreateRequestValidator(ctx, &apigateway.CreateRequestValidatorInput{
			RestApiId: &id, Name: aws.String(validatorParams), ValidateRequestParameters: true,
		})
		return err
	})
	if err != nil {
		return "", fmt.Errorf("creating the validator of parameters of the rest api %s: %w", id, err)
	}
	return aws.ToString(out.Id), nil
}

// findValidator returns the id of the api's validator of request parameters, or "" when it has
// none.
func (p RESTAPI) findValidator(ctx context.Context, id string) (string, error) {
	out, err := p.Client.GetRequestValidators(ctx, &apigateway.GetRequestValidatorsInput{RestApiId: &id, Limit: aws.Int32(500)})
	if err != nil {
		return "", fmt.Errorf("listing the validators of the rest api %s: %w", id, err)
	}
	for _, v := range out.Items {
		if aws.ToString(v.Name) == validatorParams {
			return aws.ToString(v.Id), nil
		}
	}
	return "", nil
}

// Read implements engine.Provider: by id, or after a crash by its name and tags.
func (p RESTAPI) Read(ctx context.Context, req engine.Request) (engine.Result, bool, error) {
	var in RESTAPIInputs
	if err := decodeInputs(req.Inputs, &in); err != nil {
		return engine.Result{}, false, err
	}
	var position *string
	for {
		out, err := p.Client.GetRestApis(ctx, &apigateway.GetRestApisInput{Limit: aws.Int32(500), Position: position})
		if err != nil {
			return engine.Result{}, false, fmt.Errorf("listing the rest apis: %w", err)
		}
		for _, api := range out.Items {
			id := aws.ToString(api.Id)
			if id != req.PhysicalID && (aws.ToString(api.Name) != in.Name || ours(in.Name, api.Tags, req) != nil) {
				continue
			}
			validator, err := p.findValidator(ctx, id)
			if err != nil {
				return engine.Result{}, false, err
			}
			return p.restAPIResult(id, aws.ToString(api.RootResourceId), validator), true, nil
		}
		if out.Position == nil {
			return engine.Result{}, false, nil
		}
		position = out.Position
	}
}

// Update implements engine.Provider: the name and the tags, and the validator when it is gone.
func (p RESTAPI) Update(ctx context.Context, req engine.Request) (engine.Result, error) {
	var in RESTAPIInputs
	if err := decodeInputs(req.Inputs, &in); err != nil {
		return engine.Result{}, err
	}
	id := req.PhysicalID
	var api *apigateway.UpdateRestApiOutput
	err := call(ctx, func() (err error) {
		api, err = p.Client.UpdateRestApi(ctx, &apigateway.UpdateRestApiInput{RestApiId: &id, PatchOperations: []types.PatchOperation{replace("/name", in.Name)}})
		return err
	})
	if err != nil {
		return engine.Result{}, fmt.Errorf("updating the rest api %s: %w", in.Name, err)
	}
	arn := "arn:" + Partition(p.Region) + ":apigateway:" + p.Region + "::/restapis/" + id
	if _, err := p.Client.TagResource(ctx, &apigateway.TagResourceInput{ResourceArn: &arn, Tags: req.Tags}); err != nil {
		return engine.Result{}, fmt.Errorf("tagging the rest api %s: %w", in.Name, err)
	}
	validator, err := p.validator(ctx, id)
	if err != nil {
		return engine.Result{}, err
	}
	return p.restAPIResult(id, aws.ToString(api.RootResourceId), validator), nil
}

// Delete implements engine.Provider, with everything in the api. Api gateway takes a delete of a
// rest api every 30 seconds, so a second one waits its turn.
func (p RESTAPI) Delete(ctx context.Context, req engine.Request) error {
	err := call(ctx, func() error {
		_, err := p.Client.DeleteRestApi(ctx, &apigateway.DeleteRestApiInput{RestApiId: &req.PhysicalID})
		return err
	})
	if err != nil && !awserr.Is(err, codeNotFound) {
		return fmt.Errorf("deleting the rest api %s: %w", req.PhysicalID, err)
	}
	return nil
}

// Replaces implements engine.Provider: a rest api changes in place.
func (RESTAPI) Replaces(_, _ json.RawMessage) bool { return false }

// RESTResourceClient is the part of the api gateway client the resources of the rest api use.
type RESTResourceClient interface {
	CreateResource(context.Context, *apigateway.CreateResourceInput, ...func(*apigateway.Options)) (*apigateway.CreateResourceOutput, error)
	GetResources(context.Context, *apigateway.GetResourcesInput, ...func(*apigateway.Options)) (*apigateway.GetResourcesOutput, error)
	DeleteResource(context.Context, *apigateway.DeleteResourceInput, ...func(*apigateway.Options)) (*apigateway.DeleteResourceOutput, error)
}

// RESTResourceInputs are the inputs of a resource of the rest api: one segment of a path, under
// the resource of the segments before it, and the whole path, which it is found by.
type RESTResourceInputs struct {
	API      string `json:"api"`
	Parent   string `json:"parent"`
	PathPart string `json:"pathPart"`
	Path     string `json:"path"`
}

// RESTResource is the provider of the resources of the rest api, a tree of the segments of the
// routes' paths. A resource cannot change: another segment or parent is another resource.
type RESTResource struct {
	Client RESTResourceClient
}

func restResourceResult(id string) engine.Result {
	return engine.Result{PhysicalID: id, Attributes: map[string]string{"ResourceId": id}}
}

// Create implements engine.Provider.
func (p RESTResource) Create(ctx context.Context, req engine.Request) (engine.Result, error) {
	var in RESTResourceInputs
	if err := decodeInputs(req.Inputs, &in); err != nil {
		return engine.Result{}, err
	}
	var out *apigateway.CreateResourceOutput
	err := call(ctx, func() (err error) {
		out, err = p.Client.CreateResource(ctx, &apigateway.CreateResourceInput{RestApiId: &in.API, ParentId: &in.Parent, PathPart: &in.PathPart})
		return err
	})
	if awserr.Is(err, codeConflict) {
		return engine.Result{}, engine.ErrExists
	}
	if err != nil {
		return engine.Result{}, fmt.Errorf("creating the resource %s of the rest api: %w", in.Path, err)
	}
	return restResourceResult(aws.ToString(out.Id)), nil
}

// Read implements engine.Provider: by its path, which is unique in the api.
func (p RESTResource) Read(ctx context.Context, req engine.Request) (engine.Result, bool, error) {
	var in RESTResourceInputs
	if err := decodeInputs(req.Inputs, &in); err != nil {
		return engine.Result{}, false, err
	}
	var position *string
	for {
		out, err := p.Client.GetResources(ctx, &apigateway.GetResourcesInput{RestApiId: &in.API, Limit: aws.Int32(500), Position: position})
		if awserr.Is(err, codeNotFound) {
			return engine.Result{}, false, nil
		}
		if err != nil {
			return engine.Result{}, false, fmt.Errorf("listing the resources of the rest api %s: %w", in.API, err)
		}
		for _, r := range out.Items {
			if aws.ToString(r.Path) == in.Path {
				return restResourceResult(aws.ToString(r.Id)), true, nil
			}
		}
		if out.Position == nil {
			return engine.Result{}, false, nil
		}
		position = out.Position
	}
}

// Update implements engine.Provider: nothing of a resource changes in place.
func (p RESTResource) Update(_ context.Context, req engine.Request) (engine.Result, error) {
	return restResourceResult(req.PhysicalID), nil
}

// Delete implements engine.Provider, with the methods on it.
func (p RESTResource) Delete(ctx context.Context, req engine.Request) error {
	var in RESTResourceInputs
	if err := decodeInputs(req.Prior, &in); err != nil {
		return err
	}
	err := call(ctx, func() error {
		_, err := p.Client.DeleteResource(ctx, &apigateway.DeleteResourceInput{RestApiId: &in.API, ResourceId: &req.PhysicalID})
		return err
	})
	if err != nil && !awserr.Is(err, codeNotFound) {
		return fmt.Errorf("deleting the resource %s of the rest api: %w", in.Path, err)
	}
	return nil
}

// Replaces implements engine.Provider: a resource is replaced when anything of it changes.
func (RESTResource) Replaces(prior, desired json.RawMessage) bool {
	a, b := decodeBoth[RESTResourceInputs](prior, desired)
	return a != b
}

// RESTRouteClient is the part of the api gateway client the routes of the rest api use.
type RESTRouteClient interface {
	PutMethod(context.Context, *apigateway.PutMethodInput, ...func(*apigateway.Options)) (*apigateway.PutMethodOutput, error)
	GetMethod(context.Context, *apigateway.GetMethodInput, ...func(*apigateway.Options)) (*apigateway.GetMethodOutput, error)
	DeleteMethod(context.Context, *apigateway.DeleteMethodInput, ...func(*apigateway.Options)) (*apigateway.DeleteMethodOutput, error)
	PutIntegration(context.Context, *apigateway.PutIntegrationInput, ...func(*apigateway.Options)) (*apigateway.PutIntegrationOutput, error)
}

// RESTRouteInputs are the inputs of a route of the rest api: a method of a resource, and its
// lambda proxy integration with the function's invocation uri.
type RESTRouteInputs struct {
	API      string `json:"api"`
	Resource string `json:"resource"`
	Method   string `json:"method"`
	// AuthorizationType is NONE, AWS_IAM, CUSTOM or COGNITO_USER_POOLS.
	AuthorizationType string   `json:"authorizationType"`
	Authorizer        string   `json:"authorizer"`
	Scopes            []string `json:"scopes,omitempty"`
	APIKeyRequired    bool     `json:"apiKeyRequired,omitempty"`
	// Parameters are the request parameters, such as method.request.querystring.page, each true
	// when a request must have it, and Validator checks that it does.
	Parameters map[string]bool `json:"parameters,omitempty"`
	Validator  string          `json:"validator,omitempty"`
	URI        string          `json:"uri"`
	// TimeoutMillis is left out for api gateway's default, 29 seconds.
	TimeoutMillis int32 `json:"timeoutMillis,omitempty"`
}

// RESTRoute is the provider of the routes of the rest api, each a method and its integration.
// A change puts the method again: the stage serves the deployment it points at, which the change
// does not touch, until the stage is deployed again.
type RESTRoute struct {
	Client RESTRouteClient
}

func restRouteResult(in RESTRouteInputs) engine.Result {
	return engine.Result{PhysicalID: in.Resource + " " + in.Method, Attributes: map[string]string{"HttpMethod": in.Method, "ResourceId": in.Resource}}
}

// Create implements engine.Provider: the method, then its integration.
func (p RESTRoute) Create(ctx context.Context, req engine.Request) (engine.Result, error) {
	var in RESTRouteInputs
	if err := decodeInputs(req.Inputs, &in); err != nil {
		return engine.Result{}, err
	}
	err := call(ctx, func() error {
		_, err := p.Client.PutMethod(ctx, &apigateway.PutMethodInput{
			RestApiId: &in.API, ResourceId: &in.Resource, HttpMethod: &in.Method,
			AuthorizationType: &in.AuthorizationType, AuthorizerId: optional(in.Authorizer), AuthorizationScopes: in.Scopes,
			ApiKeyRequired: in.APIKeyRequired, RequestParameters: in.Parameters, RequestValidatorId: optional(in.Validator),
		})
		return err
	})
	if awserr.Is(err, codeConflict) {
		return engine.Result{}, engine.ErrExists
	}
	if err != nil {
		return engine.Result{}, fmt.Errorf("creating the method %s of the rest api: %w", in.Method, err)
	}
	input := &apigateway.PutIntegrationInput{
		RestApiId: &in.API, ResourceId: &in.Resource, HttpMethod: &in.Method,
		Type: types.IntegrationTypeAwsProxy, IntegrationHttpMethod: aws.String("POST"), Uri: &in.URI,
	}
	if in.TimeoutMillis > 0 {
		input.TimeoutInMillis = &in.TimeoutMillis
	}
	err = call(ctx, func() error {
		_, err := p.Client.PutIntegration(ctx, input)
		return err
	})
	if err != nil {
		return engine.Result{}, fmt.Errorf("integrating %s with the rest api: %w", in.URI, err)
	}
	return restRouteResult(in), nil
}

// Read implements engine.Provider: by its resource and method.
func (p RESTRoute) Read(ctx context.Context, req engine.Request) (engine.Result, bool, error) {
	var in RESTRouteInputs
	if err := decodeInputs(req.Inputs, &in); err != nil {
		return engine.Result{}, false, err
	}
	_, err := p.Client.GetMethod(ctx, &apigateway.GetMethodInput{RestApiId: &in.API, ResourceId: &in.Resource, HttpMethod: &in.Method})
	if awserr.Is(err, codeNotFound) {
		return engine.Result{}, false, nil
	}
	if err != nil {
		return engine.Result{}, false, fmt.Errorf("reading the method %s of the rest api: %w", in.Method, err)
	}
	return restRouteResult(in), true, nil
}

// Update implements engine.Provider: the method is put again, as a method takes a new
// authorizer, key, parameters and validator only through patches of each.
func (p RESTRoute) Update(ctx context.Context, req engine.Request) (engine.Result, error) {
	if err := p.Delete(ctx, req); err != nil {
		return engine.Result{}, err
	}
	return p.Create(ctx, req)
}

// Delete implements engine.Provider, with its integration.
func (p RESTRoute) Delete(ctx context.Context, req engine.Request) error {
	var in RESTRouteInputs
	data := req.Prior
	if len(data) == 0 {
		data = req.Inputs
	}
	if err := decodeInputs(data, &in); err != nil {
		return err
	}
	err := call(ctx, func() error {
		_, err := p.Client.DeleteMethod(ctx, &apigateway.DeleteMethodInput{RestApiId: &in.API, ResourceId: &in.Resource, HttpMethod: &in.Method})
		return err
	})
	if err != nil && !awserr.Is(err, codeNotFound) {
		return fmt.Errorf("deleting the method %s of the rest api: %w", in.Method, err)
	}
	return nil
}

// Replaces implements engine.Provider: a method belongs to its api and resource.
func (RESTRoute) Replaces(prior, desired json.RawMessage) bool {
	a, b := decodeBoth[RESTRouteInputs](prior, desired)
	return a.API != b.API || a.Resource != b.Resource || a.Method != b.Method
}

// RESTAuthorizerClient is the part of the api gateway client the authorizers of the rest api use.
type RESTAuthorizerClient interface {
	CreateAuthorizer(context.Context, *apigateway.CreateAuthorizerInput, ...func(*apigateway.Options)) (*apigateway.CreateAuthorizerOutput, error)
	GetAuthorizers(context.Context, *apigateway.GetAuthorizersInput, ...func(*apigateway.Options)) (*apigateway.GetAuthorizersOutput, error)
	UpdateAuthorizer(context.Context, *apigateway.UpdateAuthorizerInput, ...func(*apigateway.Options)) (*apigateway.UpdateAuthorizerOutput, error)
	DeleteAuthorizer(context.Context, *apigateway.DeleteAuthorizerInput, ...func(*apigateway.Options)) (*apigateway.DeleteAuthorizerOutput, error)
}

// RESTAuthorizerInputs are the inputs of an authorizer of the rest api: a function handed a
// token or the request, by its invocation uri, or cognito user pools.
type RESTAuthorizerInputs struct {
	API  string `json:"api"`
	Name string `json:"name"`
	// Type is TOKEN, REQUEST or COGNITO_USER_POOLS.
	Type       string   `json:"type"`
	URI        string   `json:"uri,omitempty"`
	Identity   string   `json:"identity"`
	Validation string   `json:"validation,omitempty"`
	TTL        int32    `json:"ttl"`
	UserPools  []string `json:"userPools,omitempty"`
}

// RESTAuthorizer is the provider of the authorizers of the rest api. Everything of one changes in
// place, so the methods that name it keep it.
type RESTAuthorizer struct {
	Client RESTAuthorizerClient
}

func restAuthorizerResult(id string) engine.Result {
	return engine.Result{PhysicalID: id, Attributes: map[string]string{"AuthorizerId": id}}
}

// Create implements engine.Provider.
func (p RESTAuthorizer) Create(ctx context.Context, req engine.Request) (engine.Result, error) {
	var in RESTAuthorizerInputs
	if err := decodeInputs(req.Inputs, &in); err != nil {
		return engine.Result{}, err
	}
	var out *apigateway.CreateAuthorizerOutput
	err := call(ctx, func() (err error) {
		out, err = p.Client.CreateAuthorizer(ctx, &apigateway.CreateAuthorizerInput{
			RestApiId: &in.API, Name: &in.Name, Type: types.AuthorizerType(in.Type), AuthorizerUri: optional(in.URI),
			IdentitySource: optional(in.Identity), IdentityValidationExpression: optional(in.Validation),
			AuthorizerResultTtlInSeconds: &in.TTL, ProviderARNs: in.UserPools,
		})
		return err
	})
	if err != nil {
		return engine.Result{}, fmt.Errorf("creating the authorizer %s of the rest api: %w", in.Name, err)
	}
	return restAuthorizerResult(aws.ToString(out.Id)), nil
}

// Read implements engine.Provider: by its name, which is unique in the api.
func (p RESTAuthorizer) Read(ctx context.Context, req engine.Request) (engine.Result, bool, error) {
	var in RESTAuthorizerInputs
	if err := decodeInputs(req.Inputs, &in); err != nil {
		return engine.Result{}, false, err
	}
	out, err := p.Client.GetAuthorizers(ctx, &apigateway.GetAuthorizersInput{RestApiId: &in.API, Limit: aws.Int32(500)})
	if awserr.Is(err, codeNotFound) {
		return engine.Result{}, false, nil
	}
	if err != nil {
		return engine.Result{}, false, fmt.Errorf("listing the authorizers of the rest api %s: %w", in.API, err)
	}
	for _, a := range out.Items {
		if aws.ToString(a.Id) == req.PhysicalID || aws.ToString(a.Name) == in.Name {
			return restAuthorizerResult(aws.ToString(a.Id)), true, nil
		}
	}
	return engine.Result{}, false, nil
}

// patches are what changes an authorizer from prior to in: each field that differs set again, as
// a field of another type may not be, and the user pools it gains added and those it loses
// removed. Without a prior, as after a crash, every field is set.
func (in RESTAuthorizerInputs) patches(prior RESTAuthorizerInputs) []types.PatchOperation {
	fields := []struct{ path, value, was string }{
		{"/name", in.Name, prior.Name},
		{"/type", in.Type, prior.Type},
		{"/identitySource", in.Identity, prior.Identity},
		{"/authorizerResultTtlInSeconds", strconv.Itoa(int(in.TTL)), strconv.Itoa(int(prior.TTL))},
		{"/identityValidationExpression", in.Validation, prior.Validation},
		{"/authorizerUri", in.URI, prior.URI},
	}
	var ops []types.PatchOperation
	for _, f := range fields {
		if f.value != f.was || (prior.API == "" && f.value != "") {
			ops = append(ops, replace(f.path, f.value))
		}
	}
	for _, pool := range in.UserPools {
		if !slices.Contains(prior.UserPools, pool) {
			ops = append(ops, types.PatchOperation{Op: types.OpAdd, Path: aws.String("/providerARNs"), Value: aws.String(pool)})
		}
	}
	for _, pool := range prior.UserPools {
		if !slices.Contains(in.UserPools, pool) {
			ops = append(ops, types.PatchOperation{Op: types.OpRemove, Path: aws.String("/providerARNs"), Value: aws.String(pool)})
		}
	}
	return ops
}

// Update implements engine.Provider.
func (p RESTAuthorizer) Update(ctx context.Context, req engine.Request) (engine.Result, error) {
	prior, in := decodeBoth[RESTAuthorizerInputs](req.Prior, req.Inputs)
	ops := in.patches(prior)
	if len(ops) == 0 {
		return restAuthorizerResult(req.PhysicalID), nil
	}
	err := call(ctx, func() error {
		_, err := p.Client.UpdateAuthorizer(ctx, &apigateway.UpdateAuthorizerInput{RestApiId: &in.API, AuthorizerId: &req.PhysicalID, PatchOperations: ops})
		return err
	})
	if err != nil {
		return engine.Result{}, fmt.Errorf("updating the authorizer %s of the rest api: %w", in.Name, err)
	}
	return restAuthorizerResult(req.PhysicalID), nil
}

// Delete implements engine.Provider.
func (p RESTAuthorizer) Delete(ctx context.Context, req engine.Request) error {
	var in RESTAuthorizerInputs
	if err := decodeInputs(req.Prior, &in); err != nil {
		return err
	}
	err := call(ctx, func() error {
		_, err := p.Client.DeleteAuthorizer(ctx, &apigateway.DeleteAuthorizerInput{RestApiId: &in.API, AuthorizerId: &req.PhysicalID})
		return err
	})
	if err != nil && !awserr.Is(err, codeNotFound) {
		return fmt.Errorf("deleting the authorizer %s of the rest api: %w", in.Name, err)
	}
	return nil
}

// Replaces implements engine.Provider: an authorizer belongs to its api.
func (RESTAuthorizer) Replaces(prior, desired json.RawMessage) bool {
	a, b := decodeBoth[RESTAuthorizerInputs](prior, desired)
	return a.API != b.API
}
