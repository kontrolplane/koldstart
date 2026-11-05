package awsp

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/apigatewayv2"
	"github.com/aws/aws-sdk-go-v2/service/apigatewayv2/types"

	"github.com/kontrolplane/koldstart/internal/awserr"
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
	if awserr.Is(err, "NotFoundException") {
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
	if err != nil && !awserr.Is(err, "NotFoundException") {
		return fmt.Errorf("deleting the authorizer %s: %w", in.Name, err)
	}
	return nil
}

// Replaces implements engine.Provider: the type of an authorizer cannot change.
func (Authorizer) Replaces(prior, desired json.RawMessage) bool {
	a, b := decodeBoth[AuthorizerInputs](prior, desired)
	return a.API != b.API || a.Type != b.Type
}
