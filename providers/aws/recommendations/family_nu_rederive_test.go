package recommendations

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
)

func rederiveRDSRec(instType, engine string, count int) common.Recommendation {
	return common.Recommendation{
		Service:                     common.ServiceRDS,
		CommitmentType:              common.CommitmentReservedInstance,
		Region:                      "us-east-1",
		ResourceType:                instType,
		Count:                       count,
		CommitmentCost:              float64(count) * 100,
		AverageInstancesUsedPerHour: 3,
		Details:                     &common.DatabaseDetails{Engine: engine, AZConfig: "single-az"},
	}
}

func rederiveFixture() (PoolCoverageMap, []common.Recommendation) {
	cov := PoolCoverageMap{
		rdsPoolKey("us-east-1", "db.r7g.large", "Aurora MySQL", "Single-AZ"):  {Pct: 20, AvgInstancesPerHour: 40},
		rdsPoolKey("us-east-1", "db.r7g.xlarge", "Aurora MySQL", "Single-AZ"): {Pct: 0, AvgInstancesPerHour: 5},
		rdsPoolKey("us-east-1", "db.t4g.medium", "Aurora MySQL", "Single-AZ"): {Pct: 10, AvgInstancesPerHour: 50},
		rdsPoolKey("us-east-1", "db.m5.large", "SQL Server", "Single-AZ"):     {Pct: 0, AvgInstancesPerHour: 10},
	}
	recs := []common.Recommendation{
		rederiveRDSRec("db.r7g.large", "aurora-mysql", 30),
		rederiveRDSRec("db.r7g.xlarge", "aurora-mysql", 10),
		rederiveRDSRec("db.t4g.medium", "aurora-mysql", 20),
		rederiveRDSRec("db.m5.large", "sqlserver", 8),
		{Service: common.ServiceEC2, CommitmentType: common.CommitmentReservedInstance, Region: "us-east-1",
			ResourceType: "m5.large", Count: 4, ProjectedCoverage: 42},
	}
	return cov, recs
}

func sizedFixture(t *testing.T) (PoolCoverageMap, []common.Recommendation) {
	t.Helper()
	cov, recs := rederiveFixture()
	sized, nonRDS, _ := ApplyFamilyNUSizingRDS(recs, cov, 90)
	require.Len(t, sized, 3)
	return cov, append(sized, nonRDS...)
}

func findRec(t *testing.T, recs []common.Recommendation, instType string) *common.Recommendation {
	t.Helper()
	for i := range recs {
		if recs[i].ResourceType == instType {
			return &recs[i]
		}
	}
	t.Fatalf("rec %s not found", instType)
	return nil
}

func TestRederiveFamilyCoverage_UnchangedCountsReproduceSizing(t *testing.T) {
	t.Parallel()
	cov, recs := sizedFixture(t)
	got := RederiveFamilyCoverage(recs, cov)
	require.Len(t, got, len(recs))
	for i := range recs {
		assert.Equal(t, recs[i], got[i], "rec %d", i)
	}
}

func TestRederiveFamilyCoverage_CappedFamilyLowersCoverageOthersUntouched(t *testing.T) {
	t.Parallel()
	cov, recs := sizedFixture(t)
	capped := make([]common.Recommendation, len(recs))
	copy(capped, recs)
	large := findRec(t, capped, "db.r7g.large")
	before := large.ProjectedCoverage
	large.Count = 1
	r7gXL := findRec(t, capped, "db.r7g.xlarge")
	r7gXL.Count = 1

	got := RederiveFamilyCoverage(capped, cov)

	totalNU := 40*4.0 + 5*8.0
	want := large.ExistingCoveragePct + (1*4.0+1*8.0)/totalNU*100
	assert.InDelta(t, want, findRec(t, got, "db.r7g.large").ProjectedCoverage, 1e-9)
	assert.InDelta(t, want, findRec(t, got, "db.r7g.xlarge").ProjectedCoverage, 1e-9)
	assert.Less(t, want, before, "capping lowers family coverage")
	assert.Equal(t, *findRec(t, recs, "db.t4g.medium"), *findRec(t, got, "db.t4g.medium"), "untouched family identical")
	assert.Equal(t, before, findRec(t, recs, "db.r7g.large").ProjectedCoverage, "input not mutated")
	assert.Equal(t, 1, large.Count)
}

func TestRederiveFamilyCoverage_DroppedRecNoLongerContributes(t *testing.T) {
	t.Parallel()
	cov, recs := sizedFixture(t)
	var kept []common.Recommendation
	for _, r := range recs {
		if r.ResourceType != "db.r7g.xlarge" {
			kept = append(kept, r)
		}
	}
	got := RederiveFamilyCoverage(kept, cov)
	large := findRec(t, got, "db.r7g.large")
	want := large.ExistingCoveragePct + float64(large.Count)*4/(40*4.0+5*8.0)*100
	assert.InDelta(t, want, large.ProjectedCoverage, 1e-9)
}

func TestRederiveFamilyCoverage_MixedServiceAndSQLServerUntouched(t *testing.T) {
	t.Parallel()
	cov, recs := sizedFixture(t)
	for i := range recs {
		recs[i].Count = 1
	}
	got := RederiveFamilyCoverage(recs, cov)
	assert.Equal(t, *findRec(t, recs, "m5.large"), *findRec(t, got, "m5.large"), "EC2 untouched")
	assert.Equal(t, *findRec(t, recs, "db.m5.large"), *findRec(t, got, "db.m5.large"), "SQL Server untouched")
	assert.Equal(t, 42.0, findRec(t, got, "m5.large").ProjectedCoverage)
}

func TestRederiveFamilyCoverage_UnsizedFamilyUntouched(t *testing.T) {
	t.Parallel()
	cov, recs := rederiveFixture()
	recs[0].ProjectedCoverage = 7
	recs[0].Count = 2
	got := RederiveFamilyCoverage(recs[:1], cov)
	assert.Equal(t, 7.0, got[0].ProjectedCoverage, "rec never annotated by family sizing is left alone")
}

func TestFamilyProjectedCoverage_CapsAt100(t *testing.T) {
	t.Parallel()
	assert.Equal(t, 100.0, familyProjectedCoverage(60, 50, 100), "60% + 50% caps at exactly 100")
	assert.Equal(t, 90.0, familyProjectedCoverage(60, 30, 100))

	sized := []common.Recommendation{{Count: 10, AverageInstancesUsedPerHour: 3}}
	annotateFamilyProjection(sized, 60, 50, 100)
	assert.Equal(t, 100.0, sized[0].ProjectedCoverage, "sizing path caps at 100")
}

func TestRederiveFamilyCoverage_CapsAt100(t *testing.T) {
	t.Parallel()
	cov, recs := sizedFixture(t)
	for i := range recs {
		recs[i].Count *= 100 // count override well past the family demand
	}
	got := RederiveFamilyCoverage(recs, cov)
	assert.Equal(t, 100.0, findRec(t, got, "db.r7g.large").ProjectedCoverage)
	assert.Equal(t, 100.0, findRec(t, got, "db.t4g.medium").ProjectedCoverage)
}
