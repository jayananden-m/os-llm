package registry

import (
	"context"
	"io"
	"log/slog"
	"reflect"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/jayananden-m/os-llm/internal/config"
	"github.com/jayananden-m/os-llm/internal/gateway"
)

func newTestRegistry(t *testing.T) (*Registry, *gateway.Gateway) {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := &config.Config{
		QueueTimeout:     config.Duration{Duration: time.Second},
		ModelDeployments: &config.ModelDeploymentsSource{Namespace: "os-llm"},
	}
	gw, err := gateway.New(cfg, log, prometheus.NewRegistry())
	if err != nil {
		t.Fatal(err)
	}
	// kube is nil: these tests cover the model set, not EndpointSlice discovery.
	return New(context.Background(), gw, nil, "os-llm", log), gw
}

func TestUpsertAddsModel(t *testing.T) {
	r, gw := newTestRegistry(t)
	r.Upsert("qwen", "qwen2.5-0.5b-instruct", "", 2, 4)
	if got := gw.Models(); !reflect.DeepEqual(got, []string{"qwen2.5-0.5b-instruct"}) {
		t.Fatalf("models = %v", got)
	}
}

func TestUpsertUpdatesLimitsInPlace(t *testing.T) {
	r, gw := newTestRegistry(t)
	r.Upsert("qwen", "qwen", "", 2, 4)
	before := gw.Pool("qwen")
	r.Upsert("qwen", "qwen", "", 8, 32)
	if gw.Pool("qwen") != before {
		t.Fatal("limit change replaced the pool; in-flight counts would be lost")
	}
}

func TestModelRenameMovesPool(t *testing.T) {
	r, gw := newTestRegistry(t)
	r.Upsert("qwen", "qwen-v1", "", 2, 4)
	r.Upsert("qwen", "qwen-v2", "", 2, 4)
	if got := gw.Models(); !reflect.DeepEqual(got, []string{"qwen-v2"}) {
		t.Fatalf("models = %v, want [qwen-v2]", got)
	}
}

func TestRemoveStopsServing(t *testing.T) {
	r, gw := newTestRegistry(t)
	r.Upsert("qwen", "qwen", "", 2, 4)
	r.Remove("qwen")
	if got := gw.Models(); len(got) != 0 {
		t.Fatalf("models = %v, want none", got)
	}
}

func TestIgnoresEmptyModel(t *testing.T) {
	r, gw := newTestRegistry(t)
	r.Upsert("broken", "", "", 2, 4)
	if got := gw.Models(); len(got) != 0 {
		t.Fatalf("models = %v, want none", got)
	}
}
