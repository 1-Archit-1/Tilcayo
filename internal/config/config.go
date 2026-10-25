package config

import (
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// Version can be set at build time via -ldflags "-X ...".
var Version = "dev"

// Valid engines.
const (
	EngineGeneral = "general"
	EngineSpatial = "spatial"
	EngineML      = "ml"
)

// Application Defaults
const (
	DefaultPort                = "8080"
	DefaultOrchestrator        = "docker"
	DefaultResultsMode         = "s3"
	DefaultURLStyle            = "vhost"
	DefaultCPU                 = 1.0
	giB                  int64 = 1024 * 1024 * 1024
	miB                  int64 = 1024 * 1024
	DefaultMemoryGeneral       = 1 * giB
	DefaultMemorySpatial       = 1 * giB
	DefaultMemoryML            = 4 * giB // provisional (Item 13 measures it)
)

var s3BucketRegex = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)

// Secret is a string containing sensitive data that must not appear in logs.
type Secret string

// String implements fmt.Stringer so printing a Secret automatically redacts it.
func (s Secret) String() string {
	if s == "" {
		return ""
	}
	return "[REDACTED]"
}

// GoString implements fmt.GoStringer so %#v also redacts it.
func (s Secret) GoString() string {
	if s == "" {
		return `""`
	}
	return `"[REDACTED]"`
}

// Sentinel errors for config validation.
var (
	ErrMissingField        = errors.New("missing required field")
	ErrInvalidPrefix       = errors.New("invalid S3 prefix")
	ErrPrefixOverlap       = errors.New("input prefix overlaps with output prefix")
	ErrInvalidMode         = errors.New("invalid results mode")
	ErrOutputPrefixRefused = errors.New("output prefix refused in store-free mode")
	ErrJobCPU              = errors.New("invalid job cpu")
	ErrJobMemory           = errors.New("invalid job memory")
	ErrInvalidOrchestrator = errors.New("invalid orchestrator")
	ErrInvalidPath         = errors.New("invalid path")
	ErrUnknownEngine       = errors.New("unknown engine")
	ErrInvalidURLStyle     = errors.New("invalid S3 url style")
)

// Resources defines CPU and Memory limits for a worker container.
type Resources struct {
	CPU         float64
	MemoryBytes int64
}

// Config holds the startup configuration for the Tilcayo control plane.
type Config struct {
	Port             string
	Orchestrator     string   // "docker" or "kubernetes" (from TILCAYO_ORCHESTRATOR)
	ResultsMode      string   // "s3" or "local" (from TILCAYO_RESULTS_MODE)
	ResultsDir       string   // host path for local results (from TILCAYO_RESULTS_DIR)
	OutputPrefix     string   // required when ResultsMode == "s3", refused when "local" (from TILCAYO_S3_OUTPUT_PREFIX)
	InputPrefixes    []string // optional list of s3:// prefixes ending with / (from TILCAYO_S3_INPUT_PREFIXES)
	DataSource       string   // optional local host path or PVC (from TILCAYO_DATA_SOURCE)
	S3KeyID          Secret   // required when ResultsMode == "s3"
	S3Secret         Secret   // required when ResultsMode == "s3"
	S3Region         string   // required when ResultsMode == "s3" (no default)
	S3Endpoint       string   // optional worker endpoint
	S3PublicEndpoint string   // optional caller endpoint, defaults to S3Endpoint
	S3URLStyle       string   // "vhost" (default) or "path"
	S3UseSSL         bool     // default true
	DockerNetwork    string
	JobCPU           string
	JobMemory        string
	ImageGeneral     string
	ImageSpatial     string
	ImageML          string

	// Precomputed engine resource limits
	EngineResources map[string]Resources
}

// AllowedInputs derives the directories and prefixes worker SQL is permitted to touch.
// OutputPrefix is intentionally NEVER added here.
func (c Config) AllowedInputs() []string {
	var inputs []string
	inputs = append(inputs, c.InputPrefixes...)
	if c.DataSource != "" {
		inputs = append(inputs, "/data/")
	}
	return inputs
}

// ResourcesFor returns the resource limits for the given engine.
func (c Config) ResourcesFor(engine string) (Resources, error) {
	if c.EngineResources == nil {
		return Resources{}, fmt.Errorf("%w: %q", ErrUnknownEngine, engine)
	}
	res, ok := c.EngineResources[engine]
	if !ok {
		return Resources{}, fmt.Errorf("%w: %q", ErrUnknownEngine, engine)
	}
	return res, nil
}

// ImageFor returns the container image to run for the specified engine.
func (c Config) ImageFor(engine string) (string, error) {
	switch engine {
	case EngineGeneral:
		if c.ImageGeneral != "" {
			return c.ImageGeneral, nil
		}
	case EngineSpatial:
		if c.ImageSpatial != "" {
			return c.ImageSpatial, nil
		}
	case EngineML:
		if c.ImageML != "" {
			return c.ImageML, nil
		}
	default:
		return "", fmt.Errorf("%w: %q", ErrUnknownEngine, engine)
	}
	return fmt.Sprintf("ghcr.io/1-archit-1/tilcayo-worker-%s:%s", engine, Version), nil
}

func parseMemoryBytes(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("%w: cannot be empty", ErrJobMemory)
	}

	multiplier := int64(1)
	valStr := s

	if strings.HasSuffix(s, "Gi") {
		multiplier = giB
		valStr = strings.TrimSuffix(s, "Gi")
	} else if strings.HasSuffix(s, "Mi") {
		multiplier = miB
		valStr = strings.TrimSuffix(s, "Mi")
	}

	val, err := strconv.ParseInt(strings.TrimSpace(valStr), 10, 64)
	if err != nil || val <= 0 {
		return 0, fmt.Errorf("%w: invalid memory quantity %q", ErrJobMemory, s)
	}

	return val * multiplier, nil
}

// validateS3Prefix checks S3 prefix structure, bucket naming, and path segment safety (S1).
func validateS3Prefix(prefix string) error {
	if !strings.HasPrefix(prefix, "s3://") {
		return fmt.Errorf("%w: prefix %q must start with 's3://'", ErrInvalidPrefix, prefix)
	}
	if !strings.HasSuffix(prefix, "/") {
		return fmt.Errorf("%w: prefix %q must end with '/'", ErrInvalidPrefix, prefix)
	}

	rest := strings.TrimPrefix(prefix, "s3://")
	parts := strings.Split(rest, "/")
	bucket := parts[0]

	if !s3BucketRegex.MatchString(bucket) {
		return fmt.Errorf("%w: invalid bucket name %q in %q", ErrInvalidPrefix, bucket, prefix)
	}

	// Check path segments (skip bucket, and skip the last empty element resulting from trailing slash)
	for _, seg := range parts[1 : len(parts)-1] {
		if seg == "" || seg == "." || seg == ".." {
			return fmt.Errorf("%w: path segment %q not allowed in %q", ErrInvalidPrefix, seg, prefix)
		}
		for i := 0; i < len(seg); i++ {
			b := seg[i]
			if b < 0x20 || b == 0x7f || b == ' ' || b == '%' || b == '?' || b == '#' || b == '\'' {
				return fmt.Errorf("%w: illegal character in path segment %q of %q", ErrInvalidPrefix, seg, prefix)
			}
		}
	}
	return nil
}

func prefixesOverlap(a, b string) bool {
	return strings.HasPrefix(a, b) || strings.HasPrefix(b, a)
}

func (c Config) hasAnyS3Credentials() bool {
	return c.S3KeyID != "" || c.S3Secret != "" || c.S3Region != ""
}

// Validate checks the configuration for consistency and safety at startup.
func (c *Config) Validate() error {
	// 1. Orchestrator
	if c.Orchestrator != "docker" && c.Orchestrator != "kubernetes" {
		return fmt.Errorf("%w: %s must be 'docker' or 'kubernetes', got %q", ErrInvalidOrchestrator, EnvOrchestrator, c.Orchestrator)
	}

	// 2. Results Mode
	if c.ResultsMode != "s3" && c.ResultsMode != "local" {
		return fmt.Errorf("%w: %s must be 's3' or 'local', got %q", ErrInvalidMode, EnvResultsMode, c.ResultsMode)
	}

	// Store-free (local) is Docker only
	if c.ResultsMode == "local" && c.Orchestrator != "docker" {
		return fmt.Errorf("%w: local results mode is only supported with docker orchestrator", ErrInvalidMode)
	}

	if c.ResultsMode == "local" {
		if c.OutputPrefix != "" {
			return fmt.Errorf("%w: %s cannot be set in local mode", ErrOutputPrefixRefused, EnvS3OutputPrefix)
		}
		if c.ResultsDir == "" {
			return fmt.Errorf("%w: %s is required when results mode is local", ErrMissingField, EnvResultsDir)
		}
		if !filepath.IsAbs(c.ResultsDir) {
			return fmt.Errorf("%w: %s must be an absolute path, got %q", ErrInvalidPath, EnvResultsDir, c.ResultsDir)
		}
	}

	// 3. S3 Configuration
	if c.ResultsMode == "s3" {
		if c.OutputPrefix == "" {
			return fmt.Errorf("%w: %s", ErrMissingField, EnvS3OutputPrefix)
		}
		if err := validateS3Prefix(c.OutputPrefix); err != nil {
			return fmt.Errorf("%s: %w", EnvS3OutputPrefix, err)
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
	} else if c.hasAnyS3Credentials() || len(c.InputPrefixes) > 0 {
		// S2: Local mode S3 cross-checks
		if c.S3KeyID == "" {
			return fmt.Errorf("%w: %s required for S3 access in local mode", ErrMissingField, EnvS3KeyID)
		}
		if c.S3Secret == "" {
			return fmt.Errorf("%w: %s required for S3 access in local mode", ErrMissingField, EnvS3Secret)
		}
		if c.S3Region == "" {
			return fmt.Errorf("%w: %s required for S3 access in local mode", ErrMissingField, EnvS3Region)
		}
	}

	if c.S3URLStyle != "vhost" && c.S3URLStyle != "path" {
		return fmt.Errorf("%w: %s must be 'vhost' or 'path', got %q", ErrInvalidURLStyle, EnvS3URLStyle, c.S3URLStyle)
	}

	// 4. Input Prefixes
	for _, prefix := range c.InputPrefixes {
		if prefix == "" {
			continue
		}
		if err := validateS3Prefix(prefix); err != nil {
			return fmt.Errorf("%s: %w", EnvS3InputPrefixes, err)
		}
		if c.OutputPrefix != "" && prefixesOverlap(prefix, c.OutputPrefix) {
			return fmt.Errorf("%w: input prefix %q overlaps with output prefix %q", ErrPrefixOverlap, prefix, c.OutputPrefix)
		}
	}

	// 5. Data Source
	if c.DataSource != "" {
		if c.Orchestrator == "docker" {
			if !filepath.IsAbs(c.DataSource) || c.DataSource == "/" {
				return fmt.Errorf("%w: %s must be an absolute host path other than '/', got %q", ErrInvalidPath, EnvDataSource, c.DataSource)
			}
		} else if c.Orchestrator == "kubernetes" {
			if strings.Contains(c.DataSource, "/") {
				return fmt.Errorf("%w: %s in kubernetes must be a PVC name without slashes, got %q", ErrInvalidPath, EnvDataSource, c.DataSource)
			}
		}
	}

	// 6. Resources (M3)
	cpuVal := DefaultCPU
	if c.JobCPU != "" {
		val, err := strconv.ParseFloat(c.JobCPU, 64)
		if err != nil || val <= 0 {
			return fmt.Errorf("%w: %s must be a positive number, got %q", ErrJobCPU, EnvJobCPU, c.JobCPU)
		}
		cpuVal = val
	}

	var memOverride int64
	if c.JobMemory != "" {
		val, err := parseMemoryBytes(c.JobMemory)
		if err != nil {
			return fmt.Errorf("%s: %w", EnvJobMemory, err)
		}
		memOverride = val
	}

	c.EngineResources = make(map[string]Resources)
	for _, eng := range []string{EngineGeneral, EngineSpatial, EngineML} {
		var mem int64 = DefaultMemoryGeneral
		if eng == EngineML {
			mem = DefaultMemoryML
		}
		if memOverride > 0 {
			mem = memOverride
		}
		c.EngineResources[eng] = Resources{
			CPU:         cpuVal,
			MemoryBytes: mem,
		}
	}

	return nil
}
