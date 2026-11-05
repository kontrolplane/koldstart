package state

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
)

// S3API is the part of the s3 client the backend uses.
type S3API interface {
	HeadBucket(context.Context, *s3.HeadBucketInput, ...func(*s3.Options)) (*s3.HeadBucketOutput, error)
	CreateBucket(context.Context, *s3.CreateBucketInput, ...func(*s3.Options)) (*s3.CreateBucketOutput, error)
	PutBucketVersioning(context.Context, *s3.PutBucketVersioningInput, ...func(*s3.Options)) (*s3.PutBucketVersioningOutput, error)
	PutBucketEncryption(context.Context, *s3.PutBucketEncryptionInput, ...func(*s3.Options)) (*s3.PutBucketEncryptionOutput, error)
	PutPublicAccessBlock(context.Context, *s3.PutPublicAccessBlockInput, ...func(*s3.Options)) (*s3.PutPublicAccessBlockOutput, error)
	PutBucketLifecycleConfiguration(context.Context, *s3.PutBucketLifecycleConfigurationInput, ...func(*s3.Options)) (*s3.PutBucketLifecycleConfigurationOutput, error)
	PutBucketTagging(context.Context, *s3.PutBucketTaggingInput, ...func(*s3.Options)) (*s3.PutBucketTaggingOutput, error)
	GetObject(context.Context, *s3.GetObjectInput, ...func(*s3.Options)) (*s3.GetObjectOutput, error)
	PutObject(context.Context, *s3.PutObjectInput, ...func(*s3.Options)) (*s3.PutObjectOutput, error)
	HeadObject(context.Context, *s3.HeadObjectInput, ...func(*s3.Options)) (*s3.HeadObjectOutput, error)
	DeleteObject(context.Context, *s3.DeleteObjectInput, ...func(*s3.Options)) (*s3.DeleteObjectOutput, error)
	ListObjectVersions(context.Context, *s3.ListObjectVersionsInput, ...func(*s3.Options)) (*s3.ListObjectVersionsOutput, error)
	ListObjectsV2(context.Context, *s3.ListObjectsV2Input, ...func(*s3.Options)) (*s3.ListObjectsV2Output, error)
}

// BucketName is the state bucket of an account and region.
func BucketName(account, region string) string {
	return "koldstart-state-" + account + "-" + region
}

// S3 is a Backend in the state bucket of an account and region. Every call passes the account
// as the expected bucket owner, so a bucket of the same name in another account is refused
// instead of written to.
type S3 struct {
	Client  S3API
	Bucket  string
	Account string
}

// zipRetentionDays is how long a zip is kept: lambda copies the code when it is deployed, so a
// zip is only read again by a deploy of the same hash, which uploads it again when it is gone.
const zipRetentionDays = 90

// keptStateVersions is how many earlier versions of each state the bucket keeps, for history.
const keptStateVersions = 100

// Find returns the backend of the account and region, and whether its bucket exists yet. It
// creates nothing, for plan.
func Find(ctx context.Context, client S3API, account, region string) (*S3, bool, error) {
	b := &S3{Client: client, Bucket: BucketName(account, region), Account: account}
	_, err := client.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: &b.Bucket, ExpectedBucketOwner: &b.Account})
	switch code := errorCode(err); {
	case err == nil:
		return b, true, nil
	case code == "NotFound" || code == "NoSuchBucket":
		return b, false, nil
	case code == "Forbidden" || code == "AccessDenied" || code == "403":
		return nil, false, fmt.Errorf("the state bucket %s exists, but not in account %s, or this role may not use it: koldstart will not write to it", b.Bucket, account)
	default:
		return nil, false, fmt.Errorf("finding the state bucket %s: %w", b.Bucket, err)
	}
}

// Open returns the backend of the account and region, creating its bucket when it does not
// exist yet: versioned, encrypted, with public access blocked and old zips expiring.
func Open(ctx context.Context, client S3API, account, region string) (*S3, error) {
	b, exists, err := Find(ctx, client, account, region)
	if err != nil || exists {
		return b, err
	}
	return b, b.create(ctx, region)
}

func (b *S3) create(ctx context.Context, region string) error {
	in := &s3.CreateBucketInput{Bucket: &b.Bucket}
	if region != "us-east-1" {
		in.CreateBucketConfiguration = &types.CreateBucketConfiguration{LocationConstraint: types.BucketLocationConstraint(region)}
	}
	if _, err := b.Client.CreateBucket(ctx, in); err != nil {
		if code := errorCode(err); code == "BucketAlreadyExists" {
			return fmt.Errorf("the state bucket %s is taken by another account: koldstart will not write to it", b.Bucket)
		} else if code != "BucketAlreadyOwnedByYou" {
			return fmt.Errorf("creating the state bucket %s: %w", b.Bucket, err)
		}
	}
	steps := []struct {
		what string
		do   func() error
	}{
		{"blocking public access", func() error {
			_, err := b.Client.PutPublicAccessBlock(ctx, &s3.PutPublicAccessBlockInput{
				Bucket: &b.Bucket, ExpectedBucketOwner: &b.Account,
				PublicAccessBlockConfiguration: &types.PublicAccessBlockConfiguration{
					BlockPublicAcls: aws.Bool(true), BlockPublicPolicy: aws.Bool(true),
					IgnorePublicAcls: aws.Bool(true), RestrictPublicBuckets: aws.Bool(true),
				},
			})
			return err
		}},
		{"turning on versioning", func() error {
			_, err := b.Client.PutBucketVersioning(ctx, &s3.PutBucketVersioningInput{
				Bucket: &b.Bucket, ExpectedBucketOwner: &b.Account,
				VersioningConfiguration: &types.VersioningConfiguration{Status: types.BucketVersioningStatusEnabled},
			})
			return err
		}},
		{"turning on encryption", func() error {
			_, err := b.Client.PutBucketEncryption(ctx, &s3.PutBucketEncryptionInput{
				Bucket: &b.Bucket, ExpectedBucketOwner: &b.Account,
				ServerSideEncryptionConfiguration: &types.ServerSideEncryptionConfiguration{
					Rules: []types.ServerSideEncryptionRule{{ApplyServerSideEncryptionByDefault: &types.ServerSideEncryptionByDefault{SSEAlgorithm: types.ServerSideEncryptionAes256}}},
				},
			})
			return err
		}},
		{"expiring old zips", func() error {
			_, err := b.Client.PutBucketLifecycleConfiguration(ctx, &s3.PutBucketLifecycleConfigurationInput{
				Bucket: &b.Bucket, ExpectedBucketOwner: &b.Account,
				LifecycleConfiguration: &types.BucketLifecycleConfiguration{Rules: []types.LifecycleRule{
					{
						ID: aws.String("expire-zips"), Status: types.ExpirationStatusEnabled,
						Filter:                      &types.LifecycleRuleFilter{Prefix: aws.String("zips/")},
						Expiration:                  &types.LifecycleExpiration{Days: aws.Int32(zipRetentionDays)},
						NoncurrentVersionExpiration: &types.NoncurrentVersionExpiration{NoncurrentDays: aws.Int32(1)},
					},
					{
						ID: aws.String("keep-state-history"), Status: types.ExpirationStatusEnabled,
						Filter: &types.LifecycleRuleFilter{Prefix: aws.String("")},
						NoncurrentVersionExpiration: &types.NoncurrentVersionExpiration{
							NoncurrentDays: aws.Int32(1), NewerNoncurrentVersions: aws.Int32(keptStateVersions),
						},
					},
				}},
			})
			return err
		}},
		{"tagging", func() error {
			_, err := b.Client.PutBucketTagging(ctx, &s3.PutBucketTaggingInput{
				Bucket: &b.Bucket, ExpectedBucketOwner: &b.Account,
				Tagging: &types.Tagging{TagSet: []types.Tag{{Key: aws.String("koldstart:managed"), Value: aws.String("state")}}},
			})
			return err
		}},
	}
	for _, step := range steps {
		if err := step.do(); err != nil {
			return fmt.Errorf("%s on the state bucket %s: %w", step.what, b.Bucket, err)
		}
	}
	return nil
}

// Read implements Backend.
func (b *S3) Read(ctx context.Context, app, stage string) (*State, string, error) {
	out, err := b.Client.GetObject(ctx, &s3.GetObjectInput{Bucket: &b.Bucket, Key: aws.String(stateKey(app, stage)), ExpectedBucketOwner: &b.Account})
	if isNotFound(err) {
		return nil, "", nil
	}
	if err != nil {
		return nil, "", fmt.Errorf("reading the state of %s/%s: %w", app, stage, err)
	}
	defer func() { _ = out.Body.Close() }()
	data, err := io.ReadAll(out.Body)
	if err != nil {
		return nil, "", fmt.Errorf("reading the state of %s/%s: %w", app, stage, err)
	}
	s, err := Decode(data)
	return s, aws.ToString(out.ETag), err
}

// Write implements Backend.
func (b *S3) Write(ctx context.Context, s *State, etag string) (string, error) {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return "", err
	}
	in := &s3.PutObjectInput{
		Bucket: &b.Bucket, Key: aws.String(stateKey(s.App, s.Stage)), ExpectedBucketOwner: &b.Account,
		Body: bytes.NewReader(data), ContentType: aws.String("application/json"),
	}
	if etag == "" {
		in.IfNoneMatch = aws.String("*")
	} else {
		in.IfMatch = aws.String(etag)
	}
	out, err := b.Client.PutObject(ctx, in)
	if isPreconditionFailed(err) {
		return "", ErrConflict
	}
	if err != nil {
		return "", fmt.Errorf("writing the state of %s/%s: %w", s.App, s.Stage, err)
	}
	return aws.ToString(out.ETag), nil
}

// Delete implements Backend.
func (b *S3) Delete(ctx context.Context, app, stage string) error {
	_, err := b.Client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: &b.Bucket, Key: aws.String(stateKey(app, stage)), ExpectedBucketOwner: &b.Account})
	if err != nil {
		return fmt.Errorf("deleting the state of %s/%s: %w", app, stage, err)
	}
	return nil
}

// Lock implements Backend.
func (b *S3) Lock(ctx context.Context, app, stage string, info LockInfo) error {
	data, err := json.Marshal(info)
	if err != nil {
		return err
	}
	_, err = b.Client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: &b.Bucket, Key: aws.String(lockKey(app, stage)), ExpectedBucketOwner: &b.Account,
		Body: bytes.NewReader(data), ContentType: aws.String("application/json"), IfNoneMatch: aws.String("*"),
	})
	if isPreconditionFailed(err) {
		held, readErr := b.LockInfo(ctx, app, stage)
		if readErr != nil || held == nil {
			return &LockedError{Stage: stage, Info: LockInfo{Who: "another deploy", Where: "unknown"}}
		}
		return &LockedError{Stage: stage, Info: *held}
	}
	if err != nil {
		return fmt.Errorf("locking %s/%s: %w", app, stage, err)
	}
	return nil
}

// Unlock implements Backend.
func (b *S3) Unlock(ctx context.Context, app, stage string) error {
	held, err := b.LockInfo(ctx, app, stage)
	if err != nil {
		return err
	}
	if held == nil {
		return ErrNotLocked
	}
	_, err = b.Client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: &b.Bucket, Key: aws.String(lockKey(app, stage)), ExpectedBucketOwner: &b.Account})
	if err != nil {
		return fmt.Errorf("unlocking %s/%s: %w", app, stage, err)
	}
	return nil
}

// LockInfo implements Backend.
func (b *S3) LockInfo(ctx context.Context, app, stage string) (*LockInfo, error) {
	out, err := b.Client.GetObject(ctx, &s3.GetObjectInput{Bucket: &b.Bucket, Key: aws.String(lockKey(app, stage)), ExpectedBucketOwner: &b.Account})
	if isNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading the lock of %s/%s: %w", app, stage, err)
	}
	defer func() { _ = out.Body.Close() }()
	var info LockInfo
	if err := json.NewDecoder(out.Body).Decode(&info); err != nil {
		return nil, fmt.Errorf("reading the lock of %s/%s: %w", app, stage, err)
	}
	return &info, nil
}

// PutZip implements Backend.
func (b *S3) PutZip(ctx context.Context, app, stage, sha string, data []byte) (string, string, error) {
	key := zipKey(app, stage, sha)
	_, err := b.Client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: &b.Bucket, Key: &key, ExpectedBucketOwner: &b.Account})
	if err == nil {
		return b.Bucket, key, nil
	}
	if !isNotFound(err) {
		return "", "", fmt.Errorf("looking for %s: %w", key, err)
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != sha {
		return "", "", fmt.Errorf("the zip for %s does not have sha256 %s", key, sha)
	}
	_, err = b.Client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: &b.Bucket, Key: &key, ExpectedBucketOwner: &b.Account,
		Body: bytes.NewReader(data), ContentType: aws.String("application/zip"),
		ChecksumSHA256: aws.String(base64.StdEncoding.EncodeToString(sum[:])),
	})
	if err != nil {
		return "", "", fmt.Errorf("uploading %s: %w", key, err)
	}
	return b.Bucket, key, nil
}

// History implements Backend.
func (b *S3) History(ctx context.Context, app, stage string) ([]ObjectVersion, error) {
	key := stateKey(app, stage)
	var out []ObjectVersion
	p := s3.NewListObjectVersionsPaginator(b.Client, &s3.ListObjectVersionsInput{Bucket: &b.Bucket, Prefix: &key, ExpectedBucketOwner: &b.Account})
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("listing the history of %s/%s: %w", app, stage, err)
		}
		for _, v := range page.Versions {
			if aws.ToString(v.Key) != key {
				continue
			}
			out = append(out, ObjectVersion{ID: aws.ToString(v.VersionId), LastModified: aws.ToTime(v.LastModified), Latest: aws.ToBool(v.IsLatest)})
		}
	}
	return out, nil
}

// ReadVersion implements Backend.
func (b *S3) ReadVersion(ctx context.Context, app, stage, version string) (*State, error) {
	out, err := b.Client.GetObject(ctx, &s3.GetObjectInput{Bucket: &b.Bucket, Key: aws.String(stateKey(app, stage)), VersionId: &version, ExpectedBucketOwner: &b.Account})
	if err != nil {
		return nil, fmt.Errorf("reading version %s of the state of %s/%s: %w", version, app, stage, err)
	}
	defer func() { _ = out.Body.Close() }()
	data, err := io.ReadAll(out.Body)
	if err != nil {
		return nil, err
	}
	return Decode(data)
}

// Stages implements Backend.
func (b *S3) Stages(ctx context.Context) ([]string, error) {
	var out []string
	p := s3.NewListObjectsV2Paginator(b.Client, &s3.ListObjectsV2Input{Bucket: &b.Bucket, ExpectedBucketOwner: &b.Account})
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("listing the stages in %s: %w", b.Bucket, err)
		}
		for _, o := range page.Contents {
			key := aws.ToString(o.Key)
			if strings.HasPrefix(key, "zips/") || !strings.HasSuffix(key, "/state.json") {
				continue
			}
			out = append(out, strings.TrimSuffix(key, "/state.json"))
		}
	}
	return out, nil
}

func errorCode(err error) string {
	if api, ok := errors.AsType[smithy.APIError](err); ok {
		return api.ErrorCode()
	}
	return ""
}

func isNotFound(err error) bool {
	if err == nil {
		return false
	}
	var noKey *types.NoSuchKey
	var notFound *types.NotFound
	code := errorCode(err)
	return errors.As(err, &noKey) || errors.As(err, &notFound) || code == "NoSuchKey" || code == "NotFound" || code == "NoSuchBucket"
}

// isPreconditionFailed is a conditional write that lost: the object exists, or changed. A
// conditional write racing another one on the same key gets ConditionalRequestConflict.
func isPreconditionFailed(err error) bool {
	code := errorCode(err)
	return code == "PreconditionFailed" || code == "ConditionalRequestConflict"
}
