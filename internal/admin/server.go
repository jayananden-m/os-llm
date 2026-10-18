// Package admin serves the gateway's gRPC admin API.
package admin

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	osllmv1 "github.com/jayananden-m/os-llm/gen/osllm/v1"
	"github.com/jayananden-m/os-llm/internal/gateway"
)

type Server struct {
	osllmv1.UnimplementedAdminServer
	gw *gateway.Gateway
}

func NewServer(gw *gateway.Gateway) *Server { return &Server{gw: gw} }

// GetStatus reads the same pool snapshots the Prometheus collector uses,
// so the CLI and the dashboards always agree.
func (s *Server) GetStatus(_ context.Context, req *osllmv1.GetStatusRequest) (*osllmv1.GetStatusResponse, error) {
	resp := &osllmv1.GetStatusResponse{}
	for _, name := range s.gw.Models() {
		if req.GetModel() != "" && req.GetModel() != name {
			continue
		}
		pool := s.gw.Pool(name)
		if pool == nil {
			continue // removed between Models() and Pool()
		}
		reps, queued, capacity := pool.Snapshot()
		ms := &osllmv1.ModelStatus{Model: name, Capacity: int32(capacity), Queued: int32(queued)}
		for _, r := range reps {
			ms.Replicas = append(ms.Replicas, &osllmv1.ReplicaStatus{Address: r.Addr, InFlight: int32(r.InFlight)})
		}
		resp.Models = append(resp.Models, ms)
	}
	if req.GetModel() != "" && len(resp.Models) == 0 {
		return nil, status.Errorf(codes.NotFound, "model %q is not served", req.GetModel())
	}
	return resp, nil
}
