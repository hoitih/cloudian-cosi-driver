package provisioner

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/aws/smithy-go"
	"github.com/cloudian/cloudian-cosi-driver/pkg/authentication/iam"
	"github.com/cloudian/cloudian-cosi-driver/pkg/authentication/key"
	"github.com/cloudian/cloudian-cosi-driver/pkg/bucket"
	"github.com/cloudian/cloudian-cosi-driver/pkg/config"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	klog "k8s.io/klog/v2"
	spec "sigs.k8s.io/container-object-storage-interface/proto"
)

type S3Client interface {
	bucket.BucketCreator
	bucket.BucketDeletor
	bucket.BucketChecker
	key.PolicyCreator
	key.PolicyDeletor
}

type IAMClient interface {
	iam.BucketAccessGrantor
	iam.BucketAccessRevoker
}

type AdminClient interface {
	key.UserCreator
	key.UserDeletor
}

type Server struct {
	spec.UnimplementedProvisionerServer
	S3Client    S3Client
	IAMClient   IAMClient
	AdminClient AdminClient
	Config      config.Config
}

func (s *Server) DriverCreateBucket(ctx context.Context, request *spec.DriverCreateBucketRequest) (*spec.DriverCreateBucketResponse, error) {
	bucketID := request.GetName()
	if bucketID == "" {
		return nil, grpcError(codes.InvalidArgument, "bucket name is required")
	}

	configuration, err := bucket.ParseConfiguration(request.GetParameters())
	if err != nil {
		return nil, grpcErrorf(codes.InvalidArgument, "invalid bucket parameters: %s", err)
	}

	klog.Infof("Creating bucket %s", bucketID)

	if err := bucket.CreateBucket(ctx, s.S3Client, bucketID, configuration); err != nil {
		var apiErr smithy.APIError
		if errors.As(err, &apiErr) && apiErr.ErrorCode() == "BucketAlreadyExists" {
			return nil, grpcErrorf(codes.AlreadyExists, "bucket %s already exists", bucketID)
		}

		return nil, fmt.Errorf("failed to create bucket: %w", err)
	}

	return &spec.DriverCreateBucketResponse{
		BucketId: bucketID,
		BucketInfo: &spec.Protocol{Type: &spec.Protocol_S3{S3: &spec.S3{
			Region: s.Config.Region,
		}}},
	}, nil
}

func (s *Server) DriverDeleteBucket(ctx context.Context, request *spec.DriverDeleteBucketRequest) (*spec.DriverDeleteBucketResponse, error) {
	if request.GetBucketId() == "" {
		return nil, grpcError(codes.InvalidArgument, "bucket ID is required")
	}

	klog.Infof("Deleting bucket %s", request.GetBucketId())

	if err := bucket.DeleteBucket(ctx, s.S3Client, request.GetBucketId()); err != nil {
		return nil, fmt.Errorf("failed to delete bucket: %w", err)
	}

	return &spec.DriverDeleteBucketResponse{}, nil
}

func (s *Server) DriverGrantBucketAccess(ctx context.Context, request *spec.DriverGrantBucketAccessRequest) (*spec.DriverGrantBucketAccessResponse, error) {
	bucketActions, objectActions, err := prepareGrantRequest(request)
	if err != nil {
		return nil, err
	}

	accountID, accessKey, secretKey, err := s.grantAccess(ctx, request, bucketActions, objectActions)
	if err != nil {
		return nil, fmt.Errorf("failed to grant bucket access: %w", err)
	}

	return &spec.DriverGrantBucketAccessResponse{
		AccountId: accountID,
		Credentials: map[string]*spec.CredentialDetails{
			"s3": {Secrets: map[string]string{
				"authenticationType": request.GetAuthenticationType().String(),
				"endpoint":           s.Config.Endpoints.S3,
				"region":             s.Config.Region,
				"accessKeyID":        accessKey,
				"accessSecretKey":    secretKey,
			}},
		},
	}, nil
}

func (s *Server) DriverRevokeBucketAccess(ctx context.Context, request *spec.DriverRevokeBucketAccessRequest) (*spec.DriverRevokeBucketAccessResponse, error) {
	if err := validateRevokeRequest(request); err != nil {
		return nil, err
	}

	if err := s.revokeAccess(ctx, request); err != nil {
		return nil, fmt.Errorf("failed to revoke bucket access: %w", err)
	}

	return &spec.DriverRevokeBucketAccessResponse{}, nil
}

func (s *Server) grantAccess(ctx context.Context, request *spec.DriverGrantBucketAccessRequest, bucketActions, objectActions []string) (accountID, accessKey, secretKey string, err error) {
	accountName, bucketID := request.GetName(), request.GetBucketId()
	accountID = "iam_" + accountName

	if request.GetAuthenticationType() == spec.AuthenticationType_Key {
		if s.AdminClient == nil {
			return "", "", "", grpcError(codes.FailedPrecondition, "admin client is not configured for Key authentication")
		}

		accountID = "key_" + accountName

		klog.Infof("Granting S3 key access to bucket %s", bucketID)

		accessKey, secretKey, err = key.GrantBucketAccess(ctx, s.AdminClient, s.S3Client, accountID, s.Config.Credentials.Group, bucketID, bucketActions, objectActions)
		if err != nil {
			return "", "", "", fmt.Errorf("failed to grant HyperStore key access: %w", err)
		}

		return accountID, accessKey, secretKey, nil
	}

	klog.Infof("Granting IAM key access to bucket %s", bucketID)

	accessKey, secretKey, err = iam.GrantBucketAccess(ctx, s.IAMClient, accountID, bucketID, bucketActions, objectActions)
	if err != nil {
		return "", "", "", fmt.Errorf("failed to grant IAM key access: %w", err)
	}

	return accountID, accessKey, secretKey, nil
}

func (s *Server) revokeAccess(ctx context.Context, request *spec.DriverRevokeBucketAccessRequest) error {
	switch {
	case strings.HasPrefix(request.GetAccountId(), "iam_"):
		if err := iam.RevokeBucketAccess(ctx, s.IAMClient, request.GetAccountId()); err != nil {
			return fmt.Errorf("failed to revoke IAM access: %w", err)
		}

		return nil
	case strings.HasPrefix(request.GetAccountId(), "key_") && s.AdminClient != nil:
		if err := key.RevokeBucketAccess(ctx, s.AdminClient, s.S3Client, request.GetAccountId(), s.Config.Credentials.Group, request.GetBucketId()); err != nil {
			return fmt.Errorf("failed to revoke HyperStore key access: %w", err)
		}

		return nil
	default:
		return grpcError(codes.InvalidArgument, "unknown access account")
	}
}

func prepareGrantRequest(request *spec.DriverGrantBucketAccessRequest) ([]string, []string, error) {
	if request.GetAuthenticationType() != spec.AuthenticationType_IAM && request.GetAuthenticationType() != spec.AuthenticationType_Key {
		return nil, nil, grpcError(codes.InvalidArgument, "authentication type must be IAM or Key")
	}

	if request.GetName() == "" {
		return nil, nil, grpcError(codes.InvalidArgument, "account name is required")
	}

	if request.GetBucketId() == "" {
		return nil, nil, grpcError(codes.InvalidArgument, "bucket ID is required")
	}

	return accessPolicyActions(request.GetParameters())
}

func validateRevokeRequest(request *spec.DriverRevokeBucketAccessRequest) error {
	if request.GetAccountId() == "" {
		return grpcError(codes.InvalidArgument, "account ID is required")
	}

	if request.GetBucketId() == "" {
		return grpcError(codes.InvalidArgument, "bucket ID is required")
	}

	return nil
}

func accessPolicyActions(parameters map[string]string) (bucketActions, objectActions []string, err error) {
	dataMode, hasData := parameters["objectDataAccessMode"]
	metadataMode, hasMetadata := parameters["objectMetadataAccessMode"]
	bucketMode, hasBucket := parameters["bucketMetadataAccessMode"]

	if !hasData && !hasMetadata && !hasBucket {
		dataMode = "ReadWrite"
	}

	if err := appendAccessModeActions(dataMode, &bucketActions, &objectActions,
		[]string{"s3:ListBucket"}, nil,
		[]string{"s3:GetObject", "s3:GetObjectVersion"}, []string{"s3:PutObject", "s3:DeleteObject", "s3:DeleteObjectVersion"}); err != nil {
		return nil, nil, err
	}

	if err := appendAccessModeActions(metadataMode, &bucketActions, &objectActions,
		nil, nil,
		[]string{"s3:GetObjectTagging", "s3:GetObjectVersionTagging"}, []string{"s3:PutObjectTagging", "s3:PutObjectVersionTagging", "s3:DeleteObjectTagging", "s3:DeleteObjectVersionTagging"}); err != nil {
		return nil, nil, err
	}

	if err := appendAccessModeActions(bucketMode, &bucketActions, &objectActions,
		[]string{"s3:GetBucketTagging"}, []string{"s3:PutBucketTagging", "s3:DeleteBucketTagging"}, nil, nil); err != nil {
		return nil, nil, err
	}

	if len(bucketActions)+len(objectActions) == 0 {
		return nil, nil, grpcError(codes.InvalidArgument, "at least one access mode must grant permissions")
	}

	return bucketActions, objectActions, nil
}

func appendAccessModeActions(mode string, bucketActions, objectActions *[]string, bucketReadActions, bucketWriteActions, objectReadActions, objectWriteActions []string) error {
	read, write, err := modePermissions(mode)
	if err != nil {
		return err
	}

	if read {
		*bucketActions = append(*bucketActions, bucketReadActions...)
		*objectActions = append(*objectActions, objectReadActions...)
	}

	if write {
		*bucketActions = append(*bucketActions, bucketWriteActions...)
		*objectActions = append(*objectActions, objectWriteActions...)
	}

	return nil
}

func modePermissions(mode string) (read, write bool, err error) {
	switch mode {
	case "", "Any":
		return false, false, nil
	case "ReadOnly":
		return true, false, nil
	case "WriteOnly":
		return false, true, nil
	case "ReadWrite":
		return true, true, nil
	default:
		return false, false, grpcError(codes.InvalidArgument, "unsupported access mode")
	}
}

//nolint:wrapcheck // These helpers intentionally preserve gRPC status codes.
func grpcError(code codes.Code, message string) error {
	return status.Error(code, message)
}

//nolint:wrapcheck // These helpers intentionally preserve gRPC status codes.
func grpcErrorf(code codes.Code, format string, args ...any) error {
	return status.Errorf(code, format, args...)
}
