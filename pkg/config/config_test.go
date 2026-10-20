package config

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/pflag"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newFlags builds a pflag.FlagSet that mirrors the CLI flags relevant to Load().
// The CLI registers no --dry-run flag; its TestDryRunFlagRemoved guards that.
func newFlags() *pflag.FlagSet {
	fs := pflag.NewFlagSet("test", pflag.ContinueOnError)
	fs.Bool("purchase", false, "")
	fs.Bool("yes", false, "")
	fs.String("audit-log", "./cudly-audit.jsonl", "")
	fs.String("profile", "", "")
	fs.Float64("min-savings-pct", 0, "")
	fs.Int("max-break-even-months", 0, "")
	fs.Int("min-count", 0, "")
	fs.String("idempotency-window", "24h", "")
	return fs
}

func writeYAML(t *testing.T, content string) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "cudly-*.yaml")
	require.NoError(t, err)
	_, err = fmt.Fprint(f, content)
	require.NoError(t, err)
	f.Close()
	return f.Name()
}

func TestLoad_PurchaseFlag(t *testing.T) {
	// NOT parallel: uses t.Setenv (process-wide env mutation panics with t.Parallel).
	cases := []struct {
		name       string
		yaml       string
		env        string
		args       []string
		wantDryRun bool
	}{
		{name: "no flag keeps dry-run default", wantDryRun: true},
		{name: "purchase disables dry-run", args: []string{"--purchase"}, wantDryRun: false},
		{name: "purchase overrides yaml", yaml: "dry_run: true\n", args: []string{"--purchase"}, wantDryRun: false},
		{name: "purchase overrides env", env: "true", args: []string{"--purchase"}, wantDryRun: false},
		{name: "explicit purchase=false keeps dry-run", yaml: "dry_run: false\n", args: []string{"--purchase=false"}, wantDryRun: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("CUDLY_DRY_RUN", tc.env)
			fs := newFlags()
			require.NoError(t, fs.Parse(tc.args))
			cfg, err := Load(writeYAML(t, tc.yaml), fs)
			require.NoError(t, err)
			assert.Equal(t, tc.wantDryRun, cfg.DryRun)
		})
	}
}

func TestLoad_UnknownCloud_Error(t *testing.T) {
	t.Parallel()
	path := writeYAML(t, "enabled_clouds:\n  - oracle\n")
	_, err := Load(path, newFlags())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown cloud provider: oracle")
}

func TestLoad_NegativeThreshold_Error(t *testing.T) {
	t.Parallel()
	path := writeYAML(t, "scorer:\n  min_savings_pct: -5\n")
	_, err := Load(path, newFlags())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "min_savings_pct must be ≥ 0")
}

func TestValidate_EmptyEnabledClouds_Error(t *testing.T) {
	t.Parallel()
	// validate() is called by Load; test it directly since empty clouds
	// cannot be produced by env/YAML (empty overrides are ignored to protect defaults)
	err := validate(Config{EnabledClouds: []string{}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "at least one cloud")
}

func TestLoad_CUDLYCONFIGEnv(t *testing.T) {
	// NOT parallel: uses t.Setenv (process-wide env mutation panics with t.Parallel).
	path := writeYAML(t, "scorer:\n  min_savings_pct: 7\n")
	t.Setenv("CUDLY_CONFIG", path)
	cfg, err := Load("", newFlags())
	require.NoError(t, err)
	assert.Equal(t, 7.0, cfg.Scorer.MinSavingsPct)
}

func TestLoad_ArgPathTakesPrecedenceOverCUDLYCONFIG(t *testing.T) {
	// NOT parallel: uses t.Setenv (process-wide env mutation panics with t.Parallel).
	path1 := writeYAML(t, "scorer:\n  min_savings_pct: 5\n")
	path2 := writeYAML(t, "scorer:\n  min_savings_pct: 99\n")
	t.Setenv("CUDLY_CONFIG", path2)
	cfg, err := Load(path1, newFlags())
	require.NoError(t, err)
	assert.Equal(t, 5.0, cfg.Scorer.MinSavingsPct)
}

func TestLoad_YAMLRoundtrip(t *testing.T) {
	t.Parallel()
	content := `dry_run: false
auto_approve: true
audit_log: /tmp/audit.jsonl
enabled_clouds:
  - aws
  - gcp
scorer:
  min_savings_pct: 10
  max_break_even_months: 24
  min_count: 2
aws:
  profile: my-profile
azure:
  subscription_id: sub-xyz
  scope: single
gcp:
  org_id: org-1
  projects:
    - proj-a
  regions:
    - us-central1
`
	path := filepath.Join(t.TempDir(), "cudly.yaml")
	require.NoError(t, os.WriteFile(path, []byte(content), 0644))
	cfg, err := Load(path, newFlags())
	require.NoError(t, err)
	assert.False(t, cfg.DryRun)
	assert.True(t, cfg.AutoApprove)
	assert.Equal(t, "/tmp/audit.jsonl", cfg.AuditLog)
	assert.Equal(t, []string{"aws", "gcp"}, cfg.EnabledClouds)
	assert.Equal(t, 10.0, cfg.Scorer.MinSavingsPct)
	assert.Equal(t, 24, cfg.Scorer.MaxBreakEvenMonths)
	assert.Equal(t, 2, cfg.Scorer.MinCount)
	assert.Equal(t, "my-profile", cfg.AWS.Profile)
	assert.Equal(t, "sub-xyz", cfg.Azure.SubscriptionID)
	assert.Equal(t, "single", cfg.Azure.Scope)
	assert.Equal(t, "org-1", cfg.GCP.OrgID)
	assert.Equal(t, []string{"proj-a"}, cfg.GCP.Projects)
	assert.Equal(t, []string{"us-central1"}, cfg.GCP.Regions)
}

func TestApplyYAMLScorer_ExplicitZeroOverridesNonZero(t *testing.T) {
	cfg := defaults()
	cfg.Scorer.MinSavingsPct = 5
	cfg.Scorer.MaxBreakEvenMonths = 6
	cfg.Scorer.MinCount = 2
	path := writeYAML(t, "scorer:\n  min_savings_pct: 0\n  max_break_even_months: 0\n  min_count: 0\n")

	require.NoError(t, applyYAML(&cfg, path, true))

	assert.Zero(t, cfg.Scorer.MinSavingsPct)
	assert.Zero(t, cfg.Scorer.MaxBreakEvenMonths)
	assert.Zero(t, cfg.Scorer.MinCount)
}

func TestApplyYAMLScorer_AbsentKeepsExisting(t *testing.T) {
	cfg := defaults()
	cfg.Scorer.MinSavingsPct = 5
	path := writeYAML(t, "scorer:\n  min_count: 3\n")

	require.NoError(t, applyYAML(&cfg, path, true))

	assert.Equal(t, 5.0, cfg.Scorer.MinSavingsPct)
	assert.Equal(t, 3, cfg.Scorer.MinCount)
}

func TestLoad_NonPositiveIdempotencyWindow_Error(t *testing.T) {
	t.Run("yaml", func(t *testing.T) {
		for _, v := range []string{"0s", "-1h"} {
			path := writeYAML(t, "idempotency_window: "+v+"\n")
			_, err := Load(path, newFlags())
			require.Error(t, err, v)
			assert.Contains(t, err.Error(), "idempotency_window")
		}
	})
	t.Run("env", func(t *testing.T) {
		t.Setenv("CUDLY_IDEMPOTENCY_WINDOW", "0s")
		_, err := Load(writeYAML(t, ""), newFlags())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "idempotency_window")
	})
	t.Run("flag", func(t *testing.T) {
		fs := newFlags()
		require.NoError(t, fs.Set("idempotency-window", "-5m"))
		_, err := Load(writeYAML(t, ""), fs)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "idempotency_window")
	})
}

func TestLoad_MinSavingsPctSet(t *testing.T) {
	t.Run("absent leaves unset", func(t *testing.T) {
		cfg, err := Load(writeYAML(t, "scorer:\n  min_count: 1\n"), newFlags())
		require.NoError(t, err)
		assert.False(t, cfg.Scorer.MinSavingsPctSet)
	})
	t.Run("yaml explicit zero", func(t *testing.T) {
		cfg, err := Load(writeYAML(t, "scorer:\n  min_savings_pct: 0\n"), newFlags())
		require.NoError(t, err)
		assert.True(t, cfg.Scorer.MinSavingsPctSet)
	})
	t.Run("env explicit zero", func(t *testing.T) {
		t.Setenv("CUDLY_MIN_SAVINGS_PCT", "0")
		cfg, err := Load(writeYAML(t, ""), newFlags())
		require.NoError(t, err)
		assert.True(t, cfg.Scorer.MinSavingsPctSet)
	})
	t.Run("flag explicit zero", func(t *testing.T) {
		fs := newFlags()
		require.NoError(t, fs.Parse([]string{"--min-savings-pct", "0"}))
		cfg, err := Load(writeYAML(t, ""), fs)
		require.NoError(t, err)
		assert.True(t, cfg.Scorer.MinSavingsPctSet)
	})
	t.Run("NaN env rejected", func(t *testing.T) {
		t.Setenv("CUDLY_MIN_SAVINGS_PCT", "NaN")
		_, err := Load(writeYAML(t, ""), newFlags())
		require.Error(t, err)
	})
}
