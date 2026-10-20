package config

import (
	"fmt"
	"strings"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/scorer"
	"github.com/spf13/pflag"
)

var validClouds = map[string]struct{}{
	"aws":   {},
	"azure": {},
	"gcp":   {},
}

// defaults returns a Config with all default values applied.
func defaults() Config {
	return Config{
		DryRun:            true,
		AutoApprove:       false,
		AuditLog:          "./cudly-audit.jsonl",
		EnabledClouds:     []string{"aws", "azure", "gcp"},
		IdempotencyWindow: 24 * time.Hour,
		Server: ServerConfig{
			Enabled:   false,
			Listen:    ":8080",
			APIKeyEnv: "CUDLY_API_KEY", // #nosec G101 -- name of the env var to look up the API key, not a credential value
		},
		Azure: AzureConfig{Scope: "shared"},
	}
}

// Load returns a Config resolved from 4 layers:
//  1. defaults
//  2. YAML file (path arg, CUDLY_CONFIG env, or ./cudly.yaml)
//  3. environment variables
//  4. CLI flags (only explicitly-set flags via flags.Changed)
//
// An explicit path that does not exist is an error. A missing default ./cudly.yaml is not.
func applyFlags(cfg *Config, flags *pflag.FlagSet) error {
	if flags == nil {
		return nil
	}
	if err := applyFlagsDryRun(cfg, flags); err != nil {
		return err
	}
	if err := applyFlagsScorer(cfg, flags); err != nil {
		return err
	}
	return applyFlagsOther(cfg, flags)
}

// applyFlagsDryRun sets DryRun from --purchase, the CLI's only purchase-gating flag.
func applyFlagsDryRun(cfg *Config, flags *pflag.FlagSet) error {
	if !flags.Changed("purchase") {
		return nil
	}
	v, err := flags.GetBool("purchase")
	if err != nil {
		return fmt.Errorf("--purchase: %w", err)
	}
	cfg.DryRun = !v
	return nil
}

// applyFlagsScorer merges scorer-related CLI flags into cfg.
func applyFlagsScorer(cfg *Config, flags *pflag.FlagSet) error {
	if flags.Changed("min-savings-pct") {
		v, err := flags.GetFloat64("min-savings-pct")
		if err != nil {
			return fmt.Errorf("--min-savings-pct: %w", err)
		}
		cfg.Scorer.MinSavingsPct = v
		cfg.Scorer.MinSavingsPctSet = true
	}
	if flags.Changed("max-break-even-months") {
		v, err := flags.GetInt("max-break-even-months")
		if err != nil {
			return fmt.Errorf("--max-break-even-months: %w", err)
		}
		cfg.Scorer.MaxBreakEvenMonths = v
	}
	if flags.Changed("min-count") {
		v, err := flags.GetInt("min-count")
		if err != nil {
			return fmt.Errorf("--min-count: %w", err)
		}
		cfg.Scorer.MinCount = v
	}
	return nil
}

// applyFlagsOther merges remaining CLI flags into cfg.
func applyFlagsOther(cfg *Config, flags *pflag.FlagSet) error {
	if flags.Changed("yes") {
		v, err := flags.GetBool("yes")
		if err != nil {
			return fmt.Errorf("--yes: %w", err)
		}
		cfg.AutoApprove = v
	}
	if flags.Changed("audit-log") {
		v, err := flags.GetString("audit-log")
		if err != nil {
			return fmt.Errorf("--audit-log: %w", err)
		}
		cfg.AuditLog = v
	}
	if flags.Changed("profile") {
		v, err := flags.GetString("profile")
		if err != nil {
			return fmt.Errorf("--profile: %w", err)
		}
		cfg.AWS.Profile = v
	}
	if flags.Changed("idempotency-window") {
		v, err := flags.GetString("idempotency-window")
		if err != nil {
			return fmt.Errorf("--idempotency-window: %w", err)
		}
		d, err := time.ParseDuration(v)
		if err != nil {
			return fmt.Errorf("--idempotency-window: %w", err)
		}
		cfg.IdempotencyWindow = d
	}
	return nil
}

// validate checks the resolved config for semantic errors.
func validate(cfg Config) error {
	for _, cloud := range cfg.EnabledClouds {
		if _, ok := validClouds[strings.ToLower(cloud)]; !ok {
			return fmt.Errorf("unknown cloud provider: %s", cloud)
		}
	}
	if len(cfg.EnabledClouds) == 0 {
		return fmt.Errorf("at least one cloud must be enabled")
	}
	if cfg.IdempotencyWindow <= 0 {
		return fmt.Errorf("idempotency_window must be positive, got %s", cfg.IdempotencyWindow)
	}
	if err := cfg.Scorer.Validate(); err != nil {
		return err
	}
	return nil
}

// splitComma splits a comma-separated string and trims whitespace from each element.
func splitComma(s string) []string {
	parts := strings.Split(s, ",")
	result := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			result = append(result, t)
		}
	}
	return result
}

// DefaultScorerConfig returns a scorer.Config that can be embedded in a config.Config.
// Exposed so callers can construct a default Config without importing the scorer package directly.
func DefaultScorerConfig() scorer.Config {
	return scorer.Config{}
}
