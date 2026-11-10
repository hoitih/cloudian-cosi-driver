package bucket

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
	klog "k8s.io/klog/v2"
)

type BucketCreator interface { //nolint:revive
	CreateBucket(ctx context.Context, params *s3.CreateBucketInput, optFns ...func(*s3.Options)) (*s3.CreateBucketOutput, error)
	PutBucketVersioning(ctx context.Context, params *s3.PutBucketVersioningInput, optFns ...func(*s3.Options)) (*s3.PutBucketVersioningOutput, error)
	PutObjectLockConfiguration(ctx context.Context, params *s3.PutObjectLockConfigurationInput, optFns ...func(*s3.Options)) (*s3.PutObjectLockConfigurationOutput, error)
	PutBucketLifecycleConfiguration(ctx context.Context, params *s3.PutBucketLifecycleConfigurationInput, optFns ...func(*s3.Options)) (*s3.PutBucketLifecycleConfigurationOutput, error)
}

type BucketChecker interface { //nolint:revive
	HeadBucket(ctx context.Context, params *s3.HeadBucketInput, optFns ...func(*s3.Options)) (*s3.HeadBucketOutput, error)
}

// Create bucket via S3 API
func CreateBucket(ctx context.Context, client BucketCreator, name string, configuration Configuration) error {
	params := &s3.CreateBucketInput{
		Bucket: &name,
	}
	if configuration.ObjectLock {
		params.ObjectLockEnabledForBucket = &configuration.ObjectLock
	}

	options := func(o *s3.Options) {
		o.HTTPClient = &http.Client{
			Transport: roundTripper{http.DefaultTransport, configuration.StoragePolicyID},
		}
	}

	if _, err := client.CreateBucket(ctx, params, options); err != nil {
		var apiErr smithy.APIError
		if !errors.As(err, &apiErr) || apiErr.ErrorCode() != "BucketAlreadyOwnedByYou" {
			return fmt.Errorf("failed to create bucket %s: %w", name, err)
		}
	}

	if err := configureBucket(ctx, client, name, configuration); err != nil {
		return fmt.Errorf("failed to configure bucket %s: %w", name, err)
	}

	klog.Infof("Bucket %s created", name)

	return nil
}

func configureBucket(ctx context.Context, client BucketCreator, name string, configuration Configuration) error {
	if configuration.Versioning != "" {
		_, err := client.PutBucketVersioning(ctx, &s3.PutBucketVersioningInput{
			Bucket: &name,
			VersioningConfiguration: &types.VersioningConfiguration{
				Status: configuration.Versioning,
			},
		})
		if err != nil {
			return fmt.Errorf("failed to set versioning: %w", err)
		}
	}

	if configuration.ObjectLock {
		lock := &types.ObjectLockConfiguration{ObjectLockEnabled: types.ObjectLockEnabledEnabled}
		if configuration.Retention != nil {
			lock.Rule = &types.ObjectLockRule{DefaultRetention: configuration.Retention}
		}

		_, err := client.PutObjectLockConfiguration(ctx, &s3.PutObjectLockConfigurationInput{
			Bucket:                  &name,
			ObjectLockConfiguration: lock,
		})
		if err != nil {
			return fmt.Errorf("failed to set Object Lock retention: %w", err)
		}
	}

	if configuration.Lifecycle != nil {
		_, err := client.PutBucketLifecycleConfiguration(ctx, &s3.PutBucketLifecycleConfigurationInput{
			Bucket:                 &name,
			LifecycleConfiguration: configuration.Lifecycle,
		})
		if err != nil {
			return fmt.Errorf("failed to set lifecycle policy: %w", err)
		}
	}

	return nil
}

func CheckBucket(ctx context.Context, client BucketChecker, name string) error {
	if _, err := client.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: &name}); err != nil {
		return fmt.Errorf("failed to check bucket %s: %w", name, err)
	}

	return nil
}

type roundTripper struct {
	rt              http.RoundTripper
	storagePolicyID string
}

func (r roundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	req.Header.Set("x-gmt-policyid", r.storagePolicyID) //nolint:canonicalheader

	return r.rt.RoundTrip(req) //nolint:wrapcheck
}
