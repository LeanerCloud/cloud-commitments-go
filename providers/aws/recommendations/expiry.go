package recommendations

import (
	"math"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
)

// AdjustExistingCoverageForExpiringCommitments returns the number of adjusted recommendations.
// Demand must be complete disjoint pool shares, or freshly rebalanced by ApplyCoverageMapToRecommendations.
//
// Partial raw slices and duplicated aggregate averages cannot be detected here.
// Callers with authoritative pool demand should use AdjustExistingCoverageForExpiringCommitmentsWithCoverage.
func AdjustExistingCoverageForExpiringCommitments(
	recs []common.Recommendation,
	commitments []common.Commitment,
	windowDays int,
) int {
	demand := make(map[string]float64)
	for i := range recs {
		avg := recs[i].AverageInstancesUsedPerHour
		if avg > 0 && !math.IsNaN(avg) && !math.IsInf(avg, 0) {
			demand[lookupPoolKey(recs[i])] += avg
		}
	}
	adjusted, _ := adjustExpiringCoverage(recs, commitments, windowDays, demand)
	return adjusted
}

// AdjustExistingCoverageForExpiringCommitmentsWithCoverage uses authoritative pool demand, never row sums.
// Eligible rows lacking positive finite demand remain unchanged and are counted in missingDemand.
func AdjustExistingCoverageForExpiringCommitmentsWithCoverage(
	recs []common.Recommendation,
	commitments []common.Commitment,
	windowDays int,
	coverage PoolCoverageMap,
) (adjusted, missingDemand int) {
	demand := make(map[string]float64, len(coverage))
	for key, cov := range coverage {
		demand[key] = cov.AvgInstancesPerHour
	}
	return adjustExpiringCoverage(recs, commitments, windowDays, demand)
}

func adjustExpiringCoverage(
	recs []common.Recommendation,
	commitments []common.Commitment,
	windowDays int,
	demand map[string]float64,
) (adjusted, missingDemand int) {
	if windowDays <= 0 || len(commitments) == 0 {
		return 0, 0
	}
	cutoff := time.Now().Add(time.Duration(windowDays) * 24 * time.Hour)
	expiringByPool := expiringCountsByPool(commitments, cutoff)
	if len(expiringByPool) == 0 {
		return 0, 0
	}
	return applyExpiringAdjustments(recs, expiringByPool, demand)
}

// expiringCountsByPool aggregates active commitments expiring at-or-before
// cutoff into a pool-keyed count map. The State filter mirrors
// DuplicateChecker.filterRecentCommitments — only RIs currently providing
// coverage are counted (queued / retired RIs aren't covering demand now).
func expiringCountsByPool(commitments []common.Commitment, cutoff time.Time) map[string]int {
	out := make(map[string]int)
	for i := range commitments {
		c := &commitments[i]
		if !commitmentIsActive(*c) {
			continue
		}
		if c.EndDate.IsZero() || c.EndDate.After(cutoff) {
			continue
		}
		key := commitmentPoolKey(*c)
		if key == "" {
			continue
		}
		out[key] += c.Count
	}
	return out
}

func applyExpiringAdjustments(recs []common.Recommendation, expiringByPool map[string]int, demand map[string]float64) (adjusted, missingDemand int) {
	for i := range recs {
		if recs[i].AverageInstancesUsedPerHour <= 0 {
			continue
		}
		key := lookupPoolKey(recs[i])
		expCount, ok := expiringByPool[key]
		if !ok || expCount == 0 {
			continue
		}
		avg := demand[key]
		if avg <= 0 || math.IsNaN(avg) || math.IsInf(avg, 0) {
			missingDemand++
			continue
		}
		expiringPct := float64(expCount) / avg * 100.0
		if expiringPct > recs[i].ExistingCoveragePct {
			recs[i].ExistingCoveragePct = 0
		} else {
			recs[i].ExistingCoveragePct -= expiringPct
		}
		adjusted++
	}
	return adjusted, missingDemand
}

// commitmentIsActive returns true for commitments whose State indicates
// they're currently providing coverage. Matches the state set used by
// DuplicateChecker for consistency.
func commitmentIsActive(c common.Commitment) bool {
	return c.State == "active" || c.State == "payment-pending"
}

// commitmentPoolKey returns the same lookup key shape used by the coverage
// map: engine + deployment-aware for RDS, region+instance-type for
// everything else. Keys are org-wide (no account dimension) so an
// expiring RI in one linked account adjusts the org-wide coverage signal
// for the pool it belongs to — matching the way the coverage map itself
// is now aggregated. The Deployment field on common.Commitment carries
// the same "single-az"/"multi-az" vocabulary as DatabaseDetails.AZConfig
// on Recommendation, so a Multi-AZ commitment's expiry adjusts only the
// Multi-AZ pool's existing-coverage signal (a Single-AZ RI cannot cover
// Multi-AZ demand and vice versa).
func commitmentPoolKey(c common.Commitment) string {
	if c.Service == common.ServiceRDS || c.Service == common.ServiceRelationalDB {
		return rdsPoolKey(c.Region, c.ResourceType, c.Engine, c.Deployment)
	}
	return poolKey(c.Region, c.ResourceType)
}
