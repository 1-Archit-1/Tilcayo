package main

import (
	"fmt"
	"github.com/1-Archit-1/Tilcayo/internal/config"
	"github.com/1-Archit-1/Tilcayo/internal/orchestrator"
)

func main() {
	conf := config.FromEnv()
	currState := orchestrator.StateQueued
	fmt.Println("Main", conf, currState)
}
