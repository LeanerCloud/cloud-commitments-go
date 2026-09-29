package recfilter

import (
	"context"
	"fmt"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/provider"
)

// DefaultDuplicateCheckLookbackHours is the default lookback period for checking recent purchases.
const DefaultDuplicateCheckLookbackHours = 24

type recentCommitmentFilter interface {
	FilterRecommendationsForRecentCommitments(recs []common.Recommendation, existing []common.Commitment) (passed, filtered []common.Recommendation, err error)
}

// DuplicateChecker checks for existing commitments to avoid duplicates.
type DuplicateChecker struct {
	LookbackHours int // How many hours to look back for recent purchases

	// Logf receives the per-commitment decision trail. Nil is silent.
	// recfilter never logs through a package-level logger: cmd's AppLogger
	// writes to stdout, which the MCP server owns as its protocol transport.
	Logf Logf
}

// NewDuplicateChecker creates a new duplicate checker. Pass 0 to use the default lookback period.
func NewDuplicateChecker(hours int) *DuplicateChecker {
	if hours <= 0 {
		hours = DefaultDuplicateCheckLookbackHours
	}
	return &DuplicateChecker{
		LookbackHours: hours,
	}
}

// AdjustRecommendationsForExisting adjusts recommendations based on existing commitments
// This checks for recently purchased RIs (within LookbackHours) to avoid duplicate purchases.
// Note: This is designed to prevent re-purchasing something you just bought, not to prevent
// purchasing RIs in other accounts that happen to have the same characteristics.
func (d *DuplicateChecker) AdjustRecommendationsForExisting(ctx context.Context, recs []common.Recommendation, client provider.ServiceClient) (passed, filtered []common.Recommendation, err error) {
	existing, err := client.GetExistingCommitments(ctx)
	if err != nil {
		return recs, nil, err
	}

	d.Logf.printf("    [DuplicateChecker] Found %d total existing commitments", len(existing))

	recentExisting := d.filterRecentCommitments(existing)
	d.Logf.printf("    [DuplicateChecker] Found %d recent commitments (purchased in last %d hours)", len(recentExisting), d.LookbackHours)

	if len(recentExisting) == 0 {
		return recs, nil, nil
	}

	if filter, ok := client.(recentCommitmentFilter); ok {
		passed, filtered, err = filter.FilterRecommendationsForRecentCommitments(recs, recentExisting)
		d.Logf.printf("    [DuplicateChecker] Provider filtered %d recommendations against recent commitments", len(filtered))
		return passed, filtered, err
	}

	existingMap := buildExistingCommitmentsMap(recentExisting, d.Logf)
	d.Logf.printf("    [DuplicateChecker] Existing map has %d unique keys", len(existingMap))

	passed, filtered = adjustRecommendationsAgainstExisting(recs, existingMap, d.Logf)

	if len(filtered) > 0 {
		d.Logf.printf("    [DuplicateChecker] Result: %d recommendations kept out of %d (avoided %d duplicates)",
			len(passed), len(recs), len(filtered))
	}
	return passed, filtered, nil
}

// filterRecentCommitments filters commitments to only recent purchases within the lookback window.
func (d *DuplicateChecker) filterRecentCommitments(existing []common.Commitment) []common.Commitment {
	cutoffTime := time.Now().Add(-time.Duration(d.LookbackHours) * time.Hour)
	recentExisting := make([]common.Commitment, 0)

	for _rvc := range existing {
		c := existing[_rvc]
		if isRecentActiveCommitment(c, cutoffTime, d.Logf) {
			recentExisting = append(recentExisting, c)
		}
	}

	return recentExisting
}

// isRecentActiveCommitment checks if a commitment is owned (active, paying, or
// queued for a future start) and starts after the cutoff time. An unrecognized
// state counts as owned: wrongly skipping a purchase is recoverable, a
// duplicate commitment is not.
func isRecentActiveCommitment(c common.Commitment, cutoffTime time.Time, logf Logf) bool {
	switch c.State {
	case common.CommitmentStateActive, common.CommitmentStatePaymentPending, common.CommitmentStateQueued:
	case common.CommitmentStateRetired, common.CommitmentStatePendingReturn, common.CommitmentStateExpired,
		common.CommitmentStateCanceled, common.CommitmentStateFailed:
		return false
	default:
		logf.printf("    [DuplicateChecker] WARNING: commitment %s has unrecognized state %q; counting it as existing",
			c.CommitmentID, c.State)
	}
	return c.StartDate.After(cutoffTime)
}

// dedupeKey builds the duplicate-identity key shared by
// buildExistingCommitmentsMap and adjustSingleRecommendation. Both call
// sites MUST build the key through this function: a Single-AZ and a
// Multi-AZ RDS commitment/recommendation are priced and provisioned
// differently and do not cover each other's demand, so deployment has to
// be part of the identity or the two keys can drift and silently
// suppress (or fail to suppress) the wrong recommendation.
func dedupeKey(resourceType, region, engine, deployment string) string {
	return fmt.Sprintf("%s|%s|%s|%s", resourceType, region, engine, deployment)
}

const unknownElastiCacheEngine = "elasticache:*"

func dedupeEngine(providerType common.ProviderType, service common.ServiceType, engine string) (string, bool) {
	engine = common.NormalizeEngineName(engine)
	if providerType != common.ProviderAWS || (service != common.ServiceCache && service != common.ServiceElastiCache) {
		return engine, false
	}
	switch engine {
	case "":
		return unknownElastiCacheEngine, true
	case "valkey":
		engine = "redis"
	}
	return "elasticache:" + engine, true
}

// buildExistingCommitmentsMap builds a map of commitments by resource type, region, engine, and deployment.
func buildExistingCommitmentsMap(commitments []common.Commitment, logf Logf) map[string]int {
	existingMap := make(map[string]int)

	for _rvc := range commitments {
		c := commitments[_rvc]
		normalizedEngine, _ := dedupeEngine(c.Provider, c.Service, c.Engine)
		normalizedDeployment := common.NormalizeDeploymentName(c.Deployment)
		key := dedupeKey(c.ResourceType, c.Region, normalizedEngine, normalizedDeployment)
		existingMap[key] += c.Count
		logf.printf("    [DuplicateChecker] Recent RI: key=%s count=%d startDate=%s (raw engine=%s)",
			key, c.Count, c.StartDate.Format("2006-01-02 15:04:05"), c.Engine)
	}

	return existingMap
}

// adjustRecommendationsAgainstExisting adjusts recommendations based on existing commitments.
// Returns (passed, filtered) where filtered contains recs whose count was reduced to zero.
func adjustRecommendationsAgainstExisting(recs []common.Recommendation, existingMap map[string]int, logf Logf) (passed, filtered []common.Recommendation) {
	passed = make([]common.Recommendation, 0, len(recs))
	filtered = make([]common.Recommendation, 0)

	for _rvc := range recs {
		rec := recs[_rvc]
		adjusted := adjustSingleRecommendation(rec, existingMap, logf)
		if adjusted.Count > 0 {
			passed = append(passed, adjusted)
		} else {
			filtered = append(filtered, rec)
		}
	}

	return passed, filtered
}

// adjustSingleRecommendation adjusts a single recommendation based on existing commitments.
func adjustSingleRecommendation(rec common.Recommendation, existingMap map[string]int, logf Logf) common.Recommendation {
	if rec.Count <= 0 {
		return common.Recommendation{Count: 0}
	}
	engine, isElastiCache := dedupeEngine(rec.Provider, rec.Service, common.EngineFromDetails(rec.Details))
	deployment := common.NormalizeDeploymentName(common.DeploymentFromDetails(rec.Details))
	key := dedupeKey(rec.ResourceType, rec.Region, engine, deployment)
	keys := []string{key}
	if isElastiCache && engine != unknownElastiCacheEngine {
		keys = append(keys, dedupeKey(rec.ResourceType, rec.Region, unknownElastiCacheEngine, deployment))
	}
	remaining := rec.Count
	for _, candidate := range keys {
		if available := existingMap[candidate]; available > 0 {
			used := min(available, remaining)
			remaining -= used
			existingMap[candidate] -= used
		}
	}

	if remaining <= 0 {
		logf.printf("    [DuplicateChecker] SKIP %s: recent commitments cover recommended %d", key, rec.Count)
		return common.Recommendation{Count: 0}
	}

	adjusted := rec
	if remaining < rec.Count {
		adjusted.Count = remaining
		logf.printf("    [DuplicateChecker] PARTIAL %s: adjusted count from %d to %d", key, rec.Count, adjusted.Count)
	}

	return adjusted
}

// AdjustRecommendationsForExistingRIs is an alias for AdjustRecommendationsForExisting.
func (d *DuplicateChecker) AdjustRecommendationsForExistingRIs(ctx context.Context, recs []common.Recommendation, client provider.ServiceClient) (passed, filtered []common.Recommendation, err error) {
	return d.AdjustRecommendationsForExisting(ctx, recs, client)
}
