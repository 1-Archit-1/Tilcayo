package main

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/1-Archit-1/Tilcayo/internal/api"
	"github.com/1-Archit-1/Tilcayo/internal/config"
	"github.com/1-Archit-1/Tilcayo/internal/jobcache"
	"github.com/1-Archit-1/Tilcayo/internal/orchestrator"
	"github.com/docker/docker/client"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	cache := jobcache.New()

	var (
		orch    orchestrator.Orchestrator
		cleanup func()
	)

	switch cfg.Orchestrator {
	case config.OrchestratorDocker:
		orch, cleanup, err = newDockerOrchestrator(&cfg)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to initialize Docker orchestrator: %v\n", err)
			os.Exit(1)
		}
		defer cleanup()

	case config.OrchestratorKubernetes:
		// Future K8s / k3s:
		// orch, cleanup, err = newK8sOrchestrator(&cfg)
		fmt.Fprintf(os.Stderr, "Kubernetes orchestrator not implemented yet\n")
		os.Exit(1)

	default:
		fmt.Fprintf(os.Stderr, "Unknown orchestrator: %s\n", cfg.Orchestrator)
		os.Exit(1)
	}

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

	if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		fmt.Fprintf(os.Stderr, "Server error: %v\n", err)
		os.Exit(1)
	}
}

func newDockerOrchestrator(cfg *config.Config) (orchestrator.Orchestrator, func(), error) {
	cli, err := client.NewClientWithOpts(
		client.FromEnv,
		client.WithAPIVersionNegotiation(),
	)
	if err != nil {
		return nil, nil, fmt.Errorf("connect to docker: %w", err)
	}

	cleanup := func() {
		_ = cli.Close()
	}

	return orchestrator.NewDockerOrchestrator(cli, cfg), cleanup, nil
}
