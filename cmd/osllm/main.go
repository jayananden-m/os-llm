// osllm is the operator CLI for the os-llm platform.
package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	osllmv1 "github.com/jayananden-m/os-llm/gen/osllm/v1"
)

func main() {
	var addr string

	root := &cobra.Command{
		Use:           "osllm",
		Short:         "Operate the os-llm inference platform",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.PersistentFlags().StringVar(&addr, "addr", envOr("OSLLM_ADDR", "localhost:9090"), "gateway admin gRPC address")

	status := &cobra.Command{
		Use:   "status [model]",
		Short: "Show live queue and replica load from the gateway",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			model := ""
			if len(args) == 1 {
				model = args[0]
			}
			return runStatus(cmd.Context(), addr, model)
		},
	}
	root.AddCommand(status)

	if err := root.ExecuteContext(context.Background()); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func runStatus(ctx context.Context, addr, model string) error {
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return err
	}
	defer conn.Close()

	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	resp, err := osllmv1.NewAdminClient(conn).GetStatus(ctx, &osllmv1.GetStatusRequest{Model: model})
	if err != nil {
		return err
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(w, "MODEL\tREPLICAS\tIN-FLIGHT\tCAPACITY\tQUEUED")
	for _, m := range resp.Models {
		inFlight := int32(0)
		for _, r := range m.Replicas {
			inFlight += r.InFlight
		}
		fmt.Fprintf(w, "%s\t%d\t%d\t%d\t%d\n", m.Model, len(m.Replicas), inFlight, m.Capacity, m.Queued)
	}
	w.Flush()

	if model != "" && len(resp.Models) == 1 {
		fmt.Println()
		w = tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
		fmt.Fprintln(w, "REPLICA\tIN-FLIGHT")
		for _, r := range resp.Models[0].Replicas {
			fmt.Fprintf(w, "%s\t%d\n", strings.TrimPrefix(r.Address, "http://"), r.InFlight)
		}
		w.Flush()
	}
	return nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
