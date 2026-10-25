package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Environment variable names
const (
	EnvPort             = "PORT"
	EnvOrchestrator     = "TILCAYO_ORCHESTRATOR"
	EnvResultsMode      = "TILCAYO_RESULTS_MODE"
	EnvResultsDir       = "TILCAYO_RESULTS_DIR"
	EnvS3OutputPrefix   = "TILCAYO_S3_OUTPUT_PREFIX"
	EnvS3InputPrefixes  = "TILCAYO_S3_INPUT_PREFIXES"
	EnvDataSource       = "TILCAYO_DATA_SOURCE"
	EnvS3KeyID          = "TILCAYO_S3_KEY_ID"
	EnvS3Secret         = "TILCAYO_S3_SECRET"
	EnvS3Region         = "TILCAYO_S3_REGION"
	EnvS3Endpoint       = "TILCAYO_S3_ENDPOINT"
	EnvS3PublicEndpoint = "TILCAYO_S3_PUBLIC_ENDPOINT"
	EnvS3URLStyle       = "TILCAYO_S3_URL_STYLE"
	EnvS3UseSSL         = "TILCAYO_S3_USE_SSL"
	EnvDockerNetwork    = "TILCAYO_DOCKER_NETWORK"
	EnvJobCPU           = "TILCAYO_JOB_CPU"
	EnvJobMemory        = "TILCAYO_JOB_MEMORY"
	EnvImagePrefix      = "TILCAYO_IMAGE_" // plus the upper-cased engine name, e.g. TILCAYO_IMAGE_ML
)

// imageEnvName returns the variable that overrides the image for an engine.
func imageEnvName(engine string) string {
	return EnvImagePrefix + strings.ToUpper(engine)
}

// getenv reads a variable and trims surrounding whitespace.
func getenv(key string) string {
	return strings.TrimSpace(os.Getenv(key))
}

func fromEnvOrDefault(key, fallback string) string {
	if val := getenv(key); val != "" {
		return val
	}
	return fallback
}

// FromEnv loads the raw Config struct from environment variables.
// An empty variable counts as unset, by design (Kubernetes often sets empty values).
// TILCAYO_S3_USE_SSL accepts what strconv.ParseBool does: 1 t T TRUE true True
// 0 f F FALSE false False. Anything else, such as "off", is an error.
func FromEnv() (Config, error) {
	resultsMode := fromEnvOrDefault(EnvResultsMode, DefaultResultsMode)
	publicEndpoint := getenv(EnvS3PublicEndpoint)
	endpoint := getenv(EnvS3Endpoint)
	if publicEndpoint == "" {
		publicEndpoint = endpoint
	}

	var inputPrefixes []string
	if raw := getenv(EnvS3InputPrefixes); raw != "" {
		for _, part := range strings.Split(raw, ",") {
			part = strings.TrimSpace(part)
			if part != "" {
				inputPrefixes = append(inputPrefixes, part)
			}
		}
	}

	useSSL := true
	if rawSSL := getenv(EnvS3UseSSL); rawSSL != "" {
		parsed, err := strconv.ParseBool(rawSSL)
		if err != nil {
			return Config{}, fmt.Errorf("%s: invalid boolean %q", EnvS3UseSSL, rawSSL)
		}
		useSSL = parsed
	}

	images := make(map[string]string)
	for engine := range engines {
		if img := getenv(imageEnvName(engine)); img != "" {
			images[engine] = img
		}
	}

	return Config{
		Port:             fromEnvOrDefault(EnvPort, DefaultPort),
		Orchestrator:     getenv(EnvOrchestrator),
		ResultsMode:      resultsMode,
		ResultsDir:       getenv(EnvResultsDir),
		OutputPrefix:     getenv(EnvS3OutputPrefix),
		InputPrefixes:    inputPrefixes,
		DataSource:       getenv(EnvDataSource),
		S3KeyID:          Secret(getenv(EnvS3KeyID)),
		S3Secret:         Secret(getenv(EnvS3Secret)),
		S3Region:         getenv(EnvS3Region),
		S3Endpoint:       endpoint,
		S3PublicEndpoint: publicEndpoint,
		S3URLStyle:       fromEnvOrDefault(EnvS3URLStyle, DefaultURLStyle),
		S3UseSSL:         useSSL,
		DockerNetwork:    getenv(EnvDockerNetwork),
		JobCPU:           getenv(EnvJobCPU),
		JobMemory:        getenv(EnvJobMemory),
		Images:           images,
	}, nil
}

// Load loads environment variables and validates them in one step (M2, S5).
func Load() (Config, error) {
	cfg, err := FromEnv()
	if err != nil {
		return Config{}, fmt.Errorf("loading config: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, fmt.Errorf("validating config: %w", err)
	}
	return cfg, nil
}
