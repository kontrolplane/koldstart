package cloudcontrol_test

import (
	"context"
	"encoding/json"
	"testing"

	awscloudcontrol "github.com/aws/aws-sdk-go-v2/service/cloudcontrol"

	"github.com/kontrolplane/koldstart/internal/awstest"
	"github.com/kontrolplane/koldstart/internal/engine"
	"github.com/kontrolplane/koldstart/internal/provider/cloudcontrol"
)

// TestCloudControlEmulator creates, updates and deletes a queue through the emulator's cloud
// control. The emulator keeps resources as the properties they were given, without what aws
// sets, so this checks the requests and their waiting, not the resources.
func TestCloudControlEmulator(t *testing.T) {
	cfg := awstest.Emulator(t)
	ctx := context.Background()
	p := cloudcontrol.Provider{Client: awscloudcontrol.NewFromConfig(cfg), Poll: 200e6}
	inputs := func(visibility int) json.RawMessage {
		data, _ := json.Marshal(cloudcontrol.Inputs{Type: "AWS::SQS::Queue", Properties: map[string]any{"QueueName": awstest.Name("jobs"), "VisibilityTimeout": visibility}})
		return data
	}
	first := inputs(45)
	res, err := p.Create(ctx, engine.Request{ID: "resource/jobs", Inputs: first, ClientToken: awstest.Name("koldstart")})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if res.PhysicalID == "" || res.Attributes["VisibilityTimeout"] != "45" {
		t.Fatalf("Create = %+v", res)
	}
	var props cloudcontrol.Inputs
	_ = json.Unmarshal(first, &props)
	second, _ := json.Marshal(cloudcontrol.Inputs{Type: "AWS::SQS::Queue", Properties: map[string]any{"QueueName": props.Properties["QueueName"], "VisibilityTimeout": 60}})
	res, err = p.Update(ctx, engine.Request{ID: "resource/jobs", PhysicalID: res.PhysicalID, Prior: first, Inputs: second})
	if err != nil || res.Attributes["VisibilityTimeout"] != "60" {
		t.Fatalf("Update = %+v, %v", res, err)
	}
	if err := p.Delete(ctx, engine.Request{ID: "resource/jobs", PhysicalID: res.PhysicalID, Prior: second}); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, ok, err := p.Read(ctx, engine.Request{ID: "resource/jobs", PhysicalID: res.PhysicalID, Inputs: second}); ok || err != nil {
		t.Errorf("Read after Delete = %v, %v", ok, err)
	}
}
