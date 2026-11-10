package iam

import (
	"context"
	"errors"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/smithy-go"
	klog "k8s.io/klog/v2"
)

type BucketAccessRevoker interface {
	AccessKeyManager
	ListUserPolicies(ctx context.Context, params *iam.ListUserPoliciesInput, optFns ...func(*iam.Options)) (*iam.ListUserPoliciesOutput, error)
	DeleteUserPolicy(ctx context.Context, params *iam.DeleteUserPolicyInput, optFns ...func(*iam.Options)) (*iam.DeleteUserPolicyOutput, error)
	DeleteUser(ctx context.Context, params *iam.DeleteUserInput, optFns ...func(*iam.Options)) (*iam.DeleteUserOutput, error)
}

// Revoke bucket access for IAM
func RevokeBucketAccess(ctx context.Context, client BucketAccessRevoker, userName string) error {
	if err := deleteUserPolicies(ctx, client, userName); err != nil {
		return err
	}

	if err := deleteUserAccessKeys(ctx, client, userName); err != nil {
		return err
	}

	klog.Infof("Deleting IAM user %s", userName)

	_, err := client.DeleteUser(ctx, &iam.DeleteUserInput{UserName: &userName})
	if err != nil && !isMissingEntity(err) {
		return fmt.Errorf("failed to delete user: %w", err)
	}

	return nil
}

func deleteUserPolicies(ctx context.Context, client BucketAccessRevoker, userName string) error {
	policies, err := client.ListUserPolicies(ctx, &iam.ListUserPoliciesInput{UserName: &userName})
	if err != nil {
		if isMissingEntity(err) {
			return nil
		}

		return fmt.Errorf("failed to list user policies: %w", err)
	}

	for _, policy := range policies.PolicyNames {
		klog.Infof("Deleting policy %s for IAM user %s", policy, userName)

		_, err := client.DeleteUserPolicy(ctx, &iam.DeleteUserPolicyInput{PolicyName: &policy, UserName: &userName})
		if err != nil && !isMissingEntity(err) {
			return fmt.Errorf("failed to delete user policy '%s': %w", policy, err)
		}
	}

	return nil
}

func deleteUserAccessKeys(ctx context.Context, client BucketAccessRevoker, userName string) error {
	keys, err := client.ListAccessKeys(ctx, &iam.ListAccessKeysInput{UserName: &userName})
	if err != nil {
		if isMissingEntity(err) {
			return nil
		}

		return fmt.Errorf("failed to list access keys: %w", err)
	}

	for _, key := range keys.AccessKeyMetadata {
		if key.AccessKeyId == nil {
			continue
		}

		_, err = client.DeleteAccessKey(ctx, &iam.DeleteAccessKeyInput{AccessKeyId: key.AccessKeyId, UserName: &userName})
		if err != nil && !isMissingEntity(err) {
			return fmt.Errorf("failed to delete access key: %w", err)
		}
	}

	return nil
}

func isMissingEntity(err error) bool {
	var apiError smithy.APIError

	return errors.As(err, &apiError) && apiError.ErrorCode() == "NoSuchEntity"
}
