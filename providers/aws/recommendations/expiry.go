package recommendations

import (
	"math"
	"math/big"
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
	adjusted, _ := adjustExpiringCoverage(recs, commitments, PoolExpiryOptions{WindowDays: windowDays}, demand)
	return adjusted
}

// AdjustExistingCoverageForExpiringCommitmentsWithCoverage uses authoritative pool demand, never row sums.
// Eligible rows lacking positive finite demand remain unchanged and are counted in missingDemand.
// It ignores replacements already bought; see
// AdjustExistingCoverageForExpiringCommitmentsWithCoverageOptions.
func AdjustExistingCoverageForExpiringCommitmentsWithCoverage(
	recs []common.Recommendation,
	commitments []common.Commitment,
	windowDays int,
	coverage PoolCoverageMap,
) (adjusted, missingDemand int) {
	return AdjustExistingCoverageForExpiringCommitmentsWithCoverageOptions(
		recs, commitments, coverage, PoolExpiryOptions{WindowDays: windowDays})
}

// PoolExpiryOptions configures the per-pool (EC2 and other non-family) expiry
// deduction. It mirrors FamilyExpiryOptions, which does the same for RDS.
//
// CoverageLookbackDays must be the lookbackDays given to the coverage fetch
// (GetRICoverageMap/GetEC2RICoverageMap). Coverage is a lookback average, so an
// RI bought inside that window is only partly in it. Active, non-expiring RIs
// in the same region and instance type that started inside the window are
// credited against the expiring count of that pool. A value <= 0 credits none
// (the coverage fetch itself defaults a non-positive lookback to 30, so pass
// 30 here in that case).
//
// The rule is conservative: it can under-buy, never over-buy. The credit is
// taken in full although lagged coverage already holds part of the new RI. A
// growth purchase in the same pool is indistinguishable from a replacement. A
// replacement of a different size in the same family is not credited.
type PoolExpiryOptions struct {
	WindowDays           int
	CoverageLookbackDays int
}

// AdjustExistingCoverageForExpiringCommitmentsWithCoverageOptions deducts RIs
// expiring inside opts.WindowDays from the existing coverage of each
// recommendation's pool, net of replacements bought inside
// opts.CoverageLookbackDays: per pool the deduction is max(0, expiring -
// replacements). A pool whose expiring RIs are fully replaced is left
// untouched and is not counted as missingDemand.
//
// The rule is conservative: it can under-buy, never over-buy. A growth
// purchase in the same pool is indistinguishable from a replacement, and a
// replacement of a different size in the same family is not credited. See
// PoolExpiryOptions for the lookback contract.
func AdjustExistingCoverageForExpiringCommitmentsWithCoverageOptions(
	recs []common.Recommendation,
	commitments []common.Commitment,
	coverage PoolCoverageMap,
	opts PoolExpiryOptions,
) (adjusted, missingDemand int) {
	demand := make(map[string]float64, len(coverage))
	for key, cov := range coverage {
		demand[key] = cov.AvgInstancesPerHour
	}
	return adjustExpiringCoverage(recs, commitments, opts, demand)
}

func adjustExpiringCoverage(
	recs []common.Recommendation,
	commitments []common.Commitment,
	opts PoolExpiryOptions,
	demand map[string]float64,
) (adjusted, missingDemand int) {
	if opts.WindowDays <= 0 || len(commitments) == 0 {
		return 0, 0
	}
	now := time.Now()
	cutoff := now.Add(time.Duration(opts.WindowDays) * 24 * time.Hour)
	lagStart := now.Add(-time.Duration(opts.CoverageLookbackDays) * 24 * time.Hour)
	expiringByPool, replacedByPool := tallyPoolCommitments(commitments, cutoff, lagStart, opts.CoverageLookbackDays > 0)
	if len(expiringByPool) == 0 {
		return 0, 0
	}
	return applyExpiringAdjustments(recs, netExpiringByPool(expiringByPool, replacedByPool), demand)
}

// tallyPoolCommitments sums active commitments per pool: those expiring
// at-or-before cutoff, and (when credit is set) the non-expiring ones that
// started after lagStart, which lagged coverage does not fully reflect yet.
// The State filter mirrors DuplicateChecker.filterRecentCommitments, so
// queued / retired RIs, which are not covering demand now, are ignored.
// The credit test mirrors tallyFamilyCommitments in family_nu.go:511-512.
func tallyPoolCommitments(
	commitments []common.Commitment, cutoff, lagStart time.Time, credit bool,
) (expiring, replaced map[string]int) {
	expiring = make(map[string]int)
	replaced = make(map[string]int)
	for i := range commitments {
		c := &commitments[i]
		if !commitmentIsActive(*c) {
			continue
		}
		key := commitmentPoolKey(*c)
		if key == "" {
			continue
		}
		if !c.EndDate.IsZero() && !c.EndDate.After(cutoff) {
			expiring[key] += c.Count
		} else if credit && c.StartDate.After(lagStart) {
			replaced[key] += c.Count
		}
	}
	return expiring, replaced
}

// netExpiringByPool returns max(0, expiring - replaced) for every pool with
// expiring commitments; replacements never push coverage past the lagged value.
func netExpiringByPool(expiring, replaced map[string]int) map[string]int {
	out := make(map[string]int, len(expiring))
	for key, e := range expiring {
		out[key] = max(0, e-replaced[key])
	}
	return out
}

func applyExpiringAdjustments(recs []common.Recommendation, expiringByPool map[string]int, demand map[string]float64) (adjusted, missingDemand int) {
	for i := range recs {
		if recs[i].AverageInstancesUsedPerHour <= 0 {
			continue
		}
		key := lookupPoolKey(recs[i])
		expCount := expiringByPool[key]
		if expCount == 0 {
			continue
		}
		avg := demand[key]
		if avg <= 0 || math.IsNaN(avg) || math.IsInf(avg, 0) {
			missingDemand++
			continue
		}
		adjustCoverageForExpiry(&recs[i], expCount, avg)
		adjusted++
	}
	return adjusted, missingDemand
}

func adjustCoverageForExpiry(rec *common.Recommendation, count int, demand float64) {
	if math.IsNaN(rec.ExistingCoveragePct) || math.IsInf(rec.ExistingCoveragePct, 0) {
		rec.ExistingCoveragePercentExact = nil
		expiringPct := float64(count) / demand * 100
		if expiringPct > rec.ExistingCoveragePct {
			rec.ExistingCoveragePct = 0
		} else {
			rec.ExistingCoveragePct -= expiringPct
		}
		return
	}
	remaining := new(big.Rat).SetFloat64(rec.ExistingCoveragePct)
	if prior := rec.ExistingCoveragePercentExact; prior != nil {
		if displayed, _ := prior.Float64(); displayed == rec.ExistingCoveragePct {
			remaining.Set(prior)
		}
	}
	expiring := new(big.Rat).SetInt64(int64(count))
	expiring.Mul(expiring, big.NewRat(100, 1))
	expiring.Quo(expiring, new(big.Rat).SetFloat64(demand))
	remaining.Sub(remaining, expiring)
	if remaining.Sign() < 0 {
		remaining.SetInt64(0)
	}
	rec.ExistingCoveragePercentExact = remaining
	rec.ExistingCoveragePct, _ = remaining.Float64()
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
//
// Non-RDS services share the region:type key with no service dimension. That
// is safe only because the ResourceType shapes are disjoint (EC2 m5.large,
// ElastiCache cache.m5.large, OpenSearch m5.large.search, Redshift ra3.*,
// MemoryDB db.*); TestPoolKeys_* in expiry_poolkey_test.go guards it. Engine
// is not part of the non-RDS key, see go#475.
func commitmentPoolKey(c common.Commitment) string {
	if c.Service == common.ServiceRDS || c.Service == common.ServiceRelationalDB {
		return rdsPoolKey(c.Region, c.ResourceType, c.Engine, c.Deployment)
	}
	return poolKey(c.Region, c.ResourceType)
}
