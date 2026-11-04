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

// AuthorizerInputs are the inputs of an authorizer of the http api: a jwt one, or a function.
type AuthorizerInputs struct {
	API      string   `json:"api"`
	Name     string   `json:"name"`
	Type     string   `json:"type"`
	Issuer   string   `json:"issuer"`
	Audience []string `json:"audience"`
	Identity []string `json:"identity"`
	URI      string   `json:"uri"`
	TTL      int32    `json:"ttl"`
}

// Authorizer is the provider of the authorizers of the http api.
type Authorizer struct {
	Client *apigatewayv2.Client
}

func (in AuthorizerInputs) isJWT() bool { return in.Type == string(types.AuthorizerTypeJwt) }

func (in AuthorizerInputs) identity() []string {
	if len(in.Identity) > 0 {
		return in.Identity
	}
	return []string{"$request.header.Authorization"}
}

func authorizerResult(id string) engine.Result {
	return engine.Result{PhysicalID: id, Attributes: map[string]string{"AuthorizerId": id}}
}

// Create implements engine.Provider.
func (p Authorizer) Create(ctx context.Context, req engine.Request) (engine.Result, error) {
	var in AuthorizerInputs
	if err := decodeInputs(req.Inputs, &in); err != nil {
		return engine.Result{}, err
	}
	create := &apigatewayv2.CreateAuthorizerInput{
		ApiId: &in.API, Name: &in.Name, AuthorizerType: types.AuthorizerType(in.Type), IdentitySource: in.identity(),
	}
	if in.isJWT() {
		create.JwtConfiguration = &types.JWTConfiguration{Issuer: &in.Issuer, Audience: in.Audience}
	} else {
		create.AuthorizerUri = &in.URI
		create.AuthorizerPayloadFormatVersion = aws.String("2.0")
		create.EnableSimpleResponses = aws.Bool(true)
		create.AuthorizerResultTtlInSeconds = &in.TTL
	}
	out, err := p.Client.CreateAuthorizer(ctx, create)
	if err != nil {
		return engine.Result{}, fmt.Errorf("creating the authorizer %s: %w", in.Name, err)
	}
	return authorizerResult(aws.ToString(out.AuthorizerId)), nil
}

// Read implements engine.Provider: by its name, which is unique in the api.
func (p Authorizer) Read(ctx context.Context, req engine.Request) (engine.Result, bool, error) {
	var in AuthorizerInputs
	if err := decodeInputs(req.Inputs, &in); err != nil {
		return engine.Result{}, false, err
	}
	out, err := p.Client.GetAuthorizers(ctx, &apigatewayv2.GetAuthorizersInput{ApiId: &in.API})
	if isCode(err, "NotFoundException") {
		return engine.Result{}, false, nil
	}
	if err != nil {
		return engine.Result{}, false, fmt.Errorf("listing the authorizers of %s: %w", in.API, err)
	}
	for _, a := range out.Items {
		if aws.ToString(a.AuthorizerId) == req.PhysicalID || aws.ToString(a.Name) == in.Name {
			return authorizerResult(aws.ToString(a.AuthorizerId)), true, nil
		}
	}
	return engine.Result{}, false, nil
}

// Update implements engine.Provider.
func (p Authorizer) Update(ctx context.Context, req engine.Request) (engine.Result, error) {
	var in AuthorizerInputs
	if err := decodeInputs(req.Inputs, &in); err != nil {
		return engine.Result{}, err
	}
	update := &apigatewayv2.UpdateAuthorizerInput{
		ApiId: &in.API, AuthorizerId: &req.PhysicalID, Name: &in.Name, IdentitySource: in.identity(),
	}
	if in.isJWT() {
		update.JwtConfiguration = &types.JWTConfiguration{Issuer: &in.Issuer, Audience: in.Audience}
	} else {
		update.AuthorizerUri = &in.URI
		update.AuthorizerResultTtlInSeconds = &in.TTL
	}
	if _, err := p.Client.UpdateAuthorizer(ctx, update); err != nil {
		return engine.Result{}, fmt.Errorf("updating the authorizer %s: %w", in.Name, err)
	}
	return authorizerResult(req.PhysicalID), nil
}

// Delete implements engine.Provider.
func (p Authorizer) Delete(ctx context.Context, req engine.Request) error {
	var in AuthorizerInputs
	if err := decodeInputs(req.Prior, &in); err != nil {
		return err
	}
	_, err := p.Client.DeleteAuthorizer(ctx, &apigatewayv2.DeleteAuthorizerInput{ApiId: &in.API, AuthorizerId: &req.PhysicalID})
	if err != nil && !isCode(err, "NotFoundException") {
		return fmt.Errorf("deleting the authorizer %s: %w", in.Name, err)
	}
	return nil
}

// Replaces implements engine.Provider: the type of an authorizer cannot change.
func (Authorizer) Replaces(prior, desired json.RawMessage) bool {
	var a, b AuthorizerInputs
	_ = json.Unmarshal(prior, &a)
	_ = json.Unmarshal(desired, &b)
	return a.API != b.API || a.Type != b.Type
}

// DomainInputs are the inputs of the http api's custom domain and its mapping to the api.
type DomainInputs struct {
	Domain      string `json:"domain"`
	Certificate string `json:"certificate"`
	API         string `json:"api"`
}

// Domain is the provider of the http api's custom domain.
type Domain struct {
	Client *apigatewayv2.Client
}

func domainResult(out *types.DomainNameConfiguration, domain string) engine.Result {
	attrs := map[string]string{"DomainName": domain}
	if out != nil {
		attrs["RegionalDomainName"] = aws.ToString(out.ApiGatewayDomainName)
		attrs["RegionalHostedZoneId"] = aws.ToString(out.HostedZoneId)
	}
	return engine.Result{PhysicalID: domain, Attributes: attrs}
}

func firstConfig(configs []types.DomainNameConfiguration) *types.DomainNameConfiguration {
	if len(configs) == 0 {
		return nil
	}
	return &configs[0]
}

// Create implements engine.Provider.
func (p Domain) Create(ctx context.Context, req engine.Request) (engine.Result, error) {
	var in DomainInputs
	if err := decodeInputs(req.Inputs, &in); err != nil {
		return engine.Result{}, err
	}
	out, err := p.Client.CreateDomainName(ctx, &apigatewayv2.CreateDomainNameInput{
		DomainName: &in.Domain,
		DomainNameConfigurations: []types.DomainNameConfiguration{{
			CertificateArn: &in.Certificate, EndpointType: types.EndpointTypeRegional, SecurityPolicy: types.SecurityPolicyTls12,
		}},
		Tags: req.Tags,
	})
	if isCode(err, "ConflictException") {
		return engine.Result{}, engine.ErrExists
	}
	if err != nil {
		return engine.Result{}, fmt.Errorf("creating the domain %s: %w", in.Domain, err)
	}
	if err := p.mapping(ctx, in); err != nil {
		return engine.Result{}, err
	}
	return domainResult(firstConfig(out.DomainNameConfigurations), in.Domain), nil
}

// mapping points the domain at the api's $default stage, replacing a mapping to another api.
func (p Domain) mapping(ctx context.Context, in DomainInputs) error {
	out, err := p.Client.GetApiMappings(ctx, &apigatewayv2.GetApiMappingsInput{DomainName: &in.Domain})
	if err != nil {
		return fmt.Errorf("reading the mappings of %s: %w", in.Domain, err)
	}
	for _, m := range out.Items {
		if aws.ToString(m.ApiId) == in.API {
			return nil
		}
		if _, err := p.Client.DeleteApiMapping(ctx, &apigatewayv2.DeleteApiMappingInput{DomainName: &in.Domain, ApiMappingId: m.ApiMappingId}); err != nil {
			return fmt.Errorf("removing an old mapping of %s: %w", in.Domain, err)
		}
	}
	_, err = p.Client.CreateApiMapping(ctx, &apigatewayv2.CreateApiMappingInput{DomainName: &in.Domain, ApiId: &in.API, Stage: aws.String(defaultStage)})
	if err != nil {
		return fmt.Errorf("mapping %s to the http api: %w", in.Domain, err)
	}
	return nil
}

// Read implements engine.Provider.
func (p Domain) Read(ctx context.Context, req engine.Request) (engine.Result, bool, error) {
	out, err := p.Client.GetDomainName(ctx, &apigatewayv2.GetDomainNameInput{DomainName: &req.PhysicalID})
	if isCode(err, "NotFoundException") {
		return engine.Result{}, false, nil
	}
	if err != nil {
		return engine.Result{}, false, fmt.Errorf("reading the domain %s: %w", req.PhysicalID, err)
	}
	if err := ours(req.PhysicalID, out.Tags, req); err != nil {
		return engine.Result{}, false, err
	}
	return domainResult(firstConfig(out.DomainNameConfigurations), req.PhysicalID), true, nil
}

// Update implements engine.Provider.
func (p Domain) Update(ctx context.Context, req engine.Request) (engine.Result, error) {
	var in DomainInputs
	if err := decodeInputs(req.Inputs, &in); err != nil {
		return engine.Result{}, err
	}
	out, err := p.Client.UpdateDomainName(ctx, &apigatewayv2.UpdateDomainNameInput{
		DomainName: &in.Domain,
		DomainNameConfigurations: []types.DomainNameConfiguration{{
			CertificateArn: &in.Certificate, EndpointType: types.EndpointTypeRegional, SecurityPolicy: types.SecurityPolicyTls12,
		}},
	})
	if err != nil {
		return engine.Result{}, fmt.Errorf("updating the domain %s: %w", in.Domain, err)
	}
	if err := p.mapping(ctx, in); err != nil {
		return engine.Result{}, err
	}
	return domainResult(firstConfig(out.DomainNameConfigurations), in.Domain), nil
}

// Delete implements engine.Provider: the mappings first.
func (p Domain) Delete(ctx context.Context, req engine.Request) error {
	out, err := p.Client.GetApiMappings(ctx, &apigatewayv2.GetApiMappingsInput{DomainName: &req.PhysicalID})
	if isCode(err, "NotFoundException") {
		return nil
	}
	if err != nil {
		return fmt.Errorf("reading the mappings of %s: %w", req.PhysicalID, err)
	}
	for _, m := range out.Items {
		if _, err := p.Client.DeleteApiMapping(ctx, &apigatewayv2.DeleteApiMappingInput{DomainName: &req.PhysicalID, ApiMappingId: m.ApiMappingId}); err != nil && !isCode(err, "NotFoundException") {
			return fmt.Errorf("removing a mapping of %s: %w", req.PhysicalID, err)
		}
	}
	_, err = p.Client.DeleteDomainName(ctx, &apigatewayv2.DeleteDomainNameInput{DomainName: &req.PhysicalID})
	if err != nil && !isCode(err, "NotFoundException") {
		return fmt.Errorf("deleting the domain %s: %w", req.PhysicalID, err)
	}
	return nil
}

// Replaces implements engine.Provider: a domain cannot be renamed.
func (Domain) Replaces(prior, desired json.RawMessage) bool {
	var a, b DomainInputs
	_ = json.Unmarshal(prior, &a)
	_ = json.Unmarshal(desired, &b)
	return a.Domain != b.Domain
}
