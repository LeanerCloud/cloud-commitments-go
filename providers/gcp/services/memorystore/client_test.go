package memorystore

import (
	"context"
	"errors"
	"testing"

	"cloud.google.com/go/recommender/apiv1/recommenderpb"
	"cloud.google.com/go/redis/apiv1/redispb"
	gax "github.com/googleapis/gax-go/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/cloudbilling/v1"
	"google.golang.org/api/iterator"
	"google.golang.org/genproto/googleapis/type/money"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
)

// Mock implementations

// MockRedisService implements RedisService for testing.
type MockRedisService struct {
	instances    []*redispb.Instance
	instancesErr error
	createResult CreateInstanceOperation
	createErr    error
	createCalled bool
	closeCalled  bool
}

func (m *MockRedisService) ListInstances(ctx context.Context, req *redispb.ListInstancesRequest) RedisIterator {
	return &MockRedisIterator{instances: m.instances, err: m.instancesErr}
}

func (m *MockRedisService) CreateInstance(ctx context.Context, req *redispb.CreateInstanceRequest) (CreateInstanceOperation, error) {
	m.createCalled = true
	if m.createErr != nil {
		return nil, m.createErr
	}
	return m.createResult, nil
}

func (m *MockRedisService) Close() error {
	m.closeCalled = true
	return nil
}

// MockRedisIterator implements RedisIterator for testing.
type MockRedisIterator struct {
	instances []*redispb.Instance
	index     int
	err       error
}

func (m *MockRedisIterator) Next() (*redispb.Instance, error) {
	if m.err != nil {
		return nil, m.err
	}
	if m.index >= len(m.instances) {
		return nil, iterator.Done
	}
	instance := m.instances[m.index]
	m.index++
	return instance, nil
}

// MockCreateInstanceOperation implements CreateInstanceOperation for testing.
type MockCreateInstanceOperation struct {
	instance *redispb.Instance
	err      error
}

func (m *MockCreateInstanceOperation) Wait(ctx context.Context, opts ...gax.CallOption) (*redispb.Instance, error) {
	return m.instance, m.err
}

// MockBillingService implements BillingService for testing.
type MockBillingService struct {
	skus *cloudbilling.ListSkusResponse
	err  error
}

func (m *MockBillingService) ListSKUs(serviceID string) (*cloudbilling.ListSkusResponse, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.skus, nil
}

// MockRecommenderIterator implements RecommenderIterator for testing.
type MockRecommenderIterator struct {
	recommendations []*recommenderpb.Recommendation
	index           int
	err             error
}

func (m *MockRecommenderIterator) Next() (*recommenderpb.Recommendation, error) {
	if m.err != nil {
		return nil, m.err
	}
	if m.index >= len(m.recommendations) {
		return nil, iterator.Done
	}
	rec := m.recommendations[m.index]
	m.index++
	return rec, nil
}

func TestNewClient(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	client, err := NewClient(ctx, "test-project", "us-central1")

	require.NoError(t, err)
	require.NotNil(t, client)
	assert.Equal(t, "test-project", client.projectID)
	assert.Equal(t, "us-central1", client.region)
}

func TestMemorystoreClient_GetServiceType(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	client, _ := NewClient(ctx, "project", "region")
	assert.Equal(t, common.ServiceCache, client.GetServiceType())
}

func TestMemorystoreClient_GetRegion(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		region   string
		expected string
	}{
		{
			name:     "US Central 1",
			region:   "us-central1",
			expected: "us-central1",
		},
		{
			name:     "Europe West 1",
			region:   "europe-west1",
			expected: "europe-west1",
		},
		{
			name:     "Asia Southeast 1",
			region:   "asia-southeast1",
			expected: "asia-southeast1",
		},
	}

	ctx := context.Background()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			client, _ := NewClient(ctx, "project", tt.region)
			assert.Equal(t, tt.expected, client.GetRegion())
		})
	}
}

func TestMemorystoreClient_GetValidResourceTypes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	client, _ := NewClient(ctx, "test-project", "us-central1")

	tiers, err := client.GetValidResourceTypes(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, tiers)

	assert.Contains(t, tiers, "BASIC")
	assert.Contains(t, tiers, "STANDARD_HA")
}

func TestSkuMatchesTier(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		sku      *cloudbilling.Sku
		tier     string
		region   string
		expected bool
	}{
		{
			name: "SKU matches tier and region",
			sku: &cloudbilling.Sku{
				Description:    "Memorystore Redis STANDARD_HA",
				Category:       &cloudbilling.Category{UsageType: "OnDemand"},
				ServiceRegions: []string{"us-central1"},
			},
			tier:     "STANDARD_HA",
			region:   "us-central1",
			expected: true,
		},
		{
			name: "SKU matches tier but not region",
			sku: &cloudbilling.Sku{
				Description:    "Memorystore Redis BASIC",
				Category:       &cloudbilling.Category{UsageType: "OnDemand"},
				ServiceRegions: []string{"europe-west1"},
			},
			tier:     "BASIC",
			region:   "us-central1",
			expected: false,
		},
		{
			name: "SKU does not match tier",
			sku: &cloudbilling.Sku{
				Description:    "Memorystore Redis BASIC",
				Category:       &cloudbilling.Category{UsageType: "OnDemand"},
				ServiceRegions: []string{"us-central1"},
			},
			tier:     "STANDARD_HA",
			region:   "us-central1",
			expected: false,
		},
		{
			name: "SKU with nil service regions matches no region",
			sku: &cloudbilling.Sku{
				Description:    "Memorystore Redis BASIC instance",
				Category:       &cloudbilling.Category{UsageType: "OnDemand"},
				ServiceRegions: nil,
			},
			tier:     "BASIC",
			region:   "us-central1",
			expected: false,
		},
		{
			name: "SKU for STANDARD_HA is a decoy for tier STANDARD",
			sku: &cloudbilling.Sku{
				Description:    "Memorystore Redis STANDARD_HA instance",
				Category:       &cloudbilling.Category{UsageType: "OnDemand"},
				ServiceRegions: []string{"us-central1"},
			},
			tier:     "STANDARD",
			region:   "us-central1",
			expected: false,
		},
		{
			name: "SKU with empty service regions matches no region",
			sku: &cloudbilling.Sku{
				Description:    "Memorystore Redis BASIC instance",
				Category:       &cloudbilling.Category{UsageType: "OnDemand"},
				ServiceRegions: []string{},
			},
			tier:     "BASIC",
			region:   "us-central1",
			expected: false,
		},
		{
			name: "Case insensitive tier match",
			sku: &cloudbilling.Sku{
				Description:    "Memorystore Redis standard_ha",
				Category:       &cloudbilling.Category{UsageType: "OnDemand"},
				ServiceRegions: []string{"us-central1"},
			},
			tier:     "STANDARD_HA",
			region:   "us-central1",
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := skuMatchesTier(tt.sku, tt.tier, tt.region)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestRedisPricingStructure(t *testing.T) {
	t.Parallel()
	pricing := RedisPricing{
		HourlyRate:        0.03,
		CommitmentPrice:   262.8,
		OnDemandPrice:     438.0,
		Currency:          "USD",
		SavingsPercentage: 40.0,
	}

	assert.Equal(t, 0.03, pricing.HourlyRate)
	assert.Equal(t, 262.8, pricing.CommitmentPrice)
	assert.Equal(t, 438.0, pricing.OnDemandPrice)
	assert.Equal(t, "USD", pricing.Currency)
	assert.Equal(t, 40.0, pricing.SavingsPercentage)
}

func TestMemorystoreClient_ValidateOffering_ValidTier(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	client, _ := NewClient(ctx, "test-project", "us-central1")

	rec := common.Recommendation{
		ResourceType: "BASIC",
	}

	err := client.ValidateOffering(ctx, rec)
	assert.NoError(t, err)
}

func TestMemorystoreClient_ValidateOffering_InvalidTier(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	client, _ := NewClient(ctx, "test-project", "us-central1")

	rec := common.Recommendation{
		ResourceType: "INVALID_TIER",
	}

	err := client.ValidateOffering(ctx, rec)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid Memorystore tier")
}

// TestMemorystoreClient_GetExistingCommitments_Stub verifies the documented
// stub behavior: the GCP Memorystore Redis API does not expose commitment
// status — ReservedIpRange is the VPC-peering CIDR, not a commitment
// indicator. The production code returns (nil, nil) and the injected
// redisService is intentionally never called from this path.
//
// If a future implementation adds real commitment detection here, this test
// must be updated to match — do NOT silently swap in the mock-based variant
// that was previously merged (it asserted behavior the production code never
// implemented, causing false CI failures).
func TestMemorystoreClient_GetExistingCommitments_Stub(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	client, _ := NewClient(ctx, "test-project", "us-central1")

	// Inject a service that would error if called — verifies the stub does
	// not accidentally touch the redis service.
	sentinel := &MockRedisService{
		instancesErr: errors.New("redis service must not be called from stub"),
	}
	client.SetRedisService(sentinel)

	commitments, err := client.GetExistingCommitments(ctx)

	require.NoError(t, err, "stub must not error")
	assert.Nil(t, commitments, "stub must return nil until real detection is implemented")

	// Close must NOT have been called: the stub returns before creating any
	// client, so there is nothing to close. Asserting false here documents
	// that the stub owns the lifecycle correctly.
	assert.False(t, sentinel.closeCalled, "stub must not close a service it never opened")
}

// TestMemorystoreClient_PurchaseCommitment_NotSupported is the regression test for
// issue #640: Memorystore has no standalone CUD purchase API, so PurchaseCommitment
// must return ErrCommitmentPurchaseNotSupported and MUST NOT call any
// resource-creation API (it previously created a new billable Redis instance).
func TestMemorystoreClient_PurchaseCommitment_NotSupported(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	client, _ := NewClient(ctx, "test-project", "us-central1")

	mockService := &MockRedisService{
		// If PurchaseCommitment ever calls CreateInstance, this would return a
		// successful op; the assertions below ensure it is never invoked.
		createResult: &MockCreateInstanceOperation{instance: &redispb.Instance{Name: "test-instance"}},
	}
	client.SetRedisService(mockService)

	rec := common.Recommendation{
		ResourceType:   "STANDARD_HA",
		CommitmentCost: 100.0,
	}

	result, err := client.PurchaseCommitment(ctx, rec, common.PurchaseOptions{})

	require.Error(t, err)
	assert.ErrorIs(t, err, common.ErrCommitmentPurchaseNotSupported)
	assert.False(t, result.Success)
	assert.Empty(t, result.CommitmentID)
	assert.ErrorIs(t, result.Error, common.ErrCommitmentPurchaseNotSupported)
	// The critical guarantee: a "purchase" must never create infrastructure.
	assert.False(t, mockService.createCalled, "PurchaseCommitment must not call CreateInstance")
}

func TestMemorystoreClient_GetOfferingDetails_WithMockService(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		rec         common.Recommendation
		skus        *cloudbilling.ListSkusResponse
		billingErr  error
		wantErr     bool
		errContains string
	}{
		{
			// Both on-demand and commitment SKUs are required after the issue #1020 fix:
			// getRedisPricing now returns an error when no commitment SKU is found.
			name: "successful 1yr offering details",
			rec: common.Recommendation{
				ResourceType:  "STANDARD_HA",
				Term:          "1yr",
				PaymentOption: "all-upfront",
			},
			skus: &cloudbilling.ListSkusResponse{
				Skus: []*cloudbilling.Sku{
					{
						Description:    "Memorystore Redis STANDARD_HA instance",
						Category:       &cloudbilling.Category{UsageType: "OnDemand"},
						ServiceRegions: []string{"us-central1"},
						PricingInfo: []*cloudbilling.PricingInfo{
							{
								PricingExpression: &cloudbilling.PricingExpression{
									TieredRates: []*cloudbilling.TierRate{
										{
											UnitPrice: &cloudbilling.Money{
												CurrencyCode: "USD",
												Units:        0,
												Nanos:        50000000, // $0.05 per hour
											},
										},
									},
								},
							},
						},
					},
					{
						Description:    "Memorystore Redis STANDARD_HA commitment 1yr",
						Category:       &cloudbilling.Category{UsageType: "Commit1Yr"},
						ServiceRegions: []string{"us-central1"},
						PricingInfo: []*cloudbilling.PricingInfo{
							{
								PricingExpression: &cloudbilling.PricingExpression{
									TieredRates: []*cloudbilling.TierRate{
										{
											UnitPrice: &cloudbilling.Money{
												CurrencyCode: "USD",
												Units:        0,
												Nanos:        35000000, // $0.035 per hour
											},
										},
									},
								},
							},
						},
					},
				},
			},
			wantErr: false,
		},
		{
			// Both on-demand and commitment SKUs are required after the issue #1020 fix.
			name: "successful 3yr offering details",
			rec: common.Recommendation{
				ResourceType:  "BASIC",
				Term:          "3yr",
				PaymentOption: "monthly",
			},
			skus: &cloudbilling.ListSkusResponse{
				Skus: []*cloudbilling.Sku{
					{
						Description:    "Memorystore Redis BASIC instance",
						Category:       &cloudbilling.Category{UsageType: "OnDemand"},
						ServiceRegions: []string{"us-central1"},
						PricingInfo: []*cloudbilling.PricingInfo{
							{
								PricingExpression: &cloudbilling.PricingExpression{
									TieredRates: []*cloudbilling.TierRate{
										{
											UnitPrice: &cloudbilling.Money{
												CurrencyCode: "USD",
												Units:        0,
												Nanos:        30000000, // $0.03 per hour
											},
										},
									},
								},
							},
						},
					},
					{
						Description:    "Memorystore Redis BASIC commitment 3yr",
						Category:       &cloudbilling.Category{UsageType: "Commit3Yr"},
						ServiceRegions: []string{"us-central1"},
						PricingInfo: []*cloudbilling.PricingInfo{
							{
								PricingExpression: &cloudbilling.PricingExpression{
									TieredRates: []*cloudbilling.TierRate{
										{
											UnitPrice: &cloudbilling.Money{
												CurrencyCode: "USD",
												Units:        0,
												Nanos:        19500000, // $0.0195 per hour
											},
										},
									},
								},
							},
						},
					},
				},
			},
			wantErr: false,
		},
		{
			name: "billing API error",
			rec: common.Recommendation{
				ResourceType: "STANDARD_HA",
				Term:         "1yr",
			},
			billingErr:  errors.New("billing API error"),
			wantErr:     true,
			errContains: "failed to list SKUs",
		},
		{
			name: "no pricing found",
			rec: common.Recommendation{
				ResourceType: "STANDARD_HA",
				Term:         "1yr",
			},
			skus: &cloudbilling.ListSkusResponse{
				Skus: []*cloudbilling.Sku{}, // Empty SKUs
			},
			wantErr:     true,
			errContains: "no pricing found",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			client, _ := NewClient(ctx, "test-project", "us-central1")

			mockBilling := &MockBillingService{
				skus: tt.skus,
				err:  tt.billingErr,
			}
			client.SetBillingService(mockBilling)

			details, err := client.GetOfferingDetails(ctx, tt.rec)

			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errContains)
			} else {
				require.NoError(t, err)
				require.NotNil(t, details)
				assert.Equal(t, tt.rec.ResourceType, details.ResourceType)
				assert.Equal(t, tt.rec.Term, details.Term)
				assert.Equal(t, "USD", details.Currency)
			}
		})
	}
}

// TestGetRedisPricing_CommitmentPriceIsTermTotal is a regression test for the
// unit-mismatch bug where commitmentPrice (per-hour from SKU) was passed
// directly to calculateSavingsPercentage which expects a term total, producing
// a ~99.99% savings percentage. After the fix, CommitmentPrice must equal
// hourlyRate * hoursInTerm and SavingsPercentage must be a realistic fraction.
//
// This test FAILS on the pre-fix code where CommitmentPrice == 0.035 (per-hour).
func TestGetRedisPricing_CommitmentPriceIsTermTotal(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	client, _ := NewClient(ctx, "test-project", "us-central1")

	// onDemand = $0.05/hr, commitment = $0.035/hr
	// hoursInTerm (1yr) = 8760
	// Expected: CommitmentPrice = 0.035*8760 = 306.6, OnDemandPrice = 0.05*8760 = 438
	// Expected savings ~= (438-306.6)/438*100 ~= 30%
	mockBilling := &MockBillingService{
		skus: &cloudbilling.ListSkusResponse{
			Skus: []*cloudbilling.Sku{
				{
					Description:    "Memorystore Redis STANDARD_HA instance",
					Category:       &cloudbilling.Category{UsageType: "OnDemand"},
					ServiceRegions: []string{"us-central1"},
					PricingInfo: []*cloudbilling.PricingInfo{
						{
							PricingExpression: &cloudbilling.PricingExpression{
								TieredRates: []*cloudbilling.TierRate{
									{
										UnitPrice: &cloudbilling.Money{
											CurrencyCode: "USD",
											Units:        0,
											Nanos:        50000000, // $0.05/hr
										},
									},
								},
							},
						},
					},
				},
				{
					Description:    "Memorystore Redis STANDARD_HA commitment 1yr",
					Category:       &cloudbilling.Category{UsageType: "Commit1Yr"},
					ServiceRegions: []string{"us-central1"},
					PricingInfo: []*cloudbilling.PricingInfo{
						{
							PricingExpression: &cloudbilling.PricingExpression{
								TieredRates: []*cloudbilling.TierRate{
									{
										UnitPrice: &cloudbilling.Money{
											CurrencyCode: "USD",
											Units:        0,
											Nanos:        35000000, // $0.035/hr
										},
									},
								},
							},
						},
					},
				},
			},
		},
	}
	client.SetBillingService(mockBilling)

	pricing, err := client.getRedisPricing(ctx, "STANDARD_HA", "us-central1", 1)
	require.NoError(t, err)

	const hoursInYear = 8760.0
	// CommitmentPrice must be a term total, not the raw per-hour SKU rate.
	assert.InDelta(t, 0.035*hoursInYear, pricing.CommitmentPrice, 0.01,
		"CommitmentPrice must be commitment SKU hourly rate * hoursInTerm")
	// HourlyRate must be the per-hour commitment rate.
	assert.InDelta(t, 0.035, pricing.HourlyRate, 0.0001,
		"HourlyRate must be the per-hour commitment SKU rate")
	// OnDemandPrice stays a term total.
	assert.InDelta(t, 0.05*hoursInYear, pricing.OnDemandPrice, 0.01)
	// SavingsPercentage must be ~30%, not ~99.99%.
	assert.Greater(t, pricing.SavingsPercentage, float64(1),
		"SavingsPercentage must not be ~99.99% (unit-mismatch bug)")
	assert.Less(t, pricing.SavingsPercentage, float64(60),
		"SavingsPercentage must be a realistic 1yr CUD discount")
}

func TestMemorystoreClient_SetterMethods(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	client, _ := NewClient(ctx, "test-project", "us-central1")

	// Test SetRedisService
	mockRedis := &MockRedisService{}
	client.SetRedisService(mockRedis)
	assert.Equal(t, mockRedis, client.redisService)

	// Test SetBillingService
	mockBilling := &MockBillingService{}
	client.SetBillingService(mockBilling)
	assert.Equal(t, mockBilling, client.billingService)
}

// TestMemorystoreClient_GetOfferingDetails_RecurringMonthlyCost pins the monthly CUD
// quote derived from the catalog, in the catalog currency, for every term and
// payment option.
func TestMemorystoreClient_GetOfferingDetails_RecurringMonthlyCost(t *testing.T) {
	t.Parallel()
	const region = "europe-west1"
	ctx := context.Background()
	client, _ := NewClient(ctx, "test-project", region)

	const onDemandHourly = 0.20
	const commitmentHourly = 0.136986301 // Catalog precision gives a 1yr total within 0.00001 of 1200.
	client.SetBillingService(&MockBillingService{skus: &cloudbilling.ListSkusResponse{Skus: []*cloudbilling.Sku{
		currencySKU("od", "redis-basic Cloud Memorystore Redis", "OnDemand", region, "EUR", int64(onDemandHourly*1e9)),
		currencySKU("cud", "redis-basic Cloud Memorystore Redis commitment 1yr", "Commit1Yr", region, "EUR", int64(commitmentHourly*1e9)),
		currencySKU("cud-3y", "redis-basic Cloud Memorystore Redis commitment 3yr", "Commit3Yr", region, "EUR", int64(commitmentHourly*1e9)),
	}}})

	for term, months := range map[string]float64{"1yr": 12, "3yr": 36} {
		for _, payment := range []string{"monthly", "no-upfront"} {
			t.Run(term+"/"+payment, func(t *testing.T) {
				t.Parallel()
				rec := common.Recommendation{ResourceType: "redis-basic", Region: region, Term: term, PaymentOption: payment}
				quote, err := client.GetOfferingDetails(ctx, rec)
				require.NoError(t, err)
				assert.Equal(t, "EUR", quote.Currency)
				assert.InDelta(t, 1200*months/12, quote.TotalCost, 0.0001)
				assert.Zero(t, quote.UpfrontCost)
				assert.InDelta(t, 100, quote.RecurringCost, 0.00001)
			})
		}
	}
}

// performanceRecommender records ListRecommendations calls and answers with an
// ACTIVE cost-saving recommendation, the PerformanceRecommender shape that was
// relabeled as a 1yr CUD before issue #294.
type performanceRecommender struct{ lists int }

func (r *performanceRecommender) ListRecommendations(_ context.Context, _ *recommenderpb.ListRecommendationsRequest) RecommenderIterator {
	r.lists++
	return &MockRecommenderIterator{recommendations: []*recommenderpb.Recommendation{{
		Name:      "projects/test-project/locations/us-central1/recommenders/google.memorystore.redis.PerformanceRecommender/recommendations/r1",
		StateInfo: &recommenderpb.RecommendationStateInfo{State: recommenderpb.RecommendationStateInfo_ACTIVE},
		PrimaryImpact: &recommenderpb.Impact{
			Category: recommenderpb.Impact_COST,
			Projection: &recommenderpb.Impact_CostProjection{CostProjection: &recommenderpb.CostProjection{
				Cost: &money.Money{CurrencyCode: "USD", Units: -100},
			}},
		},
		Content: &recommenderpb.RecommendationContent{OperationGroups: []*recommenderpb.OperationGroup{{
			Operations: []*recommenderpb.Operation{{Resource: "projects/test-project/locations/us-central1/instances/BASIC"}},
		}}},
	}}}
}

func (r *performanceRecommender) Close() error { return nil }

// TestMemorystoreClient_GetRecommendations_NotSupported is the issue #294 regression
// test: Memorystore CUDs are spend-based and recommended only at billing-account
// scope, so the client must not query the project PerformanceRecommender or
// relabel its per-instance advice as a CUD. It fails loud instead of returning
// an empty list that would read as "nothing to buy".
func TestMemorystoreClient_GetRecommendations_NotSupported(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	client, err := NewClient(ctx, "test-project", "us-central1")
	require.NoError(t, err)
	recommender := &performanceRecommender{}
	client.SetRecommenderClient(recommender)

	recs, err := client.GetRecommendations(ctx, &common.RecommendationParams{})

	require.Error(t, err)
	assert.ErrorIs(t, err, common.ErrCommitmentPurchaseNotSupported)
	assert.Nil(t, recs)
	assert.Zero(t, recommender.lists, "the PerformanceRecommender must not be queried")
}
