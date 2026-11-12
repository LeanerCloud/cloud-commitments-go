package recommendations

import (
	"context"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/costexplorer"
	"github.com/aws/aws-sdk-go-v2/service/costexplorer/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
)

// poolKeyShapes holds, per service, the ResourceType the real producers emit
// for the same underlying size. Non-RDS expiry keys carry the service name, so
// these shapes must stay disjoint per service (see nonRDSExpiryPool).
//
//	EC2         services/ec2/client.go:111          string(ri.InstanceType)      m5.large
//	ElastiCache services/elasticache/client.go:106  aws.ToString(CacheNodeType)  cache.m5.large + engine
//	OpenSearch  services/opensearch/client.go:115   string(ri.InstanceType)      m5.large.search
//	Redshift    services/redshift/client.go:119     aws.ToString(NodeType)       ra3.xlplus
//	MemoryDB    services/memorydb/client.go:95      aws.ToString(NodeType)       db.r6g.large
//	RDS         own 4-part rdsPoolKey (coverage.go) db.r6g.large + engine + deployment
var poolKeyShapes = []struct {
	name         string
	service      common.ServiceType
	resourceType string
	engine       string
	deployment   string
}{
	{"ec2", common.ServiceEC2, "m5.large", "", ""},
	{"elasticache", common.ServiceCache, "cache.m5.large", "redis", ""},
	{"opensearch", common.ServiceOpenSearch, "m5.large.search", "", ""},
	{"redshift", common.ServiceRedshift, "ra3.xlplus", "", ""},
	{"memorydb", common.ServiceMemoryDB, "db.r6g.large", "", ""},
	{"rds", common.ServiceRDS, "db.r6g.large", "mysql", "single-az"},
}

func shapeCommitment(region string, i int) common.Commitment {
	s := poolKeyShapes[i]
	return common.Commitment{
		Service: s.service, Region: region, ResourceType: s.resourceType,
		Engine: s.engine, Deployment: s.deployment,
	}
}

func shapeRec(region string, i int) common.Recommendation {
	s := poolKeyShapes[i]
	rec := common.Recommendation{Service: s.service, Region: region, ResourceType: s.resourceType}
	switch {
	case isElastiCacheService(s.service):
		rec.Details = &common.CacheDetails{Engine: s.engine}
	case s.engine != "":
		rec.Details = &common.DatabaseDetails{Engine: s.engine, AZConfig: s.deployment}
	}
	return rec
}

// cacheCoverageGroup is one CE coverage group for an ElastiCache node type and
// engine, with 7200 running hours (avg 10 instances over a 30-day window).
func cacheCoverageGroup(nodeType, engine, pct string) types.ReservationCoverageGroup {
	return types.ReservationCoverageGroup{
		Attributes: map[string]string{"instanceType": nodeType, "cacheEngine": engine},
		Coverage: &types.Coverage{CoverageHours: &types.CoverageHours{
			CoverageHoursPercentage: aws.String(pct), TotalRunningHours: aws.String("7200"),
		}},
	}
}

// TestElastiCacheExpiryDoesNotNetAcrossEngines is the issue #475 regression: an
// expiring Redis reservation must not reduce the Memcached coverage of the same
// node type in the same region. Coverage comes from the CE fetch (grouped by
// CACHE_ENGINE), recs from the parsed CacheDetails, and the expiring count from
// the commitment; all three must key the pool by engine.
func TestElastiCacheExpiryDoesNotNetAcrossEngines(t *testing.T) {
	t.Parallel()
	mock := &serviceRoutedCoverageCE{byService: map[string]*costexplorer.GetReservationCoverageOutput{
		cacheCoverageService: cacheCoverageOutput(
			cacheCoverageGroup("cache.m5.large", "Redis", "80.0"),
			cacheCoverageGroup("cache.m5.large", "Memcached", "40.0"),
		),
	}}
	coverage, err := NewClientWithAPI(mock, "us-east-1").GetRICoverageMap(context.Background(), 30, []string{"us-east-1"})
	require.NoError(t, err)

	var cacheGroupBy []string
	for _, input := range mock.inputs {
		if input.Filter.And[0].Dimensions.Values[0] == cacheCoverageService {
			for _, g := range input.GroupBy {
				cacheGroupBy = append(cacheGroupBy, aws.ToString(g.Key))
			}
		}
	}
	require.Equal(t, []string{"INSTANCE_TYPE", "CACHE_ENGINE"}, cacheGroupBy)

	recs := []common.Recommendation{
		{Service: common.ServiceElastiCache, Region: "us-east-1", ResourceType: "cache.m5.large", Details: &common.CacheDetails{Engine: "Redis"}, Count: 1, RecommendedCount: 1, AverageInstancesUsedPerHour: 10, CommitmentType: common.CommitmentReservedInstance},
		{Service: common.ServiceElastiCache, Region: "us-east-1", ResourceType: "cache.m5.large", Details: &common.CacheDetails{Engine: "Memcached"}, Count: 1, RecommendedCount: 1, AverageInstancesUsedPerHour: 10, CommitmentType: common.CommitmentReservedInstance},
	}
	commits := []common.Commitment{{
		Service: common.ServiceCache, Region: "us-east-1", ResourceType: "cache.m5.large", Engine: "redis",
		Count: 2, State: "active", EndDate: time.Now().Add(15 * 24 * time.Hour),
	}}

	ApplyCoverageMapToRecommendations(recs, coverage)
	adjusted, missing := mustAdjustCoverage(AdjustExistingCoverageForExpiringCommitmentsWithCoverage(recs, commits, 30, coverage))

	assert.Equal(t, 1, adjusted, "only the Redis pool has an expiring RI")
	assert.Zero(t, missing)
	assert.InDelta(t, 60, recs[0].ExistingCoveragePct, 1e-9, "Redis: 80 percent minus 2 of 10 expiring instances")
	assert.InDelta(t, 40, recs[1].ExistingCoveragePct, 1e-9, "Memcached must keep its own 40 percent coverage")
}

// TestMemoryDBRecKeepsPlainPoolKey guards the service branch in lookupPoolKey:
// MemoryDB recs carry CacheDetails{Engine: "redis"} but must keep the
// region:type key their commitments and CE coverage use.
func TestMemoryDBRecKeepsPlainPoolKey(t *testing.T) {
	t.Parallel()
	rec := common.Recommendation{Service: common.ServiceMemoryDB, Region: "us-east-1", ResourceType: "db.r6g.large",
		Details: &common.CacheDetails{Engine: "redis", NodeType: "db.r6g.large"}}
	assert.Equal(t, poolKey("us-east-1", "db.r6g.large"), lookupPoolKey(rec))
}

func cacheCoverageOutput(groups ...types.ReservationCoverageGroup) *costexplorer.GetReservationCoverageOutput {
	return &costexplorer.GetReservationCoverageOutput{CoveragesByTime: []types.CoverageByTime{{Groups: groups}}}
}

func TestPoolKeys_DisjointAcrossServices(t *testing.T) {
	t.Parallel()
	const region = "us-east-1"
	seen := map[string]string{}
	for i, s := range poolKeyShapes {
		pool, err := commitmentExpiryPool(shapeCommitment(region, i))
		require.NoError(t, err, s.name)
		if prev, dup := seen[pool.key]; dup {
			t.Errorf("expiry key %q collides: %s and %s", pool.key, prev, s.name)
		}
		seen[pool.key] = s.name
	}
	assert.Len(t, seen, len(poolKeyShapes))
}

func TestPoolKeys_CommitmentAndRecordAgree(t *testing.T) {
	t.Parallel()
	const region = "eu-west-1"
	lookups := map[string]string{}
	for i, s := range poolKeyShapes {
		ck, err := commitmentExpiryPool(shapeCommitment(region, i))
		require.NoError(t, err, s.name)
		rk, err := recExpiryPool(shapeRec(region, i))
		require.NoError(t, err, s.name)
		assert.Equal(t, ck, rk, "%s: commitment and recommendation expiry pools must agree", s.name)
		lk := lookupPoolKey(shapeRec(region, i))
		if prev, dup := lookups[lk]; dup {
			t.Errorf("lookup key %q collides: %s and %s", lk, prev, s.name)
		}
		lookups[lk] = s.name
	}
}
