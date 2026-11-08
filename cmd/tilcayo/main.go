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
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	cache := jobcache.New()
	srv := api.NewServer(&cfg, cache)

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
