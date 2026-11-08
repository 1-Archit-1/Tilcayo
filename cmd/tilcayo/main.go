package main

import (
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
		Addr:              ":" + cfg.Port,
		Handler:           srv.Routes(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	fmt.Printf("Tilcayo starting on port %s in %s mode (orchestrator: %s)\n",
		cfg.Port, cfg.ResultsMode, cfg.Orchestrator)

	if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		fmt.Fprintf(os.Stderr, "Server error: %v\n", err)
		os.Exit(1)
	}
}
