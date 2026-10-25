package config

import (
	"os"
	"strings"
)

// Environment variable names
const (
	EnvPort             = "PORT"
	EnvResultsMode      = "TILCAYO_RESULTS_MODE"
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

const (
	defaultPort        = "8080"
	defaultResultsMode = "s3"
	defaultURLStyle    = "vhost"
)

func fromEnvOrDefault(key, fallback string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return fallback
}

// FromEnv loads the Config struct from environment variables.
func FromEnv() Config {
	resultsMode := fromEnvOrDefault(EnvResultsMode, defaultResultsMode)
	publicEndpoint := os.Getenv(EnvS3PublicEndpoint)
	endpoint := os.Getenv(EnvS3Endpoint)
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
	if ssl := strings.ToLower(os.Getenv(EnvS3UseSSL)); ssl == "false" || ssl == "0" || ssl == "no" {
		useSSL = false
	}

	return Config{
		Port:             fromEnvOrDefault(EnvPort, defaultPort),
		ResultsMode:      resultsMode,
		OutputPrefix:     os.Getenv(EnvS3OutputPrefix),
		InputPrefixes:    inputPrefixes,
		DataSource:       os.Getenv(EnvDataSource),
		S3KeyID:          os.Getenv(EnvS3KeyID),
		S3Secret:         os.Getenv(EnvS3Secret),
		S3Region:         os.Getenv(EnvS3Region),
		S3Endpoint:       endpoint,
		S3PublicEndpoint: publicEndpoint,
		S3URLStyle:       fromEnvOrDefault(EnvS3URLStyle, defaultURLStyle),
		S3UseSSL:         useSSL,
		DockerNetwork:    os.Getenv(EnvDockerNetwork),
		JobCPU:           os.Getenv(EnvJobCPU),
		JobMemory:        os.Getenv(EnvJobMemory),
		ImageGeneral:     os.Getenv(EnvImageGeneral),
		ImageSpatial:     os.Getenv(EnvImageSpatial),
		ImageML:          os.Getenv(EnvImageML),
	}
}
