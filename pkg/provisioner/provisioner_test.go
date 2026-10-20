package provisioner

import (
	"context"
	"testing"

	"github.com/cloudian/cloudian-cosi-driver/pkg/clients/admin"
	"github.com/cloudian/cloudian-cosi-driver/pkg/clients/iam"
	"github.com/cloudian/cloudian-cosi-driver/pkg/clients/s3"
	"github.com/cloudian/cloudian-cosi-driver/pkg/config"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	spec "sigs.k8s.io/container-object-storage-interface/proto"
)

func s3Protocol() *spec.ObjectProtocol {
	return &spec.ObjectProtocol{Type: spec.ObjectProtocol_S3}
}

func accessMode(mode spec.AccessMode_Mode) *spec.AccessMode {
	return &spec.AccessMode{Mode: mode}
}

func TestDriverCreateBucket(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name          string
		expectedError bool
	}{
		{name: "create_bucket_success"},
		{name: "create_bucket_fail", expectedError: true},
		{name: "create_bucket_existing_owned"},
		{name: "create_bucket_exists_other_owner", expectedError: true},
	}

	client := s3.ClientMock{}
	region := "mock_region"
	cfg := config.Config{Region: region}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			server := Server{
				S3Client: client,
				Config:   cfg,
			}

			response, err := server.DriverCreateBucket(context.TODO(), &spec.DriverCreateBucketRequest{
				BucketId:  tc.name,
				Protocols: []*spec.ObjectProtocol{s3Protocol()},
			})

			if tc.expectedError {
				require.Nil(t, response)
				require.Error(t, err)
			} else {
				require.Equal(t, response.GetProtocols().GetS3().GetBucketId(), tc.name)
				require.Equal(t, response.GetProtocols().GetS3().GetRegion(), region)
				require.NoError(t, err)
			}
		})
	}
}

func TestDriverGenerateBucketId(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name      string
		request   *spec.DriverGenerateBucketIdRequest
		wantError bool
	}{
		{name: "valid", request: &spec.DriverGenerateBucketIdRequest{Name: "bc-example", Protocols: []*spec.ObjectProtocol{s3Protocol()}}},
		{name: "empty name", request: &spec.DriverGenerateBucketIdRequest{}, wantError: true},
		{name: "unsupported protocol", request: &spec.DriverGenerateBucketIdRequest{Name: "bc-example", Protocols: []*spec.ObjectProtocol{{Type: spec.ObjectProtocol_AZURE}}}, wantError: true},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			server := Server{}

			response, err := server.DriverGenerateBucketId(context.Background(), tc.request)
			if tc.wantError {
				require.Equal(t, codes.InvalidArgument, status.Code(err))

				return
			}

			require.NoError(t, err)
			require.Equal(t, tc.request.GetName(), response.GetBucketId())
		})
	}
}

func TestDriverGetBucket(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name         string
		expectedCode codes.Code
	}{
		{name: "existing_bucket"},
		{name: "get_bucket_not_found", expectedCode: codes.NotFound},
		{name: "get_bucket_fail", expectedCode: codes.Unknown},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			server := Server{S3Client: s3.ClientMock{}}
			response, err := server.DriverGetBucket(context.Background(), &spec.DriverGetBucketRequest{
				BucketId:  tc.name,
				Protocols: []*spec.ObjectProtocol{s3Protocol()},
			})

			if tc.expectedCode != codes.OK {
				require.Equal(t, tc.expectedCode, status.Code(err))

				return
			}

			require.NoError(t, err)
			require.Equal(t, tc.name, response.GetProtocols().GetS3().GetBucketId())
		})
	}
}

func TestDriverDeleteBucket(t *testing.T) {
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
			name:          "delete_bucket_missing",
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

			server := Server{
				S3Client: client,
			}

			response, err := server.DriverDeleteBucket(context.TODO(), &spec.DriverDeleteBucketRequest{BucketId: tc.name})

			if tc.expectedError {
				require.Error(t, err)
			} else {
				require.NotNil(t, response)
				require.NoError(t, err)
			}
		})
	}
}

func TestDriverGrantBucketAccess(t *testing.T) {
	t.Parallel()

	cfg := config.Config{
		Region: "mock-region",
		Endpoints: struct {
			S3    string
			IAM   string
			Admin string
		}{
			S3: "mockS3Endpoint",
		},
	}

	server := Server{IAMClient: iam.ClientMock{}, S3Client: s3.ClientMock{}, Config: cfg}
	request := &spec.DriverGrantBucketAccessRequest{
		AccountName:        "grant_bucket_access_success",
		Protocol:           s3Protocol(),
		AuthenticationType: &spec.AuthenticationType{Type: spec.AuthenticationType_KEY},
		Buckets: []*spec.DriverGrantBucketAccessRequest_AccessedBucket{{
			BucketId:             "mock-bucket",
			ObjectDataAccessMode: accessMode(spec.AccessMode_READ_ONLY),
		}},
	}

	response, err := server.DriverGrantBucketAccess(context.Background(), request)
	require.NoError(t, err)
	require.Equal(t, "iam_"+request.GetAccountName(), response.GetAccountId())
	require.Equal(t, "mockAccessKey", response.GetCredentials().GetS3().GetAccessKeyId())
	require.Equal(t, "mockSecretKey", response.GetCredentials().GetS3().GetAccessSecretKey())
	require.Equal(t, "mock-bucket", response.GetBuckets()[0].GetBucketInfo().GetS3().GetBucketId())
}

func TestDriverGrantBucketAccessBackendRoutes(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name          string
		accountName   string
		useAdmin      bool
		wantAccountID string
		wantError     bool
	}{
		{name: "iam_success", accountName: "grant_bucket_access_success", wantAccountID: "iam_grant_bucket_access_success"},
		{name: "iam_failure", accountName: "grant_bucket_access_create_user_fail", wantError: true},
		{name: "hyperstore_success", accountName: "grant_bucket_access_success", useAdmin: true, wantAccountID: "key_grant_bucket_access_success"},
		{name: "hyperstore_failure", accountName: "grant_bucket_access_create_user_fail", useAdmin: true, wantError: true},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			server := Server{IAMClient: iam.ClientMock{}, S3Client: s3.ClientMock{}, Config: config.Config{
				Credentials: struct {
					AccessKey string
					SecretKey string
					Group     string
				}{Group: "mock-group"},
			}}
			if tc.useAdmin {
				server.AdminClient = admin.ClientMock{}
			}

			request := &spec.DriverGrantBucketAccessRequest{
				AccountName:        tc.accountName,
				Protocol:           s3Protocol(),
				AuthenticationType: &spec.AuthenticationType{Type: spec.AuthenticationType_KEY},
				Buckets: []*spec.DriverGrantBucketAccessRequest_AccessedBucket{{
					BucketId:             "mock-bucket",
					ObjectDataAccessMode: accessMode(spec.AccessMode_READ_WRITE),
				}},
			}

			response, err := server.DriverGrantBucketAccess(context.Background(), request)
			if tc.wantError {
				require.Error(t, err)
				require.Nil(t, response)

				return
			}

			require.NoError(t, err)
			require.Equal(t, tc.wantAccountID, response.GetAccountId())
		})
	}
}

func TestDriverRevokeBucketAccess(t *testing.T) {
	t.Parallel()

	server := Server{IAMClient: iam.ClientMock{}, S3Client: s3.ClientMock{}}
	request := &spec.DriverRevokeBucketAccessRequest{
		AccountId:          "iam_account",
		Protocol:           s3Protocol(),
		AuthenticationType: &spec.AuthenticationType{Type: spec.AuthenticationType_KEY},
		Buckets:            []*spec.DriverRevokeBucketAccessRequest_AccessedBucket{{BucketId: "mock-bucket"}},
	}

	response, err := server.DriverRevokeBucketAccess(context.Background(), request)
	require.NoError(t, err)
	require.NotNil(t, response)
}

func TestDriverRevokeBucketAccessBackendRoutes(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name      string
		accountID string
		bucketID  string
		useAdmin  bool
		wantError bool
	}{
		{name: "iam_success", accountID: "iam_account", bucketID: "mock-bucket"},
		{name: "iam_failure", accountID: "iam_revoke_bucket_access_list_policies_fail", bucketID: "mock-bucket", wantError: true},
		{name: "hyperstore_success", accountID: "key_account", bucketID: "mock-bucket", useAdmin: true},
		{name: "hyperstore_failure", accountID: "key_revoke_bucket_access_get_user_fail", bucketID: "mock-bucket", useAdmin: true, wantError: true},
		{name: "unknown_account", accountID: "unknown_account", bucketID: "mock-bucket", wantError: true},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			server := Server{IAMClient: iam.ClientMock{}, S3Client: s3.ClientMock{}}

			if tc.useAdmin {
				server.AdminClient = admin.ClientMock{}
			}

			request := &spec.DriverRevokeBucketAccessRequest{
				AccountId:          tc.accountID,
				Protocol:           s3Protocol(),
				AuthenticationType: &spec.AuthenticationType{Type: spec.AuthenticationType_KEY},
				Buckets:            []*spec.DriverRevokeBucketAccessRequest_AccessedBucket{{BucketId: tc.bucketID}},
			}

			response, err := server.DriverRevokeBucketAccess(context.Background(), request)
			if tc.wantError {
				require.Error(t, err)
				require.Nil(t, response)

				return
			}

			require.NoError(t, err)
			require.NotNil(t, response)
		})
	}
}

func TestDriverGrantBucketAccessRejectsMultipleBuckets(t *testing.T) {
	t.Parallel()

	server := Server{}
	request := &spec.DriverGrantBucketAccessRequest{
		AccountName:        "account",
		Protocol:           s3Protocol(),
		AuthenticationType: &spec.AuthenticationType{Type: spec.AuthenticationType_KEY},
		Buckets: []*spec.DriverGrantBucketAccessRequest_AccessedBucket{
			{BucketId: "one", ObjectDataAccessMode: accessMode(spec.AccessMode_READ_WRITE)},
			{BucketId: "two", ObjectDataAccessMode: accessMode(spec.AccessMode_READ_WRITE)},
		},
	}

	_, err := server.DriverGrantBucketAccess(context.Background(), request)
	require.Equal(t, codes.OutOfRange, status.Code(err))
}

func TestDriverGrantBucketAccessValidation(t *testing.T) {
	t.Parallel()

	newRequest := func() *spec.DriverGrantBucketAccessRequest {
		return &spec.DriverGrantBucketAccessRequest{
			AccountName:        "account",
			Protocol:           s3Protocol(),
			AuthenticationType: &spec.AuthenticationType{Type: spec.AuthenticationType_KEY},
			Buckets: []*spec.DriverGrantBucketAccessRequest_AccessedBucket{{
				BucketId:             "bucket",
				ObjectDataAccessMode: accessMode(spec.AccessMode_READ_WRITE),
			}},
		}
	}

	testCases := []struct {
		name   string
		mutate func(*spec.DriverGrantBucketAccessRequest)
	}{
		{name: "missing protocol", mutate: func(request *spec.DriverGrantBucketAccessRequest) { request.Protocol = nil }},
		{name: "unsupported protocol", mutate: func(request *spec.DriverGrantBucketAccessRequest) {
			request.Protocol = &spec.ObjectProtocol{Type: spec.ObjectProtocol_AZURE}
		}},
		{name: "missing authentication", mutate: func(request *spec.DriverGrantBucketAccessRequest) { request.AuthenticationType = nil }},
		{name: "service account authentication", mutate: func(request *spec.DriverGrantBucketAccessRequest) {
			request.AuthenticationType = &spec.AuthenticationType{Type: spec.AuthenticationType_SERVICE_ACCOUNT}
		}},
		{name: "missing account name", mutate: func(request *spec.DriverGrantBucketAccessRequest) { request.AccountName = "" }},
		{name: "missing bucket", mutate: func(request *spec.DriverGrantBucketAccessRequest) { request.Buckets = nil }},
		{name: "missing bucket ID", mutate: func(request *spec.DriverGrantBucketAccessRequest) { request.Buckets[0].BucketId = "" }},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			request := newRequest()
			tc.mutate(request)

			server := Server{}
			_, err := server.DriverGrantBucketAccess(context.Background(), request)
			require.Equal(t, codes.InvalidArgument, status.Code(err))
		})
	}
}

func TestDriverRevokeBucketAccessValidation(t *testing.T) {
	t.Parallel()

	newRequest := func() *spec.DriverRevokeBucketAccessRequest {
		return &spec.DriverRevokeBucketAccessRequest{
			AccountId:          "iam_account",
			Protocol:           s3Protocol(),
			AuthenticationType: &spec.AuthenticationType{Type: spec.AuthenticationType_KEY},
			Buckets:            []*spec.DriverRevokeBucketAccessRequest_AccessedBucket{{BucketId: "bucket"}},
		}
	}

	testCases := []struct {
		name   string
		mutate func(*spec.DriverRevokeBucketAccessRequest)
	}{
		{name: "missing protocol", mutate: func(request *spec.DriverRevokeBucketAccessRequest) { request.Protocol = nil }},
		{name: "unsupported protocol", mutate: func(request *spec.DriverRevokeBucketAccessRequest) {
			request.Protocol = &spec.ObjectProtocol{Type: spec.ObjectProtocol_AZURE}
		}},
		{name: "missing authentication", mutate: func(request *spec.DriverRevokeBucketAccessRequest) { request.AuthenticationType = nil }},
		{name: "unsupported authentication", mutate: func(request *spec.DriverRevokeBucketAccessRequest) {
			request.AuthenticationType = &spec.AuthenticationType{Type: spec.AuthenticationType_SERVICE_ACCOUNT}
		}},
		{name: "missing bucket", mutate: func(request *spec.DriverRevokeBucketAccessRequest) { request.Buckets = nil }},
		{name: "nil bucket", mutate: func(request *spec.DriverRevokeBucketAccessRequest) { request.Buckets[0] = nil }},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			request := newRequest()
			tc.mutate(request)

			server := Server{}
			_, err := server.DriverRevokeBucketAccess(context.Background(), request)
			require.Equal(t, codes.InvalidArgument, status.Code(err))
		})
	}
}

func TestAccessPolicyActions(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name             string
		bucket           *spec.DriverGrantBucketAccessRequest_AccessedBucket
		wantBucket       []string
		wantObjects      []string
		wantInvalidInput bool
	}{
		{
			name: "object data read-only",
			bucket: &spec.DriverGrantBucketAccessRequest_AccessedBucket{
				ObjectDataAccessMode: accessMode(spec.AccessMode_READ_ONLY),
			},
			wantBucket:  []string{"s3:ListBucket"},
			wantObjects: []string{"s3:GetObject", "s3:GetObjectVersion"},
		},
		{
			name: "object data write-only",
			bucket: &spec.DriverGrantBucketAccessRequest_AccessedBucket{
				ObjectDataAccessMode: accessMode(spec.AccessMode_WRITE_ONLY),
			},
			wantObjects: []string{"s3:PutObject", "s3:DeleteObject", "s3:DeleteObjectVersion"},
		},
		{
			name: "object data read-write",
			bucket: &spec.DriverGrantBucketAccessRequest_AccessedBucket{
				ObjectDataAccessMode: accessMode(spec.AccessMode_READ_WRITE),
			},
			wantBucket:  []string{"s3:ListBucket"},
			wantObjects: []string{"s3:GetObject", "s3:GetObjectVersion", "s3:PutObject", "s3:DeleteObject", "s3:DeleteObjectVersion"},
		},
		{
			name: "object metadata read-write",
			bucket: &spec.DriverGrantBucketAccessRequest_AccessedBucket{
				ObjectMetadataAccessMode: accessMode(spec.AccessMode_READ_WRITE),
			},
			wantObjects: []string{"s3:GetObjectTagging", "s3:GetObjectVersionTagging", "s3:PutObjectTagging", "s3:PutObjectVersionTagging", "s3:DeleteObjectTagging", "s3:DeleteObjectVersionTagging"},
		},
		{
			name: "object metadata read-only",
			bucket: &spec.DriverGrantBucketAccessRequest_AccessedBucket{
				ObjectMetadataAccessMode: accessMode(spec.AccessMode_READ_ONLY),
			},
			wantObjects: []string{"s3:GetObjectTagging", "s3:GetObjectVersionTagging"},
		},
		{
			name: "object metadata write-only",
			bucket: &spec.DriverGrantBucketAccessRequest_AccessedBucket{
				ObjectMetadataAccessMode: accessMode(spec.AccessMode_WRITE_ONLY),
			},
			wantObjects: []string{"s3:PutObjectTagging", "s3:PutObjectVersionTagging", "s3:DeleteObjectTagging", "s3:DeleteObjectVersionTagging"},
		},
		{
			name: "bucket metadata read-write",
			bucket: &spec.DriverGrantBucketAccessRequest_AccessedBucket{
				BucketMetadataAccessMode: accessMode(spec.AccessMode_READ_WRITE),
			},
			wantBucket: []string{"s3:GetBucketTagging", "s3:PutBucketTagging", "s3:DeleteBucketTagging"},
		},
		{
			name: "bucket metadata read-only",
			bucket: &spec.DriverGrantBucketAccessRequest_AccessedBucket{
				BucketMetadataAccessMode: accessMode(spec.AccessMode_READ_ONLY),
			},
			wantBucket: []string{"s3:GetBucketTagging"},
		},
		{
			name: "bucket metadata write-only",
			bucket: &spec.DriverGrantBucketAccessRequest_AccessedBucket{
				BucketMetadataAccessMode: accessMode(spec.AccessMode_WRITE_ONLY),
			},
			wantBucket: []string{"s3:PutBucketTagging", "s3:DeleteBucketTagging"},
		},
		{
			name: "any grants no extra permissions",
			bucket: &spec.DriverGrantBucketAccessRequest_AccessedBucket{
				ObjectDataAccessMode:     accessMode(spec.AccessMode_READ_ONLY),
				ObjectMetadataAccessMode: accessMode(spec.AccessMode_ANY),
				BucketMetadataAccessMode: accessMode(spec.AccessMode_ANY),
			},
			wantBucket:  []string{"s3:ListBucket"},
			wantObjects: []string{"s3:GetObject", "s3:GetObjectVersion"},
		},
		{
			name:             "no permissions",
			bucket:           &spec.DriverGrantBucketAccessRequest_AccessedBucket{ObjectDataAccessMode: accessMode(spec.AccessMode_ANY)},
			wantInvalidInput: true,
		},
		{
			name:             "unknown mode",
			bucket:           &spec.DriverGrantBucketAccessRequest_AccessedBucket{ObjectDataAccessMode: accessMode(spec.AccessMode_UNKNOWN)},
			wantInvalidInput: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			bucketActions, objectActions, err := accessPolicyActions(tc.bucket)
			if tc.wantInvalidInput {
				require.Equal(t, codes.InvalidArgument, status.Code(err))

				return
			}

			require.NoError(t, err)
			require.Equal(t, tc.wantBucket, bucketActions)
			require.Equal(t, tc.wantObjects, objectActions)
		})
	}
}
