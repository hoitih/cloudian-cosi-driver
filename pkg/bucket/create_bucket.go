package bucket

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
	klog "k8s.io/klog/v2"
)

type BucketCreator interface { //nolint:revive
	CreateBucket(ctx context.Context, params *s3.CreateBucketInput, optFns ...func(*s3.Options)) (*s3.CreateBucketOutput, error)
}

type BucketChecker interface { //nolint:revive
	HeadBucket(ctx context.Context, params *s3.HeadBucketInput, optFns ...func(*s3.Options)) (*s3.HeadBucketOutput, error)
}

// Create bucket via S3 API
func CreateBucket(ctx context.Context, client BucketCreator, name, storagePolicyID string) error {
	params := &s3.CreateBucketInput{
		Bucket: &name,
	}

	options := func(o *s3.Options) {
		o.HTTPClient = &http.Client{
			Transport: roundTripper{http.DefaultTransport, storagePolicyID},
		}
	}

	if _, err := client.CreateBucket(ctx, params, options); err != nil {
		var apiErr smithy.APIError
		if errors.As(err, &apiErr) && apiErr.ErrorCode() == "BucketAlreadyOwnedByYou" {
			return nil
		}

		return fmt.Errorf("failed to create bucket %s: %w", name, err)
	}

	klog.Infof("Bucket %s created", name)

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
