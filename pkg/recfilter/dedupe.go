package recfilter

import (
	"context"
	"fmt"
	"strings"
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

const (
	elastiCacheEnginePrefix    = "elasticache:"
	unknownElastiCacheEngine   = elastiCacheEnginePrefix + "*"
	elastiCacheRedisEngine     = elastiCacheEnginePrefix + "redis"
	elastiCacheValkeyEngine    = elastiCacheEnginePrefix + "valkey"
	elastiCacheMemcachedEngine = elastiCacheEnginePrefix + "memcached"
)

func dedupeEngine(providerType common.ProviderType, service common.ServiceType, engine string) (string, bool) {
	engine = common.NormalizeEngineName(engine)
	if providerType != common.ProviderAWS || (service != common.ServiceCache && service != common.ServiceElastiCache) {
		return engine, false
	}
	if engine == "" {
		return unknownElastiCacheEngine, true
	}
	return elastiCacheEnginePrefix + engine, true
}

// elastiCacheCover is a reservation engine that covers a node, with the nodes of
// the covered engine one reserved node pays for as the ratio nodes/reserved.
type elastiCacheCover struct {
	engine          string
	nodes, reserved int
}

// valkeyPerRedisNodes and redisPerValkeyNodes are the ratio of the normalized units of
// a Redis OSS node (4 for large) to a Valkey node (3.2 for large), which is the same
// 5:4 for every node size: https://docs.aws.amazon.com/AmazonElastiCache/latest/dg/CacheNodes.Reserved.html#reserved-nodes-size.normalized
const (
	valkeyPerRedisNodes = 5
	redisPerValkeyNodes = 4
)

// coveringElastiCacheEngines lists the reservation engines that cover a node of engine, in consumption order.
// Redis OSS reservations also cover Valkey nodes, never the reverse, and each Redis OSS node covers 1.25 Valkey nodes:
// https://docs.aws.amazon.com/AmazonElastiCache/latest/dg/CacheNodes.Reserved.html#reserved-nodes-upgrade-to-valkey
func coveringElastiCacheEngines(engine string) []elastiCacheCover {
	switch engine {
	case unknownElastiCacheEngine:
		return []elastiCacheCover{{unknownElastiCacheEngine, 1, 1}}
	case elastiCacheValkeyEngine:
		return []elastiCacheCover{
			{elastiCacheValkeyEngine, 1, 1},
			{elastiCacheRedisEngine, valkeyPerRedisNodes, redisPerValkeyNodes},
			{unknownElastiCacheEngine, 1, 1},
		}
	default:
		return []elastiCacheCover{{engine, 1, 1}, {unknownElastiCacheEngine, 1, 1}}
	}
}

// consume takes up to want nodes from the available reserved nodes and returns how many
// nodes were covered and how many reserved nodes that used. Both directions round toward
// less coverage: the covered nodes round down, the reserved nodes spent round up, so a
// fractional remainder is never credited and a needed purchase is never skipped.
func (c elastiCacheCover) consume(available, want int) (covered, spent int) {
	covered = min(available*c.nodes/c.reserved, want)
	spent = (covered*c.reserved + c.nodes - 1) / c.nodes
	return covered, spent
}

// warnUnrecognizedElastiCacheEngine logs an engine that is neither redis, valkey, memcached
// nor the empty wildcard: it gets a budget of its own that covers and is covered by nothing else.
func warnUnrecognizedElastiCacheEngine(engine string, logf Logf) {
	switch engine {
	case unknownElastiCacheEngine, elastiCacheRedisEngine, elastiCacheValkeyEngine, elastiCacheMemcachedEngine:
		return
	}
	logf.printf("    [DuplicateChecker] WARNING: unrecognized ElastiCache engine %q; it is deduplicated only against itself",
		strings.TrimPrefix(engine, elastiCacheEnginePrefix))
}

// buildExistingCommitmentsMap builds a map of commitments by resource type, region, engine, and deployment.
func buildExistingCommitmentsMap(commitments []common.Commitment, logf Logf) map[string]int {
	existingMap := make(map[string]int)

	for _rvc := range commitments {
		c := commitments[_rvc]
		normalizedEngine, isElastiCache := dedupeEngine(c.Provider, c.Service, c.Engine)
		if isElastiCache {
			warnUnrecognizedElastiCacheEngine(normalizedEngine, logf)
		}
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
	covers := []elastiCacheCover{{engine, 1, 1}}
	if isElastiCache {
		covers = coveringElastiCacheEngines(engine)
		warnUnrecognizedElastiCacheEngine(engine, logf)
	}
	remaining := rec.Count
	for _, cover := range covers {
		candidate := dedupeKey(rec.ResourceType, rec.Region, cover.engine, deployment)
		if available := existingMap[candidate]; available > 0 {
			covered, spent := cover.consume(available, remaining)
			remaining -= covered
			existingMap[candidate] -= spent
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
