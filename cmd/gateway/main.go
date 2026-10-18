package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"google.golang.org/grpc"
	"google.golang.org/grpc/reflection"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"

	osllmv1 "github.com/jayananden-m/os-llm/gen/osllm/v1"
	"github.com/jayananden-m/os-llm/internal/admin"
	"github.com/jayananden-m/os-llm/internal/config"
	"github.com/jayananden-m/os-llm/internal/discovery"
	"github.com/jayananden-m/os-llm/internal/gateway"
	"github.com/jayananden-m/os-llm/internal/registry"
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

	if err := startDiscovery(ctx, cfg, gw, log); err != nil {
		log.Error("start replica discovery", "err", err)
		os.Exit(1)
	}

	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           gw.Routes(),
		ReadHeaderTimeout: 10 * time.Second,
		// No WriteTimeout: a generation can legitimately take minutes.
	}

	lis, err := net.Listen("tcp", cfg.AdminListen)
	if err != nil {
		log.Error("admin listen", "err", err)
		os.Exit(1)
	}
	grpcSrv := grpc.NewServer()
	osllmv1.RegisterAdminServer(grpcSrv, admin.NewServer(gw))
	reflection.Register(grpcSrv) // lets grpcurl discover the API

	errCh := make(chan error, 2)
	go func() {
		log.Info("gateway listening", "addr", cfg.Listen)
		errCh <- srv.ListenAndServe()
	}()
	go func() {
		log.Info("admin gRPC listening", "addr", cfg.AdminListen)
		errCh <- grpcSrv.Serve(lis)
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
	grpcSrv.GracefulStop()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Error("shutdown", "err", err)
	}
	if err := shutdownTracing(shutdownCtx); err != nil {
		log.Error("flush traces", "err", err)
	}
}

// startDiscovery wires the gateway to Kubernetes: EndpointSlice watches for
// models configured with discovery, and a ModelDeployment watch if enabled.
func startDiscovery(ctx context.Context, cfg *config.Config, gw *gateway.Gateway, log *slog.Logger) error {
	needsKube := cfg.ModelDeployments != nil
	for _, m := range cfg.Models {
		if m.Discovery != nil {
			needsKube = true
		}
	}
	if !needsKube {
		return nil
	}

	restCfg, err := discovery.RestConfig()
	if err != nil {
		return err
	}
	kube, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		return err
	}

	for _, m := range cfg.Models {
		if m.Discovery == nil {
			continue
		}
		pool := gw.Pool(m.Name)
		target := discovery.Target{
			Namespace: m.Discovery.Namespace,
			Service:   m.Discovery.Service,
			PortName:  m.Discovery.Port,
			OnChange: func(addrs []string) {
				added, removed, err := pool.SetReplicas(addrs)
				if err != nil {
					log.Error("update replicas", "model", m.Name, "err", err)
				}
				if added > 0 || removed > 0 {
					log.Info("replicas updated", "model", m.Name, "added", added, "removed", removed, "replicas", addrs)
				}
			},
		}
		if err := discovery.Watch(ctx, kube, target, log); err != nil {
			return err
		}
		log.Info("watching replicas", "model", m.Name, "service", m.Discovery.Namespace+"/"+m.Discovery.Service)
	}

	if cfg.ModelDeployments != nil {
		dyn, err := dynamic.NewForConfig(restCfg)
		if err != nil {
			return err
		}
		reg := registry.New(ctx, gw, kube, cfg.ModelDeployments.Namespace, log)
		if err := reg.Start(dyn); err != nil {
			return err
		}
		log.Info("watching modeldeployments", "namespace", cfg.ModelDeployments.Namespace)
	}
	return nil
}
