package recommendations

import "github.com/LeanerCloud/cloud-commitments-go/pkg/common"

// RederiveFamilyCoverage recomputes the family-wide ProjectedCoverage that
// ApplyFamilyNUSizingRDS wrote, for recs whose Count changed after sizing
// (--max-instances, --count-override, duplicate-RI dedupe). It returns a new
// slice; recs is not modified.
//
// Only size-flexible RDS RI recs that family-NU sizing annotated
// (ExistingCoverageKnown) and whose family has demand in coverage are touched.
// Their ProjectedCoverage becomes existing% (carried on the recs, so any
// rebuy-window expiry adjustment is preserved) plus the family's current
// sum(Count x NU) over the recs supplied, relative to the family's total NU
// from coverage. Recs the caller removed no longer contribute. Everything
// else (non-RDS, Savings Plans, SQL Server, unknown sizes, families with no
// coverage signal) is returned unchanged.
//
// Call it only on recs that went through family-NU sizing; coverage must be
// the same PoolCoverageMap that was passed to sizing.
func RederiveFamilyCoverage(recs []common.Recommendation, coverage PoolCoverageMap) []common.Recommendation {
	out := make([]common.Recommendation, len(recs))
	copy(out, recs)
	familyIdx, _ := partitionRDSRecsByFamily(out)
	if len(familyIdx) == 0 {
		return out
	}
	familyCov := AggregateRDSFamilyCoverage(coverage)
	for fk, indices := range familyIdx {
		total := familyCov[fk].TotalNU
		if total <= 0 {
			continue
		}
		newNU := 0.0
		existingPct, known := 0.0, false
		for _, i := range indices {
			newNU += float64(out[i].Count) * rdsInstanceNUFromType(out[i].ResourceType)
			if out[i].ExistingCoverageKnown {
				existingPct, known = out[i].ExistingCoveragePct, true
			}
		}
		if !known {
			continue
		}
		proj := familyProjectedCoverage(existingPct, newNU, total)
		for _, i := range indices {
			if out[i].ExistingCoverageKnown {
				out[i].ProjectedCoverage = proj
			}
		}
	}
	return out
}
