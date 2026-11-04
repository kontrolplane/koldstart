package awsp

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/apigatewayv2"
	"github.com/aws/aws-sdk-go-v2/service/apigatewayv2/types"

	"github.com/kontrolplane/koldstart/internal/engine"
)

// HTTPAPIInputs are the inputs of the stage's http api, with its $default stage.
type HTTPAPIInputs struct {
	Name       string      `json:"name"`
	CORS       *URLCORS    `json:"cors"`
	Throttling *Throttling `json:"throttling"`
}

// Throttling is the stage-wide request limit of an http api.
type Throttling struct {
	Rate  float64 `json:"rate"`
	Burst int32   `json:"burst"`
}

// defaultStage is the one stage of every http api koldstart makes, deployed on every change.
const defaultStage = "$default"

// HTTPAPI is the provider of the http api all routes of a stage share. Http apis have no unique
// name, so one a crashed deploy made is found by its name and tags.
type HTTPAPI struct {
	Client *apigatewayv2.Client
	Region string
}

func (in HTTPAPIInputs) cors() *types.Cors {
	if in.CORS == nil {
		return nil
	}
	return &types.Cors{
		AllowOrigins: in.CORS.Origins, AllowMethods: in.CORS.Methods, AllowHeaders: in.CORS.Headers,
		ExposeHeaders: in.CORS.Expose, AllowCredentials: &in.CORS.Credentials, MaxAge: &in.CORS.MaxAge,
	}
}

func (in HTTPAPIInputs) routeSettings() *types.RouteSettings {
	if in.Throttling == nil {
		return &types.RouteSettings{}
	}
	return &types.RouteSettings{ThrottlingRateLimit: &in.Throttling.Rate, ThrottlingBurstLimit: &in.Throttling.Burst}
}

func apiResult(id, endpoint string) engine.Result {
	return engine.Result{PhysicalID: id, Attributes: map[string]string{"ApiId": id, "ApiEndpoint": endpoint}}
}

// Create implements engine.Provider.
func (p HTTPAPI) Create(ctx context.Context, req engine.Request) (engine.Result, error) {
	var in HTTPAPIInputs
	if err := decodeInputs(req.Inputs, &in); err != nil {
		return engine.Result{}, err
	}
	api, err := p.Client.CreateApi(ctx, &apigatewayv2.CreateApiInput{
		Name: &in.Name, ProtocolType: types.ProtocolTypeHttp, CorsConfiguration: in.cors(), Tags: req.Tags,
	})
	if err != nil {
		return engine.Result{}, fmt.Errorf("creating the http api %s: %w", in.Name, err)
	}
	_, err = p.Client.CreateStage(ctx, &apigatewayv2.CreateStageInput{
		ApiId: api.ApiId, StageName: aws.String(defaultStage), AutoDeploy: aws.Bool(true),
		DefaultRouteSettings: in.routeSettings(), Tags: req.Tags,
	})
	if err != nil {
		return engine.Result{}, fmt.Errorf("creating the stage of the http api %s: %w", in.Name, err)
	}
	return apiResult(aws.ToString(api.ApiId), aws.ToString(api.ApiEndpoint)), nil
}

// Read implements engine.Provider: by id, or after a crash by its name and tags.
func (p HTTPAPI) Read(ctx context.Context, req engine.Request) (engine.Result, bool, error) {
	var in HTTPAPIInputs
	if err := decodeInputs(req.Inputs, &in); err != nil {
		return engine.Result{}, false, err
	}
	var next *string
	for {
		out, err := p.Client.GetApis(ctx, &apigatewayv2.GetApisInput{NextToken: next})
		if err != nil {
			return engine.Result{}, false, fmt.Errorf("listing the http apis: %w", err)
		}
		for _, api := range out.Items {
			if aws.ToString(api.ApiId) == req.PhysicalID ||
				(aws.ToString(api.Name) == in.Name && ours(in.Name, api.Tags, req) == nil) {
				return apiResult(aws.ToString(api.ApiId), aws.ToString(api.ApiEndpoint)), true, nil
			}
		}
		if out.NextToken == nil {
			return engine.Result{}, false, nil
		}
		next = out.NextToken
	}
}

// Update implements engine.Provider.
func (p HTTPAPI) Update(ctx context.Context, req engine.Request) (engine.Result, error) {
	var in HTTPAPIInputs
	if err := decodeInputs(req.Inputs, &in); err != nil {
		return engine.Result{}, err
	}
	id := req.PhysicalID
	if in.CORS == nil {
		_, err := p.Client.DeleteCorsConfiguration(ctx, &apigatewayv2.DeleteCorsConfigurationInput{ApiId: &id})
		if err != nil && !isCode(err, "NotFoundException") {
			return engine.Result{}, fmt.Errorf("removing the cors of the http api %s: %w", in.Name, err)
		}
	}
	api, err := p.Client.UpdateApi(ctx, &apigatewayv2.UpdateApiInput{ApiId: &id, Name: &in.Name, CorsConfiguration: in.cors()})
	if err != nil {
		return engine.Result{}, fmt.Errorf("updating the http api %s: %w", in.Name, err)
	}
	_, err = p.Client.UpdateStage(ctx, &apigatewayv2.UpdateStageInput{ApiId: &id, StageName: aws.String(defaultStage), DefaultRouteSettings: in.routeSettings()})
	if isCode(err, "NotFoundException") {
		_, err = p.Client.CreateStage(ctx, &apigatewayv2.CreateStageInput{
			ApiId: &id, StageName: aws.String(defaultStage), AutoDeploy: aws.Bool(true), DefaultRouteSettings: in.routeSettings(), Tags: req.Tags,
		})
	}
	if err != nil {
		return engine.Result{}, fmt.Errorf("updating the stage of the http api %s: %w", in.Name, err)
	}
	arn := "arn:" + Partition(p.Region) + ":apigateway:" + p.Region + "::/apis/" + id
	if _, err := p.Client.TagResource(ctx, &apigatewayv2.TagResourceInput{ResourceArn: &arn, Tags: req.Tags}); err != nil {
		return engine.Result{}, fmt.Errorf("tagging the http api %s: %w", in.Name, err)
	}
	return apiResult(id, aws.ToString(api.ApiEndpoint)), nil
}

// Delete implements engine.Provider.
func (p HTTPAPI) Delete(ctx context.Context, req engine.Request) error {
	_, err := p.Client.DeleteApi(ctx, &apigatewayv2.DeleteApiInput{ApiId: &req.PhysicalID})
	if err != nil && !isCode(err, "NotFoundException") {
		return fmt.Errorf("deleting the http api %s: %w", req.PhysicalID, err)
	}
	return nil
}

// Replaces implements engine.Provider: everything changes in place.
func (HTTPAPI) Replaces(_, _ json.RawMessage) bool { return false }

// IntegrationInputs are the inputs of the integration of a function with the http api.
type IntegrationInputs struct {
	API      string `json:"api"`
	Function string `json:"function"`
}

// Integration is the provider of the proxy integrations that hand requests to functions.
type Integration struct {
	Client *apigatewayv2.Client
}

func integrationResult(id string) engine.Result {
	return engine.Result{PhysicalID: id, Attributes: map[string]string{"IntegrationId": id}}
}

// Create implements engine.Provider.
func (p Integration) Create(ctx context.Context, req engine.Request) (engine.Result, error) {
	var in IntegrationInputs
	if err := decodeInputs(req.Inputs, &in); err != nil {
		return engine.Result{}, err
	}
	out, err := p.Client.CreateIntegration(ctx, &apigatewayv2.CreateIntegrationInput{
		ApiId: &in.API, IntegrationType: types.IntegrationTypeAwsProxy, IntegrationUri: &in.Function,
		PayloadFormatVersion: aws.String("2.0"),
	})
	if err != nil {
		return engine.Result{}, fmt.Errorf("integrating %s with the http api: %w", in.Function, err)
	}
	return integrationResult(aws.ToString(out.IntegrationId)), nil
}

// Read implements engine.Provider: an api has one integration per function.
func (p Integration) Read(ctx context.Context, req engine.Request) (engine.Result, bool, error) {
	var in IntegrationInputs
	if err := decodeInputs(req.Inputs, &in); err != nil {
		return engine.Result{}, false, err
	}
	out, err := p.Client.GetIntegrations(ctx, &apigatewayv2.GetIntegrationsInput{ApiId: &in.API})
	if isCode(err, "NotFoundException") {
		return engine.Result{}, false, nil
	}
	if err != nil {
		return engine.Result{}, false, fmt.Errorf("listing the integrations of %s: %w", in.API, err)
	}
	for _, i := range out.Items {
		if aws.ToString(i.IntegrationId) == req.PhysicalID || aws.ToString(i.IntegrationUri) == in.Function {
			return integrationResult(aws.ToString(i.IntegrationId)), true, nil
		}
	}
	return engine.Result{}, false, nil
}

// Update implements engine.Provider.
func (p Integration) Update(ctx context.Context, req engine.Request) (engine.Result, error) {
	var in IntegrationInputs
	if err := decodeInputs(req.Inputs, &in); err != nil {
		return engine.Result{}, err
	}
	_, err := p.Client.UpdateIntegration(ctx, &apigatewayv2.UpdateIntegrationInput{
		ApiId: &in.API, IntegrationId: &req.PhysicalID, IntegrationUri: &in.Function, PayloadFormatVersion: aws.String("2.0"),
	})
	if err != nil {
		return engine.Result{}, fmt.Errorf("updating the integration of %s: %w", in.Function, err)
	}
	return integrationResult(req.PhysicalID), nil
}

// Delete implements engine.Provider.
func (p Integration) Delete(ctx context.Context, req engine.Request) error {
	var in IntegrationInputs
	if err := decodeInputs(req.Prior, &in); err != nil {
		return err
	}
	_, err := p.Client.DeleteIntegration(ctx, &apigatewayv2.DeleteIntegrationInput{ApiId: &in.API, IntegrationId: &req.PhysicalID})
	if err != nil && !isCode(err, "NotFoundException") {
		return fmt.Errorf("deleting the integration of %s: %w", in.Function, err)
	}
	return nil
}

// Replaces implements engine.Provider: an integration belongs to its api.
func (Integration) Replaces(prior, desired json.RawMessage) bool {
	var a, b IntegrationInputs
	_ = json.Unmarshal(prior, &a)
	_ = json.Unmarshal(desired, &b)
	return a.API != b.API
}

// RouteInputs are the inputs of a route of the http api.
type RouteInputs struct {
	API               string   `json:"api"`
	RouteKey          string   `json:"routeKey"`
	Target            string   `json:"target"`
	AuthorizationType string   `json:"authorizationType"`
	Authorizer        string   `json:"authorizer"`
	Scopes            []string `json:"scopes"`
}

// Route is the provider of the routes of the http api.
type Route struct {
	Client *apigatewayv2.Client
}

func routeResult(id string) engine.Result {
	return engine.Result{PhysicalID: id, Attributes: map[string]string{"RouteId": id}}
}

func optional(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// Create implements engine.Provider.
func (p Route) Create(ctx context.Context, req engine.Request) (engine.Result, error) {
	var in RouteInputs
	if err := decodeInputs(req.Inputs, &in); err != nil {
		return engine.Result{}, err
	}
	out, err := p.Client.CreateRoute(ctx, &apigatewayv2.CreateRouteInput{
		ApiId: &in.API, RouteKey: &in.RouteKey, Target: &in.Target,
		AuthorizationType: types.AuthorizationType(in.AuthorizationType), AuthorizerId: optional(in.Authorizer), AuthorizationScopes: in.Scopes,
	})
	if isCode(err, "ConflictException") {
		return engine.Result{}, engine.ErrExists
	}
	if err != nil {
		return engine.Result{}, fmt.Errorf("creating the route %s: %w", in.RouteKey, err)
	}
	return routeResult(aws.ToString(out.RouteId)), nil
}

// Read implements engine.Provider: by its route key, which is unique in the api.
func (p Route) Read(ctx context.Context, req engine.Request) (engine.Result, bool, error) {
	var in RouteInputs
	if err := decodeInputs(req.Inputs, &in); err != nil {
		return engine.Result{}, false, err
	}
	out, err := p.Client.GetRoutes(ctx, &apigatewayv2.GetRoutesInput{ApiId: &in.API})
	if isCode(err, "NotFoundException") {
		return engine.Result{}, false, nil
	}
	if err != nil {
		return engine.Result{}, false, fmt.Errorf("listing the routes of %s: %w", in.API, err)
	}
	for _, r := range out.Items {
		if aws.ToString(r.RouteKey) == in.RouteKey {
			return routeResult(aws.ToString(r.RouteId)), true, nil
		}
	}
	return engine.Result{}, false, nil
}

// Update implements engine.Provider.
func (p Route) Update(ctx context.Context, req engine.Request) (engine.Result, error) {
	var in RouteInputs
	if err := decodeInputs(req.Inputs, &in); err != nil {
		return engine.Result{}, err
	}
	authorizer := in.Authorizer
	_, err := p.Client.UpdateRoute(ctx, &apigatewayv2.UpdateRouteInput{
		ApiId: &in.API, RouteId: &req.PhysicalID, RouteKey: &in.RouteKey, Target: &in.Target,
		AuthorizationType: types.AuthorizationType(in.AuthorizationType), AuthorizerId: &authorizer, AuthorizationScopes: in.Scopes,
	})
	if err != nil {
		return engine.Result{}, fmt.Errorf("updating the route %s: %w", in.RouteKey, err)
	}
	return routeResult(req.PhysicalID), nil
}

// Delete implements engine.Provider.
func (p Route) Delete(ctx context.Context, req engine.Request) error {
	var in RouteInputs
	if err := decodeInputs(req.Prior, &in); err != nil {
		return err
	}
	_, err := p.Client.DeleteRoute(ctx, &apigatewayv2.DeleteRouteInput{ApiId: &in.API, RouteId: &req.PhysicalID})
	if err != nil && !isCode(err, "NotFoundException") {
		return fmt.Errorf("deleting the route %s: %w", in.RouteKey, err)
	}
	return nil
}

// Replaces implements engine.Provider: a route belongs to its api.
func (Route) Replaces(prior, desired json.RawMessage) bool {
	var a, b RouteInputs
	_ = json.Unmarshal(prior, &a)
	_ = json.Unmarshal(desired, &b)
	return a.API != b.API
}
