package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"

	"github.com/jayananden-m/os-llm/internal/config"
	"github.com/jayananden-m/os-llm/internal/gateway"
	"github.com/jayananden-m/os-llm/internal/telemetry"
)

func main() {
	cfgPath := flag.String("config", "configs/gateway.local.json", "path to gateway config")
	flag.Parse()

	log := slog.New(slog.NewTextHandler(os.Stdout, nil))

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Error("load config", "err", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	shutdownTracing, err := telemetry.SetupTracing(ctx, "os-llm-gateway")
	if err != nil {
		log.Error("setup tracing", "err", err)
		os.Exit(1)
	}

	reg := prometheus.NewRegistry()
	reg.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)

	gw, err := gateway.New(cfg, log, reg)
	if err != nil {
		log.Error("build gateway", "err", err)
		os.Exit(1)
	}

	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           gw.Routes(),
		ReadHeaderTimeout: 10 * time.Second,
		// No WriteTimeout: a generation can legitimately take minutes.
	}

	errCh := make(chan error, 1)
	go func() {
		log.Info("gateway listening", "addr", cfg.Listen)
		errCh <- srv.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		if !errors.Is(err, http.ErrServerClosed) {
			log.Error("server failed", "err", err)
			os.Exit(1)
		}
		return
	case <-ctx.Done():
	}

	log.Info("shutting down, draining in-flight requests")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Error("shutdown", "err", err)
	}
	if err := shutdownTracing(shutdownCtx); err != nil {
		log.Error("flush traces", "err", err)
	}
}
