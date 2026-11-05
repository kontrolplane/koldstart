package cli

import (
	"encoding/json"
	"testing"

	"github.com/kontrolplane/koldstart/internal/engine"
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
	if r.DeletionPolicy != engine.Retain || !r.Imported || r.Hash != "" || r.Attributes["Arn"] != "arn:table" || r.PhysicalID != "orders-prod" {
		t.Errorf("entry = %+v", r)
	}
	r, _ = importedEntry("AWS::DynamoDB::Table", "orders-prod", "delete", configured, live)
	if r.DeletionPolicy != engine.DeletePolicy {
		t.Errorf("deletionPolicy delete became %q", r.DeletionPolicy)
	}
}
