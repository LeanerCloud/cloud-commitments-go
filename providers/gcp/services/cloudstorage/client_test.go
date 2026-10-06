package cloudstorage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"cloud.google.com/go/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/cloudbilling/v1"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
)

// MockStorageService mocks the StorageService interface.
type MockStorageService struct {
	buckets      []*storage.BucketAttrs
	listErr      error
	bucketName   string
	createErr    error
	createCalled *bool
}

func (m *MockStorageService) Buckets(ctx context.Context, projectID string) BucketIterator {
	return &MockBucketIterator{buckets: m.buckets, err: m.listErr}
}

func (m *MockStorageService) Bucket(name string) BucketHandle {
	m.bucketName = name
	return &MockBucketHandle{createErr: m.createErr, createCalled: m.createCalled}
}

func (m *MockStorageService) Close() error {
	return nil
}

// MockBucketIterator mocks the BucketIterator interface.
type MockBucketIterator struct {
	buckets []*storage.BucketAttrs
	index   int
	err     error
}

func (m *MockBucketIterator) Next() (*storage.BucketAttrs, error) {
	if m.err != nil {
		return nil, m.err
	}
	if m.index >= len(m.buckets) {
		return nil, iterator.Done
	}
	b := m.buckets[m.index]
	m.index++
	return b, nil
}

// MockBucketHandle mocks the BucketHandle interface.
type MockBucketHandle struct {
	createErr    error
	createCalled *bool
}

func (m *MockBucketHandle) Create(ctx context.Context, projectID string, attrs *storage.BucketAttrs) error {
	if m.createCalled != nil {
		*m.createCalled = true
	}
	return m.createErr
}

// MockBillingService mocks the BillingService interface.
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

func TestNewClient(t *testing.T) {
	ctx := context.Background()
	client, err := NewClient(ctx, "test-project", "us-central1")

	require.NoError(t, err)
	require.NotNil(t, client)
	assert.Equal(t, "test-project", client.projectID)
	assert.Equal(t, "us-central1", client.region)
	assert.Equal(t, ctx, client.ctx)
}

func TestCloudStorageClient_GetServiceType(t *testing.T) {
	client := &Client{}
	assert.Equal(t, common.ServiceStorage, client.GetServiceType())
}

func TestCloudStorageClient_GetRegion(t *testing.T) {
	client := &Client{region: "europe-west1"}
	assert.Equal(t, "europe-west1", client.GetRegion())
}

func TestCloudStorageClient_GetValidResourceTypes(t *testing.T) {
	ctx := context.Background()
	client := &Client{
		ctx:       ctx,
		projectID: "test-project",
		region:    "us-central1",
	}

	types, err := client.GetValidResourceTypes(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, types)

	// Verify expected storage classes
	assert.Contains(t, types, "STANDARD")
	assert.Contains(t, types, "NEARLINE")
	assert.Contains(t, types, "COLDLINE")
	assert.Contains(t, types, "ARCHIVE")
	assert.Len(t, types, 4)
}

func TestCloudStorageClient_ValidateOffering_ValidClasses(t *testing.T) {
	ctx := context.Background()
	client := &Client{
		ctx:       ctx,
		projectID: "test-project",
		region:    "us-central1",
	}

	validClasses := []string{"STANDARD", "NEARLINE", "COLDLINE", "ARCHIVE"}

	for _, class := range validClasses {
		t.Run(class, func(t *testing.T) {
			rec := common.Recommendation{
				ResourceType: class,
			}
			err := client.ValidateOffering(ctx, rec)
			assert.NoError(t, err)
		})
	}
}

func TestCloudStorageClient_ValidateOffering_InvalidClass(t *testing.T) {
	ctx := context.Background()
	client := &Client{
		ctx:       ctx,
		projectID: "test-project",
		region:    "us-central1",
	}

	rec := common.Recommendation{
		ResourceType: "INVALID_CLASS",
	}

	err := client.ValidateOffering(ctx, rec)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid Cloud Storage class")
}

func TestStoragePricing_Fields(t *testing.T) {
	pricing := &StoragePricing{
		HourlyRate:        0.026,
		CommitmentPrice:   100.0,
		OnDemandPrice:     125.0,
		Currency:          "USD",
		SavingsPercentage: 20.0,
	}

	assert.Equal(t, 0.026, pricing.HourlyRate)
	assert.Equal(t, 100.0, pricing.CommitmentPrice)
	assert.Equal(t, 125.0, pricing.OnDemandPrice)
	assert.Equal(t, "USD", pricing.Currency)
	assert.Equal(t, 20.0, pricing.SavingsPercentage)
}

func TestSkuMatchesStorageClass(t *testing.T) {
	tests := []struct {
		name         string
		description  string
		storageClass string
		region       string
		regions      []string
		expected     bool
	}{
		{
			name:         "Matches description and region",
			description:  "Standard Storage in us-central1",
			storageClass: "Standard",
			region:       "us-central1",
			regions:      []string{"us-central1", "us-east1"},
			expected:     true,
		},
		{
			name:         "Matches description, wrong region",
			description:  "Standard Storage in us-central1",
			storageClass: "Standard",
			region:       "europe-west1",
			regions:      []string{"us-central1", "us-east1"},
			expected:     false,
		},
		{
			name:         "Doesn't match description",
			description:  "Nearline Storage in us-central1",
			storageClass: "Standard",
			region:       "us-central1",
			regions:      []string{"us-central1"},
			expected:     false,
		},
		{
			name:         "No regions specified - matches description only",
			description:  "Standard Storage multi-region",
			storageClass: "Standard",
			region:       "us-central1",
			regions:      nil,
			expected:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sku := &cloudbilling.Sku{
				Description:    tt.description,
				Category:       &cloudbilling.Category{UsageType: "OnDemand"},
				ServiceRegions: tt.regions,
			}
			result := skuMatchesStorageClass(sku, tt.storageClass, tt.region)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestCloudStorageClient_Fields(t *testing.T) {
	ctx := context.Background()
	client := &Client{
		ctx:       ctx,
		projectID: "my-project",
		region:    "asia-east1",
	}

	assert.Equal(t, ctx, client.ctx)
	assert.Equal(t, "my-project", client.projectID)
	assert.Equal(t, "asia-east1", client.region)
}

func TestCloudStorageClient_SetterMethods(t *testing.T) {
	ctx := context.Background()
	client, _ := NewClient(ctx, "test-project", "us-central1")

	// Test SetStorageService
	mockStorage := &MockStorageService{}
	client.SetStorageService(mockStorage)
	assert.Equal(t, mockStorage, client.storageService)

	// Test SetBillingService
	mockBilling := &MockBillingService{}
	client.SetBillingService(mockBilling)
	assert.Equal(t, mockBilling, client.billingService)
}

// TestCloudStorageClient_GetExistingCommitments_ReturnsEmpty asserts that
// GetExistingCommitments always returns an empty slice. GCS has no commitment
// API; enumerating regional buckets does not represent a commitment (10-L2).
func TestCloudStorageClient_GetExistingCommitments_ReturnsEmpty(t *testing.T) {
	ctx := context.Background()
	client, _ := NewClient(ctx, "test-project", "us-central1")

	commitments, err := client.GetExistingCommitments(ctx)
	require.NoError(t, err)
	assert.Empty(t, commitments, "Cloud Storage GetExistingCommitments must return empty (10-L2)")
}

// TestCloudStorageClient_PurchaseCommitment_NotSupported is the regression test for
// issue #640: Cloud Storage has no CUD or commitment purchase API, so
// PurchaseCommitment must return ErrCommitmentPurchaseNotSupported and MUST NOT call
// any resource-creation API (it previously created a new empty billable bucket).
func TestCloudStorageClient_PurchaseCommitment_NotSupported(t *testing.T) {
	ctx := context.Background()
	client, _ := NewClient(ctx, "test-project", "us-central1")

	createCalled := false
	mockService := &MockStorageService{createCalled: &createCalled}
	client.SetStorageService(mockService)

	rec := common.Recommendation{
		ResourceType:   "STANDARD",
		CommitmentCost: 100.0,
	}

	result, err := client.PurchaseCommitment(ctx, rec, common.PurchaseOptions{})

	require.Error(t, err)
	assert.ErrorIs(t, err, common.ErrCommitmentPurchaseNotSupported)
	assert.False(t, result.Success)
	assert.Empty(t, result.CommitmentID)
	assert.ErrorIs(t, result.Error, common.ErrCommitmentPurchaseNotSupported)
	// The critical guarantee: a "purchase" must never create infrastructure.
	assert.False(t, createCalled, "PurchaseCommitment must not call bucket Create")
}

// TestCloudStorageClient_GetRecommendations_NotSupported is the regression test
// for issue #78(c): the old path turned a bucket name from a CostRecommender
// operation into ResourceType, which pricing then treated as a storage class.
func TestCloudStorageClient_GetRecommendations_NotSupported(t *testing.T) {
	ctx := context.Background()
	client, _ := NewClient(ctx, "test-project", "us-central1")

	recs, err := client.GetRecommendations(ctx, &common.RecommendationParams{})
	require.ErrorIs(t, err, common.ErrCommitmentPurchaseNotSupported)
	assert.Nil(t, recs)
}

// storageMockSkus returns a slice with both an on-demand and a commitment SKU for
// the given storage class and region. Required by tests that exercise GetOfferingDetails
// after the issue #1020 fix (fabricated commitment prices are no longer allowed).
func storageMockSkus(storageClass string, onDemandNanos, commitmentNanos int64) []*cloudbilling.Sku {
	const region = "us-central1"
	return []*cloudbilling.Sku{
		{
			Description:    storageClass + " Storage in " + region,
			Category:       &cloudbilling.Category{UsageType: "OnDemand"},
			ServiceRegions: []string{region},
			PricingInfo: []*cloudbilling.PricingInfo{
				{
					PricingExpression: &cloudbilling.PricingExpression{
						UsageUnit: "GiBy.mo",
						TieredRates: []*cloudbilling.TierRate{
							{
								UnitPrice: &cloudbilling.Money{
									Units:        0,
									Nanos:        onDemandNanos,
									CurrencyCode: "USD",
								},
							},
						},
					},
				},
			},
		},
		{
			Description:    storageClass + " Storage commitment in " + region,
			Category:       &cloudbilling.Category{UsageType: "Commit1Yr"},
			ServiceRegions: []string{region},
			PricingInfo: []*cloudbilling.PricingInfo{
				{
					PricingExpression: &cloudbilling.PricingExpression{
						UsageUnit: "GiBy.mo",
						TieredRates: []*cloudbilling.TierRate{
							{
								UnitPrice: &cloudbilling.Money{
									Units:        0,
									Nanos:        commitmentNanos,
									CurrencyCode: "USD",
								},
							},
						},
					},
				},
			},
		},
	}
}

func TestCloudStorageClient_GetOfferingDetails_WithMock(t *testing.T) {
	ctx := context.Background()
	client, _ := NewClient(ctx, "test-project", "us-central1")

	// Both on-demand and commitment SKUs required after the issue #1020 fix.
	mockService := &MockBillingService{
		skus: &cloudbilling.ListSkusResponse{
			Skus: storageMockSkus("STANDARD", 26000000, 19500000),
		},
	}
	client.SetBillingService(mockService)

	rec := common.Recommendation{
		ResourceType:  "STANDARD",
		Term:          "1yr",
		PaymentOption: "upfront",
	}

	details, err := client.GetOfferingDetails(ctx, rec)
	require.NoError(t, err)
	assert.Equal(t, "STANDARD", details.ResourceType)
	assert.Equal(t, "1yr", details.Term)
	assert.Equal(t, "USD", details.Currency)
	assert.Greater(t, details.TotalCost, float64(0))
	assert.Greater(t, details.UpfrontCost, float64(0))
	assert.Equal(t, float64(0), details.RecurringCost)
}

func TestCloudStorageClient_GetOfferingDetails_3yr(t *testing.T) {
	ctx := context.Background()
	client, _ := NewClient(ctx, "test-project", "us-central1")

	// Both on-demand and commitment SKUs required after the issue #1020 fix.
	mockService := &MockBillingService{
		skus: &cloudbilling.ListSkusResponse{
			Skus: storageMockSkus("NEARLINE", 10000000, 7000000),
		},
	}
	client.SetBillingService(mockService)

	rec := common.Recommendation{
		ResourceType:  "NEARLINE",
		Term:          "3yr",
		PaymentOption: "monthly",
	}

	details, err := client.GetOfferingDetails(ctx, rec)
	require.NoError(t, err)
	assert.Equal(t, "NEARLINE", details.ResourceType)
	assert.Equal(t, "3yr", details.Term)
	assert.Equal(t, float64(0), details.UpfrontCost)
	assert.Greater(t, details.RecurringCost, float64(0))
}

func TestCloudStorageClient_GetOfferingDetails_NoPricing(t *testing.T) {
	ctx := context.Background()
	client, _ := NewClient(ctx, "test-project", "us-central1")

	mockService := &MockBillingService{
		skus: &cloudbilling.ListSkusResponse{
			Skus: []*cloudbilling.Sku{},
		},
	}
	client.SetBillingService(mockService)

	rec := common.Recommendation{
		ResourceType: "STANDARD",
	}

	_, err := client.GetOfferingDetails(ctx, rec)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no pricing found")
}

func TestCloudStorageClient_GetOfferingDetails_BillingError(t *testing.T) {
	ctx := context.Background()
	client, _ := NewClient(ctx, "test-project", "us-central1")

	mockService := &MockBillingService{
		err: errors.New("billing API error"),
	}
	client.SetBillingService(mockService)

	rec := common.Recommendation{
		ResourceType: "STANDARD",
	}

	_, err := client.GetOfferingDetails(ctx, rec)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to list SKUs")
}

func TestCloudStorageClient_GetOfferingDetails_DefaultPaymentOption(t *testing.T) {
	ctx := context.Background()
	client, _ := NewClient(ctx, "test-project", "us-central1")

	// Both on-demand and commitment SKUs required after the issue #1020 fix.
	mockService := &MockBillingService{
		skus: &cloudbilling.ListSkusResponse{
			Skus: storageMockSkus("STANDARD", 26000000, 19500000),
		},
	}
	client.SetBillingService(mockService)

	rec := common.Recommendation{
		ResourceType:  "STANDARD",
		Term:          "1yr",
		PaymentOption: "unknown", // Default case
	}

	details, err := client.GetOfferingDetails(ctx, rec)
	require.NoError(t, err)
	assert.Greater(t, details.UpfrontCost, float64(0))
}

func TestCloudStorageClient_GetStoragePricing_WithCommitmentPrice(t *testing.T) {
	ctx := context.Background()
	client, _ := NewClient(ctx, "test-project", "us-central1")
	client.SetBillingService(&MockBillingService{skus: &cloudbilling.ListSkusResponse{
		Skus: storageMockSkus("STANDARD", 26000000, 20000000),
	}})
	pricing, err := client.getStoragePricing(ctx, "STANDARD", "us-central1", 1)
	require.NoError(t, err)
	assert.Equal(t, "USD", pricing.Currency)
	assert.InDelta(t, 0.24, pricing.CommitmentPrice, 1e-12)
	assert.InDelta(t, 0.312, pricing.OnDemandPrice, 1e-12)
	assert.InDelta(t, 0.02/730, pricing.HourlyRate, 1e-12)
	assert.InDelta(t, 100.0*6/26, pricing.SavingsPercentage, 1e-10)
}

func TestCloudStorageClient_GetStoragePricing_3Year(t *testing.T) {
	ctx := context.Background()
	client, _ := NewClient(ctx, "test-project", "us-central1")

	// Both on-demand and commitment SKUs required after the issue #1020 fix:
	// without a commitment SKU, getStoragePricing returns an error.
	mockService := &MockBillingService{
		skus: &cloudbilling.ListSkusResponse{
			Skus: storageMockSkus("STANDARD", 26000000, 18200000),
		},
	}
	client.SetBillingService(mockService)

	pricing, err := client.getStoragePricing(ctx, "STANDARD", "us-central1", 3)
	require.NoError(t, err)
	assert.Greater(t, pricing.SavingsPercentage, float64(0))
	assert.Greater(t, pricing.OnDemandPrice, float64(0))
	assert.Greater(t, pricing.CommitmentPrice, float64(0))
}

func TestSkuMatchesStorageClass_CaseInsensitive(t *testing.T) {
	sku := &cloudbilling.Sku{
		Description:    "STANDARD Storage in Americas",
		Category:       &cloudbilling.Category{UsageType: "OnDemand"},
		ServiceRegions: []string{"us-central1"},
	}
	assert.True(t, skuMatchesStorageClass(sku, "standard", "us-central1"))
}

func TestSkuMatchesStorageClass_Capacity(t *testing.T) {
	for _, tc := range []struct {
		class       string
		description string
		want        bool
	}{
		{"STANDARD", "Standard Storage Doha", true},
		{"NEARLINE", "Nearline Storage Doha", true},
		{"COLDLINE", "Coldline Storage Doha", true},
		{"ARCHIVE", "Archive Storage Doha", true},
		{"STANDARD", "Regional Standard Class A Operations", false},
		{"NEARLINE", "Nearline Data Retrieval", false},
		{"NEARLINE", "Nearline Storage Doha (Early Delete)", false},
		{"COLDLINE", "Coldline Storage Doha (Early Delete)", false},
		{"ARCHIVE", "Archive Storage Doha (Early Delete)", false},
	} {
		t.Run(tc.description, func(t *testing.T) {
			sku := &cloudbilling.Sku{Description: tc.description, ServiceRegions: []string{"me-central1"}}
			assert.Equal(t, tc.want, skuMatchesStorageClass(sku, tc.class, "me-central1"))
		})
	}
}

func TestStoragePricingUnits_PublicConsumers(t *testing.T) {
	for _, tc := range []struct {
		name           string
		onDemandUnit   string
		commitmentUnit string
		onDemandNanos  int64
		commitNanos    int64
		monthlyDemand  float64
		monthlyCommit  float64
	}{
		{"monthly", "GiBy.mo", "GiBy.mo", 26000000, 20000000, 0.026, 0.020},
		{"hourly", "GiBy.h", "GiBy.h", 26000, 20000, 0.01898, 0.0146},
		{"monthly-demand", "GiBy.mo", "GiBy.h", 26000000, 20000, 0.026, 0.0146},
		{"hourly-demand", "GiBy.h", "GiBy.mo", 40000, 20000000, 0.0292, 0.020},
	} {
		t.Run(tc.name, func(t *testing.T) {
			skus := storageMockSkus("STANDARD", tc.onDemandNanos, tc.commitNanos)
			skus[0].PricingInfo[0].PricingExpression.UsageUnit = tc.onDemandUnit
			skus[1].PricingInfo[0].PricingExpression.UsageUnit = tc.commitmentUnit
			skus[0].PricingInfo[0].PricingExpression.DisplayQuantity = 1000
			unrelated := storageMockSkus("NEARLINE", 26000000, 20000000)[0]
			unrelated.PricingInfo[0].PricingExpression.UsageUnit = "unknown"
			skus = append(skus, unrelated)
			operation := storageMockSkus("STANDARD", 5000000, 0)[0]
			operation.Description = "Regional Standard Class A Operations"
			operation.PricingInfo[0].PricingExpression.UsageUnit = "count"
			skus = append([]*cloudbilling.Sku{operation}, skus...)
			skus = append(skus, operation)
			client := storageCatalogClient(t, skus)
			for _, term := range []struct {
				label  string
				months float64
			}{{"1yr", 12}, {"3yr", 36}} {
				t.Run(term.label, func(t *testing.T) {
					rec := common.Recommendation{ResourceType: "STANDARD", Term: term.label}
					for _, payment := range []string{"monthly", "all-upfront"} {
						rec.PaymentOption = payment
						details, err := client.GetOfferingDetails(t.Context(), rec)
						require.NoError(t, err)
						assert.InDelta(t, tc.monthlyCommit*term.months, details.TotalCost, 1e-12)
						assert.InDelta(t, tc.monthlyCommit/730, details.EffectiveHourlyRate, 1e-12)
						if payment == "monthly" {
							assert.Zero(t, details.UpfrontCost)
							assert.InDelta(t, tc.monthlyCommit, details.RecurringCost, 1e-12)
						} else {
							assert.InDelta(t, details.TotalCost, details.UpfrontCost, 1e-12)
							assert.Zero(t, details.RecurringCost)
						}
					}
				})
			}
		})
	}
}

func TestStoragePricingUnits_InvalidPublicConsumers(t *testing.T) {
	for _, tc := range []struct {
		name string
		unit string
		sku  int
	}{
		{"empty-demand", "", 0},
		{"empty-commitment", "", 1},
		{"unknown-demand", "GiBy.d", 0},
		{"unknown-commitment", "GiBy", 1},
		{"count-demand", "count", 0},
		{"count-commitment", "count", 1},
		{"no-commitment", "", -1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			skus := storageMockSkus("STANDARD", 26000000, 20000000)
			wantError := "no commitment pricing found"
			if tc.sku < 0 {
				skus = skus[:1]
			} else {
				skus[tc.sku].PricingInfo[0].PricingExpression.UsageUnit = tc.unit
				wantError = "unsupported Cloud Storage usage unit"
			}
			client := storageCatalogClient(t, skus)
			details, err := client.GetOfferingDetails(t.Context(), common.Recommendation{ResourceType: "STANDARD", Term: "1yr"})
			require.ErrorContains(t, err, wantError)
			assert.Nil(t, details)
		})
	}
}

func storageCatalogClient(t *testing.T, skus []*cloudbilling.Sku) *Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1/services/95FF-2EF5-5EA1/skus", r.URL.Path)
		assert.Equal(t, http.MethodGet, r.Method)
		w.Header().Set("Content-Type", "application/json")
		assert.NoError(t, json.NewEncoder(w).Encode(&cloudbilling.ListSkusResponse{Skus: skus}))
	}))
	t.Cleanup(server.Close)
	transport := &http.Transport{DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		if address != server.Listener.Addr().String() {
			return nil, fmt.Errorf("unexpected catalog address %q", address)
		}
		return (&net.Dialer{}).DialContext(ctx, network, address)
	}}
	t.Cleanup(transport.CloseIdleConnections)
	client, err := NewClient(t.Context(), "test-project", "us-central1", option.WithEndpoint(server.URL),
		option.WithHTTPClient(&http.Client{Transport: transport}), option.WithoutAuthentication())
	require.NoError(t, err)
	return client
}
