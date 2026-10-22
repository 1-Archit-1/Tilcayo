package config

import "os"

func fromEnvOrDefault(envVar string, fallback string) string {
	val := os.Getenv(envVar)
	if val != "" {
		return val
	}

	return fallback
}

func FromEnv() Config {
	port := fromEnvOrDefault("PORT", defaultPort)
	resultsMode := fromEnvOrDefault("TILCAYO_RESULTS", defaultResultsMode)
	return Config{
		Port:        port,
		ResultsMode: resultsMode,
	}
}
