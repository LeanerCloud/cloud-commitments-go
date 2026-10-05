package recfilter

import (
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
)

func TestApplyTargetCoverageExactExpiry(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name           string
		demand, target float64
		want           int
	}{
		{"integer boundary", 30, 80, 4},
		{"fractional target", 30, math.Nextafter(80, 0), 3},
		{"fraction rounds up as float", math.Nextafter(30, math.Inf(1)), 80, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			percent := new(big.Rat).Sub(big.NewRat(60, 1), new(big.Rat).Quo(big.NewRat(200, 1), new(big.Rat).SetFloat64(tc.demand)))
			before := new(big.Rat).Set(percent)
			existing, _ := percent.Float64()
			monthly := 300.0
			rec := mkRI(30, 15, existing)
			rec.ExistingCoveragePercentExact = percent
			rec.RecommendedCount = 30
			rec.RecurringMonthlyCost = &monthly
			out := ApplyTargetCoverage([]common.Recommendation{rec}, tc.target, nil, nil)
			require.Len(t, out, 1)
			t.Logf("exact percentage=%s display=%.17g target=%.17g count=%d", percent.RatString(), existing, tc.target, out[0].Count)
			assert.Equal(t, tc.want, out[0].Count)
			assert.Equal(t, 30, out[0].RecommendedCount)
			ratio := float64(tc.want) / 30
			assert.InDelta(t, ratio*rec.CommitmentCost, out[0].CommitmentCost, 1e-9)
			assert.InDelta(t, ratio*rec.OnDemandCost, out[0].OnDemandCost, 1e-9)
			assert.InDelta(t, ratio*rec.EstimatedSavings, out[0].EstimatedSavings, 1e-9)
			assert.InDelta(t, float64(tc.want)*10, *out[0].RecurringMonthlyCost, 1e-9)
			assert.NotSame(t, rec.RecurringMonthlyCost, out[0].RecurringMonthlyCost)
			assert.Equal(t, 300.0, monthly)
			assert.Equal(t, before, percent)
			assert.Same(t, percent, out[0].ExistingCoveragePercentExact)
			assert.InDelta(t, existing+float64(tc.want)/15*100, out[0].ProjectedCoverage, 1e-12)
		})
	}
}

func TestApplyTargetCoverageExactBoundaries(t *testing.T) {
	t.Parallel()
	thinGap := new(big.Rat).Sub(big.NewRat(80, 1), big.NewRat(100, 1<<60))
	for _, tc := range []struct {
		name    string
		percent *big.Rat
		avg     float64
		want    int
		drop    string
	}{
		{"zero coverage", big.NewRat(0, 1), 15, 12, ""},
		{"fractional quantity", big.NewRat(170, 3), 15, 3, ""},
		{"below one", big.NewRat(75, 1), 15, 0, common.DropTargetSizedToZero},
		{"already met", big.NewRat(80, 1), 15, 0, common.DropTargetAlreadyMet},
		{"exact gap before rounded zero", thinGap, 1 << 60, 1, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			existing, _ := tc.percent.Float64()
			rec := mkRI(30, tc.avg, existing)
			rec.ExistingCoveragePercentExact = tc.percent
			drops := common.NewDropSummary()
			out := ApplyTargetCoverage([]common.Recommendation{rec}, 80, nil, drops)
			if tc.want == 0 {
				assert.Empty(t, out)
				assert.Equal(t, "Dropped 1 recs: "+tc.drop+"=1", drops.FormatOneLine())
				return
			}
			require.Len(t, out, 1)
			assert.Equal(t, tc.want, out[0].Count)
			assert.True(t, drops.IsEmpty())
		})
	}
}

func TestApplyTargetCoverageInvalidInputDrops(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name                  string
		avg, existing, target float64
	}{
		{"nan average", math.NaN(), 0, 80},
		{"positive infinite average", math.Inf(1), 0, 80},
		{"negative infinite average", math.Inf(-1), 0, 80},
		{"max float average", math.MaxFloat64, 0, 80},
		{"finite count overflow", 1e300, 0, 80},
		{"nan target", 15, 0, math.NaN()},
		{"nan existing coverage", 15, math.NaN(), 80},
		{"positive infinite existing coverage", 15, math.Inf(1), 80},
		{"negative infinite existing coverage", 15, math.Inf(-1), 80},
		{"negative existing coverage", 15, -1, 80},
	} {
		for _, exact := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/exact=%t", tc.name, exact), func(t *testing.T) {
				t.Parallel()
				rec := mkRI(30, tc.avg, tc.existing)
				if exact && !math.IsNaN(tc.existing) && !math.IsInf(tc.existing, 0) {
					rec.ExistingCoveragePercentExact = new(big.Rat).SetFloat64(tc.existing)
				}
				assertInvalidInputDropped(t, rec, tc.target)
			})
		}
	}
	t.Run("negative exact coverage rounding to zero", func(t *testing.T) {
		t.Parallel()
		rec := mkRI(30, 15, 0)
		rec.ExistingCoveragePercentExact = new(big.Rat).SetFrac(big.NewInt(-1), new(big.Int).Lsh(big.NewInt(1), 1100))
		assertInvalidInputDropped(t, rec, 80)
	})
}

func assertInvalidInputDropped(t *testing.T, rec common.Recommendation, target float64) {
	t.Helper()
	var logs []string
	drops := common.NewDropSummary()
	out := ApplyTargetCoverage([]common.Recommendation{rec}, target, captureLogf(&logs), drops)
	assert.Empty(t, out)
	assert.Equal(t, "Dropped 1 recs: "+common.DropTargetInputInvalid+"=1", drops.FormatOneLine())
	require.Len(t, logs, 1)
	assert.Contains(t, logs[0], "WARNING: --target-coverage")
}

func TestApplyTargetCoverageExactStateLifetime(t *testing.T) {
	t.Parallel()
	percent := big.NewRat(160, 3)
	existing, _ := percent.Float64()
	original := mkRI(30, 15, existing)
	original.ExistingCoveragePercentExact = percent
	for _, tc := range []struct {
		name    string
		prepare func(*common.Recommendation)
	}{
		{"different percentage invalidates", func(rec *common.Recommendation) { rec.ExistingCoveragePct = 60 }},
		{"same percentage requires clearing", func(rec *common.Recommendation) { rec.ExistingCoveragePercentExact = nil }},
		{"json loses precise state", func(rec *common.Recommendation) {
			payload, err := json.Marshal(rec)
			require.NoError(t, err)
			*rec = common.Recommendation{}
			require.NoError(t, json.Unmarshal(payload, rec))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := original
			tc.prepare(&rec)
			out := ApplyTargetCoverage([]common.Recommendation{rec}, 80, nil, nil)
			require.Len(t, out, 1)
			assert.Equal(t, 3, out[0].Count)
		})
	}
	out := ApplyTargetCoverage([]common.Recommendation{original, original}, 80, nil, nil)
	require.Len(t, out, 2)
	assert.Equal(t, 4, out[0].Count)
	assert.Equal(t, 4, out[1].Count)
	assert.Equal(t, big.NewRat(160, 3), percent)
	assert.Equal(t, 30, original.Count)
}

func TestApplyTargetCoverageExactDoesNotChangeOtherPaths(t *testing.T) {
	t.Parallel()
	for _, provider := range []common.ProviderType{common.ProviderAWS, common.ProviderAzure, common.ProviderGCP} {
		rec := mkRI(30, 15, 60)
		rec.Provider = provider
		out := ApplyTargetCoverage([]common.Recommendation{rec}, 80, nil, nil)
		require.Len(t, out, 1)
		assert.Equal(t, 3, out[0].Count)
		assert.Nil(t, out[0].ExistingCoveragePercentExact)
	}
	for _, rec := range []common.Recommendation{mkSP(95, 2), {CommitmentType: common.CommitmentCUD, Count: 7}} {
		want := ApplyTargetCoverage([]common.Recommendation{rec}, 80, nil, nil)
		rec.ExistingCoveragePercentExact = big.NewRat(160, 3)
		got := ApplyTargetCoverage([]common.Recommendation{rec}, 80, nil, nil)
		require.Len(t, got, 1)
		got[0].ExistingCoveragePercentExact = nil
		assert.Equal(t, want, got)
	}
}
