package recommendations

import (
	"fmt"
	"math"
	"math/big"
	"strings"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
)

// AdjustExistingCoverageForExpiringCommitments returns the number of adjusted recommendations.
// Demand must be complete disjoint pool shares, or freshly rebalanced by ApplyCoverageMapToRecommendations.
//
// Partial raw slices and duplicated aggregate averages cannot be detected here.
// Callers with authoritative pool demand should use AdjustExistingCoverageForExpiringCommitmentsWithCoverage.
// It returns an error, and adjusts nothing, when a commitment or recommendation has an unknown dimension.
func AdjustExistingCoverageForExpiringCommitments(
	recs []common.Recommendation,
	commitments []common.Commitment,
	windowDays int,
) (int, error) {
	demand := make(map[string]float64)
	for i := range recs {
		avg := recs[i].AverageInstancesUsedPerHour
		if avg > 0 && !math.IsNaN(avg) && !math.IsInf(avg, 0) {
			demand[lookupPoolKey(recs[i])] += avg
		}
	}
	adjusted, _, err := adjustExpiringCoverage(recs, commitments, PoolExpiryOptions{WindowDays: windowDays}, demand)
	return adjusted, err
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
) (adjusted, missingDemand int, err error) {
	return AdjustExistingCoverageForExpiringCommitmentsWithCoverageOptions(
		recs, commitments, coverage, PoolExpiryOptions{WindowDays: windowDays})
}

// PoolExpiryOptions configures the per-pool (EC2 and other non-family) expiry
// deduction. It mirrors FamilyExpiryOptions, which does the same for RDS.
//
// CoverageLookbackDays must be the lookbackDays given to the coverage fetch
// (GetRICoverageMap/GetEC2RICoverageMap). Coverage is a lookback average, so an
// RI bought inside that window is only partly in it. Active, non-expiring RIs
// in the same pool that started inside the window are credited against the
// expiring count of that pool. A value <= 0 credits none (the coverage fetch
// itself defaults a non-positive lookback to 30, so pass 30 here in that case).
//
// The rule is conservative: it can under-buy, never over-buy. The credit is
// taken in full although lagged coverage already holds part of the new RI. A
// growth purchase in the same pool is indistinguishable from a replacement.
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
// Size-flexible EC2 families net in normalized units (see expiryPool), so a
// replacement of another size in the same family is credited. The rule is
// conservative: it can under-buy, never over-buy. A growth purchase in the same
// pool is indistinguishable from a replacement. See PoolExpiryOptions for the
// lookback contract. It returns an error, and adjusts nothing, when a
// commitment or recommendation has an unknown service or EC2 size.
func AdjustExistingCoverageForExpiringCommitmentsWithCoverageOptions(
	recs []common.Recommendation,
	commitments []common.Commitment,
	coverage PoolCoverageMap,
	opts PoolExpiryOptions,
) (adjusted, missingDemand int, err error) {
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
) (adjusted, missingDemand int, err error) {
	if opts.WindowDays <= 0 || len(commitments) == 0 {
		return 0, 0, nil
	}
	now := time.Now()
	cutoff := now.Add(time.Duration(opts.WindowDays) * 24 * time.Hour)
	lagStart := now.Add(-time.Duration(opts.CoverageLookbackDays) * 24 * time.Hour)
	expiringByPool, replacedByPool, err := tallyPoolCommitments(commitments, cutoff, lagStart, opts.CoverageLookbackDays > 0)
	if err != nil {
		return 0, 0, err
	}
	if len(expiringByPool) == 0 {
		return 0, 0, nil
	}
	return applyExpiringAdjustments(recs, netExpiringByPool(expiringByPool, replacedByPool), demand)
}

// tallyPoolCommitments sums active commitments per pool, in normalized units:
// those expiring at-or-before cutoff, and (when credit is set) the
// non-expiring ones that started after lagStart, which lagged coverage does
// not fully reflect yet. The State filter mirrors
// DuplicateChecker.filterRecentCommitments, so queued / retired RIs, which are
// not covering demand now, are ignored. Savings Plans are not pool-keyed and
// are skipped. The credit test mirrors tallyFamilyCommitments in
// family_nu.go:511-512.
func tallyPoolCommitments(
	commitments []common.Commitment, cutoff, lagStart time.Time, credit bool,
) (expiring, replaced map[string]float64, err error) {
	expiring = make(map[string]float64)
	replaced = make(map[string]float64)
	for i := range commitments {
		c := &commitments[i]
		if !commitmentIsActive(*c) || c.CommitmentType == common.CommitmentSavingsPlan {
			continue
		}
		expires := !c.EndDate.IsZero() && !c.EndDate.After(cutoff)
		if !expires && (!credit || !c.StartDate.After(lagStart)) {
			continue
		}
		pool, poolErr := commitmentExpiryPool(*c)
		if poolErr != nil {
			return nil, nil, poolErr
		}
		if expires {
			expiring[pool.key] += float64(c.Count) * pool.units
		} else {
			replaced[pool.key] += float64(c.Count) * pool.units
		}
	}
	return expiring, replaced, nil
}

// netExpiringByPool returns max(0, expiring - replaced) in normalized units for
// every pool with expiring commitments; replacements never push coverage past
// the lagged value.
func netExpiringByPool(expiring, replaced map[string]float64) map[string]float64 {
	out := make(map[string]float64, len(expiring))
	for key, e := range expiring {
		out[key] = max(0, e-replaced[key])
	}
	return out
}

// applyExpiringAdjustments validates every eligible recommendation before it
// mutates any, so an error leaves recs untouched.
func applyExpiringAdjustments(recs []common.Recommendation, netByPool, demand map[string]float64) (adjusted, missingDemand int, err error) {
	pools := make([]expiryPool, len(recs))
	for i := range recs {
		if recs[i].AverageInstancesUsedPerHour <= 0 {
			continue
		}
		if pools[i], err = recExpiryPool(recs[i]); err != nil {
			return 0, 0, err
		}
	}
	for i := range recs {
		if recs[i].AverageInstancesUsedPerHour <= 0 {
			continue
		}
		net := netByPool[pools[i].key]
		if net <= 0 {
			continue
		}
		key := lookupPoolKey(recs[i])
		avg := demand[key]
		if avg <= 0 || math.IsNaN(avg) || math.IsInf(avg, 0) {
			missingDemand++
			continue
		}
		adjustCoverageForExpiry(&recs[i], net/pools[i].units, avg)
		adjusted++
	}
	return adjusted, missingDemand, nil
}

func adjustCoverageForExpiry(rec *common.Recommendation, count, demand float64) {
	if math.IsNaN(rec.ExistingCoveragePct) || math.IsInf(rec.ExistingCoveragePct, 0) {
		rec.ExistingCoveragePercentExact = nil
		expiringPct := count / demand * 100
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
	expiring := new(big.Rat).SetFloat64(count)
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

// expiryPool is the bucket an expiring commitment or a recommendation nets in.
// units is the weight of one instance in that bucket. Sizes of a size-flexible
// EC2 family share one bucket and weigh their AWS normalization factor, so a
// large replaced by two mediums nets to zero; every other pool weighs 1.
type expiryPool struct {
	key   string
	units float64
}

// commitmentExpiryPool keys RDS by engine and deployment (rdsPoolKey), and every
// other service by its service name (nonRDSExpiryPool), so equal ResourceType
// strings in different services never net against each other.
func commitmentExpiryPool(c common.Commitment) (expiryPool, error) {
	if c.Service == common.ServiceRDS || c.Service == common.ServiceRelationalDB {
		return expiryPool{key: rdsPoolKey(c.Region, c.ResourceType, c.Engine, c.Deployment), units: 1}, nil
	}
	return nonRDSExpiryPool(c.Service, c.Region, c.ResourceType, c.Engine)
}

// recExpiryPool mirrors commitmentExpiryPool for a recommendation.
func recExpiryPool(rec common.Recommendation) (expiryPool, error) {
	if engine, deployment := rdsEngineDeploymentFromRec(rec); engine != "" {
		return expiryPool{key: rdsPoolKey(rec.Region, rec.ResourceType, engine, deployment), units: 1}, nil
	}
	return nonRDSExpiryPool(rec.Service, rec.Region, rec.ResourceType, cacheEngineFromRec(rec))
}

// nonRDSExpiryPool returns the bucket of a non-RDS commitment or recommendation.
// An ElastiCache engine is part of the key (issue #475). An EC2 type in a
// size-flexible family nets per region and family; EC2 types the family table
// excludes stay per exact type. An unknown service or an EC2 size without a
// normalization factor is an error, never a default weight.
func nonRDSExpiryPool(service common.ServiceType, region, resourceType, engine string) (expiryPool, error) {
	service = canonicalNonRDSService(service)
	switch service {
	case common.ServiceEC2:
		family, size, ok := strings.Cut(resourceType, ".")
		if !ok || family == "" {
			return expiryPool{}, fmt.Errorf("EC2 instance type %q has no family and size", resourceType)
		}
		if !common.EC2SizeFlexibleFamily(family) {
			return expiryPool{key: "ec2:" + poolKey(region, resourceType), units: 1}, nil
		}
		factor := common.EC2NormalizationFactor(strings.ToLower(size))
		if factor == 0 {
			return expiryPool{}, fmt.Errorf("EC2 instance type %q has no normalization factor for size-flexible netting", resourceType)
		}
		return expiryPool{key: "ec2:" + poolKey(region, family), units: factor}, nil
	case common.ServiceElastiCache:
		return expiryPool{key: "elasticache:" + elastiCachePoolKey(region, resourceType, engine), units: 1}, nil
	case common.ServiceOpenSearch, common.ServiceRedshift, common.ServiceMemoryDB:
		return expiryPool{key: string(service) + ":" + poolKey(region, resourceType), units: 1}, nil
	}
	return expiryPool{}, fmt.Errorf("expiry pool: unknown service %q for %q", service, resourceType)
}

// canonicalNonRDSService maps the aliases that producers emit for the same
// pool (the parser and the lister use different names) onto one service name.
func canonicalNonRDSService(service common.ServiceType) common.ServiceType {
	switch service {
	case common.ServiceCompute:
		return common.ServiceEC2
	case common.ServiceCache:
		return common.ServiceElastiCache
	case common.ServiceSearch:
		return common.ServiceOpenSearch
	case common.ServiceDataWarehouse:
		return common.ServiceRedshift
	}
	return service
}
