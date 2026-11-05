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
	if awserr.Is(err, "ConflictException") {
		return engine.Result{}, engine.ErrExists
	}
	if err != nil {
		return engine.Result{}, fmt.Errorf("creating the domain %s: %w", in.Domain, err)
	}
	if err := p.mapToAPI(ctx, in); err != nil {
		return engine.Result{}, err
	}
	return domainResult(firstConfig(out.DomainNameConfigurations), in.Domain), nil
}

// mapToAPI points the domain at the api's $default stage, replacing a mapping to another api.
func (p Domain) mapToAPI(ctx context.Context, in DomainInputs) error {
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
	if awserr.Is(err, "NotFoundException") {
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
	if err := p.mapToAPI(ctx, in); err != nil {
		return engine.Result{}, err
	}
	return domainResult(firstConfig(out.DomainNameConfigurations), in.Domain), nil
}

// Delete implements engine.Provider: the mappings first.
func (p Domain) Delete(ctx context.Context, req engine.Request) error {
	out, err := p.Client.GetApiMappings(ctx, &apigatewayv2.GetApiMappingsInput{DomainName: &req.PhysicalID})
	if awserr.Is(err, "NotFoundException") {
		return nil
	}
	if err != nil {
		return fmt.Errorf("reading the mappings of %s: %w", req.PhysicalID, err)
	}
	for _, m := range out.Items {
		if _, err := p.Client.DeleteApiMapping(ctx, &apigatewayv2.DeleteApiMappingInput{DomainName: &req.PhysicalID, ApiMappingId: m.ApiMappingId}); err != nil && !awserr.Is(err, "NotFoundException") {
			return fmt.Errorf("removing a mapping of %s: %w", req.PhysicalID, err)
		}
	}
	_, err = p.Client.DeleteDomainName(ctx, &apigatewayv2.DeleteDomainNameInput{DomainName: &req.PhysicalID})
	if err != nil && !awserr.Is(err, "NotFoundException") {
		return fmt.Errorf("deleting the domain %s: %w", req.PhysicalID, err)
	}
	return nil
}

// Replaces implements engine.Provider: a domain cannot be renamed.
func (Domain) Replaces(prior, desired json.RawMessage) bool {
	a, b := decodeBoth[DomainInputs](prior, desired)
	return a.Domain != b.Domain
}
