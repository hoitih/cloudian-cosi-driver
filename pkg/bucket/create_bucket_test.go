package bucket

import (
	"context"
	"errors"
	"fmt"
	"testing"

	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
	"github.com/cloudian/cloudian-cosi-driver/pkg/clients/s3"
	"github.com/stretchr/testify/require"
)

func TestCreateBucket(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name          string
		expectedError bool
	}{
		{
			name:          "create_bucket_success",
			expectedError: false,
		},
		{
			name:          "create_bucket_fail",
			expectedError: true,
		},
	}

	client := s3.ClientMock{}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := CreateBucket(context.TODO(), client, tc.name, Configuration{StoragePolicyID: "storagePolicyID"})

			if tc.expectedError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

type configurationClient struct {
	s3.ClientMock
	calls      []string
	failAt     string
	failure    error
	create     *awss3.CreateBucketInput
	versioning *awss3.PutBucketVersioningInput
	lock       *awss3.PutObjectLockConfigurationInput
	lifecycle  *awss3.PutBucketLifecycleConfigurationInput
}

func (client *configurationClient) record(operation string) error {
	client.calls = append(client.calls, operation)
	if client.failAt == operation {
		if client.failure != nil {
			return client.failure
		}

		return errors.New("backend rejected configuration")
	}

	return nil
}

func (client *configurationClient) CreateBucket(
	ctx context.Context, input *awss3.CreateBucketInput, options ...func(*awss3.Options),
) (*awss3.CreateBucketOutput, error) {
	client.create = input
	if err := client.record("create"); err != nil {
		return nil, err
	}

	output, err := client.ClientMock.CreateBucket(ctx, input, options...)
	if err != nil {
		return nil, fmt.Errorf("mock create bucket: %w", err)
	}

	return output, nil
}

func (client *configurationClient) PutBucketVersioning(
	_ context.Context, input *awss3.PutBucketVersioningInput, _ ...func(*awss3.Options),
) (*awss3.PutBucketVersioningOutput, error) {
	client.versioning = input

	return &awss3.PutBucketVersioningOutput{}, client.record("versioning")
}

func (client *configurationClient) PutObjectLockConfiguration(
	_ context.Context, input *awss3.PutObjectLockConfigurationInput, _ ...func(*awss3.Options),
) (*awss3.PutObjectLockConfigurationOutput, error) {
	client.lock = input

	return &awss3.PutObjectLockConfigurationOutput{}, client.record("lock")
}

func (client *configurationClient) PutBucketLifecycleConfiguration(
	_ context.Context, input *awss3.PutBucketLifecycleConfigurationInput, _ ...func(*awss3.Options),
) (*awss3.PutBucketLifecycleConfigurationOutput, error) {
	client.lifecycle = input

	return &awss3.PutBucketLifecycleConfigurationOutput{}, client.record("lifecycle")
}

func TestCreateBucketConfiguration(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"new-bucket", "create_bucket_existing_owned"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			configuration, err := ParseConfiguration(map[string]string{
				"objectLockEnabled": "true", "retentionMode": "GOVERNANCE", "retentionDays": "30",
				"lifecyclePolicy": `{"Rules":[{"Status":"Enabled","Filter":{"Prefix":""},"Expiration":{"Days":30}}]}`,
			})
			require.NoError(t, err)

			client := &configurationClient{}

			err = CreateBucket(context.Background(), client, name, configuration)
			require.NoError(t, err)
			require.Equal(t, []string{"create", "versioning", "lock", "lifecycle"}, client.calls)
			require.True(t, *client.create.ObjectLockEnabledForBucket)
			require.Equal(t, name, *client.versioning.Bucket)
			require.Equal(t, types.BucketVersioningStatusEnabled, client.versioning.VersioningConfiguration.Status)
			require.Equal(t, name, *client.lock.Bucket)
			require.Equal(t, types.ObjectLockEnabledEnabled, client.lock.ObjectLockConfiguration.ObjectLockEnabled)
			require.Equal(t, configuration.Retention, client.lock.ObjectLockConfiguration.Rule.DefaultRetention)
			require.Equal(t, name, *client.lifecycle.Bucket)
			require.Equal(t, configuration.Lifecycle, client.lifecycle.LifecycleConfiguration)
		})
	}
}

func TestCreateBucketOptionalConfiguration(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name           string
		parameters     map[string]string
		wantCalls      []string
		wantLock       bool
		wantVersioning types.BucketVersioningStatus
	}{
		{name: "unchanged defaults", wantCalls: []string{"create"}},
		{name: "lock false", parameters: map[string]string{"objectLockEnabled": "false"}, wantCalls: []string{"create"}},
		{
			name: "versioning enabled", parameters: map[string]string{"versioning": "Enabled"},
			wantCalls: []string{"create", "versioning"}, wantVersioning: types.BucketVersioningStatusEnabled,
		},
		{
			name: "versioning suspended", parameters: map[string]string{"versioning": "Suspended"},
			wantCalls: []string{"create", "versioning"}, wantVersioning: types.BucketVersioningStatusSuspended,
		},
		{
			name: "lock without retention", parameters: map[string]string{"objectLockEnabled": "true"},
			wantCalls: []string{"create", "versioning", "lock"}, wantLock: true, wantVersioning: types.BucketVersioningStatusEnabled,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			configuration, err := ParseConfiguration(tc.parameters)
			require.NoError(t, err)

			client := &configurationClient{}
			require.NoError(t, CreateBucket(context.Background(), client, "bucket", configuration))
			require.Equal(t, tc.wantCalls, client.calls)

			if tc.wantLock {
				require.True(t, *client.create.ObjectLockEnabledForBucket)
				require.Nil(t, client.lock.ObjectLockConfiguration.Rule)
			} else {
				require.Nil(t, client.create.ObjectLockEnabledForBucket)
			}

			if tc.wantVersioning != "" {
				require.Equal(t, tc.wantVersioning, client.versioning.VersioningConfiguration.Status)
			}
		})
	}
}

func TestCreateBucketConfigurationFailureAndRetry(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		failAt    string
		wantCalls []string
	}{
		{failAt: "create", wantCalls: []string{"create"}},
		{failAt: "versioning", wantCalls: []string{"create", "versioning"}},
		{failAt: "lock", wantCalls: []string{"create", "versioning", "lock"}},
		{failAt: "lifecycle", wantCalls: []string{"create", "versioning", "lock", "lifecycle"}},
	}

	for _, tc := range testCases {
		t.Run(tc.failAt, func(t *testing.T) {
			t.Parallel()

			configuration, err := ParseConfiguration(map[string]string{
				"objectLockEnabled": "true", "retentionMode": "COMPLIANCE", "retentionYears": "1",
				"lifecyclePolicy": `{"Rules":[{"Status":"Enabled","Filter":{"Prefix":""},"Expiration":{"Days":365}}]}`,
			})
			require.NoError(t, err)

			client := &configurationClient{failAt: tc.failAt}

			err = CreateBucket(context.Background(), client, "new-bucket", configuration)
			require.ErrorContains(t, err, "backend rejected configuration")
			require.Equal(t, tc.wantCalls, client.calls)

			client.failAt = ""
			client.calls = nil
			require.NoError(t, CreateBucket(context.Background(), client, "create_bucket_existing_owned", configuration))
			require.Equal(t, []string{"create", "versioning", "lock", "lifecycle"}, client.calls)
		})
	}
}

func TestCreateBucketVersioningBackendFailureAndRetry(t *testing.T) {
	t.Parallel()

	for _, versioning := range []string{"Enabled", "Suspended"} {
		for _, code := range []string{"NotImplemented", "InvalidRequest", "AccessDenied"} {
			t.Run(versioning+"/"+code, func(t *testing.T) {
				t.Parallel()

				configuration, err := ParseConfiguration(map[string]string{
					"versioning":      versioning,
					"lifecyclePolicy": `{"Rules":[{"Status":"Enabled","Filter":{"Prefix":""},"Expiration":{"Days":30}}]}`,
				})
				require.NoError(t, err)

				backendError := &smithy.GenericAPIError{Code: code, Message: "versioning request rejected"}
				client := &configurationClient{failAt: "versioning", failure: backendError}
				err = CreateBucket(context.Background(), client, "new-bucket", configuration)
				require.ErrorIs(t, err, backendError)
				require.ErrorContains(t, err, "failed to set versioning")
				require.Equal(t, []string{"create", "versioning"}, client.calls)
				require.Nil(t, client.lock)
				require.Nil(t, client.lifecycle)

				var apiError smithy.APIError
				require.ErrorAs(t, err, &apiError)
				require.Equal(t, code, apiError.ErrorCode())

				client.failAt = ""
				client.calls = nil
				err = CreateBucket(context.Background(), client, "create_bucket_existing_owned", configuration)
				require.NoError(t, err)
				require.Equal(t, []string{"create", "versioning", "lifecycle"}, client.calls)
				require.Equal(t, "create_bucket_existing_owned", *client.versioning.Bucket)
				require.Equal(t, types.BucketVersioningStatus(versioning), client.versioning.VersioningConfiguration.Status)
				require.Nil(t, client.create.ObjectLockEnabledForBucket)
			})
		}
	}
}
