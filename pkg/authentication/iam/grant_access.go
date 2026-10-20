package iam

import (
	"context"
	"errors"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/smithy-go"
	klog "k8s.io/klog/v2"
)

type AccessKeyManager interface {
	ListAccessKeys(ctx context.Context, params *iam.ListAccessKeysInput, optFns ...func(*iam.Options)) (*iam.ListAccessKeysOutput, error)
	DeleteAccessKey(ctx context.Context, params *iam.DeleteAccessKeyInput, optFns ...func(*iam.Options)) (*iam.DeleteAccessKeyOutput, error)
}

type BucketAccessGrantor interface {
	AccessKeyManager
	CreateUser(ctx context.Context, params *iam.CreateUserInput, optFns ...func(*iam.Options)) (*iam.CreateUserOutput, error)
	PutUserPolicy(ctx context.Context, params *iam.PutUserPolicyInput, optFns ...func(*iam.Options)) (*iam.PutUserPolicyOutput, error)
	CreateAccessKey(ctx context.Context, params *iam.CreateAccessKeyInput, optFns ...func(*iam.Options)) (*iam.CreateAccessKeyOutput, error)
}

// Grant bucket access using IAM
func GrantBucketAccess(ctx context.Context, client BucketAccessGrantor, name, bucketID string, bucketActions, objectActions []string) (accessKey, secretKey string, err error) {
	klog.Infof("Creating IAM user %s", name)

	if _, err := client.CreateUser(ctx, &iam.CreateUserInput{UserName: &name}); err != nil {
		var apiErr smithy.APIError
		if !errors.As(err, &apiErr) || apiErr.ErrorCode() != "EntityAlreadyExists" {
			return "", "", fmt.Errorf("failed to create iam user: %w", err)
		}

		if err := deleteAccessKeys(ctx, client, name); err != nil {
			return "", "", fmt.Errorf("failed to rotate IAM access keys: %w", err)
		}
	}

	klog.Infof("Creating IAM policy for bucket %s, user %s", bucketID, name)

	if err := CreatePolicy(ctx, client, name, bucketID, bucketActions, objectActions); err != nil {
		return "", "", fmt.Errorf("failed to create iam policy: %w", err)
	}

	klog.Infof("Creating access key for IAM user %s", name)

	output, err := client.CreateAccessKey(ctx, &iam.CreateAccessKeyInput{UserName: &name})
	if err != nil {
		return "", "", fmt.Errorf("failed to create access key for iam user: %w", err)
	}

	klog.Infof("IAM access granted for bucket %s, user %s", bucketID, name)

	return *output.AccessKey.AccessKeyId, *output.AccessKey.SecretAccessKey, nil
}

func deleteAccessKeys(ctx context.Context, client BucketAccessGrantor, name string) error {
	keys, err := client.ListAccessKeys(ctx, &iam.ListAccessKeysInput{UserName: &name})
	if err != nil {
		return fmt.Errorf("failed to list IAM access keys: %w", err)
	}

	for _, key := range keys.AccessKeyMetadata {
		if key.AccessKeyId == nil {
			continue
		}

		if _, err := client.DeleteAccessKey(ctx, &iam.DeleteAccessKeyInput{UserName: &name, AccessKeyId: key.AccessKeyId}); err != nil {
			return fmt.Errorf("failed to delete IAM access key: %w", err)
		}
	}

	return nil
}

// Create policy via IAM API
func CreatePolicy(ctx context.Context, client BucketAccessGrantor, userName, bucketName string, bucketActions, objectActions []string) error {
	policy, err := defaultPolicy(bucketName, bucketActions, objectActions)
	if err != nil {
		return fmt.Errorf("failed to serialize IAM policy: %w", err)
	}

	params := &iam.PutUserPolicyInput{
		UserName:       &userName,
		PolicyName:     &userName,
		PolicyDocument: &policy,
	}

	if _, err := client.PutUserPolicy(ctx, params); err != nil {
		return fmt.Errorf("iam client failed to put user policy: %w", err)
	}

	return nil
}
