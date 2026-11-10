package provisioner

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/cloudian/cloudian-cosi-driver/pkg/clients/admin"
	"github.com/cloudian/cloudian-cosi-driver/pkg/clients/iam"
	"github.com/cloudian/cloudian-cosi-driver/pkg/clients/s3"
	"github.com/cloudian/cloudian-cosi-driver/pkg/config"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
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

type bucketCreationRecorder struct {
	S3Client
	calls atomic.Int32
}

func (client *bucketCreationRecorder) CreateBucket(
	_ context.Context, _ *awss3.CreateBucketInput, _ ...func(*awss3.Options),
) (*awss3.CreateBucketOutput, error) {
	client.calls.Add(1)

	return nil, errors.New("unexpected backend bucket creation")
}

func TestDriverCreateBucketRejectsInvalidConfiguration(t *testing.T) {
	t.Parallel()

	const (
		bufferSize = 1024 * 1024
		rpcTimeout = 5 * time.Second
	)

	backend := &bucketCreationRecorder{}
	listener := bufconn.Listen(bufferSize)
	grpcServer := grpc.NewServer()
	spec.RegisterProvisionerServer(grpcServer, &Server{S3Client: backend})
	t.Cleanup(grpcServer.Stop)

	go func() {
		if err := grpcServer.Serve(listener); err != nil {
			t.Errorf("serve COSI gRPC: %v", err)
		}
	}()

	connection, err := grpc.NewClient("passthrough:///cosi-test",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			connection, err := listener.DialContext(ctx)
			if err != nil {
				return nil, fmt.Errorf("dial COSI test server: %w", err)
			}

			return connection, nil
		}),
	)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, connection.Close())
	})

	client := spec.NewProvisionerClient(connection)

	testCases := []struct {
		name       string
		parameters map[string]string
		wantError  string
	}{
		{
			name: "invalid versioning", parameters: map[string]string{"versioning": "invalid"},
			wantError: "versioning must be Enabled or Suspended",
		},
		{
			name: "empty versioning", parameters: map[string]string{"versioning": ""},
			wantError: "versioning must be Enabled or Suspended",
		},
		{
			name: "invalid object lock", parameters: map[string]string{"objectLockEnabled": "yes"},
			wantError: "objectLockEnabled must be true or false",
		},
		{
			name: "empty object lock", parameters: map[string]string{"objectLockEnabled": ""},
			wantError: "objectLockEnabled must be true or false",
		},
		{
			name: "retention without lock", parameters: map[string]string{"retentionMode": "GOVERNANCE", "retentionDays": "30"},
			wantError: "retention requires objectLockEnabled=true",
		},
		{
			name: "suspended locked bucket", parameters: map[string]string{"objectLockEnabled": "true", "versioning": "Suspended"},
			wantError: "object lock requires enabled versioning",
		},
		{
			name: "invalid retention mode", parameters: map[string]string{"objectLockEnabled": "true", "retentionMode": "invalid", "retentionDays": "30"},
			wantError: "retentionMode must be GOVERNANCE or COMPLIANCE",
		},
		{
			name: "missing retention mode", parameters: map[string]string{"objectLockEnabled": "true", "retentionDays": "30"},
			wantError: "retentionMode must be GOVERNANCE or COMPLIANCE",
		},
		{
			name: "missing retention duration", parameters: map[string]string{"objectLockEnabled": "true", "retentionMode": "GOVERNANCE"},
			wantError: "retention requires exactly one of retentionDays or retentionYears",
		},
		{
			name:       "conflicting durations",
			parameters: map[string]string{"objectLockEnabled": "true", "retentionMode": "GOVERNANCE", "retentionDays": "30", "retentionYears": "1"},
			wantError:  "retention requires exactly one of retentionDays or retentionYears",
		},
		{
			name: "negative retention", parameters: map[string]string{"objectLockEnabled": "true", "retentionMode": "GOVERNANCE", "retentionDays": "-1"},
			wantError: "retentionDays must be a positive 32-bit integer",
		},
		{
			name: "zero retention", parameters: map[string]string{"objectLockEnabled": "true", "retentionMode": "GOVERNANCE", "retentionDays": "0"},
			wantError: "retentionDays must be a positive 32-bit integer",
		},
		{
			name: "non-numeric retention", parameters: map[string]string{"objectLockEnabled": "true", "retentionMode": "GOVERNANCE", "retentionDays": "abc"},
			wantError: "retentionDays must be a positive 32-bit integer",
		},
		{
			name: "retention overflow", parameters: map[string]string{"objectLockEnabled": "true", "retentionMode": "COMPLIANCE", "retentionYears": "2147483648"},
			wantError: "retentionYears must be a positive 32-bit integer",
		},
		{
			name: "invalid lifecycle JSON", parameters: map[string]string{"lifecyclePolicy": "not JSON"},
			wantError: "invalid lifecyclePolicy JSON",
		},
		{
			name: "unknown lifecycle field", parameters: map[string]string{"lifecyclePolicy": `{"Rulez":[]}`},
			wantError: "unknown field",
		},
		{
			name: "multiple JSON values", parameters: map[string]string{"lifecyclePolicy": `{} {}`},
			wantError: "lifecyclePolicy must contain exactly one JSON object",
		},
		{
			name: "empty lifecycle", parameters: map[string]string{"lifecyclePolicy": `{"Rules":[]}`},
			wantError: "lifecyclePolicy must contain at least one rule",
		},
		{
			name: "null lifecycle", parameters: map[string]string{"lifecyclePolicy": `null`},
			wantError: "lifecyclePolicy must contain at least one rule",
		},
		{
			name: "missing lifecycle status", parameters: map[string]string{"lifecyclePolicy": `{"Rules":[{"Filter":{"Prefix":""},"Expiration":{"Days":30}}]}`},
			wantError: "each lifecycle rule must have Status Enabled or Disabled",
		},
		{
			name: "missing lifecycle filter", parameters: map[string]string{"lifecyclePolicy": `{"Rules":[{"Status":"Enabled","Expiration":{"Days":30}}]}`},
			wantError: "each lifecycle rule must specify Filter",
		},
		{
			name: "missing lifecycle action", parameters: map[string]string{"lifecyclePolicy": `{"Rules":[{"Status":"Enabled","Filter":{"Prefix":""}}]}`},
			wantError: "each lifecycle rule must specify an action",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			ctx, cancel := context.WithTimeout(context.Background(), rpcTimeout)
			defer cancel()

			response, err := client.DriverCreateBucket(ctx, &spec.DriverCreateBucketRequest{
				BucketId: "bucket", Protocols: []*spec.ObjectProtocol{s3Protocol()}, Parameters: tc.parameters,
			})
			require.Equal(t, codes.InvalidArgument, status.Code(err))
			require.Contains(t, status.Convert(err).Message(), "invalid bucket parameters: ")
			require.Contains(t, status.Convert(err).Message(), tc.wantError)
			require.Nil(t, response)
			require.Zero(t, backend.calls.Load(), "invalid parameters must not reach the backend")
		})
	}
}

func TestDriverCreateBucketConfigurationOverHTTP(t *testing.T) {
	t.Parallel()

	type bucketPayload struct {
		Status            string `xml:"Status"`
		ObjectLockEnabled string `xml:"ObjectLockEnabled"`
		RetentionMode     string `xml:"Rule>DefaultRetention>Mode"`
		RetentionDays     int    `xml:"Rule>DefaultRetention>Days"`
		ExpirationDays    int    `xml:"Rule>Expiration>Days"`
		NoncurrentDays    int    `xml:"Rule>NoncurrentVersionExpiration>NoncurrentDays"`
	}

	type capturedRequest struct {
		method        string
		path          string
		query         string
		storagePolicy string
		objectLock    string
		body          []byte
	}

	requests := make(chan capturedRequest, 4)
	backend := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Errorf("read S3 request body: %v", err)
			writer.WriteHeader(http.StatusBadRequest)

			return
		}

		requests <- capturedRequest{
			method: request.Method, path: request.URL.Path, query: request.URL.RawQuery,
			storagePolicy: request.Header.Get("X-Gmt-Policyid"),
			objectLock:    request.Header.Get("X-Amz-Bucket-Object-Lock-Enabled"), body: body,
		}

		writer.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(backend.Close)

	client := awss3.NewFromConfig(aws.Config{
		Region: "test-region", Credentials: aws.AnonymousCredentials{},
	}, func(options *awss3.Options) {
		options.BaseEndpoint = aws.String(backend.URL)
		options.UsePathStyle = true
		options.RetryMaxAttempts = 1
	})
	server := Server{S3Client: client}
	response, err := server.DriverCreateBucket(context.Background(), &spec.DriverCreateBucketRequest{
		BucketId: "configured-bucket",
		Parameters: map[string]string{
			"storagePolicyID": "policy-1", "objectLockEnabled": "true",
			"retentionMode": "COMPLIANCE", "retentionDays": "30",
			"lifecyclePolicy": `{"Rules":[{"Status":"Enabled","Filter":{"Prefix":""},
				"Expiration":{"Days":30},"NoncurrentVersionExpiration":{"NoncurrentDays":1}}]}`,
		},
	})
	require.NoError(t, err)
	require.NotNil(t, response)
	require.Len(t, requests, 4)

	create := <-requests
	require.Equal(t, "policy-1", create.storagePolicy)
	require.Equal(t, "true", create.objectLock)

	for _, operation := range []string{"versioning", "object-lock", "lifecycle"} {
		request := <-requests
		require.Equal(t, http.MethodPut, request.method)
		require.Equal(t, "/configured-bucket", request.path)
		require.Contains(t, request.query, operation)

		var payload bucketPayload
		require.NoError(t, xml.Unmarshal(request.body, &payload))

		switch operation {
		case "versioning":
			require.Equal(t, "Enabled", payload.Status)
		case "object-lock":
			require.Equal(t, "Enabled", payload.ObjectLockEnabled)
			require.Equal(t, "COMPLIANCE", payload.RetentionMode)
			require.Equal(t, 30, payload.RetentionDays)
		case "lifecycle":
			require.Equal(t, 30, payload.ExpirationDays)
			require.Equal(t, 1, payload.NoncurrentDays)
		}
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
