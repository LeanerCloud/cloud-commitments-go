package recommendations

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
)

func expiryTestCov(usageType string, avg, pct float64) PoolCoverageMap {
	return PoolCoverageMap{
		rdsPoolKey("us-east-1", usageType, "MySQL", "Single-AZ"): {Pct: pct, AvgInstancesPerHour: avg},
	}
}

func expiryTestRec(size string, count int) []common.Recommendation {
	return []common.Recommendation{{
		Service:        common.ServiceRDS,
		CommitmentType: common.CommitmentReservedInstance,
		Region:         "us-east-1",
		ResourceType:   size,
		Count:          count,
		CommitmentCost: 1000, OnDemandCost: 2000, EstimatedSavings: 500,
		Details: &common.DatabaseDetails{Engine: "mysql", AZConfig: "single-az"},
	}}
}

func expiryTestRI(size, account string, count, endDays int) common.Commitment {
	return common.Commitment{
		Service: common.ServiceRDS, Region: "us-east-1", ResourceType: size,
		Engine: "mysql", Deployment: "single-az", Account: account,
		Count: count, State: "active",
		EndDate: time.Now().Add(time.Duration(endDays) * 24 * time.Hour),
	}
}

// scenario: usage 10 x db.r6g.xlarge (80 NU) at 80% covered (64 NU); active
// RIs 4 x large expiring (16 NU) + 6 x xlarge long-lived (48 NU) = 64 NU.
func expiryTestScenario() (PoolCoverageMap, []common.Recommendation, []common.Commitment) {
	return expiryTestCov("db.r6g.xlarge", 10, 80), expiryTestRec("db.r6g.xlarge", 5),
		[]common.Commitment{expiryTestRI("db.r6g.large", "a", 4, 15), expiryTestRI("db.r6g.xlarge", "a", 6, 300)}
}

func TestApplyFamilyNUSizingRDSWithExpiry(t *testing.T) {
	t.Run("expiring large RI against xlarge usage: parent drops family, fix sizes it", func(t *testing.T) {
		cov, recs, commits := expiryTestScenario()
		_, _, parent := ApplyFamilyNUSizingRDS(recs, cov, 80)
		assert.Equal(t, 1, parent.AlreadyAtTarget, "parent sees 80% and drops the family")

		sized, _, drops, rep := ApplyFamilyNUSizingRDSWithExpiry(recs, cov, 80, commits, 30)
		// covered 64 -> min(64, 64-16) = 48 => 60%; gap 20% of 80 NU = 16 NU;
		// rec 5 x xlarge = 40 NU; scale 0.4 => count 2, cost x0.4.
		require.Len(t, sized, 1)
		assert.Equal(t, 0, drops.AlreadyAtTarget)
		assert.Equal(t, 2, sized[0].Count)
		assert.InDelta(t, 400.0, sized[0].CommitmentCost, 1e-9)
		assert.InDelta(t, 60.0, sized[0].ExistingCoveragePct, 1e-9)
		assert.Equal(t, 16.0, rep.ExpiringNU)
		assert.Zero(t, rep.Unusable)
	})

	t.Run("linked-account rows are deducted once", func(t *testing.T) {
		cov, recs, _ := expiryTestScenario()
		commits := []common.Commitment{
			expiryTestRI("db.r6g.large", "a", 2, 15), expiryTestRI("db.r6g.large", "b", 2, 15),
			expiryTestRI("db.r6g.xlarge", "a", 6, 300),
		}
		cov[rdsPoolKey("us-east-1", "db.r6g.large", "MySQL", "Single-AZ")] = PoolCoverage{Pct: 0, AvgInstancesPerHour: 0}
		sized, _, _, rep := ApplyFamilyNUSizingRDSWithExpiry(recs, cov, 80, commits, 30)
		require.Len(t, sized, 1)
		assert.Equal(t, 2, sized[0].Count, "a double deduction would give count 4")
		assert.Equal(t, 16.0, rep.ExpiringNU)
	})

	t.Run("partial inventory: missing accounts' RIs never remove more than expires", func(t *testing.T) {
		// Coverage is org-wide (64 NU covered) but only account A's expiring
		// 16 NU is supplied; account B's 48 NU long-lived RIs are not (cli#2131).
		// Covered must drop by exactly the expiring 16 NU (60%), not to 0.
		cov, recs, _ := expiryTestScenario()
		commits := []common.Commitment{expiryTestRI("db.r6g.large", "a", 4, 15)}
		sized, _, _, _ := ApplyFamilyNUSizingRDSWithExpiry(recs, cov, 80, commits, 30)
		require.Len(t, sized, 1)
		assert.InDelta(t, 60.0, sized[0].ExistingCoveragePct, 1e-9)
		assert.Equal(t, 2, sized[0].Count, "uncapped deduction would size 8")
	})

	t.Run("RIs exceeding usage do not inflate the gap", func(t *testing.T) {
		// covered 40 NU of 80; active 10 x xlarge = 80 NU, 2 expiring (16).
		// min(40, 64) = 40: unchanged 50%. Naive subtraction would give 24 NU.
		cov := expiryTestCov("db.r6g.xlarge", 10, 50)
		commits := []common.Commitment{expiryTestRI("db.r6g.xlarge", "a", 2, 15), expiryTestRI("db.r6g.xlarge", "a", 8, 300)}
		sized, _, _, _ := ApplyFamilyNUSizingRDSWithExpiry(expiryTestRec("db.r6g.xlarge", 10), cov, 80, commits, 30)
		require.Len(t, sized, 1)
		assert.InDelta(t, 50.0, sized[0].ExistingCoveragePct, 1e-9)
		assert.Equal(t, 3, sized[0].Count, "gap 30% of 80 NU = 24 NU over 80 NU rec")
	})

	t.Run("mixed sizes: min is in NU, not counts", func(t *testing.T) {
		// active 2 x large (8 NU) + 1 expiring xlarge (8 NU) = 16 NU; covered
		// 12 NU of 16; post-expiry covered = min(12, 16-8) = 8 => 50%.
		cov := expiryTestCov("db.r6g.xlarge", 2, 75)
		commits := []common.Commitment{expiryTestRI("db.r6g.large", "a", 2, 300), expiryTestRI("db.r6g.xlarge", "a", 1, 15)}
		sized, _, _, rep := ApplyFamilyNUSizingRDSWithExpiry(expiryTestRec("db.r6g.large", 4), cov, 80, commits, 30)
		require.Len(t, sized, 1)
		assert.InDelta(t, 50.0, sized[0].ExistingCoveragePct, 1e-9)
		assert.Equal(t, 1, sized[0].Count, "gap 30% of 16 NU = 4.8 NU over a 16 NU rec: floor(1.2)")
		assert.Equal(t, 4.0, rep.ExpiringNU, "covered 12 -> 8 is a 4 NU deduction, not the 8 NU expiring")
	})

	t.Run("ExpiringNU is the actual deduction: underutilized RIs absorb it", func(t *testing.T) {
		// 40 NU covered, 80 NU active, 16 NU expiring: covered stays 40.
		cov := expiryTestCov("db.r6g.xlarge", 10, 50)
		commits := []common.Commitment{expiryTestRI("db.r6g.xlarge", "a", 2, 15), expiryTestRI("db.r6g.xlarge", "a", 8, 300)}
		_, _, _, rep := ApplyFamilyNUSizingRDSWithExpiry(expiryTestRec("db.r6g.xlarge", 10), cov, 80, commits, 30)
		assert.Zero(t, rep.ExpiringNU)
	})

	t.Run("control: outside window", func(t *testing.T) {
		cov, recs, _ := expiryTestScenario()
		commits := []common.Commitment{expiryTestRI("db.r6g.large", "a", 4, 180), expiryTestRI("db.r6g.xlarge", "a", 6, 300)}
		sized, _, drops, rep := ApplyFamilyNUSizingRDSWithExpiry(recs, cov, 80, commits, 30)
		assert.Empty(t, sized)
		assert.Equal(t, 1, drops.AlreadyAtTarget)
		assert.Zero(t, rep)
	})

	t.Run("control: non-RDS, inactive, zero window and no commitments are ignored", func(t *testing.T) {
		cov, recs, commits := expiryTestScenario()
		ec2 := expiryTestRI("m5.large", "a", 4, 15)
		ec2.Service = common.ServiceEC2
		retired := expiryTestRI("db.r6g.large", "a", 4, 15)
		retired.State = "retired"
		for name, c := range map[string][]common.Commitment{"ec2": {ec2}, "retired": {retired}, "none": nil} {
			_, _, drops, rep := ApplyFamilyNUSizingRDSWithExpiry(recs, cov, 80, c, 30)
			assert.Equal(t, 1, drops.AlreadyAtTarget, name)
			assert.Zero(t, rep, name)
		}
		_, _, drops, _ := ApplyFamilyNUSizingRDSWithExpiry(recs, cov, 80, commits, 0)
		assert.Equal(t, 1, drops.AlreadyAtTarget, "window 0")
	})

	t.Run("unusable expiring RIs are counted, never read as no expiry", func(t *testing.T) {
		cov, recs, _ := expiryTestScenario()
		unparsable := expiryTestRI("db.r6g.weird", "a", 1, 15)
		engineMismatch := expiryTestRI("db.r6g.large", "a", 1, 15)
		engineMismatch.Engine = "aurora"
		_, _, drops, rep := ApplyFamilyNUSizingRDSWithExpiry(recs, cov, 80, []common.Commitment{unparsable, engineMismatch}, 30)
		assert.Equal(t, 2, rep.Unusable)
		assert.Zero(t, rep.ExpiringNU)
		assert.Equal(t, 1, drops.AlreadyAtTarget, "no deduction was possible")
	})

	t.Run("invalid target returns recs untouched", func(t *testing.T) {
		cov, recs, commits := expiryTestScenario()
		sized, nonRDS, _, _ := ApplyFamilyNUSizingRDSWithExpiry(recs, cov, 0, commits, 30)
		assert.Nil(t, sized)
		assert.Len(t, nonRDS, 1)
	})
}
