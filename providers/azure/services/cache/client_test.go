package cache

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/consumption/armconsumption"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/redis/armredis/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	azpricing "github.com/LeanerCloud/cloud-commitments-go/providers/azure/internal/pricing"
	"github.com/LeanerCloud/cloud-commitments-go/providers/azure/mocks"
	"github.com/LeanerCloud/cloud-commitments-go/providers/azure/services/internal/reservations/reservationstest"
)

// MockReservationsDetailsPager mocks the ReservationsDetailsPager interface.
type MockReservationsDetailsPager struct {
	pages []armconsumption.ReservationsDetailsClientListResponse
	index int
	err   error
}

func (m *MockReservationsDetailsPager) More() bool {
	return m.index < len(m.pages)
}

func (m *MockReservationsDetailsPager) NextPage(ctx context.Context) (armconsumption.ReservationsDetailsClientListResponse, error) {
	if m.err != nil {
		return armconsumption.ReservationsDetailsClientListResponse{}, m.err
	}
	if m.index >= len(m.pages) {
		return armconsumption.ReservationsDetailsClientListResponse{}, errors.New("no more pages")
	}
	page := m.pages[m.index]
	m.index++
	return page, nil
}

// MockRedisCachesPager mocks the RedisCachesPager interface.
type MockRedisCachesPager struct {
	pages []armredis.ClientListBySubscriptionResponse
	index int
	err   error
}

func (m *MockRedisCachesPager) More() bool {
	return m.index < len(m.pages)
}

func (m *MockRedisCachesPager) NextPage(ctx context.Context) (armredis.ClientListBySubscriptionResponse, error) {
	if m.err != nil {
		return armredis.ClientListBySubscriptionResponse{}, m.err
	}
	if m.index >= len(m.pages) {
		return armredis.ClientListBySubscriptionResponse{}, errors.New("no more pages")
	}
	page := m.pages[m.index]
	m.index++
	return page, nil
}

func createSampleRedisPricingResponse() string {
	return `{
		"Items": [
			{
				"currencyCode": "USD",
				"retailPrice": 350.0,
				"unitPrice": 350.0,
				"armRegionName": "eastus",
				"productName": "Azure Redis Cache Premium",
				"serviceName": "Redis Cache",
				"armSkuName": "Azure_Redis_Cache_Premium_P1_Cache",
				"meterName": "P1 Cache Instance",
				"skuName": "P1",
				"unitOfMeasure": "1 Hour",
				"reservationTerm": "1 Year",
				"type": "Reservation"
			},
			{
				"currencyCode": "USD",
				"retailPrice": 850.0,
				"unitPrice": 850.0,
				"armRegionName": "eastus",
				"productName": "Azure Redis Cache Premium",
				"serviceName": "Redis Cache",
				"armSkuName": "Azure_Redis_Cache_Premium_P1_Cache",
				"meterName": "P1 Cache Instance",
				"skuName": "P1",
				"unitOfMeasure": "1 Hour",
				"reservationTerm": "3 Years",
				"type": "Reservation"
			},
			{
				"currencyCode": "USD",
				"retailPrice": 0.125,
				"unitPrice": 0.125,
				"armRegionName": "eastus",
				"productName": "Azure Cache for Redis",
				"serviceName": "Azure Cache for Redis",
				"armSkuName": "Premium_P1",
				"type": "Consumption"
			}
		]
	}`
}

func TestNewClient(t *testing.T) {
	t.Parallel()
	client := NewClient(nil, "test-subscription", "eastus")

	require.NotNil(t, client)
	assert.Equal(t, "test-subscription", client.subscriptionID)
	assert.Equal(t, "eastus", client.region)
	assert.NotNil(t, client.httpClient)
}

func TestCacheClient_GetServiceType(t *testing.T) {
	t.Parallel()
	client := NewClient(nil, "sub", "region")
	assert.Equal(t, common.ServiceCache, client.GetServiceType())
}

func TestCacheClient_GetRegion(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		region   string
		expected string
	}{
		{
			name:     "East US",
			region:   "eastus",
			expected: "eastus",
		},
		{
			name:     "West Europe",
			region:   "westeurope",
			expected: "westeurope",
		},
		{
			name:     "Australia East",
			region:   "australiaeast",
			expected: "australiaeast",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := NewClient(nil, "sub", tt.region)
			assert.Equal(t, tt.expected, client.GetRegion())
		})
	}
}

func TestCacheClient_GetValidResourceTypes_Fallback(t *testing.T) {
	t.Parallel()
	// When API calls fail, GetValidResourceTypes should return common SKUs
	client := NewClient(nil, "invalid-subscription", "eastus")
	client.SetRedisCachesPager(&MockRedisCachesPager{})

	skus, err := client.GetValidResourceTypes(context.Background())
	require.NoError(t, err)
	require.NotEmpty(t, skus)

	// Should contain standard Redis Cache SKUs
	assert.Contains(t, skus, "Basic_C0")
	assert.Contains(t, skus, "Standard_C1")
	assert.Contains(t, skus, "Premium_P1")
}

func TestAzureRetailPriceStructure(t *testing.T) {
	t.Parallel()
	price := AzureRetailPrice{
		Items: []azpricing.RetailPriceItem{
			{
				CurrencyCode:    "USD",
				RetailPrice:     100.0,
				UnitPrice:       95.0,
				ArmRegionName:   "eastus",
				ProductName:     "Azure Cache for Redis",
				ServiceName:     "Azure Cache for Redis",
				ArmSKUName:      "Premium_P1",
				MeterName:       "P1 Instance",
				ReservationTerm: "1 Year",
				Type:            "Reservation",
			},
		},
	}

	require.Len(t, price.Items, 1)
	assert.Equal(t, "USD", price.Items[0].CurrencyCode)
	assert.Equal(t, 100.0, price.Items[0].RetailPrice)
	assert.Equal(t, "Premium_P1", price.Items[0].ArmSKUName)
}

func TestRedisPricingStructure(t *testing.T) {
	t.Parallel()
	pricing := RedisPricing{
		HourlyRate:       0.50,
		ReservationPrice: 4380.0, // 1 year
		Currency:         "USD",
	}

	assert.Equal(t, 0.50, pricing.HourlyRate)
	assert.Equal(t, 4380.0, pricing.ReservationPrice)
	assert.Equal(t, "USD", pricing.Currency)
}

func TestNewClientWithHTTP(t *testing.T) {
	t.Parallel()
	mockHTTP := &mocks.MockHTTPClient{}
	client := NewClientWithHTTP(nil, "test-subscription", "eastus", mockHTTP)

	require.NotNil(t, client)
	assert.Equal(t, "test-subscription", client.subscriptionID)
	assert.Equal(t, "eastus", client.region)
	assert.Equal(t, mockHTTP, client.httpClient)
}

func TestCacheClient_GetOfferingDetails_WithMock(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	mockHTTP := &mocks.MockHTTPClient{}
	client := NewClientWithHTTP(nil, "test-subscription", "eastus", mockHTTP)

	response1 := mocks.CreateMockHTTPResponse(http.StatusOK, createSampleRedisPricingResponse())
	t.Cleanup(func() {
		require.NoError(t, response1.Body.Close())
	})
	mockHTTP.On("Do", mock.Anything).Return(response1,
		nil,
	)

	rec := common.Recommendation{
		ResourceType:  "Premium_P1",
		Term:          "1yr",
		PaymentOption: "upfront",
	}

	details, err := client.GetOfferingDetails(ctx, rec)
	require.NoError(t, err)
	require.NotNil(t, details)
	assert.Equal(t, "Premium_P1", details.ResourceType)
	assert.Equal(t, "1yr", details.Term)
	assert.Equal(t, "USD", details.Currency)
}

func TestCacheClient_GetOfferingDetails_3YearTerm(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	mockHTTP := &mocks.MockHTTPClient{}
	client := NewClientWithHTTP(nil, "test-subscription", "eastus", mockHTTP)

	response2 := mocks.CreateMockHTTPResponse(http.StatusOK, createSampleRedisPricingResponse())
	t.Cleanup(func() {
		require.NoError(t, response2.Body.Close())
	})
	mockHTTP.On("Do", mock.Anything).Return(response2,
		nil,
	)

	rec := common.Recommendation{
		ResourceType:  "Premium_P1",
		Term:          "3yr",
		PaymentOption: "monthly",
	}

	details, err := client.GetOfferingDetails(ctx, rec)
	require.NoError(t, err)
	require.NotNil(t, details)
	assert.Equal(t, "3yr", details.Term)
	assert.Equal(t, "monthly", details.PaymentOption)
}

func TestCacheClient_GetOfferingDetails_NoUpfront(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	mockHTTP := &mocks.MockHTTPClient{}
	client := NewClientWithHTTP(nil, "test-subscription", "eastus", mockHTTP)

	response3 := mocks.CreateMockHTTPResponse(http.StatusOK, createSampleRedisPricingResponse())
	t.Cleanup(func() {
		require.NoError(t, response3.Body.Close())
	})
	mockHTTP.On("Do", mock.Anything).Return(response3,
		nil,
	)

	rec := common.Recommendation{
		ResourceType:  "Premium_P1",
		Term:          "1yr",
		PaymentOption: "no-upfront",
	}

	details, err := client.GetOfferingDetails(ctx, rec)
	require.NoError(t, err)
	require.NotNil(t, details)
	assert.Equal(t, float64(0), details.UpfrontCost)
	assert.Greater(t, details.RecurringCost, float64(0))
}

func TestCacheClient_GetOfferingDetails_APIError(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	mockHTTP := &mocks.MockHTTPClient{}
	client := NewClientWithHTTP(nil, "test-subscription", "eastus", mockHTTP)

	response4 := mocks.CreateMockHTTPResponse(http.StatusInternalServerError, "Internal Server Error")
	t.Cleanup(func() {
		require.NoError(t, response4.Body.Close())
	})
	mockHTTP.On("Do", mock.Anything).Return(response4,
		nil,
	)

	rec := common.Recommendation{
		ResourceType:  "Premium_P1",
		Term:          "1yr",
		PaymentOption: "upfront",
	}

	_, err := client.GetOfferingDetails(ctx, rec)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "pricing API returned status 500")
}

func TestCacheClient_GetOfferingDetails_NoPricing(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	mockHTTP := &mocks.MockHTTPClient{}
	client := NewClientWithHTTP(nil, "test-subscription", "eastus", mockHTTP)

	response5 := mocks.CreateMockHTTPResponse(http.StatusOK, `{"Items": []}`)
	t.Cleanup(func() {
		require.NoError(t, response5.Body.Close())
	})
	mockHTTP.On("Do", mock.Anything).Return(response5,
		nil,
	)

	rec := common.Recommendation{
		ResourceType:  "Premium_P1",
		Term:          "1yr",
		PaymentOption: "upfront",
	}

	_, err := client.GetOfferingDetails(ctx, rec)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no pricing data found")
}

// TestCacheClient_GetOfferingDetails_NoReservationPricing verifies that when
// on-demand pricing is present but no reservation line is returned, the client
// returns an error rather than fabricating a price from the hardcoded 0.45
// multiplier (issue #1020 H4). Pre-fix this would have silently surfaced a
// fabricated TotalCost/SavingsPercentage as a real quote.
func TestCacheClient_GetOfferingDetails_NoReservationPricing(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	onDemandOnly := `{
		"Items": [
			{
				"currencyCode": "USD",
				"retailPrice": 0.125,
				"unitPrice": 0.125,
				"armRegionName": "eastus",
				"armSkuName": "Premium_P1",
				"type": "Consumption"
			}
		],
		"NextPageLink": ""
	}`

	mockHTTP := &mocks.MockHTTPClient{}
	client := NewClientWithHTTP(nil, "test-subscription", "eastus", mockHTTP)
	response6 := mocks.CreateMockHTTPResponse(http.StatusOK, onDemandOnly)
	t.Cleanup(func() {
		require.NoError(t, response6.Body.Close())
	})
	mockHTTP.On("Do", mock.Anything).Return(response6, nil)

	rec := common.Recommendation{
		ResourceType:  "Premium_P1",
		Term:          "1yr",
		PaymentOption: "upfront",
	}
	_, err := client.GetOfferingDetails(ctx, rec)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no reservation pricing found")
}

func TestCacheClient_GetExistingCommitments_Empty(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	client := NewClient(nil, "test-subscription", "eastus")

	// Mock pager returns no pages — the empty-subscription case, distinct
	// from the pager-error case below. Previously this test used a nil
	// pager and relied on silent error-swallowing; that behavior was
	// unsafe and has been replaced with error propagation, so the test
	// now uses an explicit empty mock.
	client.SetReservationsPager(&MockReservationsDetailsPager{})

	commitments, err := client.GetExistingCommitments(ctx)
	require.NoError(t, err)
	assert.Empty(t, commitments)
}

func TestCacheClient_GetExistingCommitments_WithMockPager(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	client := NewClient(nil, "test-subscription", "eastus")

	reservationID := "test-reservation-123"
	skuName := "redis-premium-p1"

	// Create mock pager with Redis commitment
	mockPager := &MockReservationsDetailsPager{
		pages: []armconsumption.ReservationsDetailsClientListResponse{
			{
				ReservationDetailsListResult: armconsumption.ReservationDetailsListResult{
					Value: []*armconsumption.ReservationDetail{
						{
							Properties: &armconsumption.ReservationDetailProperties{
								ReservationID: &reservationID,
								SKUName:       &skuName,
							},
						},
					},
				},
			},
		},
	}

	client.SetReservationsPager(mockPager)

	commitments, err := client.GetExistingCommitments(ctx)
	require.NoError(t, err)
	require.Len(t, commitments, 1)
	assert.Equal(t, reservationID, commitments[0].CommitmentID)
	assert.Equal(t, skuName, commitments[0].ResourceType)
	assert.Equal(t, common.ServiceCache, commitments[0].Service)
}

// TestCacheClient_GetExistingCommitments_DedupesDailyUsageRows is the
// regression test for #73: armconsumption.ReservationsDetails is a daily
// usage API, one row per reservation per usage day. A pager returning three
// rows for the same ReservationID (simulating a reservation held across
// three usage days) must surface as a single commitment, not three.
func TestCacheClient_GetExistingCommitments_DedupesDailyUsageRows(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	client := NewClient(nil, "test-subscription", "eastus")

	reservationID := "test-reservation-123"
	skuName := "redis-premium-p1"

	dailyRow := &armconsumption.ReservationDetail{
		Properties: &armconsumption.ReservationDetailProperties{
			ReservationID: &reservationID,
			SKUName:       &skuName,
		},
	}

	mockPager := &MockReservationsDetailsPager{
		pages: []armconsumption.ReservationsDetailsClientListResponse{
			{
				ReservationDetailsListResult: armconsumption.ReservationDetailsListResult{
					Value: []*armconsumption.ReservationDetail{dailyRow, dailyRow, dailyRow},
				},
			},
		},
	}

	client.SetReservationsPager(mockPager)

	commitments, err := client.GetExistingCommitments(ctx)
	require.NoError(t, err)
	require.Len(t, commitments, 1, "three daily rows for one reservation must collapse to one commitment (issue #73)")
	assert.Equal(t, reservationID, commitments[0].CommitmentID)
}

func TestCacheClient_GetExistingCommitments_FilterNonRedis(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	client := NewClient(nil, "test-subscription", "eastus")

	// Test that non-Redis SKUs are filtered out
	nonRedisSKU := "sql-standard-s1"
	redisSKU := "redis-premium-p2"
	reservationID1 := "test-reservation-1"
	reservationID2 := "test-reservation-2"

	mockPager := &MockReservationsDetailsPager{
		pages: []armconsumption.ReservationsDetailsClientListResponse{
			{
				ReservationDetailsListResult: armconsumption.ReservationDetailsListResult{
					Value: []*armconsumption.ReservationDetail{
						{
							Properties: &armconsumption.ReservationDetailProperties{
								ReservationID: &reservationID1,
								SKUName:       &nonRedisSKU,
							},
						},
						{
							Properties: &armconsumption.ReservationDetailProperties{
								ReservationID: &reservationID2,
								SKUName:       &redisSKU,
							},
						},
					},
				},
			},
		},
	}

	client.SetReservationsPager(mockPager)

	commitments, err := client.GetExistingCommitments(ctx)
	require.NoError(t, err)
	require.Len(t, commitments, 1)
	assert.Equal(t, reservationID2, commitments[0].CommitmentID)
}

func TestCacheClient_GetExistingCommitments_NilProperties(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	client := NewClient(nil, "test-subscription", "eastus")

	// Test that nil properties are handled gracefully
	mockPager := &MockReservationsDetailsPager{
		pages: []armconsumption.ReservationsDetailsClientListResponse{
			{
				ReservationDetailsListResult: armconsumption.ReservationDetailsListResult{
					Value: []*armconsumption.ReservationDetail{
						{
							Properties: nil,
						},
					},
				},
			},
		},
	}

	client.SetReservationsPager(mockPager)

	commitments, err := client.GetExistingCommitments(ctx)
	require.NoError(t, err)
	assert.Empty(t, commitments)
}

func TestCacheClient_GetExistingCommitments_PagerError(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	client := NewClient(nil, "test-subscription", "eastus")

	// Pagination errors must propagate — see the database client for the
	// full rationale (partial lists are unsafe for the purchase flow).
	mockPager := &MockReservationsDetailsPager{
		pages: []armconsumption.ReservationsDetailsClientListResponse{
			{},
		},
		err: errors.New("API error"),
	}

	client.SetReservationsPager(mockPager)

	commitments, err := client.GetExistingCommitments(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cache: list reservations")
	assert.Nil(t, commitments)
}

func TestCacheClient_GetValidResourceTypes_WithMockPager(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	client := NewClient(nil, "test-subscription", "eastus")

	skuName := armredis.SKUNamePremium
	skuFamily := armredis.SKUFamilyP
	capacity := int32(1)

	// Create mock pager with Redis caches
	mockPager := &MockRedisCachesPager{
		pages: []armredis.ClientListBySubscriptionResponse{
			{
				ListResult: armredis.ListResult{
					Value: []*armredis.ResourceInfo{
						{
							Properties: &armredis.Properties{
								SKU: &armredis.SKU{
									Name:     &skuName,
									Family:   &skuFamily,
									Capacity: &capacity,
								},
							},
						},
					},
				},
			},
		},
	}

	client.SetRedisCachesPager(mockPager)

	skus, err := client.GetValidResourceTypes(ctx)
	require.NoError(t, err)
	require.Len(t, skus, 1)
	assert.Equal(t, "Premium_P1", skus[0])
}

func TestCacheClient_GetValidResourceTypes_MultipleCaches(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	client := NewClient(nil, "test-subscription", "eastus")

	premiumName := armredis.SKUNamePremium
	standardName := armredis.SKUNameStandard
	familyP := armredis.SKUFamilyP
	familyC := armredis.SKUFamilyC
	capacity1 := int32(1)
	capacity2 := int32(2)

	mockPager := &MockRedisCachesPager{
		pages: []armredis.ClientListBySubscriptionResponse{
			{
				ListResult: armredis.ListResult{
					Value: []*armredis.ResourceInfo{
						{
							Properties: &armredis.Properties{
								SKU: &armredis.SKU{
									Name:     &premiumName,
									Family:   &familyP,
									Capacity: &capacity1,
								},
							},
						},
						{
							Properties: &armredis.Properties{
								SKU: &armredis.SKU{
									Name:     &standardName,
									Family:   &familyC,
									Capacity: &capacity2,
								},
							},
						},
					},
				},
			},
		},
	}

	client.SetRedisCachesPager(mockPager)

	skus, err := client.GetValidResourceTypes(ctx)
	require.NoError(t, err)
	require.Len(t, skus, 2)
	assert.Contains(t, skus, "Premium_P1")
	assert.Contains(t, skus, "Standard_C2")
}

func TestCacheClient_GetValidResourceTypes_PagerError(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	client := NewClient(nil, "test-subscription", "eastus")

	// Test that pager errors result in fallback to common SKUs
	mockPager := &MockRedisCachesPager{
		pages: []armredis.ClientListBySubscriptionResponse{{}},
		err:   errors.New("API error"),
	}

	client.SetRedisCachesPager(mockPager)

	skus, err := client.GetValidResourceTypes(ctx)
	require.NoError(t, err)
	// Should fall back to common SKUs
	assert.Contains(t, skus, "Premium_P1")
	assert.Contains(t, skus, "Standard_C1")
	assert.Contains(t, skus, "Basic_C0")
}

func TestCacheClient_GetValidResourceTypes_EmptyResults(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	client := NewClient(nil, "test-subscription", "eastus")

	// Test that empty results fall back to common SKUs
	mockPager := &MockRedisCachesPager{
		pages: []armredis.ClientListBySubscriptionResponse{
			{
				ListResult: armredis.ListResult{
					Value: []*armredis.ResourceInfo{},
				},
			},
		},
	}

	client.SetRedisCachesPager(mockPager)

	skus, err := client.GetValidResourceTypes(ctx)
	require.NoError(t, err)
	// Should fall back to common SKUs
	assert.Contains(t, skus, "Premium_P1")
}

func TestCacheClient_SetterMethods(t *testing.T) {
	t.Parallel()
	client := NewClient(nil, "test-subscription", "eastus")

	// Test SetReservationsPager
	mockResPager := &MockReservationsDetailsPager{}
	client.SetReservationsPager(mockResPager)
	assert.Equal(t, mockResPager, client.reservationsPager)

	// Test SetRedisCachesPager
	mockRedisPager := &MockRedisCachesPager{}
	client.SetRedisCachesPager(mockRedisPager)
	assert.Equal(t, mockRedisPager, client.redisCachesPager)
}

func TestCacheClient_GetCommonSKUs(t *testing.T) {
	t.Parallel()
	client := NewClient(nil, "test-subscription", "eastus")
	skus := client.getCommonSKUs()

	assert.Contains(t, skus, "Basic_C0")
	assert.Contains(t, skus, "Basic_C6")
	assert.Contains(t, skus, "Standard_C0")
	assert.Contains(t, skus, "Standard_C6")
	assert.Contains(t, skus, "Premium_P1")
	assert.Contains(t, skus, "Premium_P5")
}

// Test the to package is properly imported (used in tests).
var _ = to.Ptr("test")

// infiniteRedisCachesPager is a pager that always reports More()=true,
// used to exercise the maxCachesPages budget cap.
type infiniteRedisCachesPager struct{}

func (p *infiniteRedisCachesPager) More() bool { return true }
func (p *infiniteRedisCachesPager) NextPage(_ context.Context) (armredis.ClientListBySubscriptionResponse, error) {
	return armredis.ClientListBySubscriptionResponse{}, nil
}

// TestCacheClient_GetValidResourceTypes_CtxCancelReturnsError asserts that a
// canceled context is treated as a hard stop and surfaces an error rather than
// returning a silent partial result (feedback_ctx_cancel_terminal).
func TestCacheClient_GetValidResourceTypes_CtxCancelReturnsError(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	client := NewClient(nil, "test-subscription", "eastus")
	client.SetRedisCachesPager(&infiniteRedisCachesPager{})

	_, err := client.GetValidResourceTypes(ctx)
	require.ErrorIs(t, err, context.Canceled, "canceled context must produce an error, not a silent partial result")
}

// TestCacheClient_GetValidResourceTypes_PageCapReturnsError asserts that an
// unbounded pager is stopped after maxCachesPages iterations with a loud error,
// never a partial or fallback result.
func TestCacheClient_GetValidResourceTypes_PageCapReturnsError(t *testing.T) {
	t.Parallel()
	pager := &countingRedisPager{limit: 1000}
	client := NewClient(nil, "test-subscription", "eastus")
	client.SetRedisCachesPager(pager)

	skus, err := client.GetValidResourceTypes(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "pagination cap")
	assert.Nil(t, skus)
	assert.Equal(t, maxCachesPages, pager.calls)
}

// TestCacheClient_GetValidResourceTypes_ExactlyCapPagesSucceeds asserts the cap
// is an error only when more pages remain after maxCachesPages.
func TestCacheClient_GetValidResourceTypes_ExactlyCapPagesSucceeds(t *testing.T) {
	t.Parallel()
	pager := &finalPartialRedisPager{pages: maxCachesPages, perPage: 2}
	client := NewClient(nil, "test-subscription", "eastus")
	client.SetRedisCachesPager(pager)

	skus, err := client.GetValidResourceTypes(context.Background())
	require.NoError(t, err)
	assert.Len(t, skus, 2*(maxCachesPages-1)+1)
}

// TestNewClientWithHTTP_NilFallbackIsHardened verifies that passing nil as the
// httpClient falls back to the SSRF-hardened httpclient.New(), which rejects
// IMDS connections, instead of leaving a nil client that panics on first use.
func TestNewClientWithHTTP_NilFallbackIsHardened(t *testing.T) {
	t.Parallel()
	c := NewClientWithHTTP(nil, "sub-123", "eastus", nil)
	require.NotNil(t, c.httpClient)
	require.NotEqual(t, http.DefaultClient, c.httpClient,
		"nil fallback must never be http.DefaultClient")
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://169.254.169.254/metadata/instance", nil)
	require.NoError(t, err)
	resp, err := c.httpClient.Do(req)
	if resp != nil {
		defer resp.Body.Close()
	}
	require.Error(t, err, "nil-fallback client must reject IMDS connections")
	assert.Contains(t, err.Error(), "blocked")
}

func TestCacheClient_GetExistingCommitments_DoesNotInventRegionOrState(t *testing.T) {
	t.Parallel()
	for _, region := range []string{"eastus", "westeurope"} {
		t.Run(region, func(t *testing.T) {
			t.Parallel()
			client := NewClient(nil, "test-subscription", region)
			reservationID, skuName := "res-1", "redis-premium-p1"
			client.SetReservationsPager(&MockReservationsDetailsPager{
				pages: []armconsumption.ReservationsDetailsClientListResponse{{
					ReservationDetailsListResult: armconsumption.ReservationDetailsListResult{
						Value: []*armconsumption.ReservationDetail{{
							Properties: &armconsumption.ReservationDetailProperties{
								ReservationID: &reservationID,
								SKUName:       &skuName,
							},
						}},
					},
				}},
			})

			commitments, err := client.GetExistingCommitments(context.Background())

			require.NoError(t, err)
			require.Len(t, commitments, 1)
			reservationstest.AssertUnknownInventory(t, commitments[0])
		})
	}
}
