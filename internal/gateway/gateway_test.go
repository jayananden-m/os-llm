package gateway

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/jayananden-m/os-llm/internal/config"
)

func newTestGateway(t *testing.T, backendURL string, maxPer, maxQueue int) http.Handler {
	t.Helper()
	cfg := &config.Config{
		QueueTimeout: config.Duration{Duration: time.Second},
		Models: []config.Model{{
			Name:                  "small",
			Replicas:              []string{backendURL},
			MaxInFlightPerReplica: maxPer,
			MaxQueue:              maxQueue,
		}},
	}
	g, err := New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), prometheus.NewRegistry())
	if err != nil {
		t.Fatal(err)
	}
	return g.Routes()
}

func post(h http.Handler, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func errType(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var resp struct {
		Error struct {
			Type string `json:"type"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("error body is not JSON: %v", err)
	}
	return resp.Error.Type
}

func TestRoutesToReplica(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"object":"chat.completion"}`)
	}))
	defer backend.Close()

	rec := post(newTestGateway(t, backend.URL, 1, 0), `{"model":"small"}`)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "chat.completion") {
		t.Fatalf("got %d %q", rec.Code, rec.Body.String())
	}
}

func TestUnknownModel(t *testing.T) {
	rec := post(newTestGateway(t, "http://unused:1", 1, 0), `{"model":"gpt-9"}`)
	if rec.Code != http.StatusNotFound || errType(t, rec) != "model_not_found" {
		t.Fatalf("got %d %q", rec.Code, rec.Body.String())
	}
}

func TestBadJSON(t *testing.T) {
	rec := post(newTestGateway(t, "http://unused:1", 1, 0), `{oops`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("got %d", rec.Code)
	}
}

func TestQueueFullReturns429(t *testing.T) {
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started <- struct{}{}
		<-release
	}))
	defer backend.Close()

	h := newTestGateway(t, backend.URL, 1, 0) // one slot, no queue
	done := make(chan int)
	go func() { done <- post(h, `{"model":"small"}`).Code }()
	<-started

	rec := post(h, `{"model":"small"}`)
	if rec.Code != http.StatusTooManyRequests || errType(t, rec) != "queue_full" {
		t.Fatalf("got %d %q", rec.Code, rec.Body.String())
	}
	close(release)
	if code := <-done; code != http.StatusOK {
		t.Fatalf("first request got %d", code)
	}
}

func TestModelsList(t *testing.T) {
	h := newTestGateway(t, "http://unused:1", 1, 0)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/models", nil))
	if !strings.Contains(rec.Body.String(), `"id":"small"`) {
		t.Fatalf("models = %s", rec.Body.String())
	}
}

func TestMetricsRecorded(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{}`)
	}))
	defer backend.Close()

	h := newTestGateway(t, backend.URL, 1, 0)
	post(h, `{"model":"small"}`)
	post(h, `{"model":"gpt-9"}`)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := rec.Body.String()
	for _, want := range []string{
		`osllm_gateway_requests_total{code="200",model="small"} 1`,
		`osllm_gateway_requests_total{code="404",model="unknown"} 1`,
		`osllm_gateway_capacity_slots{model="small"} 1`,
		`osllm_gateway_queue_depth{model="small"} 0`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics missing %q", want)
		}
	}
}
