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
	// S3 bucket naming: lowercase, so "MyBucket" cannot dodge the overlap
	// check while naming the same (case-insensitive) vhost bucket.
	s3BucketRegex = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)

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

// ErrUnknownEngine is returned for an engine name not in the engine table.
// Callers such as POST /jobs check for it with errors.Is to answer 400.
var ErrUnknownEngine = errors.New("unknown engine")

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
	DataSource       string   // optional host path (docker) or PVC name (kubernetes) (from TILCAYO_DATA_SOURCE)
	S3KeyID          Secret   // required whenever S3 is used
	S3Secret         Secret   // required whenever S3 is used
	S3Region         string   // required whenever S3 is used (no default)
	S3Endpoint       string   // optional worker endpoint
	S3PublicEndpoint string   // optional caller endpoint, defaults to S3Endpoint
	S3URLStyle       string   // "vhost" (default) or "path"
	S3UseSSL         bool     // default true; accepts what strconv.ParseBool does
	DockerNetwork    string
	JobCPU           float64 // cores for every engine; 0 means the default (from TILCAYO_JOB_CPU)
	JobMemoryBytes   int64   // memory for every engine; 0 means the engine's default (from TILCAYO_JOB_MEMORY)
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
// The only error is ErrUnknownEngine.
func (c Config) ResourcesFor(engine string) (Resources, error) {
	spec, ok := engines[engine]
	if !ok {
		return Resources{}, fmt.Errorf("%w: %q", ErrUnknownEngine, engine)
	}
	res := Resources{CPU: DefaultCPU, MemoryBytes: spec.DefaultMemory}
	if c.JobCPU != 0 {
		res.CPU = c.JobCPU
	}
	if c.JobMemoryBytes != 0 {
		res.MemoryBytes = c.JobMemoryBytes
	}
	return res, nil
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

// parseCPU parses a CPU count in cores.
func parseCPU(s string) (float64, error) {
	v, err := strconv.ParseFloat(s, 64)
	// Written as !(in range) on purpose: ParseFloat accepts "NaN" and "Inf",
	// and every comparison with NaN is false, so "v < min || v > max" would
	// let NaN through. This form rejects NaN and both infinities.
	if err != nil || !(v >= minCPU && v <= maxCPU) {
		return 0, fmt.Errorf("%s must be a number of cores from %v to %v, got %q", EnvJobCPU, minCPU, maxCPU, s)
	}
	return v, nil
}

// parseMemoryBytes parses "512Mi", "2Gi" or plain bytes into bytes.
func parseMemoryBytes(s string) (int64, error) {
	bad := fmt.Errorf("%s must be 64Mi to 1Ti, in Mi, Gi or plain bytes (e.g. 512Mi), got %q", EnvJobMemory, s)
	m := memoryRegex.FindStringSubmatch(s)
	if m == nil {
		return 0, bad
	}

	multiplier := int64(1)
	switch m[2] {
	case "Gi":
		multiplier = giB
	case "Mi":
		multiplier = miB
	}

	val, err := strconv.ParseInt(m[1], 10, 64)
	// Compare before multiplying: int64 multiplication wraps silently in Go,
	// so a huge value could otherwise wrap to 0 ("unlimited" in Docker).
	if err != nil || val > maxMemory/multiplier || val*multiplier < minMemory {
		return 0, bad
	}
	return val * multiplier, nil
}

// validateS3Prefix checks the s3://<bucket>/[path/] shape and bucket naming,
// and rejects empty, "." and ".." segments, which some HTTP layers collapse.
func validateS3Prefix(prefix string) error {
	if !strings.HasPrefix(prefix, "s3://") || !strings.HasSuffix(prefix, "/") {
		return fmt.Errorf("prefix %q must start with 's3://' and end with '/'", prefix)
	}

	parts := strings.Split(strings.TrimPrefix(prefix, "s3://"), "/")
	if !s3BucketRegex.MatchString(parts[0]) {
		return fmt.Errorf("invalid bucket name %q in %q", parts[0], prefix)
	}
	// Skip the bucket, and the empty element after the trailing slash.
	for _, seg := range parts[1 : len(parts)-1] {
		if seg == "" || seg == "." || seg == ".." {
			return fmt.Errorf("path segment %q not allowed in %q", seg, prefix)
		}
	}
	return nil
}

func prefixesOverlap(a, b string) bool {
	return strings.HasPrefix(a, b) || strings.HasPrefix(b, a)
}

// validateHostPath requires an absolute path other than "/". FromEnv has
// already cleaned it, so the string we compare is the string Docker mounts.
func validateHostPath(envName, p string) error {
	if !filepath.IsAbs(p) || p == "/" {
		return fmt.Errorf("%s must be an absolute path other than '/', got %q", envName, p)
	}
	return nil
}

// dirsOverlap reports whether a and b are equal or one contains the other.
// The trailing "/" stops /srv/data from matching /srv/data2.
func dirsOverlap(a, b string) bool {
	return strings.HasPrefix(a+"/", b+"/") || strings.HasPrefix(b+"/", a+"/")
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
		return fmt.Errorf("%s must be 'docker' or 'kubernetes', got %q", EnvOrchestrator, c.Orchestrator)
	}
	if c.ResultsMode != ResultsS3 && c.ResultsMode != ResultsLocal {
		return fmt.Errorf("%s must be 's3' or 'local', got %q", EnvResultsMode, c.ResultsMode)
	}
	if c.ResultsMode == ResultsLocal && c.Orchestrator != OrchestratorDocker {
		return fmt.Errorf("%s=local is only supported with %s=docker", EnvResultsMode, EnvOrchestrator)
	}
	return nil
}

// validateLocalResults checks the store-free settings. It does nothing in s3 mode.
func (c Config) validateLocalResults() error {
	if c.ResultsMode != ResultsLocal {
		return nil
	}
	if c.OutputPrefix != "" {
		return fmt.Errorf("%s cannot be set when %s=local", EnvS3OutputPrefix, EnvResultsMode)
	}
	if c.ResultsDir == "" {
		return fmt.Errorf("%s is required when %s=local", EnvResultsDir, EnvResultsMode)
	}
	return validateHostPath(EnvResultsDir, c.ResultsDir)
}

// validateS3 checks the output prefix (s3 mode), the credentials (whenever
// S3 is used: s3 mode or any input prefix) and the URL style.
func (c Config) validateS3() error {
	if c.ResultsMode == ResultsS3 {
		if c.OutputPrefix == "" {
			return fmt.Errorf("%s is required when %s=s3", EnvS3OutputPrefix, EnvResultsMode)
		}
		if err := validateS3Prefix(c.OutputPrefix); err != nil {
			return fmt.Errorf("%s: %w", EnvS3OutputPrefix, err)
		}
	}

	if c.ResultsMode == ResultsS3 || len(c.InputPrefixes) > 0 {
		if c.S3KeyID == "" {
			return fmt.Errorf("%s is required when S3 is used", EnvS3KeyID)
		}
		if c.S3Secret == "" {
			return fmt.Errorf("%s is required when S3 is used", EnvS3Secret)
		}
		if c.S3Region == "" {
			return fmt.Errorf("%s is required when S3 is used", EnvS3Region)
		}
	}

	if c.S3URLStyle != URLStyleVhost && c.S3URLStyle != URLStylePath {
		return fmt.Errorf("%s must be 'vhost' or 'path', got %q", EnvS3URLStyle, c.S3URLStyle)
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
			return fmt.Errorf("%s: input prefix %q overlaps %s %q", EnvS3InputPrefixes, prefix, EnvS3OutputPrefix, c.OutputPrefix)
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
			return fmt.Errorf("%s in kubernetes must be a PVC name (lowercase letters, digits, '-' and '.', max 253), got %q", EnvDataSource, c.DataSource)
		}
	}

	// Local results must not sit inside the data source (or contain it), or
	// every worker could read other jobs' results through /data.
	// Both paths are absolute here (local mode implies docker).
	if c.ResultsMode == ResultsLocal && dirsOverlap(c.ResultsDir, c.DataSource) {
		return fmt.Errorf("%s %q and %s %q must not contain each other", EnvResultsDir, c.ResultsDir, EnvDataSource, c.DataSource)
	}
	return nil
}
