package cli

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/sts"

	"github.com/kontrolplane/koldstart/internal/awstest"
	"github.com/kontrolplane/koldstart/internal/engine"
	"github.com/kontrolplane/koldstart/internal/state"
)

// TestSamePlan checks a plan is the same as another when it makes the same changes, whatever
// the serial it was made from.
func TestSamePlan(t *testing.T) {
	plan := func(serial int64, memory float64) *engine.Plan {
		return &engine.Plan{App: "orders", Stage: "dev", Serial: serial, Changes: []engine.Change{
			{ID: "function/api", Action: engine.Update, Fields: []string{"memory"}, After: map[string]any{"memory": memory}},
		}}
	}
	tests := []struct {
		name string
		a, b *engine.Plan
		want bool
	}{
		{name: "same", a: plan(1, 256), b: plan(1, 256), want: true},
		{name: "another serial", a: plan(1, 256), b: plan(2, 256), want: true},
		{name: "another value", a: plan(1, 256), b: plan(1, 512)},
		{name: "none shown", a: plan(1, 256)},
		{name: "both nil", want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := samePlan(tt.a, tt.b); got != tt.want {
				t.Errorf("samePlan = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestConsoleEmulator lists, reads, unlocks and removes a stage through the console, from a
// state written to the emulator's state bucket.
func TestConsoleEmulator(t *testing.T) {
	cfg := awstest.Emulator(t)
	t.Setenv("AWS_ENDPOINT_URL", os.Getenv(awstest.EnvEndpoint))
	ctx := context.Background()
	ident, err := sts.NewFromConfig(cfg).GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		t.Fatalf("GetCallerIdentity: %v", err)
	}
	account := aws.ToString(ident.Account)
	sess := newSession(cfg, account)
	b, err := state.Open(ctx, sess.S3, account, awstest.Region)
	if err != nil {
		t.Fatalf("opening the state bucket: %v", err)
	}
	ref := StageRef{Region: awstest.Region, App: awstest.Name("console"), Stage: "dev"}
	st := state.New(ref.App, ref.Stage, ref.Region, account)
	st.DeployedAt, st.DeployedBy = time.Now().UTC(), "tester"
	st.Outputs = map[string]string{"httpApiUrl": "https://example.com"}
	etag, err := b.Write(ctx, st, "")
	if err != nil {
		t.Fatalf("writing the state: %v", err)
	}
	st.Serial++
	if _, err := b.Write(ctx, st, etag); err != nil {
		t.Fatalf("writing the state again: %v", err)
	}
	if err := b.Lock(ctx, ref.App, ref.Stage, state.LockInfo{Who: "ci", Operation: "deploy", Since: time.Now()}); err != nil {
		t.Fatalf("locking: %v", err)
	}

	c := &Console{name: nameShort, opts: &Options{}, sess: sess, backends: map[string]*regionBackend{}}
	stages, err := c.Stages(ctx)
	if err != nil {
		t.Fatalf("Stages: %v", err)
	}
	var found *StageSummary
	for i := range stages {
		if stages[i].StageRef == ref {
			found = &stages[i]
		}
	}
	if found == nil || found.DeployedBy != "tester" || found.Lock == nil || found.Lock.Who != "ci" {
		t.Fatalf("Stages = %+v, want %v deployed by tester and locked by ci", stages, ref)
	}

	got, lock, err := c.Stage(ctx, ref)
	if err != nil || got.Outputs["httpApiUrl"] != "https://example.com" || lock == nil {
		t.Fatalf("Stage = %+v, %v, %v", got, lock, err)
	}
	history, err := c.History(ctx, ref)
	if err != nil || len(history) != 2 {
		t.Fatalf("History = %v, %v; want 2 versions", history, err)
	}
	first, err := c.Version(ctx, ref, history[1].ID)
	if err != nil || first.Serial != 0 {
		t.Fatalf("Version of the first = %+v, %v; want serial 0", first, err)
	}

	if err := c.Unlock(ctx, ref); err != nil {
		t.Fatalf("Unlock: %v", err)
	}
	if _, lock, _ := c.Stage(ctx, ref); lock != nil {
		t.Fatalf("still locked by %v", lock)
	}

	plan, err := c.PlanRemove(ctx, ref)
	if err != nil {
		t.Fatalf("PlanRemove: %v", err)
	}
	_, applied, gone, err := c.Remove(ctx, ref, plan, func(engine.Event) {})
	if err != nil || !applied || !gone {
		t.Fatalf("Remove = applied %v, gone %v, %v", applied, gone, err)
	}
	if got, _, _ := c.Stage(ctx, ref); got != nil {
		t.Fatalf("the state is still there: %+v", got)
	}
}

// TestConsoleSeesNewBucketEmulator checks a stage deployed to a region whose state bucket did
// not exist when the console last looked is listed on the next look, as a refresh of kdst ui
// does it.
func TestConsoleSeesNewBucketEmulator(t *testing.T) {
	cfg := awstest.Emulator(t)
	ctx := context.Background()
	ident, err := sts.NewFromConfig(cfg).GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		t.Fatalf("GetCallerIdentity: %v", err)
	}
	account := aws.ToString(ident.Account)
	region := "ap-southeast-2"
	sess := newSession(cfg, account).in(region)
	c := &Console{name: nameShort, opts: &Options{}, sess: sess, backends: map[string]*regionBackend{}}
	ref := StageRef{Region: region, App: awstest.Name("later"), Stage: "dev"}
	if st, _, err := c.Stage(ctx, ref); err != nil || st != nil {
		t.Skipf("%s has a state bucket already, or cannot be read: %v", region, err)
	}

	b, err := state.Open(ctx, sess.S3, account, region)
	if err != nil {
		t.Fatalf("creating the state bucket of %s: %v", region, err)
	}
	if _, err := b.Write(ctx, state.New(ref.App, ref.Stage, region, account), ""); err != nil {
		t.Fatalf("writing the state: %v", err)
	}
	t.Cleanup(func() { deleteBucket(t, sess.S3, state.BucketName(account, region)) })

	st, _, err := c.Stage(ctx, ref)
	if err != nil || st == nil {
		t.Fatalf("Stage after the bucket was created = %v, %v; want the state", st, err)
	}
}

// deleteBucket deletes a versioned bucket of the emulator, with every version in it, so the
// region it was in has no state bucket again.
func deleteBucket(t *testing.T, client *s3.Client, bucket string) {
	t.Helper()
	ctx := context.Background()
	versions, err := client.ListObjectVersions(ctx, &s3.ListObjectVersionsInput{Bucket: &bucket})
	if err != nil {
		t.Logf("listing %s: %v", bucket, err)
		return
	}
	for _, v := range versions.Versions {
		_, _ = client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: &bucket, Key: v.Key, VersionId: v.VersionId})
	}
	for _, m := range versions.DeleteMarkers {
		_, _ = client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: &bucket, Key: m.Key, VersionId: m.VersionId})
	}
	if _, err := client.DeleteBucket(ctx, &s3.DeleteBucketInput{Bucket: &bucket}); err != nil {
		t.Logf("deleting %s: %v", bucket, err)
	}
}
