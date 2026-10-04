package recommendations

import (
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
)

func TestExpiringCoverageMissingDemandPreservesInput(t *testing.T) {
	key := poolKey("us-east-1", "m5.large")
	for _, tc := range []struct {
		name     string
		coverage PoolCoverageMap
	}{
		{"nil", nil},
		{"missing key", PoolCoverageMap{poolKey("us-west-2", "m5.large"): {AvgInstancesPerHour: 90}}},
		{"zero", PoolCoverageMap{key: {Pct: 60}}},
		{"negative", PoolCoverageMap{key: {Pct: 60, AvgInstancesPerHour: -90}}},
		{"nan", PoolCoverageMap{key: {Pct: 60, AvgInstancesPerHour: math.NaN()}}},
		{"infinite", PoolCoverageMap{key: {Pct: 60, AvgInstancesPerHour: math.Inf(1)}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			monthly := 300.0
			rec := common.Recommendation{
				Service: common.ServiceEC2, Region: "us-east-1", ResourceType: "m5.large",
				Account: "111111111111", CommitmentType: common.CommitmentReservedInstance,
				AverageInstancesUsedPerHour: 10, ExistingCoveragePct: 60, ExistingCoverageKnown: true,
				Count: 30, RecommendedCount: 30, CommitmentCost: 3000, OnDemandCost: 6000,
				EstimatedSavings: 3000, RecurringMonthlyCost: &monthly, ProjectedCoverage: 80,
			}
			recs := []common.Recommendation{rec, rec}
			recs[1].Account = "222222222222"
			before := append([]common.Recommendation(nil), recs...)
			commits := []common.Commitment{{
				Service: common.ServiceEC2, Region: "us-east-1", ResourceType: "m5.large",
				Count: 18, State: "active", EndDate: time.Now().Add(15 * 24 * time.Hour),
			}}
			adjusted, missing := AdjustExistingCoverageForExpiringCommitmentsWithCoverage(recs, commits, 30, tc.coverage)
			assert.Zero(t, adjusted)
			assert.Equal(t, 2, missing)
			assert.Equal(t, before, recs)
			assert.Same(t, &monthly, recs[0].RecurringMonthlyCost)
			assert.Equal(t, 300.0, monthly)
		})
	}
}

func TestExpiringCoverageGuardsAndSelection(t *testing.T) {
	soon := time.Now().Add(15 * 24 * time.Hour)
	for _, tc := range []struct {
		name         string
		days         int
		avg          float64
		count        int
		state        common.CommitmentState
		end          time.Time
		wantAdjusted int
		wantCoverage float64
	}{
		{"zero window", 0, 30, 18, "active", soon, 0, 60},
		{"zero row demand", 30, 0, 18, "active", soon, 0, 60},
		{"zero count", 30, 30, 0, "active", soon, 0, 60},
		{"unknown end", 30, 30, 18, "active", time.Time{}, 0, 60},
		{"outside window", 30, 30, 18, "active", soon.Add(180 * 24 * time.Hour), 0, 60},
		{"retired", 30, 30, 18, "retired", soon, 0, 60},
		{"queued", 30, 30, 18, "queued", soon, 0, 60},
		{"payment pending", 30, 30, 18, "payment-pending", soon, 1, 40},
		{"past active", 30, 30, 18, "active", soon.Add(-30 * 24 * time.Hour), 1, 40},
		{"clamped", 30, 30, 90, "active", soon, 1, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recs := []common.Recommendation{{Service: common.ServiceEC2, Region: "us-east-1", ResourceType: "m5.large", AverageInstancesUsedPerHour: tc.avg, ExistingCoveragePct: 60}}
			commits := []common.Commitment{{Service: common.ServiceEC2, Region: "us-east-1", ResourceType: "m5.large", Count: tc.count, State: tc.state, EndDate: tc.end}}
			coverage := PoolCoverageMap{poolKey("us-east-1", "m5.large"): {AvgInstancesPerHour: 90}}
			if tc.wantAdjusted == 0 {
				coverage = nil
			}
			adjusted, missing := AdjustExistingCoverageForExpiringCommitmentsWithCoverage(recs, commits, tc.days, coverage)
			assert.Equal(t, tc.wantAdjusted, adjusted)
			assert.Zero(t, missing)
			assert.InDelta(t, tc.wantCoverage, recs[0].ExistingCoveragePct, 0.001)
		})
	}
	adjusted, missing := AdjustExistingCoverageForExpiringCommitmentsWithCoverage(nil, nil, 30, nil)
	assert.Zero(t, adjusted)
	assert.Zero(t, missing)
}

func TestExpiringCoverageKeepsPoolBoundaries(t *testing.T) {
	var nilDatabase *common.DatabaseDetails
	for _, tc := range []struct {
		name         string
		service      common.ServiceType
		region       string
		resource     string
		details      common.ServiceDetails
		engine       string
		deployment   string
		wantAdjusted int
	}{
		{"normalized EC2", common.ServiceEC2, "US-EAST-1", "M5.LARGE", nil, "", "", 1},
		{"other region", common.ServiceEC2, "us-west-2", "m5.large", nil, "", "", 0},
		{"other size same family", common.ServiceEC2, "us-east-1", "m5.xlarge", nil, "", "", 0},
		{"RDS engine alias", common.ServiceRDS, "us-east-1", "db.m5.large", &common.DatabaseDetails{Engine: "Postgres", AZConfig: "single-az"}, "postgresql", "single-az", 1},
		{"RDS other engine", common.ServiceRDS, "us-east-1", "db.m5.large", &common.DatabaseDetails{Engine: "mysql", AZConfig: "single-az"}, "postgresql", "single-az", 0},
		{"RDS other deployment", common.ServiceRDS, "us-east-1", "db.m5.large", &common.DatabaseDetails{Engine: "postgresql", AZConfig: "multi-az"}, "postgresql", "single-az", 0},
		{"nil database", common.ServiceRDS, "us-east-1", "db.m5.large", nil, "postgresql", "single-az", 0},
		{"typed nil database", common.ServiceRDS, "us-east-1", "db.m5.large", nilDatabase, "postgresql", "single-az", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := common.Recommendation{Service: tc.service, Region: tc.region, ResourceType: tc.resource, Details: tc.details, AverageInstancesUsedPerHour: 10, ExistingCoveragePct: 60}
			resource := "m5.large"
			if tc.service == common.ServiceRDS {
				resource = "db.m5.large"
			}
			commits := []common.Commitment{{Service: tc.service, Region: "us-east-1", ResourceType: resource, Engine: tc.engine, Deployment: tc.deployment, Count: 18, State: "active", EndDate: time.Now().Add(15 * 24 * time.Hour)}}
			coverage := PoolCoverageMap{lookupPoolKey(rec): {AvgInstancesPerHour: 90}}
			if tc.wantAdjusted == 0 {
				coverage = nil
			}
			for _, copies := range []int{1, 3} {
				recs := make([]common.Recommendation, copies)
				for i := range recs {
					recs[i] = rec
				}
				adjusted, missing := AdjustExistingCoverageForExpiringCommitmentsWithCoverage(recs, commits, 30, coverage)
				assert.Equal(t, tc.wantAdjusted*copies, adjusted)
				assert.Zero(t, missing)
				for _, got := range recs {
					assert.InDelta(t, 60-float64(tc.wantAdjusted)*20, got.ExistingCoveragePct, 0.001)
				}
			}
		})
	}
}

func TestExpiringCoverageLegacyIgnoresInvalidDemandContributions(t *testing.T) {
	recs := make([]common.Recommendation, 0, 6)
	for _, avg := range []float64{30, 60, 0, -1, math.NaN(), math.Inf(1)} {
		recs = append(recs, common.Recommendation{Service: common.ServiceEC2, Region: "us-east-1", ResourceType: "m5.large", AverageInstancesUsedPerHour: avg, ExistingCoveragePct: 60})
	}
	commits := []common.Commitment{{Service: common.ServiceEC2, Region: "us-east-1", ResourceType: "m5.large", Count: 18, State: "active", EndDate: time.Now().Add(15 * 24 * time.Hour)}}
	AdjustExistingCoverageForExpiringCommitments(recs, commits, 30)
	assert.InDelta(t, 40.0, recs[0].ExistingCoveragePct, 0.001)
	assert.InDelta(t, 40.0, recs[1].ExistingCoveragePct, 0.001)
}
