package key

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
	"github.com/cloudian/cloudian-cosi-driver/pkg/clients/admin"
	"github.com/cloudian/cloudian-cosi-driver/pkg/clients/admin/api"
	"github.com/cloudian/cloudian-cosi-driver/pkg/clients/s3"
	"github.com/stretchr/testify/require"
)

const (
	revokePutPolicyOperation = "put-policy"
	revokeMissingPolicyCode  = "NoSuchBucketPolicy"
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
			name:          "revoke_bucket_access_policy_found_put_policy_success",
			expectedError: false,
		},
		{
			name:          "revoke_bucket_access_policy_found_delete_policy_success",
			expectedError: false,
		},
		{
			name:          "revoke_bucket_access_get_user_fail",
			expectedError: true,
		},
		{
			name:          "revoke_bucket_access_delete_user_fail",
			expectedError: true,
		},
		{
			name:          "revoke_bucket_access_get_policy_fail",
			expectedError: true,
		},
		{
			name:          "revoke_bucket_access_policy_found_put_policy_fail",
			expectedError: true,
		},
		{
			name:          "revoke_bucket_access_policy_found_delete_policy_fail",
			expectedError: true,
		},
	}

	adminClient := admin.ClientMock{}
	s3client := s3.ClientMock{}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := RevokeBucketAccess(context.TODO(), adminClient, s3client, tc.name, "mockGroup", tc.name)

			if tc.expectedError {
				require.ErrorContains(t, err, tc.name)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

type revocationUserClient struct {
	missing      bool
	getStatus    int
	deleteStatus int
	calls        *[]string
}

func (client *revocationUserClient) GetUser(_ context.Context, _ *api.GetUserParams, _ ...api.RequestEditorFn) (*http.Response, error) {
	*client.calls = append(*client.calls, "get-user")

	status := client.getStatus
	if status == 0 {
		status = http.StatusOK
	}

	if client.missing {
		status = http.StatusNotFound
	}

	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(`{"canonicalUserId":"user-id"}`))}, nil
}

func (client *revocationUserClient) DeleteUser(_ context.Context, _ *api.DeleteUserParams, _ ...api.RequestEditorFn) (*http.Response, error) {
	*client.calls = append(*client.calls, "delete-user")

	status := client.deleteStatus
	if status == 0 {
		status = http.StatusOK
	}

	if status == http.StatusOK || status == http.StatusNotFound {
		client.missing = true
	}

	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(""))}, nil
}

type revocationPolicyClient struct {
	calls     *[]string
	failureAt string
	failure   error
	policy    string
	missing   bool
}

func (client *revocationPolicyClient) GetBucketPolicy(_ context.Context, _ *awss3.GetBucketPolicyInput, _ ...func(*awss3.Options)) (*awss3.GetBucketPolicyOutput, error) {
	*client.calls = append(*client.calls, "get-policy")
	if client.failureAt == "get-policy" {
		return nil, client.failure
	}

	if client.missing {
		return nil, &smithy.GenericAPIError{Code: revokeMissingPolicyCode}
	}

	return &awss3.GetBucketPolicyOutput{Policy: &client.policy}, nil
}

func (client *revocationPolicyClient) DeleteBucketPolicy(_ context.Context, _ *awss3.DeleteBucketPolicyInput, _ ...func(*awss3.Options)) (*awss3.DeleteBucketPolicyOutput, error) {
	*client.calls = append(*client.calls, "delete-policy")
	if client.failureAt == "delete-policy" {
		return nil, client.failure
	}

	client.missing = true

	return &awss3.DeleteBucketPolicyOutput{}, nil
}

func (client *revocationPolicyClient) PutBucketPolicy(_ context.Context, input *awss3.PutBucketPolicyInput, _ ...func(*awss3.Options)) (*awss3.PutBucketPolicyOutput, error) {
	*client.calls = append(*client.calls, revokePutPolicyOperation)
	if client.failureAt == revokePutPolicyOperation {
		return nil, client.failure
	}

	client.policy = *input.Policy

	return &awss3.PutBucketPolicyOutput{}, nil
}

func TestRevokeBucketAccessCleanupOrderAndRetries(t *testing.T) {
	t.Parallel()

	for _, failAt := range []string{"", "get-policy", revokePutPolicyOperation, "delete-user"} {
		t.Run("failure_at_"+failAt, func(t *testing.T) {
			t.Parallel()

			var calls []string

			userClient := &revocationUserClient{calls: &calls}
			policyClient := &revocationPolicyClient{
				calls: &calls,
				policy: `{"Version":"2012-10-17","Statement":[
					{"Effect":"Allow","Principal":{"CanonicalUser":"user-id"},"Action":"s3:GetObject","Resource":"arn:aws:s3:::bucket/*"},
					{"Effect":"Allow","Principal":{"CanonicalUser":"other-user"},"Action":"s3:GetObject","Resource":"arn:aws:s3:::bucket/*"}]}`,
			}

			if failAt == "delete-user" {
				userClient.deleteStatus = http.StatusServiceUnavailable
			} else {
				policyClient.failureAt = failAt
				policyClient.failure = errors.New("temporary policy error")
			}

			err := RevokeBucketAccess(context.Background(), userClient, policyClient, "user", "group", "bucket")
			if failAt != "" {
				require.Error(t, err)
				require.False(t, userClient.missing, "the user must survive a partial cleanup failure")
				require.Equal(t, failAt, calls[len(calls)-1])
			} else {
				require.NoError(t, err)
				require.Equal(t, []string{"get-user", "get-policy", revokePutPolicyOperation, "delete-user"}, calls)
			}

			policyClient.failureAt = ""
			userClient.deleteStatus = http.StatusOK
			calls = nil

			if failAt != "" {
				require.NoError(t, RevokeBucketAccess(context.Background(), userClient, policyClient, "user", "group", "bucket"))
				require.Equal(t, []string{"get-user", "get-policy", "put-policy", "delete-user"}, calls)
			} else {
				require.NoError(t, err)
			}

			require.True(t, userClient.missing)
			require.NotContains(t, policyClient.policy, `"CanonicalUser":"user-id"`)
			require.Contains(t, policyClient.policy, "other-user")

			calls = nil

			require.NoError(t, RevokeBucketAccess(context.Background(), userClient, policyClient, "user", "group", "bucket"))
			require.Equal(t, []string{"get-user"}, calls)
		})
	}
}

func TestRevokeBucketAccessMissingUsers(t *testing.T) {
	t.Parallel()

	for _, operation := range []string{"get-user", "delete-user"} {
		t.Run(operation, func(t *testing.T) {
			t.Parallel()

			var calls []string

			userClient := &revocationUserClient{calls: &calls}

			policyClient := &revocationPolicyClient{calls: &calls, missing: true}
			if operation == "get-user" {
				userClient.missing = true
			} else {
				userClient.deleteStatus = http.StatusNotFound
			}

			require.NoError(t, RevokeBucketAccess(context.Background(), userClient, policyClient, "user", "group", "bucket"))

			if operation == "get-user" {
				require.Equal(t, []string{"get-user"}, calls)
			} else {
				require.Equal(t, []string{"get-user", "get-policy", "delete-user"}, calls)
			}
		})
	}
}

func TestDeletePolicyMissingResourcesAndFailures(t *testing.T) {
	t.Parallel()

	for _, operation := range []string{"get-policy", "delete-policy", revokePutPolicyOperation} {
		for _, code := range []string{revokeMissingPolicyCode, "NoSuchBucket", "AccessDenied", "InternalError"} {
			t.Run(operation+"/"+code, func(t *testing.T) {
				t.Parallel()

				var calls []string

				backendError := &smithy.GenericAPIError{Code: code, Message: revokeMissingPolicyCode}

				client := &revocationPolicyClient{
					calls: &calls, failureAt: operation, failure: backendError,
					policy: `{"Statement":[{"Principal":{"CanonicalUser":"user-id"}}]}`,
				}
				if operation == revokePutPolicyOperation {
					client.policy = `{"Statement":[{"Principal":{"CanonicalUser":"other-user"}}]}`
				}

				err := DeletePolicy(context.Background(), client, "user-id", "bucket")
				if code == "NoSuchBucket" || (code == revokeMissingPolicyCode && operation != revokePutPolicyOperation) {
					require.NoError(t, err)
				} else {
					require.ErrorIs(t, err, backendError)
				}
			})
		}
	}
}

func TestRevokeBucketAccessDoesNotIgnoreAdminFailures(t *testing.T) {
	t.Parallel()

	for _, status := range []int{http.StatusForbidden, http.StatusServiceUnavailable} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			t.Parallel()

			var calls []string

			userClient := &revocationUserClient{calls: &calls, getStatus: status}
			require.Error(t, RevokeBucketAccess(context.Background(), userClient, nil, "user", "group", "bucket"))
			require.Equal(t, []string{"get-user"}, calls)
		})
	}
}
