package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/1-Archit-1/Tilcayo/internal/api"
	"github.com/1-Archit-1/Tilcayo/internal/config"
	"github.com/1-Archit-1/Tilcayo/internal/jobcache"
	"github.com/1-Archit-1/Tilcayo/internal/orchestrator"
	"github.com/moby/moby/client"
)

// main only exits; run returns instead, so its deferred cleanup always runs.
func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	// SIGINT or SIGTERM cancels ctx, which starts a graceful shutdown.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cache := jobcache.New()

	var (
		orch    orchestrator.Orchestrator
		cleanup func()
	)

	switch cfg.Orchestrator {
	case config.OrchestratorDocker:
		orch, cleanup, err = newDockerOrchestrator(&cfg)
		if err != nil {
			return fmt.Errorf("failed to initialize Docker orchestrator: %w", err)
		}
		defer cleanup()

	case config.OrchestratorKubernetes:
		// Future K8s / k3s:
		// orch, cleanup, err = newK8sOrchestrator(&cfg)
		return errors.New("Kubernetes orchestrator not implemented yet")

	default:
		return fmt.Errorf("unknown orchestrator: %s", cfg.Orchestrator)
	}

	// The watcher closes events when ctx is cancelled, which ends this loop.
	events, err := orch.Events(ctx)
	if err != nil {
		return fmt.Errorf("watch orchestrator events: %w", err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for ev := range events {
			cache.Update(ev)
		}
	}()

	srv := api.NewServer(&cfg, cache, orch)

	httpServer := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           srv.Routes(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	fmt.Printf("Tilcayo starting on %s in %s mode (orchestrator: %s)\n",
		cfg.ListenAddr, cfg.ResultsMode, cfg.Orchestrator)

	serveErr := make(chan error, 1)
	go func() { serveErr <- httpServer.ListenAndServe() }()

	var runErr error
	select {
	case <-ctx.Done():
		fmt.Println("Tilcayo shutting down")
	case err := <-serveErr:
		runErr = fmt.Errorf("server error: %w", err)
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: HTTP shutdown: %v\n", err)
	}

	// Stop the event watcher; a server error gets here with ctx still live.
	stop()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		fmt.Fprintf(os.Stderr, "Warning: event watcher did not stop within 5s\n")
	}
	return runErr
}

func newDockerOrchestrator(cfg *config.Config) (orchestrator.Orchestrator, func(), error) {
	// API version negotiation is the default in moby/moby/client.
	cli, err := client.New(client.FromEnv)
	if err != nil {
		return nil, nil, fmt.Errorf("connect to docker: %w", err)
	}

	cleanup := func() {
		_ = cli.Close()
	}

	orch := orchestrator.NewDockerOrchestrator(cli, cfg)
	warn := func(msg string) { fmt.Fprintf(os.Stderr, "Warning: %s\n", msg) }
	if err := orch.Preflight(context.Background(), warn); err != nil {
		cleanup()
		return nil, nil, err
	}
	return orch, cleanup, nil
}
