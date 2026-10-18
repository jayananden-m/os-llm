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

	"github.com/jayananden-m/os-llm/internal/config"
)

const maxBodyBytes = 1 << 20 // 1 MiB

type Gateway struct {
	pools        map[string]*Pool
	queueTimeout time.Duration
	log          *slog.Logger
}

func New(cfg *config.Config, log *slog.Logger) (*Gateway, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	timeout := cfg.QueueTimeout.Duration
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	g := &Gateway{pools: map[string]*Pool{}, queueTimeout: timeout, log: log}
	for _, m := range cfg.Models {
		reps := make([]*Replica, 0, len(m.Replicas))
		for _, addr := range m.Replicas {
			u, err := url.Parse(addr)
			if err != nil {
				return nil, err
			}
			reps = append(reps, &Replica{Addr: addr, proxy: newProxy(u, log)})
		}
		g.pools[m.Name] = newPool(reps, m.MaxInFlightPerReplica, m.MaxQueue)
	}
	return g, nil
}

func newProxy(target *url.URL, log *slog.Logger) *httputil.ReverseProxy {
	p := httputil.NewSingleHostReverseProxy(target)
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
	mux.HandleFunc("GET /v1/models", g.handleModels)
	mux.HandleFunc("POST /v1/chat/completions", g.handleInference)
	mux.HandleFunc("POST /v1/completions", g.handleInference)
	return mux
}

func (g *Gateway) handleInference(w http.ResponseWriter, r *http.Request) {
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

	// The timeout covers waiting in the queue only, not generation.
	ctx, cancel := context.WithTimeout(r.Context(), g.queueTimeout)
	rep, err := pool.Acquire(ctx)
	cancel()
	switch {
	case errors.Is(err, ErrQueueFull):
		w.Header().Set("Retry-After", "1")
		writeError(w, http.StatusTooManyRequests, "queue_full", "all replicas busy and queue full, retry shortly")
		return
	case errors.Is(err, context.DeadlineExceeded):
		w.Header().Set("Retry-After", "1")
		writeError(w, http.StatusServiceUnavailable, "queue_timeout", "timed out waiting for a free replica")
		return
	case err != nil:
		return // client disconnected while queued
	}
	defer pool.Release(rep)

	g.log.Info("routed", "model", req.Model, "replica", rep.Addr)
	r.Body = io.NopCloser(bytes.NewReader(body))
	r.ContentLength = int64(len(body))
	rep.proxy.ServeHTTP(w, r)
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
