package cloudcontrol_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	awscloudcontrol "github.com/aws/aws-sdk-go-v2/service/cloudcontrol"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"

	"github.com/kontrolplane/koldstart/internal/awstest"
	"github.com/kontrolplane/koldstart/internal/engine"
	"github.com/kontrolplane/koldstart/internal/provider/cloudcontrol"
)

// TestCloudControlEmulator creates and deletes a queue through the emulator's cloud control, which
// provisions it for real and names it by its url, as aws does. The emulator reads a resource back
// as it was created, without what aws sets, and has no UpdateResource: those are tested against
// the fake.
func TestCloudControlEmulator(t *testing.T) {
	cfg := awstest.Emulator(t)
	ctx := context.Background()
	p := cloudcontrol.Provider{Client: awscloudcontrol.NewFromConfig(cfg), Poll: 200e6}
	name := awstest.Name("jobs")
	inputs, _ := json.Marshal(cloudcontrol.Inputs{Type: "AWS::SQS::Queue", Properties: map[string]any{"QueueName": name, "VisibilityTimeout": 45}})
	res, err := p.Create(ctx, engine.Request{ID: "resource/jobs", Inputs: inputs, ClientToken: awstest.Name("koldstart")})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if !strings.HasSuffix(res.PhysicalID, "/"+name) || res.Attributes["VisibilityTimeout"] != "45" {
		t.Fatalf("Create = %+v, want the queue's url", res)
	}
	queues := sqs.NewFromConfig(cfg)
	attrs, err := queues.GetQueueAttributes(ctx, &sqs.GetQueueAttributesInput{QueueUrl: &res.PhysicalID, AttributeNames: []sqstypes.QueueAttributeName{sqstypes.QueueAttributeNameVisibilityTimeout}})
	if err != nil || attrs.Attributes["VisibilityTimeout"] != "45" {
		t.Fatalf("the queue in sqs = %+v, %v", attrs, err)
	}
	if _, ok, err := p.Read(ctx, engine.Request{ID: "resource/jobs", PhysicalID: res.PhysicalID, Inputs: inputs}); err != nil || !ok {
		t.Fatalf("Read = %v, %v", ok, err)
	}
	if err := p.Delete(ctx, engine.Request{ID: "resource/jobs", PhysicalID: res.PhysicalID, Prior: inputs}); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, ok, err := p.Read(ctx, engine.Request{ID: "resource/jobs", PhysicalID: res.PhysicalID, Inputs: inputs}); ok || err != nil {
		t.Errorf("Read after Delete = %v, %v", ok, err)
	}
}
