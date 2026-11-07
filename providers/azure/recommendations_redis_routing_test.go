package azure

import (
	"context"
	"slices"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/providers/azure/mocks"
	"github.com/LeanerCloud/cloud-commitments-go/providers/azure/services/managedredis"
)

func TestServiceFromExtendedProperties_RedisCacheIsMemoryDB(t *testing.T) {
	t.Parallel()
	got := serviceFromExtendedProperties(map[string]*string{"reservedResourceType": strPtr("RedisCache")})
	assert.Equal(t, string(common.ServiceMemoryDB), got)
}

func redisRetailRows() []map[string]any {
	row := func(region, term string, price float64) map[string]any {
		return mocks.ReservationRow("Redis Cache", "Azure Redis Cache Premium", region, "Azure_Redis_Cache_Premium_P1_Cache", "P1", "P1 Cache Instance", term, price)
	}
	return []map[string]any{row("eastus", "1 Year", 1100), row("eastus", "3 Years", 2600), row("westus", "1 Year", 900)}
}

// realRedisClient is the production managedredis client over a Consumption
// RedisCache payload and a stub Retail Prices catalog.
func realRedisClient() *managedredis.Client {
	c := managedredis.NewClientWithHTTP(nil, "sub", "", &mocks.PricingHTTP{Items: redisRetailRows()})
	c.SetRecommendationsPager(&mocks.MockRecommendationsPager{Results: mocks.ConsumptionUnitFixtures("Premium_P1", "eastus"), HasMore: true})
	return c
}

func redisAdapter(t *testing.T, redis serviceRecsGetter, advisor func(*RecommendationsClientAdapter) []common.Recommendation) *RecommendationsClientAdapter {
	t.Helper()
	overrideServiceClientFns(t, &fakeServiceClient{}, &fakeServiceClient{}, redis, &fakeServiceClient{}, &fakeServiceClient{})
	a := &RecommendationsClientAdapter{cred: &mockAzureTokenCredential{}, subscriptionID: "sub"}
	a.getAdvisorRecsFn = func(context.Context, common.RecommendationParams) ([]common.Recommendation, error) {
		if advisor == nil {
			return nil, nil
		}
		return advisor(a), nil
	}
	return a
}

func TestGetRecommendations_RedisCollectedViaManagedRedisOnly(t *testing.T) {
	recs, err := redisAdapter(t, realRedisClient(), nil).GetRecommendations(context.Background(), &common.RecommendationParams{})

	require.NoError(t, err)
	require.Len(t, recs, 4, "one Consumption payload of two recommendations expands to four variants, listed once")
	for _, rec := range recs {
		assert.Equal(t, common.ServiceMemoryDB, rec.Service)
	}
}

func TestGetRecommendations_RedisFilterFollowsMemoryDB(t *testing.T) {
	for _, tc := range []struct {
		service common.ServiceType
		calls   int32
	}{{common.ServiceMemoryDB, 1}, {common.ServiceCache, 0}} {
		redis := &countingFake{}
		_, err := redisAdapter(t, redis, nil).GetRecommendations(context.Background(), &common.RecommendationParams{Service: tc.service})

		require.NoError(t, err)
		assert.Equal(t, tc.calls, redis.calls.Load(), "params.Service=%s", tc.service)
	}
}

type countingFake struct {
	fakeServiceClient
	calls atomic.Int32
}

func (f *countingFake) GetRecommendations(ctx context.Context, p *common.RecommendationParams) ([]common.Recommendation, error) {
	f.calls.Add(1)
	return f.fakeServiceClient.GetRecommendations(ctx, p)
}

func redisAdvisorRow(a *RecommendationsClientAdapter, region string) common.Recommendation {
	ext := reservationExt(map[string]*string{"reservedResourceType": strPtr("RedisCache"), "sku": strPtr("Premium_P1"), "term": strPtr("P1Y"), "region": strPtr(region)})
	return *a.convertAdvisorRecommendation(context.Background(), advisorRec(subscriptionScope, ext))
}

func TestGetRecommendations_AdvisorRedisIsMemoryDBAndPricedThroughIt(t *testing.T) {
	advisor := func(a *RecommendationsClientAdapter) []common.Recommendation {
		return []common.Recommendation{redisAdvisorRow(a, "westus")}
	}
	// Only the Advisor row: the Consumption pager is empty, so nothing covers it.
	redis := managedredis.NewClientWithHTTP(nil, "sub", "", &mocks.PricingHTTP{Items: redisRetailRows()})
	redis.SetRecommendationsPager(&mocks.MockRecommendationsPager{HasMore: true})

	for _, params := range []common.RecommendationParams{{}, {Service: common.ServiceMemoryDB}} {
		recs, err := redisAdapter(t, redis, advisor).GetRecommendations(context.Background(), &params)

		require.NoError(t, err)
		require.Len(t, recs, 2, "upfront and monthly legs of the priced row")
		for _, rec := range recs {
			assert.Equal(t, common.ServiceMemoryDB, rec.Service)
		}
		assert.True(t, slices.ContainsFunc(recs, func(r common.Recommendation) bool { return r.CommitmentCost == 1800 }), "2 units x 900 from the managedredis price lookup")
	}
}

func TestGetRecommendations_AdvisorRedisCoveredByConsumptionRedis(t *testing.T) {
	// Same account, region and term as the Consumption payload (eastus, 1yr, 3yr).
	advisor := func(a *RecommendationsClientAdapter) []common.Recommendation {
		return []common.Recommendation{redisAdvisorRow(a, "eastus")}
	}

	recs, err := redisAdapter(t, realRedisClient(), advisor).GetRecommendations(context.Background(), &common.RecommendationParams{})

	require.NoError(t, err)
	assert.Len(t, recs, 4, "the Advisor row is dropped: Consumption already covers that need")
}

// Tripwire: no Redis source may relabel recommendations as ServiceCache, whose
// client is going to refuse Redis purchases.
func TestGetRecommendations_NoRecommendationIsLabelledServiceCache(t *testing.T) {
	advisor := func(a *RecommendationsClientAdapter) []common.Recommendation {
		return []common.Recommendation{redisAdvisorRow(a, "westus")}
	}

	recs, err := redisAdapter(t, realRedisClient(), advisor).GetRecommendations(context.Background(), &common.RecommendationParams{})

	require.NoError(t, err)
	require.Len(t, recs, 6, "four Consumption variants plus the two legs of the uncovered westus Advisor row")
	for _, rec := range recs {
		assert.NotEqual(t, common.ServiceCache, rec.Service)
	}
}
