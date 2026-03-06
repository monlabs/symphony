package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/monlabs/symphony/internal/orchestrator"
)

func main() {
	port := flag.Int("port", -1, "HTTP server port (overrides server.port in WORKFLOW.md)")
	flag.Parse()

	// Optional positional arg: path to WORKFLOW.md
	workflowPath := "WORKFLOW.md"
	if flag.NArg() > 0 {
		workflowPath = flag.Arg(0)
	}

	// Check workflow file exists
	if _, err := os.Stat(workflowPath); os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "Error: workflow file not found: %s\n", workflowPath)
		os.Exit(1)
	}

	// Configure structured logging
	handler := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})
	slog.SetDefault(slog.New(handler))

	slog.Info("starting Symphony",
		"workflow_path", workflowPath,
	)

	// Port override
	var portOverride *int
	if *port >= 0 {
		portOverride = port
	}

	// Create orchestrator
	orch, err := orchestrator.New(workflowPath, portOverride)
	if err != nil {
		slog.Error("failed to initialize orchestrator", "error", err)
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	// Context with signal handling
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		sig := <-sigCh
		slog.Info("received signal, shutting down", "signal", sig)
		cancel()
	}()

	// Run
	if err := orch.Run(ctx); err != nil {
		if ctx.Err() != nil {
			slog.Info("Symphony shut down gracefully")
			os.Exit(0)
		}
		slog.Error("orchestrator exited with error", "error", err)
		os.Exit(1)
	}

	slog.Info("Symphony shut down")
}
