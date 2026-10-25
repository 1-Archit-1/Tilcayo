package main

import (
	"fmt"
	"os"

	"github.com/1-Archit-1/Tilcayo/internal/config"
	"github.com/1-Archit-1/Tilcayo/internal/orchestrator"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	currState := orchestrator.StatePending
	fmt.Printf("Tilcayo starting on port %s in %s mode (orchestrator: %s, state: %s)\n",
		cfg.Port, cfg.ResultsMode, cfg.Orchestrator, currState)
}
