package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sort"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/jayananden-m/os-llm/internal/config"
)

const maxBodyBytes = 1 << 20 // 1 MiB

var tracer = otel.Tracer("github.com/jayananden-m/os-llm/gateway")

type Gateway struct {
	pools        map[string]*Pool
	queueTimeout time.Duration
	log          *slog.Logger
	reg          *prometheus.Registry
	metrics      *metrics
}

func New(cfg *config.Config, log *slog.Logger, reg *prometheus.Registry) (*Gateway, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	timeout := cfg.QueueTimeout.Duration
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	g := &Gateway{pools: map[string]*Pool{}, queueTimeout: timeout, log: log, reg: reg}
	makeReplica := func(addr string) (*Replica, error) {
		u, err := url.Parse(addr)
		if err != nil {
			return nil, err
		}
		return &Replica{Addr: addr, proxy: newProxy(u, log)}, nil
	}
	for _, m := range cfg.Models {
		pool := newPool(m.MaxInFlightPerReplica, m.MaxQueue, makeReplica)
		// Static replicas load now. Discovered ones arrive later via SetReplicas.
		if _, _, err := pool.SetReplicas(m.Replicas); err != nil {
			return nil, err
		}
		g.pools[m.Name] = pool
	}
	g.metrics = newMetrics(reg, g)
	return g, nil
}

// Pool returns the replica pool for a model, or nil if it is not served.
func (g *Gateway) Pool(model string) *Pool { return g.pools[model] }

func newProxy(target *url.URL, log *slog.Logger) *httputil.ReverseProxy {
	p := httputil.NewSingleHostReverseProxy(target)
	// Client span per replica call, and traceparent injected so vLLM can join the trace.
	p.Transport = otelhttp.NewTransport(http.DefaultTransport)
	p.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		if errors.Is(err, context.Canceled) {
			return // client went away
		}
		log.Error("replica request failed", "replica", target.Host, "err", err)
		writeError(w, http.StatusBadGateway, "upstream_error", "model replica unavailable")
	}
	return p
}

func (g *Gateway) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.Handle("GET /metrics", promhttp.HandlerFor(g.reg, promhttp.HandlerOpts{}))
	mux.HandleFunc("GET /v1/models", g.handleModels)
	mux.HandleFunc("POST /v1/chat/completions", g.handleInference)
	mux.HandleFunc("POST /v1/completions", g.handleInference)

	// Server span per request. Probes and scrapes are skipped to keep traces clean.
	return otelhttp.NewHandler(mux, "gateway",
		otelhttp.WithSpanNameFormatter(func(_ string, r *http.Request) string {
			return r.Method + " " + r.URL.Path
		}),
		otelhttp.WithFilter(func(r *http.Request) bool {
			return r.URL.Path != "/healthz" && r.URL.Path != "/metrics"
		}),
	)
}

func (g *Gateway) handleInference(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	rec := &statusRecorder{ResponseWriter: w}
	w = rec
	model := "unknown" // label stays bounded until the model is known to exist
	defer func() { g.metrics.observe(model, rec.status, time.Since(start)) }()

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request_error", "could not read request body")
		return
	}
	var req struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request_error", "invalid JSON")
		return
	}
	if req.Model == "" {
		writeError(w, http.StatusBadRequest, "invalid_request_error", "model is required")
		return
	}
	pool, ok := g.pools[req.Model]
	if !ok {
		writeError(w, http.StatusNotFound, "model_not_found", "unknown model: "+req.Model)
		return
	}
	model = req.Model
	trace.SpanFromContext(r.Context()).SetAttributes(attribute.String("llm.model", model))

	rep, ok := g.admit(w, r, pool, model)
	if !ok {
		return
	}
	defer pool.Release(rep)

	g.log.Info("routed", "model", model, "replica", rep.Addr)
	r.Body = io.NopCloser(bytes.NewReader(body))
	r.ContentLength = int64(len(body))
	rep.proxy.ServeHTTP(w, r)
}

// admit waits for a replica slot inside its own span, and writes the error
// response itself when the request is rejected.
func (g *Gateway) admit(w http.ResponseWriter, r *http.Request, pool *Pool, model string) (*Replica, bool) {
	ctx, span := tracer.Start(r.Context(), "queue.wait")
	defer span.End()

	ctx, cancel := context.WithTimeout(ctx, g.queueTimeout)
	defer cancel()

	waitStart := time.Now()
	rep, err := pool.Acquire(ctx)
	switch {
	case err == nil:
		g.metrics.queueWait.WithLabelValues(model).Observe(time.Since(waitStart).Seconds())
		span.SetAttributes(attribute.String("llm.replica", rep.Addr))
		return rep, true
	case errors.Is(err, ErrQueueFull):
		g.metrics.rejected.WithLabelValues(model, "queue_full").Inc()
		span.SetStatus(codes.Error, "queue full")
		w.Header().Set("Retry-After", "1")
		writeError(w, http.StatusTooManyRequests, "queue_full", "all replicas busy and queue full, retry shortly")
	case errors.Is(err, context.DeadlineExceeded):
		g.metrics.rejected.WithLabelValues(model, "queue_timeout").Inc()
		span.SetStatus(codes.Error, "queue timeout")
		w.Header().Set("Retry-After", "1")
		writeError(w, http.StatusServiceUnavailable, "queue_timeout", "timed out waiting for a free replica")
	default:
		span.SetStatus(codes.Error, "client cancelled") // client disconnected while queued
	}
	return nil, false
}

func (g *Gateway) handleModels(w http.ResponseWriter, r *http.Request) {
	names := make([]string, 0, len(g.pools))
	for n := range g.pools {
		names = append(names, n)
	}
	sort.Strings(names)

	data := make([]map[string]any, 0, len(names))
	for _, n := range names {
		data = append(data, map[string]any{"id": n, "object": "model", "owned_by": "os-llm"})
	}
	writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": data})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// writeError uses OpenAI's error shape so existing SDKs can parse it.
func writeError(w http.ResponseWriter, status int, errType, msg string) {
	writeJSON(w, status, map[string]any{
		"error": map[string]any{"message": msg, "type": errType},
	})
}
