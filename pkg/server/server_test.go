package server

import (
	"bytes"
	"context"
	"os"
	"testing"

	"github.com/cloudian/cloudian-cosi-driver/pkg/identity"
	"github.com/cloudian/cloudian-cosi-driver/pkg/provisioner"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	klog "k8s.io/klog/v2"
	spec "sigs.k8s.io/container-object-storage-interface/proto"
)

func TestServerRegistersV1alpha1Services(t *testing.T) {
	t.Parallel()

	server := newGRPCServer()
	t.Cleanup(server.Stop)
	spec.RegisterIdentityServer(server, &identity.Server{})
	spec.RegisterProvisionerServer(server, &provisioner.Server{})

	services := server.GetServiceInfo()
	require.Len(t, services, 2)
	require.Contains(t, services, "cosi.v1alpha1.Identity")
	require.Contains(t, services, "cosi.v1alpha1.Provisioner")

	methods := make([]string, 0, len(services["cosi.v1alpha1.Provisioner"].Methods))
	for _, method := range services["cosi.v1alpha1.Provisioner"].Methods {
		methods = append(methods, method.Name)
	}

	require.ElementsMatch(t, []string{
		"DriverCreateBucket", "DriverDeleteBucket", "DriverGrantBucketAccess", "DriverRevokeBucketAccess",
	}, methods)
}

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
		Credentials: map[string]*spec.CredentialDetails{
			"s3": {Secrets: map[string]string{"accessSecretKey": secret}},
		},
	}
	interceptor := loggingInterceptor()

	actualResponse, err := interceptor(
		context.Background(),
		&spec.DriverGrantBucketAccessRequest{},
		&grpc.UnaryServerInfo{FullMethod: "/cosi.v1alpha1.Provisioner/DriverGrantBucketAccess"},
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
