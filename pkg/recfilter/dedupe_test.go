package recfilter

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeServiceClient is a minimal provider.ServiceClient implementing only
// GetExistingCommitments, which is all AdjustRecommendationsForExisting uses.
type fakeServiceClient struct {
	commitments []common.Commitment
	err         error
}

func (f *fakeServiceClient) GetServiceType() common.ServiceType { return "" }
func (f *fakeServiceClient) GetRegion() string                  { return "" }
func (f *fakeServiceClient) GetRecommendations(ctx context.Context, params *common.RecommendationParams) ([]common.Recommendation, error) {
	return nil, nil
}
func (f *fakeServiceClient) GetExistingCommitments(ctx context.Context) ([]common.Commitment, error) {
	return f.commitments, f.err
}
func (f *fakeServiceClient) PurchaseCommitment(ctx context.Context, rec common.Recommendation, opts common.PurchaseOptions) (common.PurchaseResult, error) {
	return common.PurchaseResult{}, nil
}
func (f *fakeServiceClient) ValidateOffering(ctx context.Context, rec common.Recommendation) error {
	return nil
}
func (f *fakeServiceClient) GetOfferingDetails(ctx context.Context, rec common.Recommendation) (*common.OfferingDetails, error) {
	return nil, nil
}
func (f *fakeServiceClient) GetValidResourceTypes(ctx context.Context) ([]string, error) {
	return nil, nil
}

func TestElastiCacheEngineBudgets(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name       string
		engines    []string
		counts     []int
		recEngines []string
		recCounts  []int
		wantCounts []int
	}{
		{"redis covers valkey", []string{"ReDiS"}, []int{1}, []string{"VaLkEy"}, []int{1}, nil},
		// Redis OSS reservations cover Valkey nodes, Valkey reservations cover only Valkey:
		// https://docs.aws.amazon.com/AmazonElastiCache/latest/dg/CacheNodes.Reserved.html#reserved-nodes-upgrade-to-valkey
		{"valkey does not cover redis", []string{"valkey"}, []int{1}, []string{"redis"}, []int{1}, []int{1}},
		{"valkey covers valkey", []string{"valkey"}, []int{1}, []string{"valkey"}, []int{1}, nil},
		{"valkey and wildcard partial redis", []string{"valkey", ""}, []int{1, 1}, []string{"redis"}, []int{2}, []int{1}},
		{"valkey before redis for valkey", []string{"valkey", "redis"}, []int{1, 1}, []string{"valkey", "redis"}, []int{1, 1}, nil},
		{"redis rec first keeps valkey for valkey", []string{"valkey", "redis"}, []int{1, 1}, []string{"redis", "valkey"}, []int{1, 1}, nil},
		{"valkey overflow onto redis", []string{"valkey", "redis"}, []int{1, 1}, []string{"valkey", "redis"}, []int{2, 1}, []int{1}},
		{"redis not memcached", []string{"redis"}, []int{1}, []string{"memcached"}, []int{1}, []int{1}},
		{"valkey not memcached", []string{"valkey"}, []int{1}, []string{"memcached"}, []int{1}, []int{1}},
		{"unknown covers redis", []string{""}, []int{1}, []string{"redis"}, []int{1}, nil},
		{"unknown covers valkey", []string{""}, []int{1}, []string{"valkey"}, []int{1}, nil},
		{"unknown covers memcached", []string{""}, []int{1}, []string{"memcached"}, []int{1}, nil},
		{"combined partial", []string{"redis", ""}, []int{1, 1}, []string{"valkey"}, []int{3}, []int{1}},
		{"combined full", []string{"redis", ""}, []int{1, 1}, []string{"valkey"}, []int{2}, nil},
		{"wildcard consumed once", []string{""}, []int{1}, []string{"redis", "memcached"}, []int{1, 1}, []int{1}},
		{"exact before wildcard", []string{"redis", ""}, []int{1, 1}, []string{"valkey", "memcached"}, []int{1, 1}, nil},
		{"family consumed once", []string{"redis"}, []int{1}, []string{"valkey", "redis"}, []int{1, 1}, []int{1}},
		{"empty rec one", []string{""}, []int{1}, []string{""}, []int{3}, []int{2}},
		{"empty rec two", []string{""}, []int{2}, []string{""}, []int{3}, []int{1}},
		{"empty rec repeated", []string{""}, []int{2}, []string{"", ""}, []int{1, 2}, []int{1}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			client := &fakeServiceClient{}
			for i, engine := range tt.engines {
				client.commitments = append(client.commitments, common.Commitment{
					Provider: common.ProviderAWS, Service: common.ServiceCache, ResourceType: "cache.r6g.large",
					Region: "us-east-1", Engine: engine, Count: tt.counts[i], State: common.CommitmentStateActive, StartDate: time.Now(),
				})
			}
			recs := make([]common.Recommendation, 0, len(tt.recEngines))
			for i, engine := range tt.recEngines {
				recs = append(recs, common.Recommendation{Provider: common.ProviderAWS, Service: common.ServiceElastiCache,
					ResourceType: "cache.r6g.large", Region: "us-east-1", Count: tt.recCounts[i], Details: &common.CacheDetails{Engine: engine}})
			}
			passed, filtered, err := NewDuplicateChecker(0).AdjustRecommendationsForExisting(context.Background(), recs, client)
			require.NoError(t, err)
			require.Len(t, passed, len(tt.wantCounts))
			for i, rec := range passed {
				assert.Equal(t, tt.wantCounts[i], rec.Count)
			}
			assert.Len(t, filtered, len(recs)-len(passed))
		})
	}
}

func TestElastiCacheEngineScope(t *testing.T) {
	t.Parallel()
	for _, engine := range []string{"", "redis"} {
		for _, other := range []struct {
			provider common.ProviderType
			service  common.ServiceType
		}{
			{common.ProviderAWS, common.ServiceMemoryDB}, {common.ProviderAWS, common.ServiceRDS},
			{common.ProviderAzure, common.ServiceCache},
		} {
			for _, reverse := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/%s/reverse=%t", engine, other.provider, other.service, reverse), func(t *testing.T) {
					t.Parallel()
					cache := common.Recommendation{Provider: common.ProviderAWS, Service: common.ServiceCache,
						ResourceType: "cache.r6g.large", Region: "us-east-1", Count: 2, Details: &common.CacheDetails{Engine: engine}}
					foreign := cache
					foreign.Provider, foreign.Service = other.provider, other.service
					client := &fakeServiceClient{commitments: []common.Commitment{
						{Provider: cache.Provider, Service: cache.Service, ResourceType: cache.ResourceType, Region: cache.Region,
							Engine: engine, Count: 1, State: common.CommitmentStateActive, StartDate: time.Now()},
						{Provider: other.provider, Service: other.service, ResourceType: cache.ResourceType, Region: cache.Region,
							Engine: engine, Count: 2, State: common.CommitmentStateActive, StartDate: time.Now()},
					}}
					recs := []common.Recommendation{cache, foreign}
					if reverse {
						recs[0], recs[1] = recs[1], recs[0]
					}
					passed, filtered, err := NewDuplicateChecker(0).AdjustRecommendationsForExisting(context.Background(), recs, client)
					require.NoError(t, err)
					require.Len(t, passed, 1)
					assert.Equal(t, cache.Provider, passed[0].Provider)
					assert.Equal(t, cache.Service, passed[0].Service)
					assert.Equal(t, 1, passed[0].Count)
					require.Len(t, filtered, 1)
					assert.Equal(t, foreign.Provider, filtered[0].Provider)
					assert.Equal(t, foreign.Service, filtered[0].Service)
				})
			}
		}
	}
}

func TestElastiCacheEngineBoundaries(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name     string
		provider common.ProviderType
		service  common.ServiceType
		engine   string
		region   string
		resource string
	}{
		{"different region", common.ProviderAWS, common.ServiceCache, "", "us-west-2", "cache.r6g.large"},
		{"different resource", common.ProviderAWS, common.ServiceCache, "", "us-east-1", "cache.r6g.xlarge"},
		{"memorydb unknown", common.ProviderAWS, common.ServiceMemoryDB, "", "us-east-1", "cache.r6g.large"},
		{"memorydb redis", common.ProviderAWS, common.ServiceMemoryDB, "redis", "us-east-1", "cache.r6g.large"},
		{"rds unknown", common.ProviderAWS, common.ServiceRDS, "", "us-east-1", "cache.r6g.large"},
		{"azure unknown", common.ProviderAzure, common.ServiceCache, "", "us-east-1", "cache.r6g.large"},
		{"azure redis", common.ProviderAzure, common.ServiceCache, "redis", "us-east-1", "cache.r6g.large"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			client := &fakeServiceClient{commitments: []common.Commitment{{Provider: tt.provider, Service: tt.service,
				ResourceType: tt.resource, Region: tt.region, Engine: tt.engine, Count: 1,
				State: common.CommitmentStateActive, StartDate: time.Now()}}}
			rec := common.Recommendation{Provider: tt.provider, Service: tt.service,
				ResourceType: "cache.r6g.large", Region: "us-east-1", Count: 1, Details: &common.CacheDetails{Engine: "valkey"}}
			passed, filtered, err := NewDuplicateChecker(0).AdjustRecommendationsForExisting(context.Background(), []common.Recommendation{rec}, client)
			require.NoError(t, err)
			assert.Equal(t, []common.Recommendation{rec}, passed)
			assert.Empty(t, filtered)
		})
	}
}

type customDedupeClient struct {
	fakeServiceClient
	filterErr error
	seen      []common.Commitment
}

func (c *customDedupeClient) FilterRecommendationsForRecentCommitments(recs []common.Recommendation, existing []common.Commitment) ([]common.Recommendation, []common.Recommendation, error) {
	c.seen = existing
	return recs, nil, c.filterErr
}

func TestProviderDedupeDoesNotFallThrough(t *testing.T) {
	for _, filterErr := range []error{nil, errors.New("provider inventory unusable")} {
		c := &customDedupeClient{
			fakeServiceClient: fakeServiceClient{commitments: []common.Commitment{
				{ResourceType: "matching-size", Region: "region", Count: 10, State: common.CommitmentStateActive, StartDate: time.Now()},
				{ResourceType: "old-size", State: common.CommitmentStateActive, StartDate: time.Now().Add(-48 * time.Hour)},
			}},
			filterErr: filterErr,
		}
		recs := []common.Recommendation{{ResourceType: "matching-size", Region: "region", Count: 5}}
		passed, filtered, err := NewDuplicateChecker(24).AdjustRecommendationsForExisting(context.Background(), recs, c)
		assert.ErrorIs(t, err, filterErr)
		assert.Equal(t, recs, passed)
		assert.Empty(t, filtered)
		require.Len(t, c.seen, 1)
		assert.Equal(t, "matching-size", c.seen[0].ResourceType)
	}
}

func TestFilterRecentCommitments_StateAndWindow(t *testing.T) {
	t.Parallel()
	now := time.Now()
	commitments := []common.Commitment{
		{ResourceType: "db.t3.small", Region: "us-east-1", Engine: "mysql", Count: 1, State: "active", StartDate: now.Add(-25 * time.Hour)},                      // too old
		{ResourceType: "db.t3.small", Region: "us-east-1", Engine: "mysql", Count: 2, State: "retired", StartDate: now.Add(-1 * time.Hour)},                      // wrong state
		{ResourceType: "db.t3.small", Region: "us-east-1", Engine: "mysql", Count: 3, State: common.CommitmentStateCanceled, StartDate: now.Add(-1 * time.Hour)}, // wrong state
		{ResourceType: "db.t3.small", Region: "us-east-1", Engine: "mysql", Count: 4, State: "payment-pending", StartDate: now.Add(-1 * time.Hour)},
		{ResourceType: "db.t3.small", Region: "us-east-1", Engine: "mysql", Count: 5, State: "active", StartDate: now.Add(-1 * time.Hour)},
	}

	d := NewDuplicateChecker(DefaultDuplicateCheckLookbackHours)
	recent := d.filterRecentCommitments(commitments)

	require.Len(t, recent, 2)
	counts := []int{recent[0].Count, recent[1].Count}
	assert.ElementsMatch(t, []int{4, 5}, counts)
}

// A queued RI (scheduled future purchase, AWS state "queued") is already
// owned; it must suppress the matching recommendation or the next run buys it
// again.
func TestAdjustRecommendationsForExisting_QueuedCommitmentCollides(t *testing.T) {
	t.Parallel()
	client := &fakeServiceClient{commitments: []common.Commitment{
		{ResourceType: "m5.large", Region: "us-east-1", Count: 2, State: common.CommitmentStateQueued, StartDate: time.Now().Add(24 * time.Hour)},
	}}
	rec := common.Recommendation{ResourceType: "m5.large", Region: "us-east-1", Count: 2}

	d := NewDuplicateChecker(DefaultDuplicateCheckLookbackHours)
	passed, filtered, err := d.AdjustRecommendationsForExisting(context.Background(), []common.Recommendation{rec}, client)

	require.NoError(t, err)
	assert.Empty(t, passed)
	assert.Len(t, filtered, 1)
}

func TestAdjustRecommendationsForExisting_EngineNormalizationCollides(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	client := &fakeServiceClient{commitments: []common.Commitment{
		{ResourceType: "db.r5.large", Region: "us-east-1", Engine: "Aurora PostgreSQL", Count: 5, State: "active", StartDate: time.Now().Add(-1 * time.Hour)},
	}}
	rec := common.Recommendation{
		ResourceType: "db.r5.large", Region: "us-east-1", Count: 5,
		Details: &common.DatabaseDetails{Engine: "aurora-postgresql"},
	}

	d := NewDuplicateChecker(0)
	passed, filtered, err := d.AdjustRecommendationsForExisting(ctx, []common.Recommendation{rec}, client)

	require.NoError(t, err)
	assert.Empty(t, passed)
	assert.Len(t, filtered, 1)
}

func TestAdjustRecommendationsForExisting_UppercaseRecognizedAliasCollides(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client := &fakeServiceClient{commitments: []common.Commitment{
		{
			ResourceType: "db.r5.large",
			Region:       "us-east-1",
			Engine:       "AURORA POSTGRESQL",
			Deployment:   "single-az",
			Count:        1,
			State:        "active",
			StartDate:    time.Now().Add(-1 * time.Hour),
		},
	}}
	rec := common.Recommendation{
		ResourceType: "db.r5.large",
		Region:       "us-east-1",
		Count:        1,
		Details:      &common.DatabaseDetails{Engine: "aurora-postgresql", AZConfig: "single-az"},
	}

	d := NewDuplicateChecker(0)
	passed, filtered, err := d.AdjustRecommendationsForExisting(ctx, []common.Recommendation{rec}, client)

	require.NoError(t, err)
	assert.Empty(t, passed)
	assert.Len(t, filtered, 1)
}

func TestAdjustRecommendationsForExisting_FullCoverageDrops(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	client := &fakeServiceClient{commitments: []common.Commitment{
		{ResourceType: "db.t3.small", Region: "us-east-1", Engine: "mysql", Count: 5, State: "active", StartDate: time.Now().Add(-1 * time.Hour)},
	}}
	rec := common.Recommendation{
		ResourceType: "db.t3.small", Region: "us-east-1", Count: 5,
		Details: &common.DatabaseDetails{Engine: "mysql"},
	}

	d := NewDuplicateChecker(0)
	passed, filtered, err := d.AdjustRecommendationsForExisting(ctx, []common.Recommendation{rec}, client)

	require.NoError(t, err)
	assert.Empty(t, passed)
	require.Len(t, filtered, 1)
	assert.Equal(t, rec, filtered[0])
}

func TestAdjustRecommendationsForExisting_PartialCoverageConsumesBudget(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	client := &fakeServiceClient{commitments: []common.Commitment{
		{ResourceType: "db.t3.small", Region: "us-east-1", Engine: "mysql", Count: 2, State: "active", StartDate: time.Now().Add(-1 * time.Hour)},
	}}
	recs := []common.Recommendation{
		{ResourceType: "db.t3.small", Region: "us-east-1", Count: 5, Details: &common.DatabaseDetails{Engine: "mysql"}},
		{ResourceType: "db.t3.small", Region: "us-east-1", Count: 3, Details: &common.DatabaseDetails{Engine: "mysql"}},
	}

	d := NewDuplicateChecker(0)
	passed, filtered, err := d.AdjustRecommendationsForExisting(ctx, recs, client)

	require.NoError(t, err)
	assert.Empty(t, filtered)
	require.Len(t, passed, 2)
	assert.Equal(t, 3, passed[0].Count) // 5 - 2 = 3, budget consumed
	assert.Equal(t, 3, passed[1].Count) // no existing coverage left, unchanged
}

func TestAdjustRecommendationsForExisting_ClientErrorReturnsOriginal(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	wantErr := errors.New("boom")
	client := &fakeServiceClient{err: wantErr}
	recs := []common.Recommendation{
		{ResourceType: "db.t3.small", Region: "us-east-1", Count: 5, Details: &common.DatabaseDetails{Engine: "mysql"}},
	}

	d := NewDuplicateChecker(0)
	passed, filtered, err := d.AdjustRecommendationsForExisting(ctx, recs, client)

	assert.Equal(t, recs, passed)
	assert.Nil(t, filtered)
	assert.Equal(t, wantErr, err)
}

func TestAdjustRecommendationsForExisting_NoRecentCommitmentsPassesThrough(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	client := &fakeServiceClient{}
	recs := []common.Recommendation{
		{ResourceType: "db.t3.small", Region: "us-east-1", Count: 5, Details: &common.DatabaseDetails{Engine: "mysql"}},
	}

	d := NewDuplicateChecker(0)
	passed, filtered, err := d.AdjustRecommendationsForExisting(ctx, recs, client)

	require.NoError(t, err)
	assert.Nil(t, filtered)
	require.Len(t, passed, 1)
	// No reallocation/reordering: passed IS recs, not a copy.
	assert.Same(t, &recs[0], &passed[0])
}

func TestNewDuplicateChecker_DefaultAndCustomLookback(t *testing.T) {
	t.Parallel()
	assert.Equal(t, DefaultDuplicateCheckLookbackHours, NewDuplicateChecker(0).LookbackHours)
	assert.Equal(t, DefaultDuplicateCheckLookbackHours, NewDuplicateChecker(-1).LookbackHours)
	assert.Equal(t, 48, NewDuplicateChecker(48).LookbackHours)
}

func TestAdjustRecommendationsForExisting_NilLogfDoesNotPanic(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	client := &fakeServiceClient{commitments: []common.Commitment{
		{ResourceType: "db.t3.small", Region: "us-east-1", Engine: "mysql", Count: 2, State: "active", StartDate: time.Now().Add(-1 * time.Hour)},
	}}
	recs := []common.Recommendation{
		{ResourceType: "db.t3.small", Region: "us-east-1", Count: 5, Details: &common.DatabaseDetails{Engine: "mysql"}},
	}

	d := NewDuplicateChecker(0)
	assert.NotPanics(t, func() {
		_, _, err := d.AdjustRecommendationsForExisting(ctx, recs, client)
		require.NoError(t, err)
	})
}

func TestAdjustRecommendationsForExisting_LogfReceivesDecisionTrail(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	client := &fakeServiceClient{commitments: []common.Commitment{
		{ResourceType: "db.t3.small", Region: "us-east-1", Engine: "mysql", Count: 5, State: "active", StartDate: time.Now().Add(-1 * time.Hour)},
	}}
	recs := []common.Recommendation{
		{ResourceType: "db.t3.small", Region: "us-east-1", Count: 5, Details: &common.DatabaseDetails{Engine: "mysql"}},
	}

	var lines []string
	d := NewDuplicateChecker(0)
	d.Logf = func(format string, args ...any) {
		lines = append(lines, fmt.Sprintf(format, args...))
	}

	_, _, err := d.AdjustRecommendationsForExisting(ctx, recs, client)
	require.NoError(t, err)

	require.NotEmpty(t, lines)
	found := false
	for _, l := range lines {
		if strings.Contains(l, "[DuplicateChecker]") {
			found = true
			break
		}
	}
	assert.True(t, found)
}

// TestAdjustRecommendationsForExisting_SingleAZCommitmentDoesNotSuppressMultiAZRec
// is a regression test for a duplicate-identity key that omitted the RDS
// deployment dimension. A recent Single-AZ commitment must not reduce or
// drop a Multi-AZ recommendation for the same instance type/region/engine:
// the two are priced and provisioned differently and do not cover each
// other's demand.
func TestAdjustRecommendationsForExisting_SingleAZCommitmentDoesNotSuppressMultiAZRec(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	client := &fakeServiceClient{commitments: []common.Commitment{
		{ResourceType: "db.r5.large", Region: "us-east-1", Engine: "mysql", Deployment: "single-az", Count: 5, State: "active", StartDate: time.Now().Add(-1 * time.Hour)},
	}}
	rec := common.Recommendation{
		ResourceType: "db.r5.large", Region: "us-east-1", Count: 5,
		Details: &common.DatabaseDetails{Engine: "mysql", AZConfig: "multi-az"},
	}

	d := NewDuplicateChecker(0)
	passed, filtered, err := d.AdjustRecommendationsForExisting(ctx, []common.Recommendation{rec}, client)

	require.NoError(t, err)
	assert.Empty(t, filtered)
	require.Len(t, passed, 1)
	assert.Equal(t, rec.Count, passed[0].Count)
}

// TestAdjustRecommendationsForExisting_MultiAZCommitmentDoesNotSuppressSingleAZRec
// is the mirror direction of the above: a recent Multi-AZ commitment must
// not suppress a Single-AZ recommendation.
func TestAdjustRecommendationsForExisting_MultiAZCommitmentDoesNotSuppressSingleAZRec(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	client := &fakeServiceClient{commitments: []common.Commitment{
		{ResourceType: "db.r5.large", Region: "us-east-1", Engine: "mysql", Deployment: "multi-az", Count: 5, State: "active", StartDate: time.Now().Add(-1 * time.Hour)},
	}}
	rec := common.Recommendation{
		ResourceType: "db.r5.large", Region: "us-east-1", Count: 5,
		Details: &common.DatabaseDetails{Engine: "mysql", AZConfig: "single-az"},
	}

	d := NewDuplicateChecker(0)
	passed, filtered, err := d.AdjustRecommendationsForExisting(ctx, []common.Recommendation{rec}, client)

	require.NoError(t, err)
	assert.Empty(t, filtered)
	require.Len(t, passed, 1)
	assert.Equal(t, rec.Count, passed[0].Count)
}

// TestAdjustRecommendationsForExisting_SingleAZCommitmentStillSuppressesSingleAZRec
// is the positive control: matching deployment on both sides must still
// dedupe, so the fix above does not simply disable deduplication for RDS.
func TestAdjustRecommendationsForExisting_SingleAZCommitmentStillSuppressesSingleAZRec(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	client := &fakeServiceClient{commitments: []common.Commitment{
		{ResourceType: "db.r5.large", Region: "us-east-1", Engine: "mysql", Deployment: "single-az", Count: 5, State: "active", StartDate: time.Now().Add(-1 * time.Hour)},
	}}
	rec := common.Recommendation{
		ResourceType: "db.r5.large", Region: "us-east-1", Count: 5,
		Details: &common.DatabaseDetails{Engine: "mysql", AZConfig: "single-az"},
	}

	d := NewDuplicateChecker(0)
	passed, filtered, err := d.AdjustRecommendationsForExisting(ctx, []common.Recommendation{rec}, client)

	require.NoError(t, err)
	assert.Empty(t, passed)
	require.Len(t, filtered, 1)
}

// TestAdjustRecommendationsForExisting_NonRDSCommitmentStillDeduplicates
// guards the non-RDS path: a commitment/recommendation pair with no
// deployment dimension at all (e.g. EC2) must still land on the same key
// and dedupe, so adding deployment to the key doesn't regress non-RDS
// resource types that never populate it.
func TestAdjustRecommendationsForExisting_NonRDSCommitmentStillDeduplicates(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	client := &fakeServiceClient{commitments: []common.Commitment{
		{ResourceType: "m5.large", Region: "us-east-1", Count: 5, State: "active", StartDate: time.Now().Add(-1 * time.Hour)},
	}}
	rec := common.Recommendation{
		ResourceType: "m5.large", Region: "us-east-1", Count: 5,
		Details: &common.ComputeDetails{Platform: "linux"},
	}

	d := NewDuplicateChecker(0)
	passed, filtered, err := d.AdjustRecommendationsForExisting(ctx, []common.Recommendation{rec}, client)

	require.NoError(t, err)
	assert.Empty(t, passed)
	require.Len(t, filtered, 1)
}

// An unrecognized state fails closed (issue #142): the commitment counts as
// existing and is logged, instead of being dropped and allowing a second
// purchase.
func TestAdjustRecommendationsForExisting_UnknownStateCountsAndLogs(t *testing.T) {
	t.Parallel()
	client := &fakeServiceClient{commitments: []common.Commitment{
		{CommitmentID: "cud-1", ResourceType: "n2-standard-4", Region: "us-central1", Count: 2, State: "not_yet_active", StartDate: time.Now().Add(-time.Hour)},
	}}
	rec := common.Recommendation{ResourceType: "n2-standard-4", Region: "us-central1", Count: 2}

	var logs []string
	d := NewDuplicateChecker(DefaultDuplicateCheckLookbackHours)
	d.Logf = func(format string, args ...any) { logs = append(logs, fmt.Sprintf(format, args...)) }
	passed, filtered, err := d.AdjustRecommendationsForExisting(context.Background(), []common.Recommendation{rec}, client)

	require.NoError(t, err)
	assert.Empty(t, passed)
	assert.Len(t, filtered, 1)
	assert.Contains(t, strings.Join(logs, "\n"), `commitment cud-1 has unrecognized state "not_yet_active"`)
}

// Ended states still do not suppress a purchase.
func TestFilterRecentCommitments_EndedStatesIgnored(t *testing.T) {
	t.Parallel()
	start := time.Now().Add(-time.Hour)
	states := []common.CommitmentState{
		common.CommitmentStateRetired, common.CommitmentStatePendingReturn, common.CommitmentStateExpired,
		common.CommitmentStateCanceled, common.CommitmentStateFailed,
	}
	commitments := make([]common.Commitment, 0, len(states))
	for _, s := range states {
		commitments = append(commitments, common.Commitment{ResourceType: "m5.large", Region: "us-east-1", Count: 1, State: s, StartDate: start})
	}

	assert.Empty(t, NewDuplicateChecker(DefaultDuplicateCheckLookbackHours).filterRecentCommitments(commitments))
}
