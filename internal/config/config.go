package config

import (
	"errors"
	"fmt"
	"strings"
)

var _ = fmt.Sprintf

// Sentinel errors for config validation.
var (
	ErrMissingField        = errors.New("missing required field")
	ErrInvalidPrefix       = errors.New("invalid S3 prefix")
	ErrPrefixOverlap       = errors.New("input prefix overlaps with output prefix")
	ErrInvalidMode         = errors.New("invalid results mode")
	ErrOutputPrefixRefused = errors.New("output prefix refused in store-free mode")
)

// Config holds the startup configuration for the Tilcayo control plane.
type Config struct {
	Port             string
	ResultsMode      string   // "s3" or "local" (from TILCAYO_RESULTS_MODE)
	OutputPrefix     string   // required when ResultsMode == "s3", refused when "local" (from TILCAYO_S3_OUTPUT_PREFIX)
	InputPrefixes    []string // optional list of s3:// prefixes ending with / (from TILCAYO_S3_INPUT_PREFIXES)
	DataSource       string   // optional local data source mount (from TILCAYO_DATA_SOURCE)
	S3KeyID          string   // required when ResultsMode == "s3"
	S3Secret         string   // required when ResultsMode == "s3"
	S3Region         string   // required when ResultsMode == "s3" (no default)
	S3Endpoint       string   // optional
	S3PublicEndpoint string   // optional, defaults to S3Endpoint
	S3URLStyle       string   // "vhost" (default) or "path"
	S3UseSSL         bool     // default true
	DockerNetwork    string
	JobCPU           string
	JobMemory        string
	ImageGeneral     string
	ImageSpatial     string
	ImageML          string
}

// AllowedInputs derives the directories and prefixes worker SQL is permitted
// to touch
// OutputPrefix is intentionally NEVER added here.
func (c Config) AllowedInputs() []string {
	var inputs []string
	inputs = append(inputs, c.InputPrefixes...)
	if c.DataSource != "" {
		inputs = append(inputs, "/data/")
	}
	return inputs
}

// validateS3Prefix checks that a prefix starts with "s3://", ends with "/",
// and contains a non-empty bucket name (e.g. "s3://bucket/path/").
func validateS3Prefix(prefix string) error {

	if !strings.HasPrefix(prefix, "s3://") {
		return fmt.Errorf("%w: prefix %q must start with 's3://'", ErrInvalidPrefix, prefix)
	}
	if !strings.HasSuffix(prefix, "/") {
		return fmt.Errorf("%w: prefix %q must end with '/'", ErrInvalidPrefix, prefix)
	}
	bucket := strings.Split(strings.TrimPrefix(prefix, "s3://"), "/")[0]
	if bucket == "" {
		return fmt.Errorf("%w: prefix %q bucket must not be empty", ErrInvalidPrefix, prefix)
	}
	return nil
}

// prefixesOverlap checks if prefix 'a' starts with 'b', or 'b' starts with 'a'.
func prefixesOverlap(a, b string) bool {
	return strings.HasPrefix(a, b) || strings.HasPrefix(b, a)
}

// Validate checks the configuration for consistency and safety at startup.
//
// 1. ResultsMode (TILCAYO_RESULTS_MODE) must be "s3" or "local".
// 2. If ResultsMode == "s3":
//   - OutputPrefix (TILCAYO_S3_OUTPUT_PREFIX) is required, and must be a valid S3 prefix (validateS3Prefix).
//   - S3Region is REQUIRED (refuse if empty, naming "TILCAYO_S3_REGION" specifically).
//   - S3KeyID and S3Secret are required.
//
// 3. If ResultsMode == "local":
//   - OutputPrefix must be empty (refuse with ErrOutputPrefixRefused if set).
//
// 4. InputPrefixes (TILCAYO_S3_INPUT_PREFIXES):
//   - Every non-empty prefix must be a valid S3 prefix (validateS3Prefix).
//   - If OutputPrefix is set, no InputPrefix may overlap with OutputPrefix (prefixesOverlap).
func (c Config) Validate() error {
	if c.ResultsMode != "s3" && c.ResultsMode != "local" {
		return fmt.Errorf("%w: %s", ErrInvalidMode, c.ResultsMode)
	}
	if c.ResultsMode == "s3" {
		if c.OutputPrefix == "" {
			return fmt.Errorf("%w: %s", ErrMissingField, EnvS3OutputPrefix)
		}
		if err := validateS3Prefix(c.OutputPrefix); err != nil {
			return fmt.Errorf("Validating %s: %w", EnvS3OutputPrefix, err)
		}
		if c.S3Region == "" {
			return fmt.Errorf("%w: %s", ErrMissingField, EnvS3Region)
		}
		if c.S3KeyID == "" {
			return fmt.Errorf("%w: %s", ErrMissingField, EnvS3KeyID)
		}
		if c.S3Secret == "" {
			return fmt.Errorf("%w: %s", ErrMissingField, EnvS3Secret)
		}
	}
	if c.ResultsMode == "local" && c.OutputPrefix != "" {
		return fmt.Errorf("%w: %s cannot be set in local mode", ErrOutputPrefixRefused, EnvS3OutputPrefix)
	}

	for _, prefix := range c.InputPrefixes {
		if prefix == "" {
			continue
		}
		if err := validateS3Prefix(prefix); err != nil {
			return fmt.Errorf("Validating %s: %w", EnvS3InputPrefixes, err)
		}
		if c.OutputPrefix != "" && prefixesOverlap(prefix, c.OutputPrefix) {
			return fmt.Errorf("%w: input prefix %q overlaps with output prefix %q", ErrPrefixOverlap, prefix, c.OutputPrefix)
		}
	}
	return nil
}
