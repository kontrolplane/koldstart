package state

import (
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
)

// ownerFake is an s3 that records the expected bucket owner of every call, holding nothing.
type ownerFake struct {
	owners     []string
	headBucket error
	create     error
}

func (f *ownerFake) record(in any) {
	owner := reflect.ValueOf(in).Elem().FieldByName("ExpectedBucketOwner")
	if owner.IsValid() && !owner.IsNil() {
		f.owners = append(f.owners, owner.Elem().String())
	} else {
		f.owners = append(f.owners, "")
	}
}

func (f *ownerFake) HeadBucket(_ context.Context, in *s3.HeadBucketInput, _ ...func(*s3.Options)) (*s3.HeadBucketOutput, error) {
	f.record(in)
	return &s3.HeadBucketOutput{}, f.headBucket
}

func (f *ownerFake) CreateBucket(context.Context, *s3.CreateBucketInput, ...func(*s3.Options)) (*s3.CreateBucketOutput, error) {
	return &s3.CreateBucketOutput{}, f.create
}

func (f *ownerFake) PutBucketVersioning(_ context.Context, in *s3.PutBucketVersioningInput, _ ...func(*s3.Options)) (*s3.PutBucketVersioningOutput, error) {
	f.record(in)
	return &s3.PutBucketVersioningOutput{}, nil
}

func (f *ownerFake) PutBucketEncryption(_ context.Context, in *s3.PutBucketEncryptionInput, _ ...func(*s3.Options)) (*s3.PutBucketEncryptionOutput, error) {
	f.record(in)
	return &s3.PutBucketEncryptionOutput{}, nil
}

func (f *ownerFake) PutPublicAccessBlock(_ context.Context, in *s3.PutPublicAccessBlockInput, _ ...func(*s3.Options)) (*s3.PutPublicAccessBlockOutput, error) {
	f.record(in)
	return &s3.PutPublicAccessBlockOutput{}, nil
}

func (f *ownerFake) PutBucketLifecycleConfiguration(_ context.Context, in *s3.PutBucketLifecycleConfigurationInput, _ ...func(*s3.Options)) (*s3.PutBucketLifecycleConfigurationOutput, error) {
	f.record(in)
	return &s3.PutBucketLifecycleConfigurationOutput{}, nil
}

func (f *ownerFake) PutBucketTagging(_ context.Context, in *s3.PutBucketTaggingInput, _ ...func(*s3.Options)) (*s3.PutBucketTaggingOutput, error) {
	f.record(in)
	return &s3.PutBucketTaggingOutput{}, nil
}

func (f *ownerFake) GetObject(_ context.Context, in *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	f.record(in)
	if strings.HasSuffix(aws.ToString(in.Key), "/lock") {
		return &s3.GetObjectOutput{Body: io.NopCloser(strings.NewReader(`{"who": "ci"}`))}, nil
	}
	return nil, &types.NoSuchKey{}
}

func (f *ownerFake) PutObject(_ context.Context, in *s3.PutObjectInput, _ ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	f.record(in)
	if in.IfNoneMatch != nil && strings.HasSuffix(aws.ToString(in.Key), "/lock") {
		return nil, &smithy.GenericAPIError{Code: "PreconditionFailed"}
	}
	return &s3.PutObjectOutput{ETag: aws.String(`"1"`)}, nil
}

func (f *ownerFake) HeadObject(_ context.Context, in *s3.HeadObjectInput, _ ...func(*s3.Options)) (*s3.HeadObjectOutput, error) {
	f.record(in)
	return nil, &types.NotFound{}
}

func (f *ownerFake) DeleteObject(_ context.Context, in *s3.DeleteObjectInput, _ ...func(*s3.Options)) (*s3.DeleteObjectOutput, error) {
	f.record(in)
	return &s3.DeleteObjectOutput{}, nil
}

func (f *ownerFake) ListObjectVersions(_ context.Context, in *s3.ListObjectVersionsInput, _ ...func(*s3.Options)) (*s3.ListObjectVersionsOutput, error) {
	f.record(in)
	return &s3.ListObjectVersionsOutput{}, nil
}

func (f *ownerFake) ListObjectsV2(_ context.Context, in *s3.ListObjectsV2Input, _ ...func(*s3.Options)) (*s3.ListObjectsV2Output, error) {
	f.record(in)
	return &s3.ListObjectsV2Output{}, nil
}

// TestS3ExpectedOwner checks every call to the bucket names the account as its owner, so a
// bucket of the same name in another account is never read or written.
func TestS3ExpectedOwner(t *testing.T) {
	ctx := context.Background()
	fake := &ownerFake{headBucket: &smithy.GenericAPIError{Code: "NotFound"}}
	b, err := Open(ctx, fake, "123456789012", "eu-west-1")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	_, _, _ = b.Read(ctx, "orders", "dev")
	_, _ = b.Write(ctx, New("orders", "dev", "eu-west-1", "123456789012"), "")
	var locked *LockedError
	if err := b.Lock(ctx, "orders", "dev", LockInfo{}); !errors.As(err, &locked) || locked.Info.Who != "ci" {
		t.Errorf("Lock of a held lock = %v, want a LockedError naming ci", err)
	}
	_ = b.Unlock(ctx, "orders", "dev")
	_ = b.PutZip(ctx, "orders", "dev", sha256Hex([]byte("z")), []byte("z"))
	_, _ = b.History(ctx, "orders", "dev")
	_, _ = b.Stages(ctx)
	_ = b.Delete(ctx, "orders", "dev")

	if len(fake.owners) < 15 {
		t.Fatalf("only %d calls recorded", len(fake.owners))
	}
	for i, owner := range fake.owners {
		if owner != "123456789012" {
			t.Errorf("call %d has expected owner %q, want 123456789012", i, owner)
		}
	}
}

// TestOpenRefuses checks a bucket of another account, or one taken, is refused.
func TestOpenRefuses(t *testing.T) {
	tests := []struct {
		name string
		fake *ownerFake
		want string
	}{
		{name: "another owner", fake: &ownerFake{headBucket: &smithy.GenericAPIError{Code: "Forbidden"}}, want: "not in account"},
		{name: "taken", fake: &ownerFake{headBucket: &smithy.GenericAPIError{Code: "NotFound"}, create: &smithy.GenericAPIError{Code: "BucketAlreadyExists"}}, want: "taken by another account"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Open(context.Background(), tt.fake, "123456789012", "eu-west-1")
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("Open = %v, want an error with %q", err, tt.want)
			}
		})
	}
}

// TestPutZipChecksHash checks a zip is not stored under a hash it does not have.
func TestPutZipChecksHash(t *testing.T) {
	b := &S3{Client: &ownerFake{}, Bucket: "b", Account: "1"}
	if err := b.PutZip(context.Background(), "orders", "dev", sha256Hex([]byte("a")), []byte("b")); err == nil {
		t.Fatal("PutZip under the wrong hash: want an error")
	}
}
