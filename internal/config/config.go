package config

import (
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

var (
	s3BucketRegex = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)

	// Characters allowed in an S3 prefix path segment. A comma can never be
	// allowed: input prefixes are comma-separated in TILCAYO_S3_INPUT_PREFIXES
	// and again in the worker's TILCAYO_ALLOWED_INPUTS, so one would split an entry.
	s3SegmentRegex = regexp.MustCompile(`^[A-Za-z0-9._=@+-]+$`)

	// Plain digits with an optional decimal part. This rejects NaN, Inf, 1e3,
	// hex floats and underscores, which strconv.ParseFloat would accept.
	cpuRegex = regexp.MustCompile(`^[0-9]+(\.[0-9]+)?$`)

	// Digits with an optional Mi or Gi unit (plain bytes if no unit).
	memoryRegex = regexp.MustCompile(`^([0-9]+)(Mi|Gi)?$`)

	// Kubernetes object names (a PVC name) are DNS-1123 subdomains.
	dnsSubdomainRegex = regexp.MustCompile(`^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$`)
)

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

// MarshalText makes encoding/json (and slog's JSON handler) redact too.
// json ignores String(), but uses MarshalText for a string-based type.
func (s Secret) MarshalText() ([]byte, error) {
	return []byte(s.String()), nil
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
	ErrDirOverlap          = errors.New("results dir and data source overlap")
)

// Resources defines CPU and Memory limits for a worker container.
// CPU is in cores (0.01 to 256) and MemoryBytes is 64 Mi to 1 Ti, so neither
// can be 0, which Docker treats as "unlimited". For Docker, use NanoCPUs()
// for NanoCPUs and MemoryBytes for both Memory and MemorySwap.
type Resources struct {
	CPU         float64
	MemoryBytes int64
}

// NanoCPUs converts CPU cores to Docker's unit. It is never 0 for a Resources
// that came from ResourcesFor, because CPU is at least 0.01.
func (r Resources) NanoCPUs() int64 {
	return int64(math.Round(r.CPU * 1e9))
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
	S3UseSSL         bool     // default true; accepts 1 t T TRUE true True 0 f F FALSE false False (strconv.ParseBool)
	DockerNetwork    string
	JobCPU           string // optional override for every engine, e.g. "0.5" (from TILCAYO_JOB_CPU)
	JobMemory        string // optional override for every engine, e.g. "512Mi" (from TILCAYO_JOB_MEMORY)
	// Images maps engine name to an override image (from TILCAYO_IMAGE_<ENGINE>).
	// Only engines with an override are present. Treat as read-only after
	// FromEnv: copies of Config share this map.
	Images map[string]string
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

// ResourcesFor returns the resource limits for the given engine: the
// TILCAYO_JOB_CPU and TILCAYO_JOB_MEMORY overrides if set, else the defaults.
// It works on a Config straight from FromEnv, and always succeeds for a known
// engine once Validate has passed.
func (c Config) ResourcesFor(engine string) (Resources, error) {
	spec, ok := engines[engine]
	if !ok {
		return Resources{}, fmt.Errorf("%w: %q", ErrUnknownEngine, engine)
	}
	mem := spec.DefaultMemory

	cpu := DefaultCPU
	if c.JobCPU != "" {
		val, err := parseCPU(c.JobCPU)
		if err != nil {
			return Resources{}, fmt.Errorf("%s: %w", EnvJobCPU, err)
		}
		cpu = val
	}
	if c.JobMemory != "" {
		val, err := parseMemoryBytes(c.JobMemory)
		if err != nil {
			return Resources{}, fmt.Errorf("%s: %w", EnvJobMemory, err)
		}
		mem = val
	}
	return Resources{CPU: cpu, MemoryBytes: mem}, nil
}

// ImageFor returns the container image to run for the specified engine:
// the TILCAYO_IMAGE_<ENGINE> override if set, else the default for Version.
func (c Config) ImageFor(engine string) (string, error) {
	if _, ok := engines[engine]; !ok {
		return "", fmt.Errorf("%w: %q", ErrUnknownEngine, engine)
	}
	if img := c.Images[engine]; img != "" {
		return img, nil
	}
	return fmt.Sprintf(defaultImageFormat, engine, Version), nil
}

// parseCPU parses a CPU count in cores. Beware: NaN slips through a plain
// "val <= 0" check because every comparison with NaN is false, hence the regexp.
func parseCPU(s string) (float64, error) {
	if !cpuRegex.MatchString(s) {
		return 0, fmt.Errorf("%w: %q must be a plain decimal number such as 0.5", ErrJobCPU, s)
	}
	val, err := strconv.ParseFloat(s, 64)
	if err != nil { // too large to represent
		return 0, fmt.Errorf("%w: %q is out of range", ErrJobCPU, s)
	}
	if val < minCPU || val > maxCPU {
		return 0, fmt.Errorf("%w: %q must be between %v and %v", ErrJobCPU, s, minCPU, maxCPU)
	}
	return val, nil
}

// parseMemoryBytes parses "512Mi", "2Gi" or plain bytes into bytes.
func parseMemoryBytes(s string) (int64, error) {
	m := memoryRegex.FindStringSubmatch(s)
	if m == nil {
		return 0, fmt.Errorf("%w: invalid memory quantity %q", ErrJobMemory, s)
	}

	multiplier := int64(1)
	switch m[2] {
	case "Gi":
		multiplier = giB
	case "Mi":
		multiplier = miB
	}

	val, err := strconv.ParseInt(m[1], 10, 64)
	// Check before multiplying: int64 multiplication wraps silently in Go.
	if err != nil || val > math.MaxInt64/multiplier {
		return 0, fmt.Errorf("%w: memory quantity %q is out of range", ErrJobMemory, s)
	}

	bytes := val * multiplier
	if bytes < minMemory || bytes > maxMemory {
		return 0, fmt.Errorf("%w: %q must be between 64Mi and 1Ti", ErrJobMemory, s)
	}
	return bytes, nil
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
		if !s3SegmentRegex.MatchString(seg) {
			return fmt.Errorf("%w: illegal character in path segment %q of %q", ErrInvalidPrefix, seg, prefix)
		}
	}
	return nil
}

func prefixesOverlap(a, b string) bool {
	return strings.HasPrefix(a, b) || strings.HasPrefix(b, a)
}

// validateHostPath requires an absolute, already-cleaned path other than "/",
// so the string we compare is the string Docker mounts.
func validateHostPath(envName, p string) error {
	if !filepath.IsAbs(p) || filepath.Clean(p) != p || p == "/" {
		return fmt.Errorf("%w: %s must be an absolute, clean path other than '/' (no trailing slash), got %q", ErrInvalidPath, envName, p)
	}
	return nil
}

// dirsOverlap reports whether a and b are equal or one contains the other.
// The trailing "/" stops /srv/data from matching /srv/data2.
func dirsOverlap(a, b string) bool {
	return strings.HasPrefix(a+"/", b+"/") || strings.HasPrefix(b+"/", a+"/")
}

func (c Config) hasAnyS3Credentials() bool {
	return c.S3KeyID != "" || c.S3Secret != "" || c.S3Region != ""
}

// Validate checks the configuration for consistency and safety at startup.
// It has no side effects; it only reads c. The checks run in this order and
// the first failure is returned.
func (c Config) Validate() error {
	checks := []func() error{
		c.validateModes,
		c.validateLocalResults,
		c.validateS3,
		c.validateInputPrefixes,
		c.validateDataSource,
		c.validateResources,
	}
	for _, check := range checks {
		if err := check(); err != nil {
			return err
		}
	}
	return nil
}

// validateModes checks the orchestrator and results mode, and that they fit together.
func (c Config) validateModes() error {
	if c.Orchestrator != OrchestratorDocker && c.Orchestrator != OrchestratorKubernetes {
		return fmt.Errorf("%w: %s must be 'docker' or 'kubernetes', got %q", ErrInvalidOrchestrator, EnvOrchestrator, c.Orchestrator)
	}
	if c.ResultsMode != ResultsS3 && c.ResultsMode != ResultsLocal {
		return fmt.Errorf("%w: %s must be 's3' or 'local', got %q", ErrInvalidMode, EnvResultsMode, c.ResultsMode)
	}
	// Store-free (local) is Docker only
	if c.ResultsMode == ResultsLocal && c.Orchestrator != OrchestratorDocker {
		return fmt.Errorf("%w: local results mode is only supported with docker orchestrator", ErrInvalidMode)
	}
	return nil
}

// validateLocalResults checks the store-free settings. It does nothing in s3 mode.
func (c Config) validateLocalResults() error {
	if c.ResultsMode != ResultsLocal {
		return nil
	}
	if c.OutputPrefix != "" {
		return fmt.Errorf("%w: %s cannot be set in local mode", ErrOutputPrefixRefused, EnvS3OutputPrefix)
	}
	if c.ResultsDir == "" {
		return fmt.Errorf("%w: %s is required when results mode is local", ErrMissingField, EnvResultsDir)
	}
	return validateHostPath(EnvResultsDir, c.ResultsDir)
}

// validateS3 checks the output prefix and credentials (required in s3 mode,
// all-or-nothing in local mode) and the URL style.
func (c Config) validateS3() error {
	if c.ResultsMode == ResultsS3 {
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
		// Local mode: S3 is optional, but if any of it is used, all of it is needed.
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

	if c.S3URLStyle != URLStyleVhost && c.S3URLStyle != URLStylePath {
		return fmt.Errorf("%w: %s must be 'vhost' or 'path', got %q", ErrInvalidURLStyle, EnvS3URLStyle, c.S3URLStyle)
	}
	return nil
}

// validateInputPrefixes checks each input prefix and that none overlaps the output prefix.
func (c Config) validateInputPrefixes() error {
	for _, prefix := range c.InputPrefixes {
		if err := validateS3Prefix(prefix); err != nil {
			return fmt.Errorf("%s: %w", EnvS3InputPrefixes, err)
		}
		if c.OutputPrefix != "" && prefixesOverlap(prefix, c.OutputPrefix) {
			return fmt.Errorf("%w: input prefix %q overlaps with output prefix %q", ErrPrefixOverlap, prefix, c.OutputPrefix)
		}
	}
	return nil
}

// validateDataSource checks the data source for the orchestrator in use, and
// that local results do not sit inside it.
func (c Config) validateDataSource() error {
	if c.DataSource == "" {
		return nil
	}
	switch c.Orchestrator {
	case OrchestratorDocker:
		if err := validateHostPath(EnvDataSource, c.DataSource); err != nil {
			return err
		}
	case OrchestratorKubernetes:
		if len(c.DataSource) > 253 || !dnsSubdomainRegex.MatchString(c.DataSource) {
			return fmt.Errorf("%w: %s in kubernetes must be a PVC name (lowercase letters, digits, '-' and '.', max 253), got %q", ErrInvalidPath, EnvDataSource, c.DataSource)
		}
	}

	// Local results must not sit inside the data source (or contain it), or
	// every worker could read other jobs' results through /data.
	// Both paths are known to be absolute and clean here (local mode implies docker).
	if c.ResultsMode == ResultsLocal && dirsOverlap(c.ResultsDir, c.DataSource) {
		return fmt.Errorf("%w: %s %q and %s %q must not contain each other", ErrDirOverlap, EnvResultsDir, c.ResultsDir, EnvDataSource, c.DataSource)
	}
	return nil
}

// validateResources parses and range-checks the CPU and memory overrides.
// Any known engine will do: the overrides are the same for all of them.
func (c Config) validateResources() error {
	_, err := c.ResourcesFor(EngineGeneral)
	return err
}
