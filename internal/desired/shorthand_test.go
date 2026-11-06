package desired

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/kontrolplane/koldstart/internal/build"
	"github.com/kontrolplane/koldstart/internal/config"
	"github.com/kontrolplane/koldstart/internal/engine"
	"github.com/kontrolplane/koldstart/internal/pkl"
)

// shorthands is a project of kdst.pkl's resource shorthands, which its function links, refers to
// and takes events from by name.
const shorthands = `
app = "shop"

functions {
  ["api"] {
    runtime = "nodejs22.x"
    handler = "src/api.handler"
    environment { ["TABLE"] = kdst.ref("orders", "TableName") }
    link {
      ["orders"] = "read"
      ["jobs"] = "write"
      ["uploads"] = "readwrite"
      ["alerts"] = "write"
    }
    events {
      kdst.fromQueue("jobs")
      kdst.fromTableStream("orders")
      kdst.fromTopic("alerts")
      kdst.fromBucket("uploads")
    }
  }
}

resources {
  ["orders"] = (kdst.table("id")) { properties { StreamSpecification { StreamViewType = "NEW_IMAGE" } } }
  ["jobs"] = kdst.queue()
  ["uploads"] = kdst.bucket()
  ["alerts"] = kdst.topic()
  ["retries"] = (kdst.queue()) { properties { RedrivePolicy { deadLetterTargetArn = kdst.ref("jobs", "Arn"); maxReceiveCount = 3 } } }
}
`

// loadShorthands evaluates shorthands against the schema in pkl/ with the pkl koldstart would
// use. Outside ci it does not download one: without pkl installed, the test skips.
func loadShorthands(t *testing.T) *config.Project {
	t.Helper()
	if os.Getenv("CI") == "" && os.Getenv(pkl.EnvBinary) == "" {
		if _, err := exec.LookPath("pkl"); err != nil {
			t.Skip("no pkl on the PATH")
		}
	}
	binary, err := pkl.Find(context.Background())
	if err != nil {
		t.Fatalf("finding pkl: %v", err)
	}
	schema, err := filepath.Abs(filepath.Join("..", "..", "pkl"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "koldstart.pkl")
	body := "amends \"" + filepath.Join(schema, "Project.pkl") + "\"\n\nimport \"" + filepath.Join(schema, "kdst.pkl") + "\"\n" + shorthands
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	p, err := config.Load(context.Background(), pkl.Evaluator{Binary: binary}, path, "dev")
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// TestShorthands checks link, refs and events by name work on the resources kdst.table(),
// kdst.queue(), kdst.bucket() and kdst.topic() give as on ones written out: the variables and
// policy of link by type, refs turned into node refs, and the sources of the events.
func TestShorthands(t *testing.T) {
	p := loadShorthands(t)
	nodes, err := Build(p, Target{Account: "123456789012", Region: "eu-west-1", Bucket: "b", Archives: map[string]build.Archive{"api": {SHA256: "aaa"}}})
	if err != nil {
		t.Fatal(err)
	}
	got := byID(nodes)
	env, _ := got[FunctionID("api")].Inputs["environment"].(map[string]any)
	policy := js(t, got[RoleID("api")].Inputs["policy"])
	tests := []struct {
		name string
		got  any
		want any
	}{
		{name: "a ref of the table", got: env["TABLE"], want: engine.Ref("resource/orders", "TableName")},
		{name: "the table's name", got: env["ORDERS_NAME"], want: engine.Ref("resource/orders", "TableName")},
		{name: "the table's arn", got: env["ORDERS_ARN"], want: engine.Ref("resource/orders", "Arn")},
		{name: "the queue's url", got: env["JOBS_URL"], want: engine.Ref("resource/jobs", "QueueUrl")},
		{name: "the bucket's name", got: env["UPLOADS_NAME"], want: engine.Ref("resource/uploads", "BucketName")},
		{name: "the topic's arn", got: env["ALERTS_ARN"], want: engine.Ref("resource/alerts", "TopicArn")},
		{name: "the queue of the mapping", got: got["mapping/api/jobs"].Inputs["source"], want: engine.Ref("resource/jobs", "Arn")},
		{name: "the stream of the mapping", got: got["mapping/api/orders"].Inputs["source"], want: engine.Ref("resource/orders", "StreamArn")},
		{name: "the topic of the subscription", got: got["subscription/api/alerts"].Inputs["topic"], want: engine.Ref("resource/alerts", "TopicArn")},
		{name: "the bucket of the notification", got: got["notification/uploads"].Inputs["bucket"], want: engine.Ref("resource/uploads", "BucketName")},
		{
			name: "a ref inside a shorthand's properties",
			got:  got["resource/retries"].Inputs["properties"].(map[string]any)["RedrivePolicy"].(map[string]any)["deadLetterTargetArn"],
			want: engine.Ref("resource/jobs", "Arn"),
		},
	}
	for _, tt := range tests {
		if js(t, tt.got) != js(t, tt.want) {
			t.Errorf("%s = %s, want %s", tt.name, js(t, tt.got), js(t, tt.want))
		}
	}
	for _, want := range []string{`"dynamodb:GetItem"`, `"sqs:SendMessage"`, `"s3:PutObject"`, `"sns:Publish"`} {
		if !strings.Contains(policy, want) {
			t.Errorf("the api's policy lacks %s: %s", want, policy)
		}
	}
	if strings.Contains(policy, `"dynamodb:PutItem"`) {
		t.Errorf("a read link to the table grants writes: %s", policy)
	}
	if deps := got["resource/retries"].Deps(); !slices.Contains(deps, "resource/jobs") {
		t.Errorf("the queue that refers to jobs does not wait for it: %v", deps)
	}
	if got["resource/orders"].DeletionPolicy != engine.PolicyRetain {
		t.Errorf("the table's deletion policy = %q, want it retained", got["resource/orders"].DeletionPolicy)
	}
}
