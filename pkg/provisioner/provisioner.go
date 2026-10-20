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

//nolint:revive // RPC method name is defined by the COSI protobuf API.
func (s *Server) DriverGenerateBucketId(_ context.Context, request *spec.DriverGenerateBucketIdRequest) (*spec.DriverGenerateBucketIdResponse, error) {
	if err := validateProtocols(request.GetProtocols()); err != nil {
		return nil, err
	}

	if request.GetName() == "" {
		return nil, grpcError(codes.InvalidArgument, "bucket name is required")
	}

	return &spec.DriverGenerateBucketIdResponse{BucketId: request.GetName()}, nil
}

func (s *Server) DriverCreateBucket(ctx context.Context, request *spec.DriverCreateBucketRequest) (*spec.DriverCreateBucketResponse, error) {
	if err := validateProtocols(request.GetProtocols()); err != nil {
		return nil, err
	}

	bucketID := request.GetBucketId()
	if bucketID == "" {
		return nil, grpcError(codes.InvalidArgument, "bucket ID is required")
	}

	params := request.GetParameters()
	storagePolicyID := params["storagePolicyID"]

	klog.Infof("Creating bucket %s", bucketID)

	if err := bucket.CreateBucket(ctx, s.S3Client, bucketID, storagePolicyID); err != nil {
		var apiErr smithy.APIError
		if errors.As(err, &apiErr) && apiErr.ErrorCode() == "BucketAlreadyExists" {
			return nil, grpcErrorf(codes.AlreadyExists, "bucket %s already exists", bucketID)
		}

		return nil, fmt.Errorf("failed to create bucket: %w", err)
	}

	return &spec.DriverCreateBucketResponse{
		Protocols: s3BucketInfo(bucketID, s.Config),
	}, nil
}

func (s *Server) DriverGetBucket(ctx context.Context, request *spec.DriverGetBucketRequest) (*spec.DriverGetBucketResponse, error) {
	if err := validateProtocols(request.GetProtocols()); err != nil {
		return nil, err
	}

	bucketID := request.GetBucketId()
	if bucketID == "" {
		return nil, grpcError(codes.InvalidArgument, "bucket ID is required")
	}

	if err := bucket.CheckBucket(ctx, s.S3Client, bucketID); err != nil {
		var apiErr smithy.APIError
		if errors.As(err, &apiErr) && (apiErr.ErrorCode() == "NoSuchBucket" || apiErr.ErrorCode() == "NotFound") {
			return nil, grpcErrorf(codes.NotFound, "bucket %s does not exist", bucketID)
		}

		return nil, fmt.Errorf("failed to get bucket: %w", err)
	}

	return &spec.DriverGetBucketResponse{Protocols: s3BucketInfo(bucketID, s.Config)}, nil
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
	bucketRequest, bucketActions, objectActions, err := prepareGrantRequest(request)
	if err != nil {
		return nil, err
	}

	accountID, accessKey, secretKey, err := s.grantAccess(ctx, request.GetAccountName(), bucketRequest.GetBucketId(), bucketActions, objectActions)
	if err != nil {
		return nil, fmt.Errorf("failed to grant bucket access: %w", err)
	}

	return &spec.DriverGrantBucketAccessResponse{
		AccountId: accountID,
		Buckets: []*spec.DriverGrantBucketAccessResponse_BucketInfo{{
			BucketId:   bucketRequest.GetBucketId(),
			BucketInfo: s3BucketInfo(bucketRequest.GetBucketId(), s.Config),
		}},
		Credentials: &spec.CredentialInfo{
			S3: &spec.S3CredentialInfo{AccessKeyId: accessKey, AccessSecretKey: secretKey},
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

func (s *Server) grantAccess(ctx context.Context, accountName, bucketID string, bucketActions, objectActions []string) (accountID, accessKey, secretKey string, err error) {
	accountID = "iam_" + accountName
	if s.AdminClient != nil {
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
		if err := key.RevokeBucketAccess(ctx, s.AdminClient, s.S3Client, request.GetAccountId(), s.Config.Credentials.Group, request.GetBuckets()[0].GetBucketId()); err != nil {
			return fmt.Errorf("failed to revoke HyperStore key access: %w", err)
		}

		return nil
	default:
		return grpcError(codes.InvalidArgument, "unknown access account")
	}
}

func prepareGrantRequest(request *spec.DriverGrantBucketAccessRequest) (*spec.DriverGrantBucketAccessRequest_AccessedBucket, []string, []string, error) {
	if request.GetProtocol() == nil || request.GetProtocol().GetType() != spec.ObjectProtocol_S3 {
		return nil, nil, nil, grpcError(codes.InvalidArgument, "only the S3 protocol is supported")
	}

	if request.GetAuthenticationType() == nil || request.GetAuthenticationType().GetType() != spec.AuthenticationType_KEY {
		return nil, nil, nil, grpcError(codes.InvalidArgument, "only key-based authentication is supported")
	}

	if request.GetAccountName() == "" {
		return nil, nil, nil, grpcError(codes.InvalidArgument, "account name is required")
	}

	if len(request.GetBuckets()) == 0 {
		return nil, nil, nil, grpcError(codes.InvalidArgument, "at least one bucket is required")
	}

	if len(request.GetBuckets()) > 1 {
		return nil, nil, nil, grpcError(codes.OutOfRange, "multi-bucket access is not supported")
	}

	bucketRequest := request.GetBuckets()[0]
	if bucketRequest == nil || bucketRequest.GetBucketId() == "" {
		return nil, nil, nil, grpcError(codes.InvalidArgument, "bucket ID is required")
	}

	bucketActions, objectActions, err := accessPolicyActions(bucketRequest)

	return bucketRequest, bucketActions, objectActions, err
}

func validateRevokeRequest(request *spec.DriverRevokeBucketAccessRequest) error {
	if request.GetProtocol() == nil || request.GetProtocol().GetType() != spec.ObjectProtocol_S3 {
		return grpcError(codes.InvalidArgument, "only the S3 protocol is supported")
	}

	if request.GetAuthenticationType() == nil || request.GetAuthenticationType().GetType() != spec.AuthenticationType_KEY {
		return grpcError(codes.InvalidArgument, "only key-based authentication is supported")
	}

	if len(request.GetBuckets()) != 1 || request.GetBuckets()[0] == nil {
		return grpcError(codes.InvalidArgument, "exactly one bucket is required")
	}

	return nil
}

func validateProtocols(protocols []*spec.ObjectProtocol) error {
	for _, protocol := range protocols {
		if protocol == nil || protocol.GetType() != spec.ObjectProtocol_S3 {
			return grpcError(codes.InvalidArgument, "only the S3 protocol is supported")
		}
	}

	return nil
}

func s3BucketInfo(bucketID string, config config.Config) *spec.ObjectProtocolAndBucketInfo {
	return &spec.ObjectProtocolAndBucketInfo{
		S3: &spec.S3BucketInfo{
			BucketId: bucketID,
			Endpoint: config.Endpoints.S3,
			Region:   config.Region,
			AddressingStyle: &spec.S3AddressingStyle{
				Style: spec.S3AddressingStyle_PATH,
			},
		},
	}
}

func accessPolicyActions(bucketRequest *spec.DriverGrantBucketAccessRequest_AccessedBucket) (bucketActions, objectActions []string, err error) {
	if err := appendAccessModeActions(bucketRequest.GetObjectDataAccessMode(), &bucketActions, &objectActions,
		[]string{"s3:ListBucket"}, nil,
		[]string{"s3:GetObject", "s3:GetObjectVersion"}, []string{"s3:PutObject", "s3:DeleteObject", "s3:DeleteObjectVersion"}); err != nil {
		return nil, nil, err
	}

	if err := appendAccessModeActions(bucketRequest.GetObjectMetadataAccessMode(), &bucketActions, &objectActions,
		nil, nil,
		[]string{"s3:GetObjectTagging", "s3:GetObjectVersionTagging"}, []string{"s3:PutObjectTagging", "s3:PutObjectVersionTagging", "s3:DeleteObjectTagging", "s3:DeleteObjectVersionTagging"}); err != nil {
		return nil, nil, err
	}

	if err := appendAccessModeActions(bucketRequest.GetBucketMetadataAccessMode(), &bucketActions, &objectActions,
		[]string{"s3:GetBucketTagging"}, []string{"s3:PutBucketTagging", "s3:DeleteBucketTagging"}, nil, nil); err != nil {
		return nil, nil, err
	}

	if len(bucketActions)+len(objectActions) == 0 {
		return nil, nil, grpcError(codes.InvalidArgument, "at least one access mode must grant permissions")
	}

	return bucketActions, objectActions, nil
}

func appendAccessModeActions(mode *spec.AccessMode, bucketActions, objectActions *[]string, bucketReadActions, bucketWriteActions, objectReadActions, objectWriteActions []string) error {
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

func modePermissions(mode *spec.AccessMode) (read, write bool, err error) {
	if mode == nil {
		return false, false, nil
	}

	switch mode.GetMode() {
	case spec.AccessMode_ANY:
		return false, false, nil
	case spec.AccessMode_READ_ONLY:
		return true, false, nil
	case spec.AccessMode_WRITE_ONLY:
		return false, true, nil
	case spec.AccessMode_READ_WRITE:
		return true, true, nil
	case spec.AccessMode_UNKNOWN:
		return false, false, grpcError(codes.InvalidArgument, "unknown access mode")
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
