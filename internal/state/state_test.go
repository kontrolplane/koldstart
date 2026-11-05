package state

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/sts"

	"github.com/kontrolplane/koldstart/internal/awstest"
)

// testBackend runs what every Backend must do against b, for the app app.
func testBackend(t *testing.T, b Backend, app string) {
	t.Run("read of a stage never deployed", func(t *testing.T) { testNeverDeployed(t, b, app) })
	t.Run("writes are conditional on the etag", func(t *testing.T) { testConditionalWrites(t, b, app) })
	t.Run("lock", func(t *testing.T) { testLock(t, b, app) })
	t.Run("zips", func(t *testing.T) { testZips(t, b, app) })
	t.Run("history, stages and delete", func(t *testing.T) { testHistory(t, b, app) })
}

func testNeverDeployed(t *testing.T, b Backend, app string) {
	ctx := context.Background()
	{
		s, etag, err := b.Read(ctx, app, "dev")
		if err != nil || s != nil || etag != "" {
			t.Fatalf("Read = %v, %q, %v, want nothing", s, etag, err)
		}
	}
}

func testConditionalWrites(t *testing.T, b Backend, app string) {
	ctx := context.Background()
	{
		s := New(app, "dev", "eu-west-1", "000000000000")
		s.Resources["function/api"] = &Resource{Type: "AWS::Lambda::Function", PhysicalID: app + "-dev-api", Hash: "h1"}
		first, err := b.Write(ctx, s, "")
		if err != nil {
			t.Fatalf("first Write: %v", err)
		}
		if _, err := b.Write(ctx, s, ""); !errors.Is(err, ErrConflict) {
			t.Fatalf("a second first Write = %v, want ErrConflict", err)
		}
		s.Serial++
		second, err := b.Write(ctx, s, first)
		if err != nil {
			t.Fatalf("Write with the current etag: %v", err)
		}
		if _, err := b.Write(ctx, s, first); !errors.Is(err, ErrConflict) {
			t.Fatalf("Write with a stale etag = %v, want ErrConflict", err)
		}
		got, etag, err := b.Read(ctx, app, "dev")
		if err != nil || etag != second || got.Serial != 1 || got.Resources["function/api"].PhysicalID != app+"-dev-api" {
			t.Fatalf("Read = %+v, %q, %v", got, etag, err)
		}
	}
}

func testLock(t *testing.T, b Backend, app string) {
	ctx := context.Background()
	{
		info := LockInfo{Who: "levi", Where: "laptop", Operation: "deploy", Since: time.Now().UTC().Truncate(time.Second)}
		if err := b.Lock(ctx, app, "dev", info); err != nil {
			t.Fatalf("Lock: %v", err)
		}
		var locked *LockedError
		if err := b.Lock(ctx, app, "dev", LockInfo{Who: "ci"}); !errors.As(err, &locked) || locked.Info.Who != "levi" {
			t.Fatalf("a second Lock = %v, want a LockedError naming levi", err)
		}
		if err := b.Lock(ctx, app, "prod", info); err != nil {
			t.Fatalf("Lock of another stage: %v", err)
		}
		held, err := b.LockInfo(ctx, app, "dev")
		if err != nil || held == nil || *held != info {
			t.Fatalf("LockInfo = %+v, %v, want %+v", held, err, info)
		}
		if err := b.Unlock(ctx, app, "dev"); err != nil {
			t.Fatalf("Unlock: %v", err)
		}
		if err := b.Unlock(ctx, app, "dev"); !errors.Is(err, ErrNotLocked) {
			t.Fatalf("a second Unlock = %v, want ErrNotLocked", err)
		}
		if err := b.Lock(ctx, app, "dev", info); err != nil {
			t.Fatalf("Lock after Unlock: %v", err)
		}
		_ = b.Unlock(ctx, app, "dev")
		_ = b.Unlock(ctx, app, "prod")
	}
}

func testZips(t *testing.T, b Backend, app string) {
	ctx := context.Background()
	{
		data := []byte("zip")
		sum := sha256Hex(data)
		if err := b.PutZip(ctx, app, "dev", sum, data); err != nil {
			t.Fatalf("PutZip: %v", err)
		}
		if err := b.PutZip(ctx, app, "dev", sum, data); err != nil {
			t.Fatalf("PutZip of a zip already there: %v", err)
		}
	}
}

func testHistory(t *testing.T, b Backend, app string) {
	ctx := context.Background()
	{
		versions, err := b.History(ctx, app, "dev")
		if err != nil || len(versions) != 2 || !versions[0].Latest {
			t.Fatalf("History = %+v, %v, want 2 versions, the newest first", versions, err)
		}
		old, err := b.ReadVersion(ctx, app, "dev", versions[1].ID)
		if err != nil || old.Serial != 0 {
			t.Fatalf("ReadVersion of the first = %+v, %v", old, err)
		}
		stages, err := b.Stages(ctx)
		if err != nil || !slices.Contains(stages, app+"/dev") {
			t.Fatalf("Stages = %v, %v, want %s/dev among them", stages, err, app)
		}
		if err := b.Delete(ctx, app, "dev"); err != nil {
			t.Fatalf("Delete: %v", err)
		}
		if s, _, err := b.Read(ctx, app, "dev"); err != nil || s != nil {
			t.Fatalf("Read after Delete = %v, %v", s, err)
		}
		stages, _ = b.Stages(ctx)
		if slices.Contains(stages, app+"/dev") {
			t.Fatalf("Stages after Delete still lists %s/dev", app)
		}
	}
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// TestMemory runs the backend tests against the memory backend.
func TestMemory(t *testing.T) {
	testBackend(t, NewMemory(), "orders")
}

// TestS3Emulator runs the backend tests against the s3 backend, in a bucket of the emulator.
func TestS3Emulator(t *testing.T) {
	cfg := awstest.Emulator(t)
	ctx := context.Background()
	ident, err := sts.NewFromConfig(cfg).GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		t.Fatalf("GetCallerIdentity: %v", err)
	}
	client := s3.NewFromConfig(cfg, func(o *s3.Options) { o.UsePathStyle = true })
	b, err := Open(ctx, client, *ident.Account, awstest.Region)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := Open(ctx, client, *ident.Account, awstest.Region); err != nil {
		t.Fatalf("Open of a bucket that exists: %v", err)
	}
	testBackend(t, b, awstest.Name("orders"))
}

// TestDecodeNewer checks a state written by a newer koldstart is refused.
func TestDecodeNewer(t *testing.T) {
	if _, err := Decode([]byte(`{"version": 99}`)); err == nil {
		t.Fatal("Decode of version 99: want an error")
	}
	s, err := Decode([]byte(`{"version": 1}`))
	if err != nil || s.Resources == nil {
		t.Fatalf("Decode = %+v, %v", s, err)
	}
}
