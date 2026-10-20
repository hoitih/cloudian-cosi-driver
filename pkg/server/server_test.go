package server

import (
	"bytes"
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	klog "k8s.io/klog/v2"
	spec "sigs.k8s.io/container-object-storage-interface/proto"
)

//nolint:paralleltest // This test changes process-global klog output.
func TestLoggingInterceptorRedactsGrantCredentials(t *testing.T) {
	var logs bytes.Buffer

	klog.LogToStderr(false)
	klog.SetOutput(&logs)
	t.Cleanup(func() {
		klog.SetOutput(os.Stderr)
		klog.LogToStderr(true)
	})

	secret := "test-secret-that-must-not-be-logged"
	response := &spec.DriverGrantBucketAccessResponse{
		Credentials: &spec.CredentialInfo{
			S3: &spec.S3CredentialInfo{AccessSecretKey: secret},
		},
	}
	interceptor := loggingInterceptor()

	actualResponse, err := interceptor(
		context.Background(),
		&spec.DriverGrantBucketAccessRequest{},
		&grpc.UnaryServerInfo{FullMethod: "/cosi.Provisioner/DriverGrantBucketAccess"},
		func(context.Context, interface{}) (interface{}, error) {
			return response, nil
		},
	)

	klog.Flush()

	require.NoError(t, err)
	require.Same(t, response, actualResponse)
	require.Contains(t, logs.String(), "DriverGrantBucketAccess")
	require.NotContains(t, logs.String(), secret)
}
