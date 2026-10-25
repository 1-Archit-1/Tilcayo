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
	EnvImageGeneral     = "TILCAYO_IMAGE_GENERAL"
	EnvImageSpatial     = "TILCAYO_IMAGE_SPATIAL"
	EnvImageML          = "TILCAYO_IMAGE_ML"
)

func fromEnvOrDefault(key, fallback string) string {
	if val := strings.TrimSpace(os.Getenv(key)); val != "" {
		return val
	}
	return fallback
}

// FromEnv loads the raw Config struct from environment variables.
func FromEnv() (Config, error) {
	resultsMode := fromEnvOrDefault(EnvResultsMode, DefaultResultsMode)
	publicEndpoint := strings.TrimSpace(os.Getenv(EnvS3PublicEndpoint))
	endpoint := strings.TrimSpace(os.Getenv(EnvS3Endpoint))
	if publicEndpoint == "" {
		publicEndpoint = endpoint
	}

	var inputPrefixes []string
	if raw := os.Getenv(EnvS3InputPrefixes); raw != "" {
		for _, part := range strings.Split(raw, ",") {
			part = strings.TrimSpace(part)
			if part != "" {
				inputPrefixes = append(inputPrefixes, part)
			}
		}
	}

	useSSL := true
	if rawSSL := strings.TrimSpace(os.Getenv(EnvS3UseSSL)); rawSSL != "" {
		parsed, err := strconv.ParseBool(rawSSL)
		if err != nil {
			return Config{}, fmt.Errorf("%s: invalid boolean %q", EnvS3UseSSL, rawSSL)
		}
		useSSL = parsed
	}

	return Config{
		Port:             fromEnvOrDefault(EnvPort, DefaultPort),
		Orchestrator:     fromEnvOrDefault(EnvOrchestrator, DefaultOrchestrator),
		ResultsMode:      resultsMode,
		ResultsDir:       strings.TrimSpace(os.Getenv(EnvResultsDir)),
		OutputPrefix:     strings.TrimSpace(os.Getenv(EnvS3OutputPrefix)),
		InputPrefixes:    inputPrefixes,
		DataSource:       strings.TrimSpace(os.Getenv(EnvDataSource)),
		S3KeyID:          Secret(strings.TrimSpace(os.Getenv(EnvS3KeyID))),
		S3Secret:         Secret(strings.TrimSpace(os.Getenv(EnvS3Secret))),
		S3Region:         strings.TrimSpace(os.Getenv(EnvS3Region)),
		S3Endpoint:       endpoint,
		S3PublicEndpoint: publicEndpoint,
		S3URLStyle:       fromEnvOrDefault(EnvS3URLStyle, DefaultURLStyle),
		S3UseSSL:         useSSL,
		DockerNetwork:    strings.TrimSpace(os.Getenv(EnvDockerNetwork)),
		JobCPU:           strings.TrimSpace(os.Getenv(EnvJobCPU)),
		JobMemory:        strings.TrimSpace(os.Getenv(EnvJobMemory)),
		ImageGeneral:     strings.TrimSpace(os.Getenv(EnvImageGeneral)),
		ImageSpatial:     strings.TrimSpace(os.Getenv(EnvImageSpatial)),
		ImageML:          strings.TrimSpace(os.Getenv(EnvImageML)),
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
