package iam

import (
	"context"
	"fmt"
	"testing"

	awsiam "github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/smithy-go"
	"github.com/cloudian/cloudian-cosi-driver/pkg/clients/iam"
	"github.com/stretchr/testify/require"
)

func TestRevokeBucketAccess(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name          string
		expectedError bool
	}{
		{
			name:          "revoke_bucket_access_success",
			expectedError: false,
		},
		{
			name:          "revoke_bucket_access_list_policies_fail",
			expectedError: true,
		},
		{
			name:          "revoke_bucket_access_delete_policy_fail",
			expectedError: true,
		},
		{
			name:          "revoke_bucket_access_list_access_keys_fail",
			expectedError: true,
		},
		{
			name:          "revoke_bucket_access_delete_access_key_fail",
			expectedError: true,
		},
		{
			name:          "revoke_bucket_access_delete_user_fail",
			expectedError: true,
		},
	}

	client := iam.ClientMock{}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := RevokeBucketAccess(context.TODO(), client, tc.name)

			if tc.expectedError {
				require.ErrorContains(t, err, tc.name)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

type revocationClient struct {
	iam.ClientMock
	failAt  string
	failure error
	deleted bool
	calls   []string
}

func (client *revocationClient) ListUserPolicies(ctx context.Context, input *awsiam.ListUserPoliciesInput, options ...func(*awsiam.Options)) (*awsiam.ListUserPoliciesOutput, error) {
	client.calls = append(client.calls, "list-policies")
	if client.deleted {
		return nil, &smithy.GenericAPIError{Code: "NoSuchEntity"}
	}

	if client.failAt == "list-policies" {
		return nil, client.failure
	}

	output, err := client.ClientMock.ListUserPolicies(ctx, input, options...)
	if err != nil {
		return nil, fmt.Errorf("mock list user policies: %w", err)
	}

	return output, nil
}

func (client *revocationClient) DeleteUserPolicy(ctx context.Context, input *awsiam.DeleteUserPolicyInput, options ...func(*awsiam.Options)) (*awsiam.DeleteUserPolicyOutput, error) {
	client.calls = append(client.calls, "delete-policy")
	if client.failAt == "delete-policy" {
		return nil, client.failure
	}

	output, err := client.ClientMock.DeleteUserPolicy(ctx, input, options...)
	if err != nil {
		return nil, fmt.Errorf("mock delete user policy: %w", err)
	}

	return output, nil
}

func (client *revocationClient) ListAccessKeys(ctx context.Context, input *awsiam.ListAccessKeysInput, options ...func(*awsiam.Options)) (*awsiam.ListAccessKeysOutput, error) {
	client.calls = append(client.calls, "list-keys")
	if client.deleted {
		return nil, &smithy.GenericAPIError{Code: "NoSuchEntity"}
	}

	if client.failAt == "list-keys" {
		return nil, client.failure
	}

	output, err := client.ClientMock.ListAccessKeys(ctx, input, options...)
	if err != nil {
		return nil, fmt.Errorf("mock list access keys: %w", err)
	}

	return output, nil
}

func (client *revocationClient) DeleteAccessKey(ctx context.Context, input *awsiam.DeleteAccessKeyInput, options ...func(*awsiam.Options)) (*awsiam.DeleteAccessKeyOutput, error) {
	client.calls = append(client.calls, "delete-key")
	if client.failAt == "delete-key" {
		return nil, client.failure
	}

	output, err := client.ClientMock.DeleteAccessKey(ctx, input, options...)
	if err != nil {
		return nil, fmt.Errorf("mock delete access key: %w", err)
	}

	return output, nil
}

func (client *revocationClient) DeleteUser(_ context.Context, _ *awsiam.DeleteUserInput, _ ...func(*awsiam.Options)) (*awsiam.DeleteUserOutput, error) {
	client.calls = append(client.calls, "delete-user")
	if client.deleted {
		return nil, &smithy.GenericAPIError{Code: "NoSuchEntity"}
	}

	if client.failAt == "delete-user" {
		return nil, client.failure
	}

	client.deleted = true

	return &awsiam.DeleteUserOutput{}, nil
}

func TestRevokeBucketAccessMissingEntitiesAndFailures(t *testing.T) {
	t.Parallel()

	for _, operation := range []string{"list-policies", "delete-policy", "list-keys", "delete-key", "delete-user"} {
		for _, code := range []string{"NoSuchEntity", "AccessDenied", "ServiceFailure"} {
			t.Run(operation+"/"+code, func(t *testing.T) {
				t.Parallel()

				backendError := &smithy.GenericAPIError{Code: code}
				client := &revocationClient{failAt: operation, failure: backendError}

				err := RevokeBucketAccess(context.Background(), client, "user")
				if code == "NoSuchEntity" {
					require.NoError(t, err)
					require.Contains(t, client.calls, "delete-user")

					return
				}

				require.ErrorIs(t, err, backendError)
				require.Equal(t, operation, client.calls[len(client.calls)-1], "cleanup must stop on backend failures")
			})
		}
	}
}

func TestRevokeBucketAccessRepeatedDeletion(t *testing.T) {
	t.Parallel()

	client := &revocationClient{}
	require.NoError(t, RevokeBucketAccess(context.Background(), client, "user"))
	require.True(t, client.deleted)

	client.calls = nil
	require.NoError(t, RevokeBucketAccess(context.Background(), client, "user"))
	require.Equal(t, []string{"list-policies", "list-keys", "delete-user"}, client.calls)
}

func TestRevokeBucketAccessRetriesAfterPartialDeletion(t *testing.T) {
	t.Parallel()

	backendError := &smithy.GenericAPIError{Code: "ServiceFailure"}
	client := &revocationClient{failAt: "delete-user", failure: backendError}
	require.ErrorIs(t, RevokeBucketAccess(context.Background(), client, "user"), backendError)
	require.False(t, client.deleted)

	client.failAt = ""
	require.NoError(t, RevokeBucketAccess(context.Background(), client, "user"))
	require.True(t, client.deleted)
	require.NoError(t, RevokeBucketAccess(context.Background(), client, "user"))
}
