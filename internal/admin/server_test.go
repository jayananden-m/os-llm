package admin

import (
	"context"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	osllmv1 "github.com/jayananden-m/os-llm/gen/osllm/v1"
	"github.com/jayananden-m/os-llm/internal/config"
	"github.com/jayananden-m/os-llm/internal/gateway"
)

// newClient runs the admin server on an in-memory listener: real gRPC, no ports.
func newClient(t *testing.T) osllmv1.AdminClient {
	t.Helper()
	cfg := &config.Config{
		QueueTimeout: config.Duration{Duration: time.Second},
		Models: []config.Model{{
			Name:                  "qwen",
			Replicas:              []string{"http://10.0.0.1:8000", "http://10.0.0.2:8000"},
			MaxInFlightPerReplica: 2,
			MaxQueue:              4,
		}},
	}
	gw, err := gateway.New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), prometheus.NewRegistry())
	if err != nil {
		t.Fatal(err)
	}

	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer()
	osllmv1.RegisterAdminServer(srv, NewServer(gw))
	go srv.Serve(lis)
	t.Cleanup(srv.Stop)

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return osllmv1.NewAdminClient(conn)
}

func TestGetStatusAllModels(t *testing.T) {
	resp, err := newClient(t).GetStatus(context.Background(), &osllmv1.GetStatusRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Models) != 1 {
		t.Fatalf("models = %d, want 1", len(resp.Models))
	}
	m := resp.Models[0]
	if m.Model != "qwen" || m.Capacity != 4 || m.Queued != 0 || len(m.Replicas) != 2 {
		t.Fatalf("status = %+v", m)
	}
}

func TestGetStatusUnknownModel(t *testing.T) {
	_, err := newClient(t).GetStatus(context.Background(), &osllmv1.GetStatusRequest{Model: "gpt-9"})
	if status.Code(err) != codes.NotFound {
		t.Fatalf("err = %v, want NotFound", err)
	}
}
