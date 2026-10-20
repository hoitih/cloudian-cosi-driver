package identity

import (
	"context"

	spec "sigs.k8s.io/container-object-storage-interface/proto"
)

type Server struct {
	spec.UnimplementedIdentityServer
}

func (s *Server) DriverGetInfo(_ context.Context, _ *spec.DriverGetInfoRequest) (*spec.DriverGetInfoResponse, error) {
	return &spec.DriverGetInfoResponse{
		Name: "cloudian-cosi-driver",
		SupportedProtocols: []*spec.ObjectProtocol{
			{Type: spec.ObjectProtocol_S3},
		},
	}, nil
}
