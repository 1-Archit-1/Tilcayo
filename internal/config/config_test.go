package config_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/1-Archit-1/Tilcayo/internal/config"
)

const (
	mi = 1 << 20
	gi = 1 << 30
)

var engines = []string{config.EngineGeneral, config.EngineSpatial, config.EngineML}

func validS3Config() config.Config {
	return config.Config{
		Orchestrator:  "docker",
		ResultsMode:   "s3",
		OutputPrefix:  "s3://mybucket/results/",
		InputPrefixes: []string{"s3://mybucket/inputs/", "s3://otherbucket/data/"},
		S3KeyID:       "minioadmin",
		S3Secret:      "minioadmin",
		S3Region:      "garage",
		S3URLStyle:    "vhost",
	}
}

func validLocalConfig() config.Config {
	return config.Config{
		Orchestrator: "docker",
		ResultsMode:  "local",
		ResultsDir:   "/var/results",
		S3URLStyle:   "vhost",
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name         string
		base         func() config.Config // defaults to validS3Config
		modify       func(*config.Config)
		wantSentinel error  // nil together with empty wantSubstr means "valid"
		wantSubstr   string // usually the variable the error must name
	}{
		// Valid configs
		{
			name:   "valid s3 config",
			modify: func(c *config.Config) {},
		},
		{
			name:   "valid local config",
			base:   validLocalConfig,
			modify: func(c *config.Config) {},
		},
		{
			name: "valid local config with data source",
			base: validLocalConfig,
			modify: func(c *config.Config) {
				c.DataSource = "/var/data"
			},
		},
		{
			name: "valid local config with S3 inputs and keys",
			base: validLocalConfig,
			modify: func(c *config.Config) {
				c.InputPrefixes = []string{"s3://inbucket/data/"}
				c.S3KeyID, c.S3Secret, c.S3Region = "k", "s", "garage"
			},
		},
		{
			name: "local config with keys but no prefixes is valid",
			base: validLocalConfig,
			modify: func(c *config.Config) {
				c.S3KeyID, c.S3Secret, c.S3Region = "k", "s", "garage"
			},
		},
		{
			name: "valid kubernetes s3 config",
			modify: func(c *config.Config) {
				c.Orchestrator = "kubernetes"
				c.DataSource = "my-pvc"
			},
		},
		{
			name:   "url style path is valid",
			modify: func(c *config.Config) { c.S3URLStyle = "path" },
		},

		// Orchestrator and results mode
		{
			name:         "orchestrator empty",
			modify:       func(c *config.Config) { c.Orchestrator = "" },
			wantSentinel: config.ErrInvalidOrchestrator,
			wantSubstr:   "TILCAYO_ORCHESTRATOR",
		},
		{
			name:         "invalid orchestrator",
			modify:       func(c *config.Config) { c.Orchestrator = "nomad" },
			wantSentinel: config.ErrInvalidOrchestrator,
			wantSubstr:   "TILCAYO_ORCHESTRATOR",
		},
		{
			name:         "invalid results mode",
			modify:       func(c *config.Config) { c.ResultsMode = "azure-blob" },
			wantSentinel: config.ErrInvalidMode,
			wantSubstr:   "TILCAYO_RESULTS_MODE",
		},
		{
			name:         "empty results mode",
			modify:       func(c *config.Config) { c.ResultsMode = "" },
			wantSentinel: config.ErrInvalidMode,
		},
		{
			name: "local mode refused in kubernetes",
			base: validLocalConfig,
			modify: func(c *config.Config) {
				c.Orchestrator = "kubernetes"
			},
			wantSentinel: config.ErrInvalidMode,
		},

		// Local mode: results dir and output prefix
		{
			name:         "local mode missing results dir",
			base:         validLocalConfig,
			modify:       func(c *config.Config) { c.ResultsDir = "" },
			wantSentinel: config.ErrMissingField,
			wantSubstr:   "TILCAYO_RESULTS_DIR",
		},
		{
			name:         "local mode relative results dir",
			base:         validLocalConfig,
			modify:       func(c *config.Config) { c.ResultsDir = "relative/results" },
			wantSentinel: config.ErrInvalidPath,
			wantSubstr:   "TILCAYO_RESULTS_DIR",
		},
		{
			name:         "local mode unclean results dir",
			base:         validLocalConfig,
			modify:       func(c *config.Config) { c.ResultsDir = "/a/../b" },
			wantSentinel: config.ErrInvalidPath,
			wantSubstr:   "TILCAYO_RESULTS_DIR",
		},
		{
			name:         "local mode results dir with trailing slash",
			base:         validLocalConfig,
			modify:       func(c *config.Config) { c.ResultsDir = "/var/results/" },
			wantSentinel: config.ErrInvalidPath,
		},
		{
			name:         "local mode results dir is root",
			base:         validLocalConfig,
			modify:       func(c *config.Config) { c.ResultsDir = "/" },
			wantSentinel: config.ErrInvalidPath,
			wantSubstr:   "TILCAYO_RESULTS_DIR",
		},
		{
			name:         "local mode with output prefix refused",
			base:         validLocalConfig,
			modify:       func(c *config.Config) { c.OutputPrefix = "s3://mybucket/results/" },
			wantSentinel: config.ErrOutputPrefixRefused,
		},

		// S3 mode: required fields
		{
			name:         "s3 mode missing output prefix",
			modify:       func(c *config.Config) { c.OutputPrefix = "" },
			wantSentinel: config.ErrMissingField,
			wantSubstr:   "TILCAYO_S3_OUTPUT_PREFIX",
		},
		{
			name:         "s3 mode missing region",
			modify:       func(c *config.Config) { c.S3Region = "" },
			wantSentinel: config.ErrMissingField,
			wantSubstr:   "TILCAYO_S3_REGION",
		},
		{
			name:         "s3 mode missing key id",
			modify:       func(c *config.Config) { c.S3KeyID = "" },
			wantSentinel: config.ErrMissingField,
			wantSubstr:   "TILCAYO_S3_KEY_ID",
		},
		{
			name:         "s3 mode missing secret",
			modify:       func(c *config.Config) { c.S3Secret = "" },
			wantSentinel: config.ErrMissingField,
			wantSubstr:   "TILCAYO_S3_SECRET",
		},

		// Local mode: S3 credential coupling (issue #18 section 7, issue #5 decision 6)
		{
			name: "local prefixes without any keys",
			base: validLocalConfig,
			modify: func(c *config.Config) {
				c.InputPrefixes = []string{"s3://inbucket/data/"}
			},
			wantSentinel: config.ErrMissingField,
			wantSubstr:   "TILCAYO_S3_KEY_ID",
		},
		{
			name: "local prefixes without key id",
			base: validLocalConfig,
			modify: func(c *config.Config) {
				c.InputPrefixes = []string{"s3://inbucket/data/"}
				c.S3Secret, c.S3Region = "s", "garage"
			},
			wantSentinel: config.ErrMissingField,
			wantSubstr:   "TILCAYO_S3_KEY_ID",
		},
		{
			name: "local prefixes without secret",
			base: validLocalConfig,
			modify: func(c *config.Config) {
				c.InputPrefixes = []string{"s3://inbucket/data/"}
				c.S3KeyID, c.S3Region = "k", "garage"
			},
			wantSentinel: config.ErrMissingField,
			wantSubstr:   "TILCAYO_S3_SECRET",
		},
		{
			name: "local prefixes without region",
			base: validLocalConfig,
			modify: func(c *config.Config) {
				c.InputPrefixes = []string{"s3://inbucket/data/"}
				c.S3KeyID, c.S3Secret = "k", "s"
			},
			wantSentinel: config.ErrMissingField,
			wantSubstr:   "TILCAYO_S3_REGION",
		},
		{
			name:         "local only key id set",
			base:         validLocalConfig,
			modify:       func(c *config.Config) { c.S3KeyID = "k" },
			wantSentinel: config.ErrMissingField,
			wantSubstr:   "TILCAYO_S3_SECRET",
		},
		{
			name:         "local only secret set",
			base:         validLocalConfig,
			modify:       func(c *config.Config) { c.S3Secret = "s" },
			wantSentinel: config.ErrMissingField,
			wantSubstr:   "TILCAYO_S3_KEY_ID",
		},
		{
			name:         "local only region set",
			base:         validLocalConfig,
			modify:       func(c *config.Config) { c.S3Region = "garage" },
			wantSentinel: config.ErrMissingField,
			wantSubstr:   "TILCAYO_S3_KEY_ID",
		},
		{
			name: "local keys without region",
			base: validLocalConfig,
			modify: func(c *config.Config) {
				c.S3KeyID, c.S3Secret = "k", "s"
			},
			wantSentinel: config.ErrMissingField,
			wantSubstr:   "TILCAYO_S3_REGION",
		},

		// Overlap between input and output prefixes
		{
			name: "input prefix overlaps output prefix (parent)",
			modify: func(c *config.Config) {
				c.OutputPrefix = "s3://mybucket/results/jobs/"
				c.InputPrefixes = []string{"s3://mybucket/results/"}
			},
			wantSentinel: config.ErrPrefixOverlap,
		},
		{
			name: "input prefix overlaps output prefix (child)",
			modify: func(c *config.Config) {
				c.OutputPrefix = "s3://mybucket/data/"
				c.InputPrefixes = []string{"s3://mybucket/data/subpath/"}
			},
			wantSentinel: config.ErrPrefixOverlap,
		},
		{
			name: "input prefix equals output prefix",
			modify: func(c *config.Config) {
				c.InputPrefixes = []string{c.OutputPrefix}
			},
			wantSentinel: config.ErrPrefixOverlap,
		},
		{
			name: "sibling buckets do not overlap (abc vs abcd)",
			modify: func(c *config.Config) {
				c.OutputPrefix = "s3://abcd/"
				c.InputPrefixes = []string{"s3://abc/"}
			},
		},
		{
			name: "sibling buckets do not overlap (abcd vs abc)",
			modify: func(c *config.Config) {
				c.OutputPrefix = "s3://abc/"
				c.InputPrefixes = []string{"s3://abcd/"}
			},
		},
		{
			name: "sibling folders do not overlap (res vs results)",
			modify: func(c *config.Config) {
				c.OutputPrefix = "s3://mybucket/results/"
				c.InputPrefixes = []string{"s3://mybucket/res/"}
			},
		},
		{
			name: "empty input prefix entry is invalid",
			modify: func(c *config.Config) {
				c.InputPrefixes = []string{""}
			},
			wantSentinel: config.ErrInvalidPrefix,
			wantSubstr:   "TILCAYO_S3_INPUT_PREFIXES",
		},

		// URL style
		{
			name:         "url style Path (wrong case)",
			modify:       func(c *config.Config) { c.S3URLStyle = "Path" },
			wantSentinel: config.ErrInvalidURLStyle,
			wantSubstr:   "TILCAYO_S3_URL_STYLE",
		},
		{
			name:         "url style virtual",
			modify:       func(c *config.Config) { c.S3URLStyle = "virtual" },
			wantSentinel: config.ErrInvalidURLStyle,
		},
		{
			name:         "url style empty",
			modify:       func(c *config.Config) { c.S3URLStyle = "" },
			wantSentinel: config.ErrInvalidURLStyle,
		},

		// Data source
		{
			name:         "docker data source relative",
			modify:       func(c *config.Config) { c.DataSource = "relative/path" },
			wantSentinel: config.ErrInvalidPath,
			wantSubstr:   "TILCAYO_DATA_SOURCE",
		},
		{
			name:         "docker data source is root",
			modify:       func(c *config.Config) { c.DataSource = "/" },
			wantSentinel: config.ErrInvalidPath,
		},
		{
			name:         "docker data source unclean",
			modify:       func(c *config.Config) { c.DataSource = "/data/../etc" },
			wantSentinel: config.ErrInvalidPath,
		},
		{
			name:         "docker data source trailing slash",
			modify:       func(c *config.Config) { c.DataSource = "/var/data/" },
			wantSentinel: config.ErrInvalidPath,
		},
		{
			name:   "docker data source absolute",
			modify: func(c *config.Config) { c.DataSource = "/var/data" },
		},
		{
			name: "kubernetes data source with slashes",
			modify: func(c *config.Config) {
				c.Orchestrator = "kubernetes"
				c.DataSource = "pvc/with/slash"
			},
			wantSentinel: config.ErrInvalidPath,
		},
		{
			name: "kubernetes data source valid dotted name",
			modify: func(c *config.Config) {
				c.Orchestrator = "kubernetes"
				c.DataSource = "a.b-c1"
			},
		},
		{
			name: "kubernetes data source 253 chars is valid",
			modify: func(c *config.Config) {
				c.Orchestrator = "kubernetes"
				c.DataSource = strings.Repeat("a", 253)
			},
		},
		{
			name: "kubernetes data source 254 chars",
			modify: func(c *config.Config) {
				c.Orchestrator = "kubernetes"
				c.DataSource = strings.Repeat("a", 254)
			},
			wantSentinel: config.ErrInvalidPath,
		},
		{
			name: "kubernetes data source uppercase",
			modify: func(c *config.Config) {
				c.Orchestrator = "kubernetes"
				c.DataSource = "MyPVC"
			},
			wantSentinel: config.ErrInvalidPath,
		},
		{
			name: "kubernetes data source uppercase in the middle",
			modify: func(c *config.Config) {
				c.Orchestrator = "kubernetes"
				c.DataSource = "mYpvc"
			},
			wantSentinel: config.ErrInvalidPath,
		},
		{
			name: "kubernetes data source leading dash",
			modify: func(c *config.Config) {
				c.Orchestrator = "kubernetes"
				c.DataSource = "-pvc"
			},
			wantSentinel: config.ErrInvalidPath,
		},
		{
			name: "kubernetes data source trailing dash",
			modify: func(c *config.Config) {
				c.Orchestrator = "kubernetes"
				c.DataSource = "pvc-"
			},
			wantSentinel: config.ErrInvalidPath,
		},
		{
			name: "kubernetes data source with space",
			modify: func(c *config.Config) {
				c.Orchestrator = "kubernetes"
				c.DataSource = "pvc name"
			},
			wantSentinel: config.ErrInvalidPath,
		},
		{
			name: "kubernetes data source with colon",
			modify: func(c *config.Config) {
				c.Orchestrator = "kubernetes"
				c.DataSource = "x:y"
			},
			wantSentinel: config.ErrInvalidPath,
		},
		{
			name: "kubernetes data source dot-dot",
			modify: func(c *config.Config) {
				c.Orchestrator = "kubernetes"
				c.DataSource = ".."
			},
			wantSentinel: config.ErrInvalidPath,
		},

		// Results dir vs data source (local mode)
		{
			name: "results dir inside data source",
			base: validLocalConfig,
			modify: func(c *config.Config) {
				c.ResultsDir = "/srv/data/results"
				c.DataSource = "/srv/data"
			},
			wantSentinel: config.ErrDirOverlap,
			wantSubstr:   "TILCAYO_RESULTS_DIR",
		},
		{
			name: "data source inside results dir",
			base: validLocalConfig,
			modify: func(c *config.Config) {
				c.ResultsDir = "/srv/data"
				c.DataSource = "/srv/data/inputs"
			},
			wantSentinel: config.ErrDirOverlap,
			wantSubstr:   "TILCAYO_DATA_SOURCE",
		},
		{
			name: "results dir equals data source",
			base: validLocalConfig,
			modify: func(c *config.Config) {
				c.ResultsDir = "/srv/data"
				c.DataSource = "/srv/data"
			},
			wantSentinel: config.ErrDirOverlap,
		},
		{
			name: "results dir and data source are siblings (data vs data2)",
			base: validLocalConfig,
			modify: func(c *config.Config) {
				c.ResultsDir = "/srv/data2"
				c.DataSource = "/srv/data"
			},
		},
		{
			name: "results dir and data source are siblings (data2 vs data)",
			base: validLocalConfig,
			modify: func(c *config.Config) {
				c.ResultsDir = "/srv/data"
				c.DataSource = "/srv/data2"
			},
		},

		// Resources (full tables in TestResourcesFor*)
		{
			name:         "invalid CPU negative",
			modify:       func(c *config.Config) { c.JobCPU = "-1" },
			wantSentinel: config.ErrJobCPU,
			wantSubstr:   "TILCAYO_JOB_CPU",
		},
		{
			name:         "invalid memory unit",
			modify:       func(c *config.Config) { c.JobMemory = "banana" },
			wantSentinel: config.ErrJobMemory,
			wantSubstr:   "TILCAYO_JOB_MEMORY",
		},
		{
			name:         "whitespace-only memory",
			modify:       func(c *config.Config) { c.JobMemory = "   " },
			wantSentinel: config.ErrJobMemory,
		},
		{
			name: "valid CPU and memory overrides",
			modify: func(c *config.Config) {
				c.JobCPU = "0.5"
				c.JobMemory = "512Mi"
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			base := tt.base
			if base == nil {
				base = validS3Config
			}
			cfg := base()
			tt.modify(&cfg)
			err := cfg.Validate()

			if tt.wantSentinel == nil && tt.wantSubstr == "" {
				if err != nil {
					t.Fatalf("expected clean validation, got error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error, got nil")
			}
			if tt.wantSentinel != nil && !errors.Is(err, tt.wantSentinel) {
				t.Errorf("expected error wrapping sentinel %v, got %v", tt.wantSentinel, err)
			}
			if tt.wantSubstr != "" && !strings.Contains(err.Error(), tt.wantSubstr) {
				t.Errorf("expected error containing %q, got %q", tt.wantSubstr, err.Error())
			}
		})
	}
}

// Each row breaks exactly one prefix rule. Every prefix is tried as the output
// prefix and as an input prefix (the other one stays valid), so validating
// input prefixes cannot be skipped.
func TestValidatePrefixRules(t *testing.T) {
	bucket63 := strings.Repeat("a", 63)
	bucket64 := strings.Repeat("a", 64)

	tests := []struct {
		name   string
		prefix string
		valid  bool
	}{
		{"plain prefix", "s3://other/data/", true},
		{"bucket only", "s3://other/", true},
		{"nested folders", "s3://other/a/b/c/", true},
		{"allowed punctuation in segment", "s3://other/a.b_c=d@e+f-g/", true},
		{"dotted bucket", "s3://a.b-c/x/", true},
		{"63 char bucket", "s3://" + bucket63 + "/x/", true},
		{"3 char bucket", "s3://abc/x/", true},

		{"no scheme", "other/data/", false},
		{"http scheme", "http://other/data/", false},
		{"uppercase scheme", "S3://other/data/", false},
		{"no trailing slash", "s3://other/data", false},
		{"scheme only", "s3://", false},
		{"empty bucket", "s3:///data/", false},
		{"64 char bucket", "s3://" + bucket64 + "/x/", false},
		{"2 char bucket", "s3://ab/x/", false},
		{"bucket leading dash", "s3://-abc/x/", false},
		{"bucket leading dot", "s3://.abc/x/", false},
		{"bucket trailing dash", "s3://abc-/x/", false},
		{"bucket trailing dot", "s3://abc./x/", false},
		{"bucket underscore in the middle", "s3://a_b_c/x/", false},
		{"bucket underscore at the end", "s3://abc_/x/", false},
		{"bucket underscore at the start", "s3://_abc/x/", false},
		{"bucket uppercase", "s3://Other/x/", false},
		{"bucket uppercase in the middle", "s3://oTher/x/", false},
		{"bucket uppercase at the end", "s3://otheR/x/", false},
		{"bucket trailing newline", "s3://other\n/x/", false},

		{"empty segment", "s3://other//x/", false},
		{"dot segment", "s3://other/./x/", false},
		{"dot-dot segment", "s3://other/a/../x/", false},
		{"NUL", "s3://other/a\x00b/", false},
		{"tab", "s3://other/a\tb/", false},
		{"newline", "s3://other/a\nb/", false},
		{"0x7f", "s3://other/a\x7fb/", false},
		{"space", "s3://other/a b/", false},
		{"percent", "s3://other/%72esults/", false},
		{"question mark", "s3://other/a?b/", false},
		{"hash", "s3://other/a#b/", false},
		{"single quote", "s3://other/a'b/", false},
		{"double quote", "s3://other/a\"b/", false},
		{"backslash", "s3://other/a\\b/", false},
		{"comma", "s3://other/a,b/", false},
		{"non-ASCII", "s3://other/caf\u00e9/", false},
	}

	for _, tt := range tests {
		t.Run("output/"+tt.name, func(t *testing.T) {
			cfg := validS3Config()
			cfg.InputPrefixes = nil
			cfg.OutputPrefix = tt.prefix
			assertPrefixResult(t, cfg.Validate(), tt.valid)
		})
		t.Run("input/"+tt.name, func(t *testing.T) {
			cfg := validS3Config()
			cfg.OutputPrefix = "s3://mybucket/results/"
			cfg.InputPrefixes = []string{tt.prefix}
			assertPrefixResult(t, cfg.Validate(), tt.valid)
		})
	}
}

func assertPrefixResult(t *testing.T, err error, valid bool) {
	t.Helper()
	switch {
	case valid && err != nil:
		t.Fatalf("expected valid prefix, got error: %v", err)
	case !valid && !errors.Is(err, config.ErrInvalidPrefix):
		t.Fatalf("expected ErrInvalidPrefix, got %v", err)
	}
}

func TestResourcesForDefaults(t *testing.T) {
	// Straight from FromEnv, before Validate: ResourcesFor must still work.
	cfg := validS3Config()
	tests := []struct {
		engine  string
		wantMem int64
	}{
		{config.EngineGeneral, 1 * gi},
		{config.EngineSpatial, 1 * gi},
		{config.EngineML, 4 * gi},
	}
	for _, tt := range tests {
		t.Run(tt.engine, func(t *testing.T) {
			res, err := cfg.ResourcesFor(tt.engine)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if res.CPU != 1.0 || res.MemoryBytes != tt.wantMem {
				t.Errorf("got %+v, want CPU 1 and %d bytes", res, tt.wantMem)
			}
		})
	}
}

func TestResourcesForUnknownEngine(t *testing.T) {
	cfg := validS3Config()
	for _, engine := range []string{"", "unknown", "ML", "General", "gpu"} {
		if _, err := cfg.ResourcesFor(engine); !errors.Is(err, config.ErrUnknownEngine) {
			t.Errorf("ResourcesFor(%q): expected ErrUnknownEngine, got %v", engine, err)
		}
	}
}

// A valid override applies to every engine, including ml.
func TestResourcesForOverrides(t *testing.T) {
	tests := []struct {
		name    string
		cpu     string
		memory  string
		wantCPU float64 // 0 means the default (1)
		wantMem int64   // 0 means the engine's default
	}{
		{name: "cpu only", cpu: "0.5", wantCPU: 0.5},
		{name: "cpu 2", cpu: "2", wantCPU: 2},
		{name: "cpu floor", cpu: "0.01", wantCPU: 0.01},
		{name: "cpu ceiling", cpu: "256", wantCPU: 256},
		{name: "memory Mi", memory: "512Mi", wantMem: 512 * mi},
		{name: "memory Gi", memory: "2Gi", wantMem: 2 * gi},
		{name: "memory floor", memory: "64Mi", wantMem: 64 * mi},
		{name: "memory ceiling", memory: "1024Gi", wantMem: 1024 * gi},
		{name: "memory plain bytes", memory: "134217728", wantMem: 128 * mi},
		{name: "both", cpu: "1.5", memory: "3Gi", wantCPU: 1.5, wantMem: 3 * gi},
	}
	defaults := map[string]int64{
		config.EngineGeneral: 1 * gi,
		config.EngineSpatial: 1 * gi,
		config.EngineML:      4 * gi,
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validS3Config()
			cfg.JobCPU = tt.cpu
			cfg.JobMemory = tt.memory
			if err := cfg.Validate(); err != nil {
				t.Fatalf("Validate failed: %v", err)
			}
			for _, engine := range engines {
				wantCPU, wantMem := tt.wantCPU, tt.wantMem
				if wantCPU == 0 {
					wantCPU = 1
				}
				if wantMem == 0 {
					wantMem = defaults[engine]
				}
				res, err := cfg.ResourcesFor(engine)
				if err != nil {
					t.Fatalf("%s: unexpected error: %v", engine, err)
				}
				if res.CPU != wantCPU || res.MemoryBytes != wantMem {
					t.Errorf("%s: got %+v, want CPU %v and %d bytes", engine, res, wantCPU, wantMem)
				}
			}
		})
	}
}

func TestResourcesForInvalid(t *testing.T) {
	tests := []struct {
		name         string
		cpu          string
		memory       string
		wantSentinel error
	}{
		// CPU: NaN passes a plain "<= 0" check, and tiny values would become NanoCPUs 0 (unlimited).
		{name: "cpu zero", cpu: "0", wantSentinel: config.ErrJobCPU},
		{name: "cpu negative", cpu: "-1", wantSentinel: config.ErrJobCPU},
		{name: "cpu NaN", cpu: "NaN", wantSentinel: config.ErrJobCPU},
		{name: "cpu Inf", cpu: "Inf", wantSentinel: config.ErrJobCPU},
		{name: "cpu +Inf", cpu: "+Inf", wantSentinel: config.ErrJobCPU},
		{name: "cpu -Inf", cpu: "-Inf", wantSentinel: config.ErrJobCPU},
		{name: "cpu 1e-10", cpu: "1e-10", wantSentinel: config.ErrJobCPU},
		{name: "cpu 1e300", cpu: "1e300", wantSentinel: config.ErrJobCPU},
		{name: "cpu 1e3", cpu: "1e3", wantSentinel: config.ErrJobCPU},
		{name: "cpu abc", cpu: "abc", wantSentinel: config.ErrJobCPU},
		{name: "cpu comma", cpu: "1,5", wantSentinel: config.ErrJobCPU},
		{name: "cpu hex float", cpu: "0x1p-2", wantSentinel: config.ErrJobCPU},
		{name: "cpu underscore", cpu: "1_0", wantSentinel: config.ErrJobCPU},
		{name: "cpu no leading digit", cpu: ".5", wantSentinel: config.ErrJobCPU},
		{name: "cpu no trailing digit", cpu: "5.", wantSentinel: config.ErrJobCPU},
		{name: "cpu plus sign", cpu: "+1", wantSentinel: config.ErrJobCPU},
		{name: "cpu below floor", cpu: "0.001", wantSentinel: config.ErrJobCPU},
		{name: "cpu above ceiling", cpu: "257", wantSentinel: config.ErrJobCPU},
		{name: "cpu too many digits for a float", cpu: strings.Repeat("9", 400), wantSentinel: config.ErrJobCPU},

		// Memory
		{name: "memory zero Mi", memory: "0Mi", wantSentinel: config.ErrJobMemory},
		{name: "memory zero", memory: "0", wantSentinel: config.ErrJobMemory},
		{name: "memory negative", memory: "-1Gi", wantSentinel: config.ErrJobMemory},
		{name: "memory whitespace only", memory: "  ", wantSentinel: config.ErrJobMemory},
		{name: "memory abc", memory: "abc", wantSentinel: config.ErrJobMemory},
		{name: "memory unit only", memory: "Gi", wantSentinel: config.ErrJobMemory},
		{name: "memory bad unit", memory: "1Gx", wantSentinel: config.ErrJobMemory},
		{name: "memory space before unit", memory: "1 Gi", wantSentinel: config.ErrJobMemory},
		{name: "memory trailing junk after unit", memory: "2Gix", wantSentinel: config.ErrJobMemory},
		{name: "memory trailing junk after bytes", memory: "134217728x", wantSentinel: config.ErrJobMemory},
		{name: "memory plus sign", memory: "+5Gi", wantSentinel: config.ErrJobMemory},
		{name: "memory fraction", memory: "1.5Gi", wantSentinel: config.ErrJobMemory},
		{name: "memory 1 byte", memory: "1", wantSentinel: config.ErrJobMemory},
		{name: "memory 512 bytes", memory: "512", wantSentinel: config.ErrJobMemory},
		{name: "memory below floor", memory: "63Mi", wantSentinel: config.ErrJobMemory},
		{name: "memory above ceiling", memory: "1025Gi", wantSentinel: config.ErrJobMemory},
		// These used to wrap around int64 to 0, 1 Gi or 1 Mi.
		{name: "memory wraps to 0", memory: "17179869184Gi", wantSentinel: config.ErrJobMemory},
		{name: "memory wraps to 1Gi", memory: "17179869185Gi", wantSentinel: config.ErrJobMemory},
		{name: "memory wraps to 1Mi", memory: "17592186044417Mi", wantSentinel: config.ErrJobMemory},
		{name: "memory 8 EiB", memory: "8589934591Gi", wantSentinel: config.ErrJobMemory},
		{name: "memory max int64 bytes", memory: "9223372036854775807", wantSentinel: config.ErrJobMemory},
		{name: "memory beyond int64 digits", memory: "99999999999999999999Gi", wantSentinel: config.ErrJobMemory},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validS3Config()
			cfg.JobCPU = tt.cpu
			cfg.JobMemory = tt.memory

			if err := cfg.Validate(); !errors.Is(err, tt.wantSentinel) {
				t.Errorf("Validate: expected %v, got %v", tt.wantSentinel, err)
			}
			for _, engine := range engines {
				if _, err := cfg.ResourcesFor(engine); !errors.Is(err, tt.wantSentinel) {
					t.Errorf("ResourcesFor(%s): expected %v, got %v", engine, tt.wantSentinel, err)
				}
			}
		})
	}
}

// Docker treats NanoCPUs 0 as "unlimited", so it must never be 0.
func TestNanoCPUs(t *testing.T) {
	tests := []struct {
		cpu  string
		want int64 // 0 means only check the lower bound
	}{
		{"0.01", 10_000_000},
		{"0.5", 500_000_000},
		{"1", 1_000_000_000},
		{"2", 2_000_000_000},
		{"1.5", 1_500_000_000},
		{"2.01", 2_010_000_000}, // 2.01*1e9 is 2009999999.9999998 in float64, so it needs math.Round
		{"256", 256_000_000_000},
	}
	for _, tt := range tests {
		t.Run(tt.cpu, func(t *testing.T) {
			cfg := validS3Config()
			cfg.JobCPU = tt.cpu
			res, err := cfg.ResourcesFor(config.EngineGeneral)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			got := res.NanoCPUs()
			if got < 10_000_000 {
				t.Errorf("NanoCPUs %d is below the 0.01 CPU floor", got)
			}
			if got != tt.want {
				t.Errorf("NanoCPUs got %d, want %d", got, tt.want)
			}
		})
	}
}

func TestImageFor(t *testing.T) {
	t.Run("defaults use Version for every engine", func(t *testing.T) {
		if config.Version != "dev" {
			t.Fatalf("Version default got %q, want %q", config.Version, "dev")
		}
		cfg := validS3Config()
		for _, engine := range engines {
			want := "ghcr.io/1-archit-1/tilcayo-worker-" + engine + ":dev"
			got, err := cfg.ImageFor(engine)
			if err != nil || got != want {
				t.Errorf("ImageFor(%s) got %q, %v; want %q", engine, got, err, want)
			}
		}
	})

	t.Run("tag follows Version", func(t *testing.T) {
		old := config.Version
		t.Cleanup(func() { config.Version = old })
		config.Version = "v1.2.3"

		got, err := validS3Config().ImageFor(config.EngineML)
		if want := "ghcr.io/1-archit-1/tilcayo-worker-ml:v1.2.3"; err != nil || got != want {
			t.Errorf("got %q, %v; want %q", got, err, want)
		}
	})

	t.Run("each override applies to its own engine only", func(t *testing.T) {
		cfg := validS3Config()
		cfg.ImageGeneral = "custom-general:test"
		cfg.ImageSpatial = "custom-spatial:test"
		cfg.ImageML = "custom-ml:test"
		want := map[string]string{
			config.EngineGeneral: "custom-general:test",
			config.EngineSpatial: "custom-spatial:test",
			config.EngineML:      "custom-ml:test",
		}
		for _, engine := range engines {
			got, err := cfg.ImageFor(engine)
			if err != nil || got != want[engine] {
				t.Errorf("ImageFor(%s) got %q, %v; want %q", engine, got, err, want[engine])
			}
		}
	})

	t.Run("one override leaves the others at default", func(t *testing.T) {
		cfg := validS3Config()
		cfg.ImageGeneral = "custom-general:test"
		got, err := cfg.ImageFor(config.EngineML)
		if want := "ghcr.io/1-archit-1/tilcayo-worker-ml:dev"; err != nil || got != want {
			t.Errorf("got %q, %v; want %q", got, err, want)
		}
	})

	t.Run("unknown engine", func(t *testing.T) {
		cfg := validS3Config()
		for _, engine := range []string{"", "unknown", "ML", "General", "gpu"} {
			if got, err := cfg.ImageFor(engine); !errors.Is(err, config.ErrUnknownEngine) || got != "" {
				t.Errorf("ImageFor(%q) got %q, %v; want ErrUnknownEngine", engine, got, err)
			}
		}
	})
}

func TestAllowedInputs(t *testing.T) {
	tests := []struct {
		name   string
		inputs []string
		data   string
		output string
		want   []string
	}{
		{name: "empty config"},
		{name: "only inputs", inputs: []string{"s3://b/in/", "s3://c/in/"}, want: []string{"s3://b/in/", "s3://c/in/"}},
		{name: "only data source", data: "/var/data", want: []string{"/data/"}},
		{name: "both", inputs: []string{"s3://b/in/"}, data: "/var/data", want: []string{"s3://b/in/", "/data/"}},
		{name: "output prefix is never allowed", inputs: []string{"s3://b/in/"}, output: "s3://b/out/", want: []string{"s3://b/in/"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := config.Config{InputPrefixes: tt.inputs, DataSource: tt.data, OutputPrefix: tt.output}
			if got := cfg.AllowedInputs(); !slices.Equal(got, tt.want) {
				t.Errorf("AllowedInputs got %v, want %v", got, tt.want)
			}
		})
	}
}

// allEnvVars uses literal names on purpose: it pins the names from issue #3,
// which a typo in a config.Env* constant would otherwise hide.
var allEnvVars = []string{
	"PORT",
	"TILCAYO_ORCHESTRATOR",
	"TILCAYO_RESULTS_MODE",
	"TILCAYO_RESULTS_DIR",
	"TILCAYO_S3_OUTPUT_PREFIX",
	"TILCAYO_S3_INPUT_PREFIXES",
	"TILCAYO_DATA_SOURCE",
	"TILCAYO_S3_KEY_ID",
	"TILCAYO_S3_SECRET",
	"TILCAYO_S3_REGION",
	"TILCAYO_S3_ENDPOINT",
	"TILCAYO_S3_PUBLIC_ENDPOINT",
	"TILCAYO_S3_URL_STYLE",
	"TILCAYO_S3_USE_SSL",
	"TILCAYO_DOCKER_NETWORK",
	"TILCAYO_JOB_CPU",
	"TILCAYO_JOB_MEMORY",
	"TILCAYO_IMAGE_GENERAL",
	"TILCAYO_IMAGE_SPATIAL",
	"TILCAYO_IMAGE_ML",
}

// setEnv blanks every variable first so nothing from the real environment
// leaks in, then applies vars. t.Setenv restores the originals afterwards.
func setEnv(t *testing.T, vars map[string]string) {
	t.Helper()
	for _, name := range allEnvVars {
		t.Setenv(name, "")
	}
	for name, value := range vars {
		t.Setenv(name, value)
	}
}

func TestFromEnvAllSet(t *testing.T) {
	vars := map[string]string{
		"PORT":                       "9090",
		"TILCAYO_ORCHESTRATOR":       "kubernetes",
		"TILCAYO_RESULTS_MODE":       "local",
		"TILCAYO_RESULTS_DIR":        "/srv/results",
		"TILCAYO_S3_OUTPUT_PREFIX":   "s3://out/res/",
		"TILCAYO_S3_INPUT_PREFIXES":  "s3://in1/a/,s3://in2/b/",
		"TILCAYO_DATA_SOURCE":        "/srv/data",
		"TILCAYO_S3_KEY_ID":          "key-id-value",
		"TILCAYO_S3_SECRET":          "secret-value",
		"TILCAYO_S3_REGION":          "region-value",
		"TILCAYO_S3_ENDPOINT":        "http://garage:3900",
		"TILCAYO_S3_PUBLIC_ENDPOINT": "http://localhost:3900",
		"TILCAYO_S3_URL_STYLE":       "path",
		"TILCAYO_S3_USE_SSL":         "false",
		"TILCAYO_DOCKER_NETWORK":     "tilcayo-net",
		"TILCAYO_JOB_CPU":            "0.5",
		"TILCAYO_JOB_MEMORY":         "512Mi",
		"TILCAYO_IMAGE_GENERAL":      "img-general:1",
		"TILCAYO_IMAGE_SPATIAL":      "img-spatial:2",
		"TILCAYO_IMAGE_ML":           "img-ml:3",
	}
	want := config.Config{
		Port:             "9090",
		Orchestrator:     "kubernetes",
		ResultsMode:      "local",
		ResultsDir:       "/srv/results",
		OutputPrefix:     "s3://out/res/",
		InputPrefixes:    []string{"s3://in1/a/", "s3://in2/b/"},
		DataSource:       "/srv/data",
		S3KeyID:          "key-id-value",
		S3Secret:         "secret-value",
		S3Region:         "region-value",
		S3Endpoint:       "http://garage:3900",
		S3PublicEndpoint: "http://localhost:3900",
		S3URLStyle:       "path",
		S3UseSSL:         false,
		DockerNetwork:    "tilcayo-net",
		JobCPU:           "0.5",
		JobMemory:        "512Mi",
		ImageGeneral:     "img-general:1",
		ImageSpatial:     "img-spatial:2",
		ImageML:          "img-ml:3",
	}
	if len(vars) != len(allEnvVars) {
		t.Fatalf("test sets %d variables but allEnvVars has %d", len(vars), len(allEnvVars))
	}

	t.Run("exact values", func(t *testing.T) {
		setEnv(t, vars)
		got, err := config.FromEnv()
		if err != nil {
			t.Fatalf("FromEnv failed: %v", err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("FromEnv got\n%+v\nwant\n%+v", got, want)
		}
	})

	t.Run("padded values are trimmed", func(t *testing.T) {
		padded := make(map[string]string, len(vars))
		for name, value := range vars {
			padded[name] = " \t" + value + "\t "
		}
		setEnv(t, padded)
		got, err := config.FromEnv()
		if err != nil {
			t.Fatalf("FromEnv failed: %v", err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("FromEnv got\n%+v\nwant\n%+v", got, want)
		}
	})
}

func TestFromEnvDefaults(t *testing.T) {
	// "Empty means unset": blank variables get the same defaults as missing ones.
	setEnv(t, nil)
	got, err := config.FromEnv()
	if err != nil {
		t.Fatalf("FromEnv failed: %v", err)
	}
	want := config.Config{
		Port:        "8080",
		ResultsMode: "s3",
		S3URLStyle:  "vhost",
		S3UseSSL:    true,
		// Orchestrator has no default: it stays "" and Validate rejects it.
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("FromEnv got\n%+v\nwant\n%+v", got, want)
	}
}

func TestFromEnvPublicEndpoint(t *testing.T) {
	t.Run("falls back to endpoint", func(t *testing.T) {
		setEnv(t, map[string]string{"TILCAYO_S3_ENDPOINT": "http://garage:3900"})
		got, err := config.FromEnv()
		if err != nil {
			t.Fatalf("FromEnv failed: %v", err)
		}
		if got.S3PublicEndpoint != "http://garage:3900" {
			t.Errorf("S3PublicEndpoint got %q, want the endpoint", got.S3PublicEndpoint)
		}
	})
	t.Run("public endpoint without endpoint", func(t *testing.T) {
		setEnv(t, map[string]string{"TILCAYO_S3_PUBLIC_ENDPOINT": "http://localhost:3900"})
		got, err := config.FromEnv()
		if err != nil {
			t.Fatalf("FromEnv failed: %v", err)
		}
		if got.S3Endpoint != "" || got.S3PublicEndpoint != "http://localhost:3900" {
			t.Errorf("got endpoint %q, public %q", got.S3Endpoint, got.S3PublicEndpoint)
		}
	})
}

func TestFromEnvInputPrefixList(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want []string
	}{
		{"empty", "", nil},
		{"only a comma", ",", nil},
		{"only blanks", " , ,", nil},
		{"single", "s3://a/", []string{"s3://a/"}},
		{"empty entries and padding", "a/,,b/ , ,s3://x/", []string{"a/", "b/", "s3://x/"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setEnv(t, map[string]string{"TILCAYO_S3_INPUT_PREFIXES": tt.raw})
			got, err := config.FromEnv()
			if err != nil {
				t.Fatalf("FromEnv failed: %v", err)
			}
			if !slices.Equal(got.InputPrefixes, tt.want) {
				t.Errorf("InputPrefixes got %q, want %q", got.InputPrefixes, tt.want)
			}
		})
	}
}

func TestFromEnvSSL(t *testing.T) {
	good := map[string]bool{"true": true, "TRUE": true, "1": true, "t": true, "false": false, "False": false, "0": false, "F": false}
	for raw, want := range good {
		t.Run("accepts "+raw, func(t *testing.T) {
			setEnv(t, map[string]string{"TILCAYO_S3_USE_SSL": raw})
			got, err := config.FromEnv()
			if err != nil || got.S3UseSSL != want {
				t.Errorf("got %v, %v; want %v", got.S3UseSSL, err, want)
			}
		})
	}
	for _, raw := range []string{"flase", "off", "no", "yes", "2"} {
		t.Run("rejects "+raw, func(t *testing.T) {
			setEnv(t, map[string]string{"TILCAYO_S3_USE_SSL": raw})
			_, err := config.FromEnv()
			if err == nil || !strings.Contains(err.Error(), "TILCAYO_S3_USE_SSL") {
				t.Errorf("expected an error naming TILCAYO_S3_USE_SSL, got %v", err)
			}
		})
	}
}

func TestLoad(t *testing.T) {
	validS3Env := map[string]string{
		"TILCAYO_ORCHESTRATOR":     "docker",
		"TILCAYO_S3_OUTPUT_PREFIX": "s3://mybucket/results/",
		"TILCAYO_S3_KEY_ID":        "AKIATESTKEY",
		"TILCAYO_S3_SECRET":        "SECRETVALUE",
		"TILCAYO_S3_REGION":        "garage",
	}
	with := func(name, value string) map[string]string {
		vars := map[string]string{}
		for k, v := range validS3Env {
			vars[k] = v
		}
		vars[name] = value
		return vars
	}

	t.Run("valid s3 env", func(t *testing.T) {
		setEnv(t, validS3Env)
		cfg, err := config.Load()
		if err != nil {
			t.Fatalf("Load failed: %v", err)
		}
		if cfg.OutputPrefix != "s3://mybucket/results/" || cfg.Port != "8080" || cfg.ResultsMode != "s3" {
			t.Errorf("unexpected config: %+v", cfg)
		}
		printed := fmt.Sprintf("%v %+v %#v", cfg, cfg, cfg)
		if strings.Contains(printed, "SECRETVALUE") || strings.Contains(printed, "AKIATESTKEY") {
			t.Errorf("credentials leaked in printed config: %s", printed)
		}
	})

	t.Run("valid local env", func(t *testing.T) {
		setEnv(t, map[string]string{
			"TILCAYO_ORCHESTRATOR": "docker",
			"TILCAYO_RESULTS_MODE": "local",
			"TILCAYO_RESULTS_DIR":  "/var/results",
		})
		if _, err := config.Load(); err != nil {
			t.Fatalf("Load failed: %v", err)
		}
	})

	tests := []struct {
		name         string
		vars         map[string]string
		wantSentinel error
		wantSubstr   string
	}{
		{"no orchestrator", with("TILCAYO_ORCHESTRATOR", ""), config.ErrInvalidOrchestrator, "TILCAYO_ORCHESTRATOR"},
		{"missing output prefix", with("TILCAYO_S3_OUTPUT_PREFIX", ""), config.ErrMissingField, "TILCAYO_S3_OUTPUT_PREFIX"},
		{"missing secret", with("TILCAYO_S3_SECRET", ""), config.ErrMissingField, "TILCAYO_S3_SECRET"},
		{"bad results mode", with("TILCAYO_RESULTS_MODE", "azure"), config.ErrInvalidMode, "TILCAYO_RESULTS_MODE"},
		{"input overlaps output", with("TILCAYO_S3_INPUT_PREFIXES", "s3://mybucket/results/a/"), config.ErrPrefixOverlap, ""},
		{"bad cpu", with("TILCAYO_JOB_CPU", "NaN"), config.ErrJobCPU, "TILCAYO_JOB_CPU"},
		{"bad ssl boolean", with("TILCAYO_S3_USE_SSL", "flase"), nil, "TILCAYO_S3_USE_SSL"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setEnv(t, tt.vars)
			cfg, err := config.Load()
			if err == nil {
				t.Fatalf("expected error, got config %+v", cfg)
			}
			if tt.wantSentinel != nil && !errors.Is(err, tt.wantSentinel) {
				t.Errorf("expected sentinel %v, got %v", tt.wantSentinel, err)
			}
			if tt.wantSubstr != "" && !strings.Contains(err.Error(), tt.wantSubstr) {
				t.Errorf("expected error containing %q, got %q", tt.wantSubstr, err)
			}
			if strings.Contains(err.Error(), "SECRETVALUE") || strings.Contains(err.Error(), "AKIATESTKEY") {
				t.Errorf("credentials leaked in error: %v", err)
			}
		})
	}
}

func TestSecretRedaction(t *testing.T) {
	cfg := validS3Config()
	cfg.S3KeyID = "AKIATESTKEY"
	cfg.S3Secret = "SECRETVALUE"

	var jsonLog, textLog bytes.Buffer
	slog.New(slog.NewJSONHandler(&jsonLog, nil)).Info("startup", "cfg", cfg, "secret", cfg.S3Secret)
	slog.New(slog.NewTextHandler(&textLog, nil)).Info("startup", "cfg", cfg, "secret", cfg.S3Secret)
	jsonCfg, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("json.Marshal(cfg): %v", err)
	}
	jsonSecret, err := json.Marshal(cfg.S3Secret)
	if err != nil {
		t.Fatalf("json.Marshal(secret): %v", err)
	}

	outputs := map[string]string{
		"%v cfg":            fmt.Sprintf("%v", cfg),
		"%+v cfg":           fmt.Sprintf("%+v", cfg),
		"%#v cfg":           fmt.Sprintf("%#v", cfg),
		"%v &cfg":           fmt.Sprintf("%v", &cfg),
		"%s secret":         fmt.Sprintf("%s", cfg.S3Secret),
		"%q secret":         fmt.Sprintf("%q", cfg.S3Secret),
		"%v secret":         fmt.Sprintf("%v", cfg.S3Secret),
		"%#v secret":        fmt.Sprintf("%#v", cfg.S3Secret),
		"%#v key id":        fmt.Sprintf("%#v", cfg.S3KeyID),
		"json cfg":          string(jsonCfg),
		"json secret":       string(jsonSecret),
		"slog json handler": jsonLog.String(),
		"slog text handler": textLog.String(),
	}
	for name, out := range outputs {
		t.Run(name, func(t *testing.T) {
			if strings.Contains(out, "SECRETVALUE") || strings.Contains(out, "AKIATESTKEY") {
				t.Errorf("credentials leaked: %s", out)
			}
			if !strings.Contains(out, "[REDACTED]") {
				t.Errorf("expected [REDACTED] in: %s", out)
			}
		})
	}
}

func TestSecretEmptyShowsAsEmpty(t *testing.T) {
	var s config.Secret
	if got := s.String(); got != "" {
		t.Errorf("String got %q, want empty", got)
	}
	if got := fmt.Sprintf("%#v", s); got != `""` {
		t.Errorf("GoString got %s, want \"\"", got)
	}
	if got, _ := json.Marshal(s); string(got) != `""` {
		t.Errorf("json got %s, want \"\"", got)
	}
}

// Error messages name variables and show prefixes or paths, never credentials.
func TestErrorsDoNotContainSecrets(t *testing.T) {
	breakages := map[string]func(*config.Config){
		"bad orchestrator":  func(c *config.Config) { c.Orchestrator = "nomad" },
		"bad mode":          func(c *config.Config) { c.ResultsMode = "azure" },
		"missing output":    func(c *config.Config) { c.OutputPrefix = "" },
		"bad output":        func(c *config.Config) { c.OutputPrefix = "http://x/" },
		"bad input":         func(c *config.Config) { c.InputPrefixes = []string{"nope"} },
		"overlap":           func(c *config.Config) { c.InputPrefixes = []string{c.OutputPrefix} },
		"missing region":    func(c *config.Config) { c.S3Region = "" },
		"bad url style":     func(c *config.Config) { c.S3URLStyle = "x" },
		"bad cpu":           func(c *config.Config) { c.JobCPU = "NaN" },
		"bad memory":        func(c *config.Config) { c.JobMemory = "0" },
		"bad data source":   func(c *config.Config) { c.DataSource = "relative" },
		"local with output": func(c *config.Config) { c.ResultsMode = "local"; c.ResultsDir = "/r" },
		"local bad results": func(c *config.Config) { c.ResultsMode = "local"; c.OutputPrefix = ""; c.ResultsDir = "r" },
		"local missing key id": func(c *config.Config) {
			c.ResultsMode = "local"
			c.OutputPrefix = ""
			c.ResultsDir = "/r"
			c.S3KeyID = ""
		},
		"local dirs overlap": func(c *config.Config) {
			c.ResultsMode, c.OutputPrefix, c.ResultsDir, c.DataSource = "local", "", "/d/r", "/d"
		},
	}
	for name, breakIt := range breakages {
		t.Run(name, func(t *testing.T) {
			cfg := validS3Config()
			cfg.S3KeyID = "AKIATESTKEY"
			cfg.S3Secret = "SECRETVALUE"
			breakIt(&cfg)

			err := cfg.Validate()
			if err == nil {
				t.Fatal("expected an error")
			}
			if strings.Contains(err.Error(), "SECRETVALUE") || strings.Contains(err.Error(), "AKIATESTKEY") {
				t.Errorf("credentials leaked in error: %v", err)
			}
		})
	}
}
