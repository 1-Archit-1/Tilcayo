package config

import (
	"fmt"
	"os"
	"path/filepath"
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

// cleanHostPath tidies an absolute path ("/var/data/" becomes "/var/data")
// so a trailing slash is not an error. Anything else, such as a relative
// path or a PVC name, is left as is for Validate to judge.
func cleanHostPath(p string) string {
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	return p
}

func fromEnvOrDefault(key, fallback string) string {
	if val := getenv(key); val != "" {
		return val
	}
	return fallback
}

// FromEnv loads the raw Config struct from environment variables.
// An empty variable counts as unset, by design (Kubernetes often sets empty values).
// Values are trimmed, absolute host paths are cleaned, and TILCAYO_JOB_CPU and
// TILCAYO_JOB_MEMORY are parsed and range-checked here.
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

	var jobCPU float64
	if raw := getenv(EnvJobCPU); raw != "" {
		parsed, err := parseCPU(raw)
		if err != nil {
			return Config{}, err
		}
		jobCPU = parsed
	}

	var jobMemory int64
	if raw := getenv(EnvJobMemory); raw != "" {
		parsed, err := parseMemoryBytes(raw)
		if err != nil {
			return Config{}, err
		}
		jobMemory = parsed
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
		ResultsDir:       cleanHostPath(getenv(EnvResultsDir)),
		OutputPrefix:     getenv(EnvS3OutputPrefix),
		InputPrefixes:    inputPrefixes,
		DataSource:       cleanHostPath(getenv(EnvDataSource)),
		S3KeyID:          Secret(getenv(EnvS3KeyID)),
		S3Secret:         Secret(getenv(EnvS3Secret)),
		S3Region:         getenv(EnvS3Region),
		S3Endpoint:       endpoint,
		S3PublicEndpoint: publicEndpoint,
		S3URLStyle:       fromEnvOrDefault(EnvS3URLStyle, DefaultURLStyle),
		S3UseSSL:         useSSL,
		DockerNetwork:    getenv(EnvDockerNetwork),
		JobCPU:           jobCPU,
		JobMemoryBytes:   jobMemory,
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
