package iam

import (
	"context"
	"testing"

	"github.com/cloudian/cloudian-cosi-driver/pkg/clients/iam"
	"github.com/stretchr/testify/require"
)

func TestGrantBucketAccess(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name          string
		expectedError bool
	}{
		{
			name:          "grant_bucket_access_success",
			expectedError: false,
		},
		{
			name:          "grant_bucket_access_existing_user",
			expectedError: false,
		},
		{
			name:          "grant_bucket_access_create_user_fail",
			expectedError: true,
		},
		{
			name:          "grant_bucket_access_create_policy_fail",
			expectedError: true,
		},
		{
			name:          "grant_bucket_access_create_access_keys_fail",
			expectedError: true,
		},
	}

	client := iam.ClientMock{}
	bucketActions := []string{"s3:ListBucket"}
	objectActions := []string{"s3:GetObject", "s3:GetObjectVersion", "s3:PutObject", "s3:DeleteObject", "s3:DeleteObjectVersion"}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			accessKey, secretKey, err := GrantBucketAccess(context.TODO(), client, tc.name, "mockBucketID", bucketActions, objectActions)

			if tc.expectedError {
				require.ErrorContains(t, err, tc.name)
			} else {
				require.NoError(t, err)
				require.Equal(t, accessKey, "mockAccessKey")
				require.Equal(t, secretKey, "mockSecretKey")
			}
		})
	}
}
