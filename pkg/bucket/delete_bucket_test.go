package bucket

import (
	"context"
	"fmt"
	"testing"

	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
	"github.com/cloudian/cloudian-cosi-driver/pkg/clients/s3"
	"github.com/stretchr/testify/require"
)

func TestDeleteBucket(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name          string
		expectedError bool
	}{
		{
			name:          "delete_bucket_success",
			expectedError: false,
		},
		{
			name:          "delete_bucket_fail",
			expectedError: true,
		},
	}

	client := s3.ClientMock{}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := DeleteBucket(context.TODO(), client, tc.name)

			if tc.expectedError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

type deletionClient struct {
	calls   int
	failure error
}

func (client *deletionClient) DeleteBucket(_ context.Context, _ *awss3.DeleteBucketInput, _ ...func(*awss3.Options)) (*awss3.DeleteBucketOutput, error) {
	client.calls++
	if client.failure != nil {
		return nil, client.failure
	}

	return &awss3.DeleteBucketOutput{}, nil
}

func TestDeleteBucketMissingAndBlockedResources(t *testing.T) {
	t.Parallel()

	for _, code := range []string{"NoSuchBucket", "NotFound", "BucketNotEmpty", "AccessDenied", "InternalError"} {
		t.Run(code, func(t *testing.T) {
			t.Parallel()

			backendError := &smithy.GenericAPIError{Code: code}
			client := &deletionClient{failure: fmt.Errorf("S3 delete response: %w", backendError)}

			err := DeleteBucket(context.Background(), client, "bucket")
			if code == "NoSuchBucket" || code == "NotFound" {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, backendError)
			}

			require.Equal(t, 1, client.calls)
		})
	}
}

func TestDeleteBucketRepeatedDeletion(t *testing.T) {
	t.Parallel()

	client := &deletionClient{}
	require.NoError(t, DeleteBucket(context.Background(), client, "bucket"))

	client.failure = &smithy.GenericAPIError{Code: "NoSuchBucket"}
	require.NoError(t, DeleteBucket(context.Background(), client, "bucket"))
	require.Equal(t, 2, client.calls)
}
