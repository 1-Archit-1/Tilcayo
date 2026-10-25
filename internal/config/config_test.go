package config_test

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/1-Archit-1/Tilcayo/internal/config"
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

func TestValidate(t *testing.T) {
	tests := []struct {
		name         string
		modify       func(*config.Config)
		wantErr      bool
		wantSentinel error
		wantSubstr   string
	}{
		{
			name:   "valid s3 config",
			modify: func(c *config.Config) {},
		},
		{
			name: "valid local config",
			modify: func(c *config.Config) {
				c.ResultsMode = "local"
				c.ResultsDir = "/var/results"
				c.OutputPrefix = ""
				c.InputPrefixes = nil
				c.S3KeyID = ""
				c.S3Secret = ""
				c.S3Region = ""
			},
		},
		{
			name: "invalid orchestrator",
			modify: func(c *config.Config) {
				c.Orchestrator = "nomad"
			},
			wantErr:      true,
			wantSentinel: config.ErrInvalidOrchestrator,
		},
		{
			name: "invalid results mode",
			modify: func(c *config.Config) {
				c.ResultsMode = "azure-blob"
			},
			wantErr:      true,
			wantSentinel: config.ErrInvalidMode,
		},
		{
			name: "local mode refused in kubernetes",
			modify: func(c *config.Config) {
				c.Orchestrator = "kubernetes"
				c.ResultsMode = "local"
				c.ResultsDir = "/var/results"
				c.OutputPrefix = ""
			},
			wantErr:      true,
			wantSentinel: config.ErrInvalidMode,
		},
		{
			name: "local mode missing results dir",
			modify: func(c *config.Config) {
				c.ResultsMode = "local"
				c.ResultsDir = ""
				c.OutputPrefix = ""
			},
			wantErr:    true,
			wantSubstr: "TILCAYO_RESULTS_DIR",
		},
		{
			name: "local mode relative results dir",
			modify: func(c *config.Config) {
				c.ResultsMode = "local"
				c.ResultsDir = "relative/results"
				c.OutputPrefix = ""
			},
			wantErr:      true,
			wantSentinel: config.ErrInvalidPath,
		},
		{
			name: "s3 mode missing region",
			modify: func(c *config.Config) {
				c.S3Region = ""
			},
			wantErr:    true,
			wantSubstr: "TILCAYO_S3_REGION",
		},
		{
			name: "s3 mode missing key id",
			modify: func(c *config.Config) {
				c.S3KeyID = ""
			},
			wantErr:      true,
			wantSentinel: config.ErrMissingField,
			wantSubstr:   "TILCAYO_S3_KEY_ID",
		},
		{
			name: "s3 mode missing secret",
			modify: func(c *config.Config) {
				c.S3Secret = ""
			},
			wantErr:      true,
			wantSentinel: config.ErrMissingField,
			wantSubstr:   "TILCAYO_S3_SECRET",
		},
		{
			name: "s3 mode missing output prefix",
			modify: func(c *config.Config) {
				c.OutputPrefix = ""
			},
			wantErr:    true,
			wantSubstr: "TILCAYO_S3_OUTPUT_PREFIX",
		},
		{
			name: "s3 mode output prefix missing trailing slash",
			modify: func(c *config.Config) {
				c.OutputPrefix = "s3://mybucket/results"
			},
			wantErr:      true,
			wantSentinel: config.ErrInvalidPrefix,
		},
		{
			name: "s3 mode output prefix invalid bucket uppercase",
			modify: func(c *config.Config) {
				c.OutputPrefix = "s3://MyBucket/results/"
			},
			wantErr:      true,
			wantSentinel: config.ErrInvalidPrefix,
		},
		{
			name: "s3 mode output prefix empty bucket",
			modify: func(c *config.Config) {
				c.OutputPrefix = "s3:///results/"
			},
			wantErr:      true,
			wantSentinel: config.ErrInvalidPrefix,
		},
		{
			name: "s3 mode output prefix dot-dot traversal",
			modify: func(c *config.Config) {
				c.OutputPrefix = "s3://mybucket/a/../results/"
			},
			wantErr:      true,
			wantSentinel: config.ErrInvalidPrefix,
		},
		{
			name: "local mode with output prefix refused",
			modify: func(c *config.Config) {
				c.ResultsMode = "local"
				c.ResultsDir = "/tmp/out"
				c.OutputPrefix = "s3://mybucket/results/"
			},
			wantErr:      true,
			wantSentinel: config.ErrOutputPrefixRefused,
		},
		{
			name: "local mode with S3 input prefix requires credentials",
			modify: func(c *config.Config) {
				c.ResultsMode = "local"
				c.ResultsDir = "/tmp/out"
				c.OutputPrefix = ""
				c.InputPrefixes = []string{"s3://in/data/"}
				c.S3KeyID = ""
			},
			wantErr:      true,
			wantSentinel: config.ErrMissingField,
		},
		{
			name: "input prefix overlaps output prefix (parent)",
			modify: func(c *config.Config) {
				c.OutputPrefix = "s3://mybucket/results/jobs/"
				c.InputPrefixes = []string{"s3://mybucket/results/"}
			},
			wantErr:      true,
			wantSentinel: config.ErrPrefixOverlap,
		},
		{
			name: "input prefix overlaps output prefix (child)",
			modify: func(c *config.Config) {
				c.OutputPrefix = "s3://mybucket/data/"
				c.InputPrefixes = []string{"s3://mybucket/data/subpath/"}
			},
			wantErr:      true,
			wantSentinel: config.ErrPrefixOverlap,
		},
		{
			name: "invalid CPU negative",
			modify: func(c *config.Config) {
				c.JobCPU = "-1"
			},
			wantErr:      true,
			wantSentinel: config.ErrJobCPU,
		},
		{
			name: "invalid memory unit",
			modify: func(c *config.Config) {
				c.JobMemory = "banana"
			},
			wantErr:      true,
			wantSentinel: config.ErrJobMemory,
		},
		{
			name: "invalid data source relative in docker",
			modify: func(c *config.Config) {
				c.DataSource = "relative/path"
			},
			wantErr:      true,
			wantSentinel: config.ErrInvalidPath,
		},
		{
			name: "invalid data source slash in kubernetes",
			modify: func(c *config.Config) {
				c.Orchestrator = "kubernetes"
				c.DataSource = "pvc/with/slash"
			},
			wantErr:      true,
			wantSentinel: config.ErrInvalidPath,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validS3Config()
			tt.modify(&cfg)
			err := cfg.Validate()
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				if tt.wantSentinel != nil && !errors.Is(err, tt.wantSentinel) {
					t.Errorf("expected error wrapping sentinel %v, got %v", tt.wantSentinel, err)
				}
				if tt.wantSubstr != "" && !strings.Contains(err.Error(), tt.wantSubstr) {
					t.Errorf("expected error containing %q, got %q", tt.wantSubstr, err.Error())
				}
			} else {
				if err != nil {
					t.Fatalf("expected clean validation, got error: %v", err)
				}
			}
		})
	}
}

func TestSecretRedaction(t *testing.T) {
	cfg := validS3Config()
	cfg.S3KeyID = "AKIAMYSECRETID"
	cfg.S3Secret = "SUPERSECRETVALUE"

	printed := fmt.Sprintf("%+v", cfg)
	if strings.Contains(printed, "AKIAMYSECRETID") || strings.Contains(printed, "SUPERSECRETVALUE") {
		t.Fatalf("credentials leaked in struct print: %s", printed)
	}
	if !strings.Contains(printed, "[REDACTED]") {
		t.Fatalf("expected [REDACTED] in struct print, got: %s", printed)
	}
}

func TestAllowedInputs(t *testing.T) {
	cfg := validS3Config()
	cfg.InputPrefixes = []string{"s3://b/in/"}
	cfg.DataSource = "/var/data"

	got := cfg.AllowedInputs()
	want := []string{"s3://b/in/", "/data/"}
	if !slices.Equal(got, want) {
		t.Errorf("AllowedInputs got %v, want %v", got, want)
	}

	cfg.DataSource = ""
	gotNoData := cfg.AllowedInputs()
	if !slices.Equal(gotNoData, []string{"s3://b/in/"}) {
		t.Errorf("AllowedInputs without data source got %v, want %v", gotNoData, []string{"s3://b/in/"})
	}
}

func TestEngineResources(t *testing.T) {
	cfg := validS3Config()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("validate failed: %v", err)
	}

	resGen, err := cfg.ResourcesFor(config.EngineGeneral)
	if err != nil || resGen.CPU != 1.0 || resGen.MemoryBytes != 1024*1024*1024 {
		t.Errorf("unexpected general resources: %+v, err: %v", resGen, err)
	}

	resML, err := cfg.ResourcesFor(config.EngineML)
	if err != nil || resML.CPU != 1.0 || resML.MemoryBytes != 4*1024*1024*1024 {
		t.Errorf("unexpected ml resources: %+v, err: %v", resML, err)
	}

	_, errUnknown := cfg.ResourcesFor("unknown")
	if !errors.Is(errUnknown, config.ErrUnknownEngine) {
		t.Errorf("expected ErrUnknownEngine, got %v", errUnknown)
	}
}

func TestImageFor(t *testing.T) {
	cfg := validS3Config()
	img, err := cfg.ImageFor(config.EngineGeneral)
	if err != nil || !strings.Contains(img, "tilcayo-worker-general") {
		t.Errorf("ImageFor general failed: %s, %v", img, err)
	}

	cfg.ImageGeneral = "custom-general:test"
	imgCustom, err := cfg.ImageFor(config.EngineGeneral)
	if err != nil || imgCustom != "custom-general:test" {
		t.Errorf("ImageFor custom general failed: %s, %v", imgCustom, err)
	}
}

func TestFromEnv(t *testing.T) {
	t.Setenv(config.EnvOrchestrator, "docker")
	t.Setenv(config.EnvResultsMode, "local")
	t.Setenv(config.EnvResultsDir, "/tmp/results")
	t.Setenv(config.EnvS3InputPrefixes, "s3://b/in1/ , , s3://b/in2/")
	t.Setenv(config.EnvS3UseSSL, "false")

	cfg, err := config.FromEnv()
	if err != nil {
		t.Fatalf("FromEnv failed: %v", err)
	}

	if cfg.S3UseSSL {
		t.Errorf("expected S3UseSSL to be false")
	}
	if len(cfg.InputPrefixes) != 2 {
		t.Errorf("expected 2 trimmed input prefixes, got %v", cfg.InputPrefixes)
	}
}
