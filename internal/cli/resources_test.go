package cli

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudformation"
	cftypes "github.com/aws/aws-sdk-go-v2/service/cloudformation/types"

	"github.com/kontrolplane/koldstart/internal/config"
	"github.com/kontrolplane/koldstart/internal/engine"
	"github.com/kontrolplane/koldstart/internal/provider/cloudcontrol"
)

// TestImportedEntry checks an import records the live values of the keys the configuration sets,
// retains the resource unless it says delete, and leaves the hash empty for the next deploy.
func TestImportedEntry(t *testing.T) {
	live := map[string]any{"TableName": "orders-prod", "BillingMode": "PROVISIONED", "Arn": "arn:table", "StreamSpecification": map[string]any{"StreamViewType": "NEW_IMAGE"}}
	configured := map[string]any{"TableName": "x", "BillingMode": "PAY_PER_REQUEST", "SSESpecification": map[string]any{}}
	r, err := importedEntry("AWS::DynamoDB::Table", "orders-prod", "", configured, live)
	if err != nil {
		t.Fatal(err)
	}
	var in struct {
		Properties map[string]any `json:"properties"`
	}
	_ = json.Unmarshal(r.Inputs, &in)
	if len(in.Properties) != 2 || in.Properties["BillingMode"] != "PROVISIONED" {
		t.Errorf("inputs = %s, want the live TableName and BillingMode alone", r.Inputs)
	}
	if r.DeletionPolicy != engine.PolicyRetain || !r.Imported || r.Hash != "" || r.Attributes["Arn"] != "arn:table" || r.PhysicalID != "orders-prod" {
		t.Errorf("entry = %+v", r)
	}
	r, _ = importedEntry("AWS::DynamoDB::Table", "orders-prod", "delete", configured, live)
	if r.DeletionPolicy != engine.PolicyDelete {
		t.Errorf("deletionPolicy delete became %q", r.DeletionPolicy)
	}
}

// TestPropertyProblems checks the problems of a resource are listed one a line, under it.
func TestPropertyProblems(t *testing.T) {
	err := propertyProblems("orders", "AWS::DynamoDB::Table", []cloudcontrol.Problem{
		{Path: "BillingMod", Message: "no such property: did you mean BillingMode?"},
		{Path: "KeySchema", Message: "required, and not set"},
		{Message: "and 3 more"},
	})
	want := `resources[orders] does not fit the schema of AWS::DynamoDB::Table:
  properties.BillingMod: no such property: did you mean BillingMode?
  properties.KeySchema: required, and not set
  and 3 more`
	if err.Error() != want {
		t.Errorf("got:\n%s\nwant:\n%s", err, want)
	}
}

// fakeRegistry answers DescribeType with a schema, or with err.
type fakeRegistry struct {
	schema string
	err    error
}

func (f fakeRegistry) DescribeType(context.Context, *cloudformation.DescribeTypeInput, ...func(*cloudformation.Options)) (*cloudformation.DescribeTypeOutput, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &cloudformation.DescribeTypeOutput{Schema: aws.String(f.schema), ProvisioningType: cftypes.ProvisioningTypeFullyMutable}, nil
}

// TestCheckResources checks a plan is refused with what the schema does not allow, and goes on
// when the registry cannot be read.
func TestCheckResources(t *testing.T) {
	schema := `{"typeName": "AWS::SQS::Queue", "additionalProperties": false,
		"properties": {"VisibilityTimeout": {"type": "integer"}, "QueueName": {"type": "string"}, "Arn": {"type": "string"}},
		"readOnlyProperties": ["/properties/Arn"]}`
	tests := []struct {
		name  string
		props map[string]any
		err   error
		want  string
	}{
		{name: "fits", props: map[string]any{"VisibilityTimeout": 60.0}},
		{name: "a ref", props: map[string]any{"QueueName": map[string]any{"$koldstart": "ref", "node": "resource/x", "attribute": "Name"}}},
		{name: "does not fit", props: map[string]any{"VisibiltyTimeout": 60.0}, want: "properties.VisibiltyTimeout: no such property: did you mean VisibilityTimeout?"},
		{name: "no registry", props: map[string]any{"VisibiltyTimeout": 60.0}, err: errors.New("no DescribeType here")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &stage{
				project: &project{Project: &config.Project{Resources: map[string]config.Resource{"jobs": {Type: "AWS::SQS::Queue", Properties: tt.props}}}},
				sess:    &session{Schemas: &cloudcontrol.Registry{Client: fakeRegistry{schema: schema, err: tt.err}}},
			}
			err := s.checkResources(context.Background())
			switch {
			case tt.want == "" && err != nil:
				t.Fatalf("checkResources: %v", err)
			case tt.want != "" && (err == nil || !strings.Contains(err.Error(), tt.want)):
				t.Fatalf("checkResources = %v, want it to say %q", err, tt.want)
			}
		})
	}
}
