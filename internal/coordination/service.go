// Package coordination implements concord's CoordinationService: the single RPC
// seam through which hook clients and tests drive the version check and the
// intent registry.
package coordination

import (
	"context"

	"connectrpc.com/connect"
	concordv1 "github.com/Kminhas21/concord/gen/concord/v1"
	"github.com/Kminhas21/concord/gen/concord/v1/concordv1connect"
)

// Service implements concordv1connect.CoordinationServiceHandler.
type Service struct{}

// NewService constructs a Service.
func NewService() *Service {
	return &Service{}
}

var _ concordv1connect.CoordinationServiceHandler = (*Service)(nil)

// Ping echoes the request message, proving the seam is wired end to end.
func (s *Service) Ping(_ context.Context, req *connect.Request[concordv1.PingRequest]) (*connect.Response[concordv1.PingResponse], error) {
	return connect.NewResponse(&concordv1.PingResponse{Message: req.Msg.GetMessage()}), nil
}
