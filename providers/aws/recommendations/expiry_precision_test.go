package recommendations

import (
	"math"
	"math/big"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/recfilter"
)

func expiryPrecisionFixture() ([]common.Recommendation, []common.Commitment, PoolCoverageMap) {
	recs := make([]common.Recommendation, 2)
	for i, account := range []string{"111111111111", "222222222222"} {
		monthly := 300.0
		recs[i] = common.Recommendation{
			Service: common.ServiceEC2, Region: "us-east-1", ResourceType: "m5.large", Account: account,
			CommitmentType: common.CommitmentReservedInstance, Count: 30, RecommendedCount: 30,
			AverageInstancesUsedPerHour: 15, ExistingCoveragePct: 60, ExistingCoverageKnown: true,
			CommitmentCost: 3000, OnDemandCost: 6000, EstimatedSavings: 3000, RecurringMonthlyCost: &monthly,
		}
	}
	commits := []common.Commitment{{
		Service: common.ServiceEC2, Region: "us-east-1", ResourceType: "m5.large",
		Count: 2, State: "active", EndDate: time.Now().Add(15 * 24 * time.Hour),
	}}
	coverage := PoolCoverageMap{poolKey("us-east-1", "m5.large"): {Pct: 60, AvgInstancesPerHour: 30}}
	return recs, commits, coverage
}

func TestExpiringCoverageExactSizing(t *testing.T) {
	for _, api := range []string{"legacy", "coverage"} {
		for _, tc := range []struct {
			name     string
			target   float64
			expiring int
			want     int
		}{
			{"integer boundary", 80, 2, 4},
			{"fractional target", math.Nextafter(80, 0), 2, 3},
			{"fractional quantity", 80, 1, 3},
			{"below one", 60, 1, 0},
		} {
			t.Run(api+"/"+tc.name, func(t *testing.T) {
				recs, commits, coverage := expiryPrecisionFixture()
				commits[0].Count = tc.expiring
				ApplyCoverageMapToRecommendations(recs, coverage)
				if api == "legacy" {
					assert.Equal(t, 2, AdjustExistingCoverageForExpiringCommitments(recs, commits, 30))
				} else {
					adjusted, missing := AdjustExistingCoverageForExpiringCommitmentsWithCoverage(recs, commits, 30, coverage)
					assert.Equal(t, 2, adjusted)
					assert.Zero(t, missing)
				}
				sized := recfilter.ApplyTargetCoverage(recs, tc.target, nil, nil)
				if tc.want == 0 {
					assert.Empty(t, sized)
					return
				}
				require.Len(t, sized, 2)
				for i, rec := range sized {
					t.Logf("%s coverage=%.17g target=%.17g count=%d", rec.Account, rec.ExistingCoveragePct, tc.target, rec.Count)
					assert.Equal(t, tc.want, rec.Count)
					assert.Equal(t, 30, rec.RecommendedCount)
					assert.Equal(t, recs[i].Account, rec.Account)
					assert.InDelta(t, float64(tc.want)*100, rec.CommitmentCost, 1e-9)
					assert.InDelta(t, float64(tc.want)*200, rec.OnDemandCost, 1e-9)
					assert.InDelta(t, float64(tc.want)*100, rec.EstimatedSavings, 1e-9)
					assert.InDelta(t, float64(tc.want)*10, *rec.RecurringMonthlyCost, 1e-9)
					assert.NotSame(t, recs[i].RecurringMonthlyCost, rec.RecurringMonthlyCost)
					assert.Equal(t, 300.0, *recs[i].RecurringMonthlyCost)
					assert.InDelta(t, rec.ExistingCoveragePct+float64(tc.want)/15*100, rec.ProjectedCoverage, 1e-12)
				}
			})
		}
	}
}

func TestExpiringCoverageExactFractionRoundsUp(t *testing.T) {
	recs, commits, coverage := expiryPrecisionFixture()
	demand := math.Nextafter(30, math.Inf(1))
	coverage[poolKey("us-east-1", "m5.large")] = PoolCoverage{Pct: 60, AvgInstancesPerHour: demand}
	adjusted, missing := AdjustExistingCoverageForExpiringCommitmentsWithCoverage(recs, commits, 30, coverage)
	assert.Equal(t, 2, adjusted)
	assert.Zero(t, missing)
	sized := recfilter.ApplyTargetCoverage(recs, 80, nil, nil)
	require.Len(t, sized, 2)
	for _, rec := range sized {
		assert.Equal(t, 3, rec.Count)
		assert.InDelta(t, 300.0, rec.CommitmentCost, 1e-9)
		assert.InDelta(t, 600.0, rec.OnDemandCost, 1e-9)
		assert.InDelta(t, 300.0, rec.EstimatedSavings, 1e-9)
		assert.InDelta(t, 30.0, *rec.RecurringMonthlyCost, 1e-9)
		require.NotNil(t, rec.ExistingCoveragePercentExact)
		quantity := new(big.Rat).Sub(big.NewRat(80, 1), rec.ExistingCoveragePercentExact)
		quantity.Mul(quantity, big.NewRat(15, 100))
		assert.Negative(t, quantity.Cmp(big.NewRat(4, 1)))
		rounded, _ := quantity.Float64()
		assert.Equal(t, 4.0, rounded, "this control distinguishes exact floor from a final float round-trip")
	}
}

func TestExpiringCoverageExactRepeatedAndReplaced(t *testing.T) {
	for _, api := range []string{"legacy", "coverage"} {
		t.Run(api, func(t *testing.T) {
			recs, commits, coverage := expiryPrecisionFixture()
			adjust := func(rows []common.Recommendation, count int) {
				commits[0].Count = count
				if api == "legacy" {
					assert.Equal(t, len(rows), AdjustExistingCoverageForExpiringCommitments(rows, commits, 30))
				} else {
					n, missing := AdjustExistingCoverageForExpiringCommitmentsWithCoverage(rows, commits, 30, coverage)
					assert.Equal(t, len(rows), n)
					assert.Zero(t, missing)
				}
			}
			adjust(recs, 1)
			first := append([]common.Recommendation(nil), recs...)
			require.Equal(t, big.NewRat(170, 3), first[0].ExistingCoveragePercentExact)
			recs[1].ExistingCoveragePercentExact = recs[0].ExistingCoveragePercentExact
			adjust(recs, 1)
			single, _, _ := expiryPrecisionFixture()
			adjust(single, 2)
			for i := range recs {
				assert.Equal(t, single[i].ExistingCoveragePct, recs[i].ExistingCoveragePct)
				assert.Equal(t, big.NewRat(160, 3), recs[i].ExistingCoveragePercentExact)
				assert.Equal(t, big.NewRat(170, 3), first[i].ExistingCoveragePercentExact)
				assert.NotSame(t, first[i].ExistingCoveragePercentExact, recs[i].ExistingCoveragePercentExact)
			}
			replaced := append([]common.Recommendation(nil), recs...)
			for i := range replaced {
				replaced[i].ExistingCoveragePct = 66
			}
			adjust(replaced, 1)
			assert.Equal(t, big.NewRat(188, 3), replaced[0].ExistingCoveragePercentExact)
			cleared := append([]common.Recommendation(nil), recs...)
			want := new(big.Rat).Sub(new(big.Rat).SetFloat64(cleared[0].ExistingCoveragePct), big.NewRat(10, 3))
			for i := range cleared {
				cleared[i].ExistingCoveragePercentExact = nil
			}
			adjust(cleared, 1)
			assert.Equal(t, want, cleared[0].ExistingCoveragePercentExact)
			assert.NotEqual(t, big.NewRat(50, 1), cleared[0].ExistingCoveragePercentExact)
			assert.Equal(t, big.NewRat(160, 3), recs[0].ExistingCoveragePercentExact)
			changedDemand := append([]common.Recommendation(nil), first...)
			for i := range changedDemand {
				changedDemand[i].AverageInstancesUsedPerHour = 30
			}
			coverage[poolKey("us-east-1", "m5.large")] = PoolCoverage{AvgInstancesPerHour: 60}
			adjust(changedDemand, 1)
			assert.Equal(t, big.NewRat(55, 1), changedDemand[0].ExistingCoveragePercentExact)
		})
	}
}

func TestExpiringCoverageExactNonfiniteReplacement(t *testing.T) {
	for _, pct := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		recs, commits, coverage := expiryPrecisionFixture()
		for i := range recs {
			recs[i].ExistingCoveragePct = pct
			recs[i].ExistingCoveragePercentExact = big.NewRat(60, 1)
		}
		adjusted, missing := AdjustExistingCoverageForExpiringCommitmentsWithCoverage(recs, commits, 30, coverage)
		assert.Equal(t, 2, adjusted)
		assert.Zero(t, missing)
		for _, rec := range recs {
			assert.Nil(t, rec.ExistingCoveragePercentExact)
			switch {
			case math.IsNaN(pct):
				assert.True(t, math.IsNaN(rec.ExistingCoveragePct))
			case math.IsInf(pct, 1):
				assert.True(t, math.IsInf(rec.ExistingCoveragePct, 1))
			default:
				assert.Zero(t, rec.ExistingCoveragePct)
			}
		}
	}
}
