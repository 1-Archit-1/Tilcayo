package config_test

import (
	"encoding/json"
	"errors"
	"fmt"
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

// Each row is one rule from issue #3 / #5: an invalid row must fail naming
// the variable to fix, a valid row pins an edge the rule must not reject.
func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		base    func() config.Config // defaults to validS3Config
		modify  func(*config.Config)
		wantErr string // substring of the error; "" means valid
	}{
		{name: "valid s3", modify: func(c *config.Config) {}},
		{name: "valid local", base: validLocalConfig, modify: func(c *config.Config) {}},
		{name: "valid path url style", modify: func(c *config.Config) { c.S3URLStyle = "path" }},

		// Modes
		{name: "unknown orchestrator", modify: func(c *config.Config) { c.Orchestrator = "nomad" }, wantErr: "TILCAYO_ORCHESTRATOR"},
		{name: "unknown results mode", modify: func(c *config.Config) { c.ResultsMode = "azure" }, wantErr: "TILCAYO_RESULTS_MODE"},
		{name: "local mode refused in kubernetes", base: validLocalConfig,
			modify: func(c *config.Config) { c.Orchestrator = "kubernetes" }, wantErr: "TILCAYO_RESULTS_MODE"},
		{name: "unknown url style", modify: func(c *config.Config) { c.S3URLStyle = "virtual" }, wantErr: "TILCAYO_S3_URL_STYLE"},

		// Local results
		{name: "local without results dir", base: validLocalConfig,
			modify: func(c *config.Config) { c.ResultsDir = "" }, wantErr: "TILCAYO_RESULTS_DIR"},
		{name: "local relative results dir", base: validLocalConfig,
			modify: func(c *config.Config) { c.ResultsDir = "results" }, wantErr: "TILCAYO_RESULTS_DIR"},
		{name: "local results dir is root", base: validLocalConfig,
			modify: func(c *config.Config) { c.ResultsDir = "/" }, wantErr: "TILCAYO_RESULTS_DIR"},
		{name: "local refuses an output prefix", base: validLocalConfig,
			modify: func(c *config.Config) { c.OutputPrefix = "s3://mybucket/results/" }, wantErr: "TILCAYO_S3_OUTPUT_PREFIX"},

		// S3 required fields
		{name: "s3 without output prefix", modify: func(c *config.Config) { c.OutputPrefix = "" }, wantErr: "TILCAYO_S3_OUTPUT_PREFIX"},
		{name: "s3 without secret", modify: func(c *config.Config) { c.S3Secret = "" }, wantErr: "TILCAYO_S3_SECRET"},
		{name: "s3 without region", modify: func(c *config.Config) { c.S3Region = "" }, wantErr: "TILCAYO_S3_REGION"},
		{name: "local input prefixes need keys", base: validLocalConfig,
			modify: func(c *config.Config) { c.InputPrefixes = []string{"s3://inbucket/data/"} }, wantErr: "TILCAYO_S3_KEY_ID"},
		{name: "local input prefixes with keys", base: validLocalConfig, modify: func(c *config.Config) {
			c.InputPrefixes = []string{"s3://inbucket/data/"}
			c.S3KeyID, c.S3Secret, c.S3Region = "k", "s", "garage"
		}},
		{name: "local unused partial keys are harmless", base: validLocalConfig,
			modify: func(c *config.Config) { c.S3KeyID = "k" }},

		// Input prefixes: the overlap rule stops a job reading other jobs' results.
		{name: "bad input prefix", modify: func(c *config.Config) { c.InputPrefixes = []string{"s3://in/data"} }, wantErr: "TILCAYO_S3_INPUT_PREFIXES"},
		{name: "input is parent of output", modify: func(c *config.Config) {
			c.OutputPrefix, c.InputPrefixes = "s3://mybucket/results/jobs/", []string{"s3://mybucket/results/"}
		}, wantErr: "overlaps"},
		{name: "input is child of output", modify: func(c *config.Config) {
			c.InputPrefixes = []string{"s3://mybucket/results/old/"}
		}, wantErr: "overlaps"},
		{name: "input equals output", modify: func(c *config.Config) {
			c.InputPrefixes = []string{c.OutputPrefix}
		}, wantErr: "overlaps"},
		// The mandatory trailing "/" is what keeps these apart.
		{name: "sibling buckets abc and abcd", modify: func(c *config.Config) {
			c.OutputPrefix, c.InputPrefixes = "s3://abcd/", []string{"s3://abc/"}
		}},
		{name: "sibling folders res and results", modify: func(c *config.Config) {
			c.InputPrefixes = []string{"s3://mybucket/res/"}
		}},

		// Data source
		{name: "docker data source", modify: func(c *config.Config) { c.DataSource = "/var/data" }},
		{name: "docker relative data source", modify: func(c *config.Config) { c.DataSource = "data" }, wantErr: "TILCAYO_DATA_SOURCE"},
		{name: "docker data source is root", modify: func(c *config.Config) { c.DataSource = "/" }, wantErr: "TILCAYO_DATA_SOURCE"},
		{name: "kubernetes dotted pvc name", modify: func(c *config.Config) {
			c.Orchestrator, c.DataSource = "kubernetes", "a.b-c1"
		}},
		{name: "kubernetes pvc name 253 chars", modify: func(c *config.Config) {
			c.Orchestrator, c.DataSource = "kubernetes", strings.Repeat("a", 253)
		}},
		{name: "kubernetes pvc name 254 chars", modify: func(c *config.Config) {
			c.Orchestrator, c.DataSource = "kubernetes", strings.Repeat("a", 254)
		}, wantErr: "TILCAYO_DATA_SOURCE"},
		{name: "kubernetes uppercase pvc name", modify: func(c *config.Config) {
			c.Orchestrator, c.DataSource = "kubernetes", "MyPVC"
		}, wantErr: "TILCAYO_DATA_SOURCE"},
		{name: "kubernetes path as pvc name", modify: func(c *config.Config) {
			c.Orchestrator, c.DataSource = "kubernetes", "/var/data"
		}, wantErr: "TILCAYO_DATA_SOURCE"},

		// Local results inside /data would let every worker read them.
		{name: "results dir inside data source", base: validLocalConfig, modify: func(c *config.Config) {
			c.ResultsDir, c.DataSource = "/srv/data/results", "/srv/data"
		}, wantErr: "must not contain"},
		{name: "data source inside results dir", base: validLocalConfig, modify: func(c *config.Config) {
			c.ResultsDir, c.DataSource = "/srv/data", "/srv/data/inputs"
		}, wantErr: "must not contain"},
		{name: "results dir equals data source", base: validLocalConfig, modify: func(c *config.Config) {
			c.ResultsDir, c.DataSource = "/srv/data", "/srv/data"
		}, wantErr: "must not contain"},
		{name: "results dir and data source are siblings", base: validLocalConfig, modify: func(c *config.Config) {
			c.ResultsDir, c.DataSource = "/srv/data2", "/srv/data"
		}},
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

			switch {
			case tt.wantErr == "" && err != nil:
				t.Fatalf("expected valid, got %v", err)
			case tt.wantErr != "" && err == nil:
				t.Fatalf("expected an error containing %q, got nil", tt.wantErr)
			case tt.wantErr != "" && !strings.Contains(err.Error(), tt.wantErr):
				t.Fatalf("expected an error containing %q, got %q", tt.wantErr, err)
			}
		})
	}
}

func TestValidateS3Prefix(t *testing.T) {
	tests := []struct {
		prefix string
		valid  bool
	}{
		{"s3://other/data/", true},
		{"s3://other/", true},
		{"s3://a.b-c/x/y/", true},

		{"other/data/", false},      // no scheme
		{"s3://other/data", false},  // no trailing slash
		{"s3:///data/", false},      // empty bucket
		{"s3://ab/x/", false},       // bucket too short
		{"s3://MyBucket/x/", false}, // uppercase would dodge the overlap check on vhost
		{"s3://a_b/x/", false},      // underscore
		{"s3://other//x/", false},   // empty segment
		{"s3://other/./x/", false},  // dot segment
		{"s3://other/a/../x/", false},
	}
	for _, tt := range tests {
		t.Run(tt.prefix, func(t *testing.T) {
			cfg := validS3Config()
			cfg.InputPrefixes = nil
			cfg.OutputPrefix = tt.prefix
			err := cfg.Validate()
			if tt.valid && err != nil {
				t.Fatalf("expected valid, got %v", err)
			}
			if !tt.valid && (err == nil || !strings.Contains(err.Error(), "TILCAYO_S3_OUTPUT_PREFIX")) {
				t.Fatalf("expected an error naming TILCAYO_S3_OUTPUT_PREFIX, got %v", err)
			}
		})
	}
}

func TestResourcesFor(t *testing.T) {
	t.Run("defaults per engine", func(t *testing.T) {
		want := map[string]int64{config.EngineGeneral: 1 * gi, config.EngineSpatial: 1 * gi, config.EngineML: 4 * gi}
		for engine, wantMem := range want {
			res, err := config.Config{}.ResourcesFor(engine)
			if err != nil || res.CPU != 1 || res.MemoryBytes != wantMem {
				t.Errorf("%s: got %+v, %v; want CPU 1 and %d bytes", engine, res, err, wantMem)
			}
		}
	})

	t.Run("overrides apply to every engine, ml too", func(t *testing.T) {
		cfg := config.Config{JobCPU: 0.5, JobMemoryBytes: 512 * mi}
		res, err := cfg.ResourcesFor(config.EngineML)
		if err != nil || res.CPU != 0.5 || res.MemoryBytes != 512*mi {
			t.Errorf("got %+v, %v; want CPU 0.5 and 512Mi", res, err)
		}
	})

	t.Run("unknown engine", func(t *testing.T) {
		if _, err := (config.Config{}).ResourcesFor("gpu"); !errors.Is(err, config.ErrUnknownEngine) {
			t.Errorf("expected ErrUnknownEngine, got %v", err)
		}
	})
}

// Docker treats NanoCPUs 0 as "unlimited", and float64 needs rounding.
func TestNanoCPUs(t *testing.T) {
	tests := []struct {
		cpu  float64
		want int64
	}{
		{0.01, 10_000_000},    // the floor is never 0
		{2.01, 2_010_000_000}, // 2.01*1e9 is 2009999999.9999998 in float64
	}
	for _, tt := range tests {
		if got := (config.Resources{CPU: tt.cpu}).NanoCPUs(); got != tt.want {
			t.Errorf("NanoCPUs(%v) got %d, want %d", tt.cpu, got, tt.want)
		}
	}
}

func TestImageFor(t *testing.T) {
	cfg := config.Config{Images: map[string]string{config.EngineML: "tilcayo/worker-ml:test"}}

	if got, err := cfg.ImageFor(config.EngineML); err != nil || got != "tilcayo/worker-ml:test" {
		t.Errorf("override: got %q, %v", got, err)
	}
	want := "ghcr.io/1-archit-1/tilcayo-worker-general:" + config.Version
	if got, err := cfg.ImageFor(config.EngineGeneral); err != nil || got != want {
		t.Errorf("default: got %q, %v; want %q", got, err, want)
	}
	if _, err := cfg.ImageFor("gpu"); !errors.Is(err, config.ErrUnknownEngine) {
		t.Errorf("unknown engine: expected ErrUnknownEngine, got %v", err)
	}
}

func TestAllowedInputs(t *testing.T) {
	tests := []struct {
		name   string
		inputs []string
		data   string
		want   []string
	}{
		{name: "nothing configured"},
		{name: "input prefixes", inputs: []string{"s3://b/in/"}, want: []string{"s3://b/in/"}},
		{name: "data source adds /data/", inputs: []string{"s3://b/in/"}, data: "/var/data", want: []string{"s3://b/in/", "/data/"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// The output prefix is set every time and must never appear.
			cfg := config.Config{InputPrefixes: tt.inputs, DataSource: tt.data, OutputPrefix: "s3://b/out/"}
			if got := cfg.AllowedInputs(); !slices.Equal(got, tt.want) {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

// allEnvVars uses literal names on purpose: it pins the names from issue #3,
// which a typo in a config.Env* constant would otherwise hide.
var allEnvVars = []string{
	"PORT", "TILCAYO_ORCHESTRATOR", "TILCAYO_RESULTS_MODE", "TILCAYO_RESULTS_DIR",
	"TILCAYO_S3_OUTPUT_PREFIX", "TILCAYO_S3_INPUT_PREFIXES", "TILCAYO_DATA_SOURCE",
	"TILCAYO_S3_KEY_ID", "TILCAYO_S3_SECRET", "TILCAYO_S3_REGION",
	"TILCAYO_S3_ENDPOINT", "TILCAYO_S3_PUBLIC_ENDPOINT", "TILCAYO_S3_URL_STYLE", "TILCAYO_S3_USE_SSL",
	"TILCAYO_DOCKER_NETWORK", "TILCAYO_JOB_CPU", "TILCAYO_JOB_MEMORY",
	"TILCAYO_IMAGE_GENERAL", "TILCAYO_IMAGE_SPATIAL", "TILCAYO_IMAGE_ML",
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

func TestFromEnvReadsEveryVariable(t *testing.T) {
	setEnv(t, map[string]string{
		"PORT":                       "9090",
		"TILCAYO_ORCHESTRATOR":       "docker",
		"TILCAYO_RESULTS_MODE":       "local",
		"TILCAYO_RESULTS_DIR":        "/srv/results/", // cleaned, not rejected
		"TILCAYO_S3_OUTPUT_PREFIX":   "s3://out/res/",
		"TILCAYO_S3_INPUT_PREFIXES":  " s3://in1/a/, ,s3://in2/b/ ", // blanks and empty entries dropped
		"TILCAYO_DATA_SOURCE":        "  /srv/data",                 // trimmed
		"TILCAYO_S3_KEY_ID":          "key-id",
		"TILCAYO_S3_SECRET":          "secret",
		"TILCAYO_S3_REGION":          "garage",
		"TILCAYO_S3_ENDPOINT":        "http://garage:3900",
		"TILCAYO_S3_PUBLIC_ENDPOINT": "http://localhost:3900",
		"TILCAYO_S3_URL_STYLE":       "path",
		"TILCAYO_S3_USE_SSL":         "false",
		"TILCAYO_DOCKER_NETWORK":     "tilcayo-net",
		"TILCAYO_JOB_CPU":            "0.5",
		"TILCAYO_JOB_MEMORY":         "2Gi",
		"TILCAYO_IMAGE_GENERAL":      "img-general:1",
		"TILCAYO_IMAGE_SPATIAL":      "img-spatial:2",
		"TILCAYO_IMAGE_ML":           "img-ml:3",
	})
	want := config.Config{
		Port:             "9090",
		Orchestrator:     "docker",
		ResultsMode:      "local",
		ResultsDir:       "/srv/results",
		OutputPrefix:     "s3://out/res/",
		InputPrefixes:    []string{"s3://in1/a/", "s3://in2/b/"},
		DataSource:       "/srv/data",
		S3KeyID:          "key-id",
		S3Secret:         "secret",
		S3Region:         "garage",
		S3Endpoint:       "http://garage:3900",
		S3PublicEndpoint: "http://localhost:3900",
		S3URLStyle:       "path",
		S3UseSSL:         false,
		DockerNetwork:    "tilcayo-net",
		JobCPU:           0.5,
		JobMemoryBytes:   2 * gi,
		Images: map[string]string{
			config.EngineGeneral: "img-general:1",
			config.EngineSpatial: "img-spatial:2",
			config.EngineML:      "img-ml:3",
		},
	}
	got, err := config.FromEnv()
	if err != nil {
		t.Fatalf("FromEnv failed: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("FromEnv got\n%+v\nwant\n%+v", got, want)
	}
}

func TestFromEnvDefaults(t *testing.T) {
	// Blank variables count as unset and get the defaults.
	setEnv(t, map[string]string{"TILCAYO_S3_ENDPOINT": "http://garage:3900"})
	got, err := config.FromEnv()
	if err != nil {
		t.Fatalf("FromEnv failed: %v", err)
	}
	want := config.Config{
		Port:             "8080",
		ResultsMode:      "s3",
		S3Endpoint:       "http://garage:3900",
		S3PublicEndpoint: "http://garage:3900", // falls back to the endpoint
		S3URLStyle:       "vhost",
		S3UseSSL:         true,
		Images:           map[string]string{},
		// Orchestrator has no default: it stays "" and Validate rejects it.
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("FromEnv got\n%+v\nwant\n%+v", got, want)
	}
}

func TestFromEnvRejectsBadValues(t *testing.T) {
	tests := []struct{ name, value string }{
		{"TILCAYO_S3_USE_SSL", "off"},
		{"TILCAYO_JOB_CPU", "0"},   // Docker reads 0 as unlimited
		{"TILCAYO_JOB_CPU", "NaN"}, // ParseFloat accepts it
		{"TILCAYO_JOB_CPU", "257"},
		{"TILCAYO_JOB_MEMORY", "0"},
		{"TILCAYO_JOB_MEMORY", "1.5Gi"},
		{"TILCAYO_JOB_MEMORY", "63Mi"},
		{"TILCAYO_JOB_MEMORY", "17179869185Gi"}, // 2^34+1 Gi would wrap int64 to an in-range 1Gi
	}
	for _, tt := range tests {
		t.Run(tt.name+"="+tt.value, func(t *testing.T) {
			setEnv(t, map[string]string{tt.name: tt.value})
			if _, err := config.FromEnv(); err == nil || !strings.Contains(err.Error(), tt.name) {
				t.Errorf("expected an error naming %s, got %v", tt.name, err)
			}
		})
	}
}

func TestLoad(t *testing.T) {
	validEnv := map[string]string{
		"TILCAYO_ORCHESTRATOR":     "docker",
		"TILCAYO_S3_OUTPUT_PREFIX": "s3://mybucket/results/",
		"TILCAYO_S3_KEY_ID":        "AKIATESTKEY",
		"TILCAYO_S3_SECRET":        "SECRETVALUE",
		"TILCAYO_S3_REGION":        "garage",
	}

	t.Run("valid", func(t *testing.T) {
		setEnv(t, validEnv)
		if _, err := config.Load(); err != nil {
			t.Fatalf("Load failed: %v", err)
		}
	})

	t.Run("validates", func(t *testing.T) {
		setEnv(t, validEnv)
		t.Setenv("TILCAYO_S3_SECRET", "")
		if _, err := config.Load(); err == nil || !strings.Contains(err.Error(), "TILCAYO_S3_SECRET") {
			t.Errorf("expected an error naming TILCAYO_S3_SECRET, got %v", err)
		}
	})
}

func TestSecretRedaction(t *testing.T) {
	cfg := validS3Config()
	cfg.S3KeyID = "AKIATESTKEY"
	cfg.S3Secret = "SECRETVALUE"

	jsonCfg, err := json.Marshal(cfg) // what slog's JSON handler does
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	outputs := map[string]string{
		"%+v":  fmt.Sprintf("%+v", cfg),
		"%#v":  fmt.Sprintf("%#v", cfg),
		"json": string(jsonCfg),
	}
	for name, out := range outputs {
		if strings.Contains(out, "SECRETVALUE") || strings.Contains(out, "AKIATESTKEY") {
			t.Errorf("%s leaked credentials: %s", name, out)
		}
		if !strings.Contains(out, "[REDACTED]") {
			t.Errorf("%s: expected [REDACTED] in %s", name, out)
		}
	}
}
