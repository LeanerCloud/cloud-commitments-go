package recommendations

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/recfilter"
)

// TestAdjustExistingCoverageForExpiringCommitments covers the four cases:
// the no-op guards, the typical case (some commitments expiring, some not),
// the over-subtract clamp, and pool-key mismatch (commitment for a pool
// nobody recommended).
func TestAdjustExistingCoverageForExpiringCommitments(t *testing.T) {
	now := time.Now()
	soon := now.Add(15 * 24 * time.Hour)     // expires in 15 days
	later := now.Add(180 * 24 * time.Hour)   // expires in 180 days
	pastDate := now.Add(-7 * 24 * time.Hour) // already expired
	pgEngine := "Aurora PostgreSQL"

	t.Run("no-op when windowDays is zero", func(t *testing.T) {
		recs := []common.Recommendation{{
			Service:                     common.ServiceRDS,
			Region:                      "us-east-1",
			ResourceType:                "db.r6g.large",
			AverageInstancesUsedPerHour: 10,
			ExistingCoveragePct:         80,
			Details:                     &common.DatabaseDetails{Engine: pgEngine},
		}}
		commits := []common.Commitment{{
			Service:      common.ServiceRDS,
			Region:       "us-east-1",
			ResourceType: "db.r6g.large",
			Engine:       pgEngine,
			Count:        5,
			State:        "active",
			EndDate:      soon,
		}}
		n := AdjustExistingCoverageForExpiringCommitments(recs, commits, 0)
		assert.Equal(t, 0, n)
		assert.Equal(t, 80.0, recs[0].ExistingCoveragePct, "ExistingCoveragePct must be untouched when windowDays=0")
	})

	t.Run("typical case: subtracts expiring share within window", func(t *testing.T) {
		// avg=10, existing=80%, 5 of those covered by RIs expiring in 15 days.
		// expiringPct = 5/10*100 = 50. New existing = 80 - 50 = 30.
		recs := []common.Recommendation{{
			Service:                     common.ServiceRDS,
			Region:                      "us-east-1",
			ResourceType:                "db.r6g.large",
			AverageInstancesUsedPerHour: 10,
			ExistingCoveragePct:         80,
			Details:                     &common.DatabaseDetails{Engine: pgEngine},
		}}
		commits := []common.Commitment{{
			Service:      common.ServiceRDS,
			Region:       "us-east-1",
			ResourceType: "db.r6g.large",
			Engine:       pgEngine,
			Count:        5,
			State:        "active",
			EndDate:      soon,
		}}
		n := AdjustExistingCoverageForExpiringCommitments(recs, commits, 30)
		assert.Equal(t, 1, n, "one rec adjusted")
		assert.InDelta(t, 30.0, recs[0].ExistingCoveragePct, 0.001)
	})

	t.Run("deployment-aware matching: Single-AZ commit only adjusts Single-AZ rec", func(t *testing.T) {
		// Two recs for the same (region, type, engine) but different
		// deployments. An expiring Single-AZ commitment must only affect
		// the Single-AZ rec's existing-coverage signal — a Single-AZ RI
		// cannot cover Multi-AZ demand. Without Deployment threaded into
		// commitmentPoolKey, the commitment's empty-deployment key would
		// miss both recs (both have deployment-aware lookup keys).
		recs := []common.Recommendation{
			{
				Service:                     common.ServiceRDS,
				Region:                      "us-east-1",
				ResourceType:                "db.r6g.large",
				AverageInstancesUsedPerHour: 10,
				ExistingCoveragePct:         80,
				Details:                     &common.DatabaseDetails{Engine: pgEngine, AZConfig: "single-az"},
			},
			{
				Service:                     common.ServiceRDS,
				Region:                      "us-east-1",
				ResourceType:                "db.r6g.large",
				AverageInstancesUsedPerHour: 10,
				ExistingCoveragePct:         80,
				Details:                     &common.DatabaseDetails{Engine: pgEngine, AZConfig: "multi-az"},
			},
		}
		commits := []common.Commitment{{
			Service:      common.ServiceRDS,
			Region:       "us-east-1",
			ResourceType: "db.r6g.large",
			Engine:       pgEngine,
			Deployment:   "single-az",
			Count:        5,
			State:        "active",
			EndDate:      soon,
		}}
		n := AdjustExistingCoverageForExpiringCommitments(recs, commits, 30)
		assert.Equal(t, 1, n, "only the Single-AZ rec adjusted")
		assert.InDelta(t, 30.0, recs[0].ExistingCoveragePct, 0.001, "Single-AZ rec: 80% - 50% expiring = 30%")
		assert.Equal(t, 80.0, recs[1].ExistingCoveragePct, "Multi-AZ rec untouched (different deployment pool)")
	})

	t.Run("commitments expiring outside window are ignored", func(t *testing.T) {
		recs := []common.Recommendation{{
			Service:                     common.ServiceRDS,
			Region:                      "us-east-1",
			ResourceType:                "db.r6g.large",
			AverageInstancesUsedPerHour: 10,
			ExistingCoveragePct:         80,
			Details:                     &common.DatabaseDetails{Engine: pgEngine},
		}}
		commits := []common.Commitment{{
			Service:      common.ServiceRDS,
			Region:       "us-east-1",
			ResourceType: "db.r6g.large",
			Engine:       pgEngine,
			Count:        5,
			State:        "active",
			EndDate:      later, // outside window
		}}
		n := AdjustExistingCoverageForExpiringCommitments(recs, commits, 30)
		assert.Equal(t, 0, n)
		assert.Equal(t, 80.0, recs[0].ExistingCoveragePct)
	})

	t.Run("over-subtract clamps to zero", func(t *testing.T) {
		// Expiring count exceeds the share that ExistingCoveragePct claims —
		// can happen when CE coverage is averaged across accounts but the
		// commitments list is for the full region. Clamp to 0 rather than
		// going negative.
		recs := []common.Recommendation{{
			Service:                     common.ServiceRDS,
			Region:                      "us-east-1",
			ResourceType:                "db.r6g.large",
			AverageInstancesUsedPerHour: 10,
			ExistingCoveragePct:         20, // only 20% per CE
			Details:                     &common.DatabaseDetails{Engine: pgEngine},
		}}
		commits := []common.Commitment{{
			Service:      common.ServiceRDS,
			Region:       "us-east-1",
			ResourceType: "db.r6g.large",
			Engine:       pgEngine,
			Count:        5, // 5/10 = 50% expiring
			State:        "active",
			EndDate:      soon,
		}}
		n := AdjustExistingCoverageForExpiringCommitments(recs, commits, 30)
		assert.Equal(t, 1, n)
		assert.Equal(t, 0.0, recs[0].ExistingCoveragePct, "must clamp to 0, not go negative")
	})

	t.Run("non-matching pool key is skipped", func(t *testing.T) {
		recs := []common.Recommendation{{
			Service:                     common.ServiceRDS,
			Region:                      "us-east-1",
			ResourceType:                "db.r6g.large",
			AverageInstancesUsedPerHour: 10,
			ExistingCoveragePct:         80,
			Details:                     &common.DatabaseDetails{Engine: pgEngine},
		}}
		commits := []common.Commitment{{
			Service:      common.ServiceRDS,
			Region:       "us-east-1",
			ResourceType: "db.r6g.large",
			Engine:       "MySQL", // different engine
			Count:        5,
			State:        "active",
			EndDate:      soon,
		}}
		n := AdjustExistingCoverageForExpiringCommitments(recs, commits, 30)
		assert.Equal(t, 0, n, "engine mismatch should skip")
		assert.Equal(t, 80.0, recs[0].ExistingCoveragePct)
	})

	t.Run("inactive commitments are skipped", func(t *testing.T) {
		recs := []common.Recommendation{{
			Service:                     common.ServiceRDS,
			Region:                      "us-east-1",
			ResourceType:                "db.r6g.large",
			AverageInstancesUsedPerHour: 10,
			ExistingCoveragePct:         80,
			Details:                     &common.DatabaseDetails{Engine: pgEngine},
		}}
		commits := []common.Commitment{{
			Service:      common.ServiceRDS,
			Region:       "us-east-1",
			ResourceType: "db.r6g.large",
			Engine:       pgEngine,
			Count:        5,
			State:        "retired", // not active
			EndDate:      soon,
		}}
		n := AdjustExistingCoverageForExpiringCommitments(recs, commits, 30)
		assert.Equal(t, 0, n, "retired commitments don't currently provide coverage; skip")
		assert.Equal(t, 80.0, recs[0].ExistingCoveragePct)
	})

	t.Run("active commits with EndDate before cutoff are counted (incl. past dates)", func(t *testing.T) {
		// The window check is EndDate <= cutoff: past dates satisfy that
		// too. State="active" + EndDate<cutoff is treated as expiring
		// regardless of whether EndDate is in the past or future. The
		// upstream State filter is the gate for "is this RI providing
		// coverage right now"; once a commitment passes that filter, this
		// function trusts its EndDate alone.
		//
		// Document the intent here so a future reader doesn't try to add
		// "EndDate > now" as an additional guard and silently change the
		// semantics of pastDate commitments.
		recs := []common.Recommendation{{
			Service:                     common.ServiceRDS,
			Region:                      "us-east-1",
			ResourceType:                "db.r6g.large",
			AverageInstancesUsedPerHour: 10,
			ExistingCoveragePct:         80,
			Details:                     &common.DatabaseDetails{Engine: pgEngine},
		}}
		commits := []common.Commitment{{
			Service:      common.ServiceRDS,
			Region:       "us-east-1",
			ResourceType: "db.r6g.large",
			Engine:       pgEngine,
			Count:        5,
			State:        "active",
			EndDate:      pastDate,
		}}
		n := AdjustExistingCoverageForExpiringCommitments(recs, commits, 30)
		assert.Equal(t, 1, n, "EndDate=pastDate <= cutoff so the commit IS counted")
		assert.InDelta(t, 30.0, recs[0].ExistingCoveragePct, 0.001)
	})

	t.Run("no-op when commitments is empty", func(t *testing.T) {
		recs := []common.Recommendation{{
			Service:                     common.ServiceRDS,
			AverageInstancesUsedPerHour: 10,
			ExistingCoveragePct:         50,
		}}
		n := AdjustExistingCoverageForExpiringCommitments(recs, nil, 30)
		assert.Equal(t, 0, n)
		assert.Equal(t, 50.0, recs[0].ExistingCoveragePct)
	})
}

func TestExpiringCoverageUsesWholePoolDemand(t *testing.T) {
	type expiryAdjust func([]common.Recommendation, []common.Commitment, int) int
	adjust := expiryAdjust(AdjustExistingCoverageForExpiringCommitments)
	apis := []struct {
		name   string
		adjust func([]common.Recommendation, []common.Commitment, int, PoolCoverageMap) (int, int)
	}{
		{"legacy", func(recs []common.Recommendation, commits []common.Commitment, days int, _ PoolCoverageMap) (int, int) {
			return adjust(recs, commits, days), 0
		}},
		{"coverage", AdjustExistingCoverageForExpiringCommitmentsWithCoverage},
	}
	accounts := []string{"111111111111", "222222222222", "333333333333"}
	for _, tc := range []struct {
		name         string
		rawAverages  []float64
		wantAverages []float64
		wantCounts   []int
		expiryDays   int
		wantCoverage float64
		wantAdjusted int
	}{
		{
			name: "three linked accounts", rawAverages: []float64{30, 30, 30},
			wantAverages: []float64{30, 30, 30}, wantCounts: []int{12, 12, 12},
			expiryDays: 15, wantCoverage: 40, wantAdjusted: 3,
		},
		{
			name: "unequal demand shares", rawAverages: []float64{15, 30, 45},
			wantAverages: []float64{15, 30, 45}, wantCounts: []int{6, 12, 18},
			expiryDays: 15, wantCoverage: 40, wantAdjusted: 3,
		},
		{
			name: "zero raw shares rebalance evenly", rawAverages: []float64{0, 0, 0},
			wantAverages: []float64{30, 30, 30}, wantCounts: []int{12, 12, 12},
			expiryDays: 15, wantCoverage: 40, wantAdjusted: 3,
		},
		{
			name: "two surviving accounts rebalance before expiry", rawAverages: []float64{30, 30},
			wantAverages: []float64{45, 45}, wantCounts: []int{18, 18},
			expiryDays: 15, wantCoverage: 40, wantAdjusted: 2,
		},
		{
			name: "single surviving account control", rawAverages: []float64{30},
			wantAverages: []float64{90}, wantCounts: []int{36},
			expiryDays: 15, wantCoverage: 40, wantAdjusted: 1,
		},
		{
			name: "outside expiry window control", rawAverages: []float64{30, 30, 30},
			wantAverages: []float64{30, 30, 30}, wantCounts: []int{6, 6, 6},
			expiryDays: 180, wantCoverage: 60, wantAdjusted: 0,
		},
	} {
		for _, api := range apis {
			t.Run(tc.name+"/"+api.name, func(t *testing.T) {
				recs := make([]common.Recommendation, len(tc.rawAverages))
				for i, avg := range tc.rawAverages {
					monthly := 300.0
					recs[i] = common.Recommendation{
						Service: common.ServiceEC2, Region: "us-east-1", ResourceType: "m5.large",
						Account: accounts[i], CommitmentType: common.CommitmentReservedInstance,
						Count: 30, RecommendedCount: 30, AverageInstancesUsedPerHour: avg,
						CommitmentCost: 3000, OnDemandCost: 6000, EstimatedSavings: 3000,
						RecurringMonthlyCost: &monthly,
					}
				}
				coverage := PoolCoverageMap{
					poolKey("us-east-1", "m5.large"): {Pct: 60, AvgInstancesPerHour: 90},
				}
				ApplyCoverageMapToRecommendations(recs, coverage)
				for i := range recs {
					assert.Equal(t, tc.wantAverages[i], recs[i].AverageInstancesUsedPerHour)
					assert.Equal(t, 60.0, recs[i].ExistingCoveragePct)
					assert.True(t, recs[i].ExistingCoverageKnown)
				}
				commits := []common.Commitment{{
					Service: common.ServiceEC2, Region: "us-east-1", ResourceType: "m5.large",
					Count: 18, State: "active", EndDate: time.Now().Add(time.Duration(tc.expiryDays) * 24 * time.Hour),
				}}
				adjusted, missing := api.adjust(recs, commits, 30, coverage)
				assert.Equal(t, tc.wantAdjusted, adjusted)
				assert.Zero(t, missing)
				for i := range recs {
					assert.InDelta(t, tc.wantCoverage, recs[i].ExistingCoveragePct, 0.001, "account %s", recs[i].Account)
				}
				sized := recfilter.ApplyTargetCoverage(recs, 80, nil, nil)
				if !assert.Len(t, sized, len(recs)) {
					return
				}
				for i, rec := range sized {
					wantCount := tc.wantCounts[i]
					assert.Equal(t, accounts[i], rec.Account)
					assert.Equal(t, wantCount, rec.Count)
					assert.Equal(t, 30, rec.RecommendedCount)
					assert.InDelta(t, float64(wantCount)*100, rec.CommitmentCost, 0.001)
					assert.InDelta(t, float64(wantCount)*200, rec.OnDemandCost, 0.001)
					assert.InDelta(t, float64(wantCount)*100, rec.EstimatedSavings, 0.001)
					if assert.NotNil(t, rec.RecurringMonthlyCost) {
						assert.InDelta(t, float64(wantCount)*10, *rec.RecurringMonthlyCost, 0.001)
						assert.NotSame(t, recs[i].RecurringMonthlyCost, rec.RecurringMonthlyCost)
					}
					assert.Equal(t, 300.0, *recs[i].RecurringMonthlyCost)
					assert.InDelta(t, 80.0, rec.ProjectedCoverage, 0.001)
					assert.Equal(t, 100.0, rec.ProjectedUtilization)
				}
			})
		}
	}
}

func TestExpiringCoveragePreservesZeroDemandShare(t *testing.T) {
	recs := []common.Recommendation{
		{Service: common.ServiceEC2, Region: "us-east-1", ResourceType: "m5.large", AverageInstancesUsedPerHour: 30},
		{Service: common.ServiceEC2, Region: "us-east-1", ResourceType: "m5.large", AverageInstancesUsedPerHour: 0},
		{Service: common.ServiceEC2, Region: "us-east-1", ResourceType: "m5.large", AverageInstancesUsedPerHour: 60},
	}
	coverage := PoolCoverageMap{
		poolKey("us-east-1", "m5.large"): {Pct: 60, AvgInstancesPerHour: 90},
	}
	ApplyCoverageMapToRecommendations(recs, coverage)
	zeroShare := recs[1]
	commits := []common.Commitment{{
		Service: common.ServiceEC2, Region: "us-east-1", ResourceType: "m5.large",
		Count: 18, State: "active", EndDate: time.Now().Add(15 * 24 * time.Hour),
	}}
	assert.Equal(t, 2, AdjustExistingCoverageForExpiringCommitments(recs, commits, 30))
	assert.Equal(t, zeroShare, recs[1])
	assert.InDelta(t, 40.0, recs[0].ExistingCoveragePct, 0.001)
	assert.InDelta(t, 40.0, recs[2].ExistingCoveragePct, 0.001)
}
