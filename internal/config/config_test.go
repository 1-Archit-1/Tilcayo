package config_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/1-Archit-1/Tilcayo/internal/config"
)

func TestValidate(t *testing.T) {
	tests := []struct {
		name         string
		cfg          config.Config
		wantErr      bool
		wantSentinel error
		wantSubstr   string
	}{
		{
			name: "valid s3 config",
			cfg: config.Config{
				ResultsMode:   "s3",
				OutputPrefix:  "s3://mybucket/results/",
				InputPrefixes: []string{"s3://mybucket/inputs/", "s3://otherbucket/data/"},
				S3KeyID:       "minioadmin",
				S3Secret:      "minioadmin",
				S3Region:      "garage",
			},
			wantErr: false,
		},
		{
			name: "valid local config",
			cfg: config.Config{
				ResultsMode:  "local",
				OutputPrefix: "",
			},
			wantErr: false,
		},
		{
			name: "invalid results mode",
			cfg: config.Config{
				ResultsMode: "azure-blob",
			},
			wantErr:      true,
			wantSentinel: config.ErrInvalidMode,
		},
		{
			name: "s3 mode missing region",
			cfg: config.Config{
				ResultsMode:  "s3",
				OutputPrefix: "s3://mybucket/results/",
				S3KeyID:      "minioadmin",
				S3Secret:     "minioadmin",
				S3Region:     "",
			},
			wantErr:    true,
			wantSubstr: "TILCAYO_S3_REGION",
		},
		{
			name: "s3 mode missing credentials",
			cfg: config.Config{
				ResultsMode:  "s3",
				OutputPrefix: "s3://mybucket/results/",
				S3KeyID:      "",
				S3Secret:     "minioadmin",
				S3Region:     "us-east-1",
			},
			wantErr:      true,
			wantSentinel: config.ErrMissingField,
		},
		{
			name: "s3 mode missing output prefix",
			cfg: config.Config{
				ResultsMode:  "s3",
				OutputPrefix: "",
				S3KeyID:      "minioadmin",
				S3Secret:     "minioadmin",
				S3Region:     "us-east-1",
			},
			wantErr:    true,
			wantSubstr: "TILCAYO_S3_OUTPUT_PREFIX",
		},
		{
			name: "s3 mode output prefix missing trailing slash",
			cfg: config.Config{
				ResultsMode:  "s3",
				OutputPrefix: "s3://mybucket/results",
				S3KeyID:      "minioadmin",
				S3Secret:     "minioadmin",
				S3Region:     "us-east-1",
			},
			wantErr:      true,
			wantSentinel: config.ErrInvalidPrefix,
		},
		{
			name: "s3 mode output prefix missing s3 scheme",
			cfg: config.Config{
				ResultsMode:  "s3",
				OutputPrefix: "/out/results/",
				S3KeyID:      "minioadmin",
				S3Secret:     "minioadmin",
				S3Region:     "us-east-1",
			},
			wantErr:      true,
			wantSentinel: config.ErrInvalidPrefix,
		},
		{
			name: "local mode refuses output prefix",
			cfg: config.Config{
				ResultsMode:  "local",
				OutputPrefix: "s3://mybucket/results/",
			},
			wantErr:      true,
			wantSentinel: config.ErrOutputPrefixRefused,
		},
		{
			name: "input prefix missing trailing slash",
			cfg: config.Config{
				ResultsMode:   "s3",
				OutputPrefix:  "s3://mybucket/results/",
				InputPrefixes: []string{"s3://mybucket/inputs"},
				S3KeyID:       "minioadmin",
				S3Secret:      "minioadmin",
				S3Region:      "us-east-1",
			},
			wantErr:      true,
			wantSentinel: config.ErrInvalidPrefix,
		},
		{
			name: "input prefix not s3",
			cfg: config.Config{
				ResultsMode:   "s3",
				OutputPrefix:  "s3://mybucket/results/",
				InputPrefixes: []string{"file:///var/data/"},
				S3KeyID:       "minioadmin",
				S3Secret:      "minioadmin",
				S3Region:      "us-east-1",
			},
			wantErr:      true,
			wantSentinel: config.ErrInvalidPrefix,
		},
		{
			name: "input prefix overlaps output prefix (input is parent of output)",
			cfg: config.Config{
				ResultsMode:   "s3",
				OutputPrefix:  "s3://mybucket/results/jobs/",
				InputPrefixes: []string{"s3://mybucket/results/"},
				S3KeyID:       "minioadmin",
				S3Secret:      "minioadmin",
				S3Region:      "us-east-1",
			},
			wantErr:      true,
			wantSentinel: config.ErrPrefixOverlap,
		},
		{
			name: "input prefix overlaps output prefix (output is parent of input)",
			cfg: config.Config{
				ResultsMode:   "s3",
				OutputPrefix:  "s3://mybucket/data/",
				InputPrefixes: []string{"s3://mybucket/data/subpath/"},
				S3KeyID:       "minioadmin",
				S3Secret:      "minioadmin",
				S3Region:      "us-east-1",
			},
			wantErr:      true,
			wantSentinel: config.ErrPrefixOverlap,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cfg.Validate()
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

func TestAllowedInputs(t *testing.T) {
	cfg := config.Config{
		ResultsMode:   "s3",
		OutputPrefix:  "s3://mybucket/results/",
		InputPrefixes: []string{"s3://mybucket/data/"},
		DataSource:    "/host/data",
	}

	allowed := cfg.AllowedInputs()

	// Must include input prefix
	foundInput := false
	for _, a := range allowed {
		if a == "s3://mybucket/data/" {
			foundInput = true
		}
	}
	if !foundInput {
		t.Errorf("AllowedInputs() missing s3://mybucket/data/, got %v", allowed)
	}

	// Must include /data/ because DataSource is set
	foundData := false
	for _, a := range allowed {
		if a == "/data/" {
			foundData = true
		}
	}
	if !foundData {
		t.Errorf("AllowedInputs() missing /data/, got %v", allowed)
	}

	// Must NEVER include output prefix
	for _, a := range allowed {
		if a == cfg.OutputPrefix {
			t.Errorf("AllowedInputs() must NEVER include OutputPrefix, got %v", allowed)
		}
	}
}
