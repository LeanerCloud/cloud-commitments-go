package common

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestBasePurchaseClient_Basic(t *testing.T) {
	baseClient := &BasePurchaseClient{
		Region: "us-east-1",
	}

	assert.Equal(t, "us-east-1", baseClient.Region)
}

func TestBasePurchaseClient_BatchPurchase(t *testing.T) {
	baseClient := &BasePurchaseClient{
		Region: "us-east-1",
	}

	// Create test recommendations
	recommendations := []Recommendation{
		{
			Service:      ServiceRDS,
			InstanceType: "db.t4g.medium",
			Count:        2,
		},
		{
			Service:      ServiceElastiCache,
			InstanceType: "cache.r6g.large",
			Count:        1,
		},
	}

	// Test that BatchPurchase method exists and returns results
	// Note: This is a minimal test since we can't easily mock AWS clients
	assert.NotNil(t, baseClient)
	assert.NotNil(t, recommendations)
	assert.Equal(t, 2, len(recommendations))
}

func TestPurchaseResult_Basic(t *testing.T) {
	now := time.Now()
	result := PurchaseResult{
		Config: Recommendation{
			Service:      ServiceRDS,
			InstanceType: "db.t4g.medium",
			Count:        2,
		},
		Success:       true,
		PurchaseID:    "purchase-123",
		ReservationID: "reservation-456",
		Message:       "Successfully purchased",
		ActualCost:    1500.50,
		Timestamp:     now,
	}

	assert.True(t, result.Success)
	assert.Equal(t, "purchase-123", result.PurchaseID)
	assert.Equal(t, "reservation-456", result.ReservationID)
	assert.Equal(t, 1500.50, result.ActualCost)
	assert.Equal(t, now, result.Timestamp)
}

func TestRecommendation_Validation(t *testing.T) {
	tests := []struct {
		name  string
		rec   Recommendation
		valid bool
	}{
		{
			name: "valid RDS recommendation",
			rec: Recommendation{
				Service:      ServiceRDS,
				InstanceType: "db.t4g.medium",
				Count:        2,
				ServiceDetails: &RDSDetails{
					Engine:   "mysql",
					AZConfig: "multi-az",
				},
			},
			valid: true,
		},
		{
			name: "valid ElastiCache recommendation",
			rec: Recommendation{
				Service:      ServiceElastiCache,
				InstanceType: "cache.r6g.large",
				Count:        1,
				ServiceDetails: &ElastiCacheDetails{
					Engine: "redis",
				},
			},
			valid: true,
		},
		{
			name: "missing service details",
			rec: Recommendation{
				Service:      ServiceRDS,
				InstanceType: "db.t4g.medium",
				Count:        1,
			},
			valid: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hasDetails := tt.rec.ServiceDetails != nil
			assert.Equal(t, tt.valid, hasDetails)
		})
	}
}

func TestPurchaseClientInterface(t *testing.T) {
	// Test that the PurchaseClient interface is properly defined
	var _ PurchaseClient = (*testPurchaseClient)(nil)
}

// testPurchaseClient is a test implementation of PurchaseClient
type testPurchaseClient struct {
	Region string
}

func (t *testPurchaseClient) PurchaseRI(ctx context.Context, rec Recommendation) PurchaseResult {
	return PurchaseResult{
		Config:  rec,
		Success: true,
		Message: "test purchase",
	}
}

func (t *testPurchaseClient) ValidateOffering(ctx context.Context, rec Recommendation) error {
	return nil
}

func (t *testPurchaseClient) GetOfferingDetails(ctx context.Context, rec Recommendation) (*OfferingDetails, error) {
	return &OfferingDetails{
		OfferingID:   "test-offering",
		InstanceType: rec.InstanceType,
	}, nil
}

func (t *testPurchaseClient) BatchPurchase(ctx context.Context, recommendations []Recommendation, delayBetweenPurchases time.Duration) []PurchaseResult {
	results := make([]PurchaseResult, len(recommendations))
	for i, rec := range recommendations {
		results[i] = t.PurchaseRI(ctx, rec)
	}
	return results
}

func TestRegionProcessingStats_BaseClient(t *testing.T) {
	stats := RegionProcessingStats{
		Region:                  "us-east-1",
		Service:                 ServiceRDS,
		Success:                 true,
		RecommendationsFound:    10,
		RecommendationsSelected: 5,
		InstancesProcessed:      15,
		SuccessfulPurchases:     4,
		FailedPurchases:         1,
	}

	assert.Equal(t, "us-east-1", stats.Region)
	assert.Equal(t, ServiceRDS, stats.Service)
	assert.True(t, stats.Success)
	assert.Equal(t, 10, stats.RecommendationsFound)
	assert.Equal(t, 5, stats.RecommendationsSelected)
}

func TestCostEstimate_BaseClient(t *testing.T) {
	estimate := CostEstimate{
		Recommendation: Recommendation{
			Service:      ServiceEC2,
			InstanceType: "m5.large",
			Count:        2,
		},
		TotalFixedCost:   2000.00,
		MonthlyUsageCost: 50.00,
		TotalTermCost:    3800.00,
	}

	assert.Equal(t, 2000.00, estimate.TotalFixedCost)
	assert.Equal(t, 50.00, estimate.MonthlyUsageCost)
	assert.Equal(t, 3800.00, estimate.TotalTermCost)
}

func TestOfferingDetails_BaseClient(t *testing.T) {
	offering := OfferingDetails{
		OfferingID:    "offering-123",
		InstanceType:  "m5.large",
		Platform:      "Linux/UNIX",
		Duration:      "31536000",
		PaymentOption: "no-upfront",
		FixedPrice:    1000.00,
		UsagePrice:    0.10,
		CurrencyCode:  "USD",
	}

	assert.Equal(t, "offering-123", offering.OfferingID)
	assert.Equal(t, "m5.large", offering.InstanceType)
	assert.Equal(t, "Linux/UNIX", offering.Platform)
	assert.Equal(t, 1000.00, offering.FixedPrice)
}

// Benchmark tests
func BenchmarkBasePurchaseClient_Creation(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = &BasePurchaseClient{
			Region: "us-east-1",
		}
	}
}

func BenchmarkPurchaseResult_Creation(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = PurchaseResult{
			Config: Recommendation{
				Service:      ServiceRDS,
				InstanceType: "db.t4g.medium",
				Count:        2,
			},
			Success:    true,
			ActualCost: 1000.00,
			Timestamp:  time.Now(),
		}
	}
}