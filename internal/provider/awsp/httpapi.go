package awsp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/apigatewayv2"
	"github.com/aws/aws-sdk-go-v2/service/apigatewayv2/types"

	"github.com/kontrolplane/koldstart/internal/awserr"
	"github.com/kontrolplane/koldstart/internal/engine"
)

// HTTPAPIInputs are the inputs of the stage's http api, with its $default stage, or of its
// websocket api, with a stage of the stage's name. The fields of a websocket api are left out of
// an http api's inputs, so one deployed before them keeps its hash.
type HTTPAPIInputs struct {
	Name       string      `json:"name"`
	CORS       *CORS       `json:"cors"`
	Throttling *Throttling `json:"throttling"`
	// Protocol is WEBSOCKET for a websocket api, empty for an http api.
	Protocol       string `json:"protocol,omitempty"`
	RouteSelection string `json:"routeSelection,omitempty"`
	Stage          string `json:"stage,omitempty"`
	// AccessLogs is left out when the api has none, so one deployed before them keeps its hash.
	AccessLogs *AccessLogs `json:"accessLogs,omitempty"`
}

// AccessLogs are where the stage writes a line for every request, and what it writes.
type AccessLogs struct {
	// Destination is the log group's arn, which ends in :* as DescribeLogGroups gives it.
	Destination string `json:"destination"`
	Format      string `json:"format"`
}

// accessLogs is how the stage logs requests, or nil when it does not.
func (in HTTPAPIInputs) accessLogs() *types.AccessLogSettings {
	if in.AccessLogs == nil {
		return nil
	}
	return &types.AccessLogSettings{DestinationArn: aws.String(strings.TrimSuffix(in.AccessLogs.Destination, ":*")), Format: &in.AccessLogs.Format}
}

// protocolWebSocket is the protocol of a websocket api.
const protocolWebSocket = "WEBSOCKET"

func (in HTTPAPIInputs) protocol() types.ProtocolType {
	if in.Protocol == protocolWebSocket {
		return types.ProtocolTypeWebsocket
	}
	return types.ProtocolTypeHttp
}

// stage is the api's one stage: $default for an http api.
func (in HTTPAPIInputs) stage() string {
	if in.Stage == "" {
		return defaultStage
	}
	return in.Stage
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

// cors is the api's cross-origin configuration, nil without one: DeleteCorsConfiguration removes
// what an api had.
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

func apiResult(id, endpoint, stage string) engine.Result {
	return engine.Result{PhysicalID: id, Attributes: map[string]string{"ApiId": id, "ApiEndpoint": endpoint, "Stage": stage}}
}

// Create implements engine.Provider.
func (p HTTPAPI) Create(ctx context.Context, req engine.Request) (engine.Result, error) {
	var in HTTPAPIInputs
	if err := decodeInputs(req.Inputs, &in); err != nil {
		return engine.Result{}, err
	}
	api, err := p.Client.CreateApi(ctx, &apigatewayv2.CreateApiInput{
		Name: &in.Name, ProtocolType: in.protocol(), CorsConfiguration: in.cors(), Tags: req.Tags,
		RouteSelectionExpression: optional(in.RouteSelection),
	})
	if err != nil {
		return engine.Result{}, fmt.Errorf("creating the api %s: %w", in.Name, err)
	}
	_, err = p.Client.CreateStage(ctx, &apigatewayv2.CreateStageInput{
		ApiId: api.ApiId, StageName: aws.String(in.stage()), AutoDeploy: aws.Bool(true),
		DefaultRouteSettings: in.routeSettings(), AccessLogSettings: in.accessLogs(), Tags: req.Tags,
	})
	if err != nil {
		return engine.Result{}, fmt.Errorf("creating the stage of the api %s: %w", in.Name, err)
	}
	return apiResult(aws.ToString(api.ApiId), aws.ToString(api.ApiEndpoint), in.stage()), nil
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
				(aws.ToString(api.Name) == in.Name && api.ProtocolType == in.protocol() && ours(in.Name, api.Tags, req) == nil) {
				return apiResult(aws.ToString(api.ApiId), aws.ToString(api.ApiEndpoint), in.stage()), true, nil
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
	if in.CORS == nil && in.protocol() == types.ProtocolTypeHttp {
		_, err := p.Client.DeleteCorsConfiguration(ctx, &apigatewayv2.DeleteCorsConfigurationInput{ApiId: &id})
		if err != nil && !awserr.Is(err, "NotFoundException") {
			return engine.Result{}, fmt.Errorf("removing the cors of the http api %s: %w", in.Name, err)
		}
	}
	api, err := p.Client.UpdateApi(ctx, &apigatewayv2.UpdateApiInput{
		ApiId: &id, Name: &in.Name, CorsConfiguration: in.cors(), RouteSelectionExpression: optional(in.RouteSelection),
	})
	if err != nil {
		return engine.Result{}, fmt.Errorf("updating the api %s: %w", in.Name, err)
	}
	_, err = p.Client.UpdateStage(ctx, &apigatewayv2.UpdateStageInput{
		ApiId: &id, StageName: aws.String(in.stage()), DefaultRouteSettings: in.routeSettings(), AccessLogSettings: in.accessLogs(),
	})
	if awserr.Is(err, "NotFoundException") {
		_, err = p.Client.CreateStage(ctx, &apigatewayv2.CreateStageInput{
			ApiId: &id, StageName: aws.String(in.stage()), AutoDeploy: aws.Bool(true), DefaultRouteSettings: in.routeSettings(),
			AccessLogSettings: in.accessLogs(), Tags: req.Tags,
		})
	}
	if err != nil {
		return engine.Result{}, fmt.Errorf("updating the stage of the http api %s: %w", in.Name, err)
	}
	var prior HTTPAPIInputs
	if err := decodeInputs(req.Prior, &prior); err != nil {
		return engine.Result{}, err
	}
	if in.AccessLogs == nil && prior.AccessLogs != nil {
		_, err := p.Client.DeleteAccessLogSettings(ctx, &apigatewayv2.DeleteAccessLogSettingsInput{ApiId: &id, StageName: aws.String(in.stage())})
		if err != nil && !awserr.Is(err, "NotFoundException") {
			return engine.Result{}, fmt.Errorf("removing the access logs of the http api %s: %w", in.Name, err)
		}
	}
	arn := "arn:" + Partition(p.Region) + ":apigateway:" + p.Region + "::/apis/" + id
	if _, err := p.Client.TagResource(ctx, &apigatewayv2.TagResourceInput{ResourceArn: &arn, Tags: req.Tags}); err != nil {
		return engine.Result{}, fmt.Errorf("tagging the http api %s: %w", in.Name, err)
	}
	return apiResult(id, aws.ToString(api.ApiEndpoint), in.stage()), nil
}

// Delete implements engine.Provider.
func (p HTTPAPI) Delete(ctx context.Context, req engine.Request) error {
	_, err := p.Client.DeleteApi(ctx, &apigatewayv2.DeleteApiInput{ApiId: &req.PhysicalID})
	if err != nil && !awserr.Is(err, "NotFoundException") {
		return fmt.Errorf("deleting the http api %s: %w", req.PhysicalID, err)
	}
	return nil
}

// Replaces implements engine.Provider: everything changes in place but the protocol, and a
// websocket api's stage.
func (HTTPAPI) Replaces(prior, desired json.RawMessage) bool {
	a, b := decodeBoth[HTTPAPIInputs](prior, desired)
	return a.protocol() != b.protocol() || a.stage() != b.stage()
}

// IntegrationInputs are the inputs of the integration of a function with the http api, by the
// function's arn, or with the websocket api, by its invocation uri.
type IntegrationInputs struct {
	API       string `json:"api"`
	Function  string `json:"function"`
	WebSocket bool   `json:"webSocket,omitempty"`
}

// payloadFormat is the version of the event an http api hands its functions; a websocket api has
// one version only, which is not set.
func (in IntegrationInputs) payloadFormat() *string {
	if in.WebSocket {
		return nil
	}
	return aws.String("2.0")
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
		PayloadFormatVersion: in.payloadFormat(),
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
	if awserr.Is(err, "NotFoundException") {
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
		ApiId: &in.API, IntegrationId: &req.PhysicalID, IntegrationUri: &in.Function, PayloadFormatVersion: in.payloadFormat(),
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
	if err != nil && !awserr.Is(err, "NotFoundException") {
		return fmt.Errorf("deleting the integration of %s: %w", in.Function, err)
	}
	return nil
}

// Replaces implements engine.Provider: an integration belongs to its api.
func (Integration) Replaces(prior, desired json.RawMessage) bool {
	a, b := decodeBoth[IntegrationInputs](prior, desired)
	return a.API != b.API
}

// RouteInputs are the inputs of a route of the http api or the websocket api, with its own
// request limit on the api's stage when it has one.
type RouteInputs struct {
	API               string      `json:"api"`
	RouteKey          string      `json:"routeKey"`
	Target            string      `json:"target"`
	AuthorizationType string      `json:"authorizationType"`
	Authorizer        string      `json:"authorizer"`
	Scopes            []string    `json:"scopes"`
	Throttling        *Throttling `json:"throttling,omitempty"`
	Stage             string      `json:"stage,omitempty"`
}

// stage is the stage the route's request limit is set on: $default for an http api.
func (in RouteInputs) stage() string {
	if in.Stage == "" {
		return defaultStage
	}
	return in.Stage
}

// throttle sets the route's own request limit on the stage, or removes the one it had.
func (p Route) throttle(ctx context.Context, in RouteInputs, prior *RouteInputs) error {
	if in.Throttling == nil {
		if prior == nil || prior.Throttling == nil {
			return nil
		}
		_, err := p.Client.DeleteRouteSettings(ctx, &apigatewayv2.DeleteRouteSettingsInput{ApiId: &in.API, StageName: aws.String(in.stage()), RouteKey: &in.RouteKey})
		if err != nil && !awserr.Is(err, "NotFoundException") {
			return fmt.Errorf("removing the request limit of %s: %w", in.RouteKey, err)
		}
		return nil
	}
	_, err := p.Client.UpdateStage(ctx, &apigatewayv2.UpdateStageInput{
		ApiId: &in.API, StageName: aws.String(in.stage()),
		RouteSettings: map[string]types.RouteSettings{in.RouteKey: {ThrottlingRateLimit: &in.Throttling.Rate, ThrottlingBurstLimit: &in.Throttling.Burst}},
	})
	if err != nil {
		return fmt.Errorf("setting the request limit of %s: %w", in.RouteKey, err)
	}
	return nil
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
	if awserr.Is(err, "ConflictException") {
		return engine.Result{}, engine.ErrExists
	}
	if err != nil {
		return engine.Result{}, fmt.Errorf("creating the route %s: %w", in.RouteKey, err)
	}
	if err := p.throttle(ctx, in, nil); err != nil {
		return engine.Result{}, err
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
	if awserr.Is(err, "NotFoundException") {
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
	var prior *RouteInputs
	if len(req.Prior) > 0 {
		prior = &RouteInputs{}
		if err := decodeInputs(req.Prior, prior); err != nil {
			return engine.Result{}, err
		}
	}
	if err := p.throttle(ctx, in, prior); err != nil {
		return engine.Result{}, err
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
	if err != nil && !awserr.Is(err, "NotFoundException") {
		return fmt.Errorf("deleting the route %s: %w", in.RouteKey, err)
	}
	return nil
}

// Replaces implements engine.Provider: a route belongs to its api.
func (Route) Replaces(prior, desired json.RawMessage) bool {
	a, b := decodeBoth[RouteInputs](prior, desired)
	return a.API != b.API
}
