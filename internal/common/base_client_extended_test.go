package common

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

// Mock implementation of PurchaseClient interface
type MockPurchaseClient struct {
	mock.Mock
}

func (m *MockPurchaseClient) PurchaseRI(ctx context.Context, rec Recommendation) PurchaseResult {
	args := m.Called(ctx, rec)
	return args.Get(0).(PurchaseResult)
}

func (m *MockPurchaseClient) ValidateOffering(ctx context.Context, rec Recommendation) error {
	args := m.Called(ctx, rec)
	return args.Error(0)
}

func (m *MockPurchaseClient) GetOfferingDetails(ctx context.Context, rec Recommendation) (*OfferingDetails, error) {
	args := m.Called(ctx, rec)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*OfferingDetails), args.Error(1)
}

func (m *MockPurchaseClient) BatchPurchase(ctx context.Context, recommendations []Recommendation, delayBetweenPurchases time.Duration) []PurchaseResult {
	args := m.Called(ctx, recommendations, delayBetweenPurchases)
	return args.Get(0).([]PurchaseResult)
}

func TestBasePurchaseClient_BatchPurchase_WithDelay(t *testing.T) {
	baseClient := &BasePurchaseClient{
		Region: "us-east-1",
	}
	mockClient := &MockPurchaseClient{}

	recommendations := []Recommendation{
		{
			Service:      ServiceRDS,
			InstanceType: "db.t3.small",
			Count:        1,
		},
		{
			Service:      ServiceRDS,
			InstanceType: "db.t3.medium",
			Count:        2,
		},
		{
			Service:      ServiceRDS,
			InstanceType: "db.t3.large",
			Count:        3,
		},
	}

	// Mock successful purchases
	for _, rec := range recommendations {
		mockClient.On("PurchaseRI", mock.Anything, rec).Return(PurchaseResult{
			Config:  rec,
			Success: true,
			Message: "Successfully purchased",
		})
	}

	// Test with delay
	start := time.Now()
	results := baseClient.BatchPurchase(context.Background(), mockClient, recommendations, 100*time.Millisecond)
	duration := time.Since(start)

	assert.Len(t, results, 3)
	for i, result := range results {
		assert.True(t, result.Success)
		assert.Equal(t, recommendations[i].InstanceType, result.Config.InstanceType)
	}

	// Should have at least 200ms delay (2 delays between 3 purchases)
	assert.GreaterOrEqual(t, duration, 200*time.Millisecond)
	mockClient.AssertExpectations(t)
}

func TestBasePurchaseClient_BatchPurchase_MixedResults(t *testing.T) {
	baseClient := &BasePurchaseClient{
		Region: "us-west-2",
	}
	mockClient := &MockPurchaseClient{}

	recommendations := []Recommendation{
		{
			Service:      ServiceElastiCache,
			InstanceType: "cache.t3.small",
			Count:        1,
		},
		{
			Service:      ServiceElastiCache,
			InstanceType: "cache.t3.medium",
			Count:        2,
		},
		{
			Service:      ServiceElastiCache,
			InstanceType: "cache.t3.large",
			Count:        3,
		},
	}

	// Mock mixed results
	mockClient.On("PurchaseRI", mock.Anything, recommendations[0]).Return(PurchaseResult{
		Config:  recommendations[0],
		Success: true,
		Message: "Successfully purchased",
	})

	mockClient.On("PurchaseRI", mock.Anything, recommendations[1]).Return(PurchaseResult{
		Config:  recommendations[1],
		Success: false,
		Message: "Insufficient funds",
	})

	mockClient.On("PurchaseRI", mock.Anything, recommendations[2]).Return(PurchaseResult{
		Config:  recommendations[2],
		Success: true,
		Message: "Successfully purchased",
	})

	results := baseClient.BatchPurchase(context.Background(), mockClient, recommendations, 0)

	assert.Len(t, results, 3)
	assert.True(t, results[0].Success)
	assert.False(t, results[1].Success)
	assert.True(t, results[2].Success)
	assert.Contains(t, results[1].Message, "Insufficient funds")
	mockClient.AssertExpectations(t)
}

func TestBasePurchaseClient_BatchPurchase_EmptyRecommendations(t *testing.T) {
	baseClient := &BasePurchaseClient{
		Region: "eu-west-1",
	}
	mockClient := &MockPurchaseClient{}

	recommendations := []Recommendation{}

	results := baseClient.BatchPurchase(context.Background(), mockClient, recommendations, 0)

	assert.Len(t, results, 0)
	mockClient.AssertNotCalled(t, "PurchaseRI")
}

func TestRecommendation_GetServiceName_Extended(t *testing.T) {
	tests := []struct {
		service  ServiceType
		expected string
	}{
		{ServiceType("custom-service"), "Unknown"},
		{ServiceType(""), "Unknown"},
		{ServiceType("very-long-service-name-that-exceeds-normal-length"), "Unknown"},
	}

	for _, tt := range tests {
		t.Run(string(tt.service), func(t *testing.T) {
			rec := Recommendation{Service: tt.service}
			assert.Equal(t, tt.expected, rec.GetServiceName())
		})
	}
}

func TestRecommendation_GetDescription_EdgeCases(t *testing.T) {
	tests := []struct {
		name     string
		rec      Recommendation
		expected string
	}{
		{
			name: "nil service details",
			rec: Recommendation{
				Service:        ServiceRDS,
				InstanceType:   "db.t3.medium",
				Count:          2,
				ServiceDetails: nil,
			},
			expected: "db.t3.medium 2x",
		},
		{
			name: "zero count",
			rec: Recommendation{
				Service:      ServiceEC2,
				InstanceType: "m5.large",
				Count:        0,
			},
			expected: "m5.large 0x",
		},
		{
			name: "empty instance type",
			rec: Recommendation{
				Service:      ServiceRedshift,
				InstanceType: "",
				Count:        5,
			},
			expected: " 5x",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, tt.rec.GetDescription())
		})
	}
}

func TestRecommendation_GetDurationString_AllCases(t *testing.T) {
	tests := []struct {
		term     int
		expected string
	}{
		{12, "31536000"},  // 1 year (valid)
		{36, "94608000"},  // 3 years (valid)
		{0, "94608000"},   // Invalid - defaults to 3 years
		{-1, "94608000"},  // Negative - defaults to 3 years
		{6, "94608000"},   // 6 months - defaults to 3 years
		{24, "94608000"},  // 2 years - defaults to 3 years
		{48, "94608000"},  // 4 years - defaults to 3 years
		{60, "94608000"},  // 5 years - defaults to 3 years
	}

	for _, tt := range tests {
		t.Run(fmt.Sprintf("term_%d_months", tt.term), func(t *testing.T) {
			rec := Recommendation{Term: tt.term}
			assert.Equal(t, tt.expected, rec.GetDurationString())
		})
	}
}

func TestPurchaseResult_Fields(t *testing.T) {
	now := time.Now()

	result := PurchaseResult{
		Config: Recommendation{
			Service:      ServiceRDS,
			InstanceType: "db.t3.medium",
			Count:        2,
		},
		Success:       true,
		PurchaseID:    "purchase-123",
		ReservationID: "reservation-456",
		Message:       "Successfully purchased",
		ActualCost:    1234.56,
		Timestamp:     now,
	}

	assert.True(t, result.Success)
	assert.Equal(t, "purchase-123", result.PurchaseID)
	assert.Equal(t, "reservation-456", result.ReservationID)
	assert.Equal(t, "Successfully purchased", result.Message)
	assert.Equal(t, 1234.56, result.ActualCost)
	assert.Equal(t, now, result.Timestamp)
	assert.Equal(t, ServiceRDS, result.Config.Service)
}

func TestServiceDetails_AllTypes(t *testing.T) {
	tests := []struct {
		name        string
		details     ServiceDetails
		serviceType ServiceType
		description string
	}{
		{
			name: "RDS with Aurora",
			details: &RDSDetails{
				Engine:   "aurora-mysql",
				AZConfig: "multi-az",
			},
			serviceType: ServiceRDS,
			description: "aurora-mysql multi-az",
		},
		{
			name: "ElastiCache with memcached",
			details: &ElastiCacheDetails{
				Engine:   "memcached",
				NodeType: "cache.m6g.xlarge",
			},
			serviceType: ServiceElastiCache,
			description: "memcached",
		},
		{
			name: "EC2 with dedicated tenancy",
			details: &EC2Details{
				Platform: "Windows",
				Tenancy:  "dedicated",
				Scope:    "availability-zone",
			},
			serviceType: ServiceEC2,
			description: "Windows dedicated availability-zone",
		},
		{
			name: "Redshift single node",
			details: &RedshiftDetails{
				NodeType:      "ra3.xlplus",
				NumberOfNodes: 1,
				ClusterType:   "single-node",
			},
			serviceType: ServiceRedshift,
			description: "ra3.xlplus 1-node single-node",
		},
		{
			name: "MemoryDB multi-shard",
			details: &MemoryDBDetails{
				NodeType:      "db.r6g.2xlarge",
				NumberOfNodes: 6,
				ShardCount:    3,
			},
			serviceType: ServiceMemoryDB,
			description: "db.r6g.2xlarge 6-node 3-shard",
		},
		{
			name: "OpenSearch without master",
			details: &OpenSearchDetails{
				InstanceType:    "r5.xlarge.search",
				InstanceCount:   5,
				MasterEnabled:   false,
				DataNodeStorage: 500,
			},
			serviceType: ServiceOpenSearch,
			description: "r5.xlarge.search x5",
		},
		{
			name: "OpenSearch with dedicated master",
			details: &OpenSearchDetails{
				InstanceType:    "r5.large.search",
				InstanceCount:   3,
				MasterEnabled:   true,
				MasterType:      "c5.large.search",
				MasterCount:     3,
				DataNodeStorage: 100,
			},
			serviceType: ServiceOpenSearch,
			description: "r5.large.search x3 (Master: c5.large.search x3)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.serviceType, tt.details.GetServiceType())
			assert.Equal(t, tt.description, tt.details.GetDetailDescription())
		})
	}
}

func TestRegionProcessingStats_Extended(t *testing.T) {
	stats := RegionProcessingStats{
		Region:                  "ap-southeast-1",
		Service:                 ServiceMemoryDB,
		Success:                 false,
		RecommendationsFound:    0,
		RecommendationsSelected: 0,
		InstancesProcessed:      0,
		SuccessfulPurchases:     0,
		FailedPurchases:         0,
	}

	assert.Equal(t, "ap-southeast-1", stats.Region)
	assert.Equal(t, ServiceMemoryDB, stats.Service)
	assert.False(t, stats.Success)
	assert.Equal(t, 0, stats.RecommendationsFound)
}

func TestCostEstimate_Extended(t *testing.T) {
	estimate := CostEstimate{
		Recommendation: Recommendation{
			Service:      ServiceElastiCache,
			InstanceType: "cache.r6g.xlarge",
			Count:        5,
		},
		TotalFixedCost:   5000.00,
		MonthlyUsageCost: 200.00,
		TotalTermCost:    12400.00,
	}

	assert.Equal(t, 5000.00, estimate.TotalFixedCost)
	assert.Equal(t, 200.00, estimate.MonthlyUsageCost)
	assert.Equal(t, 12400.00, estimate.TotalTermCost)
}

func TestOfferingDetails_AllFields(t *testing.T) {
	offering := OfferingDetails{
		OfferingID:    "ri-2024-12-01-abc123",
		InstanceType:  "m5.2xlarge",
		Engine:        "postgres",
		Platform:      "Linux/UNIX",
		NodeType:      "cache.r6g.large",
		Duration:      "94608000",
		PaymentOption: "partial-upfront",
		MultiAZ:       true,
		FixedPrice:    2500.00,
		UsagePrice:    0.08,
		CurrencyCode:  "EUR",
		OfferingType:  "Convertible",
	}

	assert.Equal(t, "ri-2024-12-01-abc123", offering.OfferingID)
	assert.Equal(t, "m5.2xlarge", offering.InstanceType)
	assert.Equal(t, "postgres", offering.Engine)
	assert.Equal(t, "Linux/UNIX", offering.Platform)
	assert.Equal(t, "cache.r6g.large", offering.NodeType)
	assert.Equal(t, "94608000", offering.Duration)
	assert.Equal(t, "partial-upfront", offering.PaymentOption)
	assert.True(t, offering.MultiAZ)
	assert.Equal(t, 2500.00, offering.FixedPrice)
	assert.Equal(t, 0.08, offering.UsagePrice)
	assert.Equal(t, "EUR", offering.CurrencyCode)
	assert.Equal(t, "Convertible", offering.OfferingType)
}

func TestRecommendationParams_Extended(t *testing.T) {
	params := RecommendationParams{
		Service:            ServiceRedshift,
		Region:             "ca-central-1",
		AccountID:          "987654321098",
		PaymentOption:      "all-upfront",
		TermInYears:        1,
		LookbackPeriodDays: 60,
	}

	assert.Equal(t, ServiceRedshift, params.Service)
	assert.Equal(t, "ca-central-1", params.Region)
	assert.Equal(t, "987654321098", params.AccountID)
	assert.Equal(t, "all-upfront", params.PaymentOption)
	assert.Equal(t, 1, params.TermInYears)
	assert.Equal(t, 60, params.LookbackPeriodDays)
}

// Benchmark tests
func BenchmarkRecommendation_GetDescription(b *testing.B) {
	rec := Recommendation{
		Service:      ServiceRDS,
		InstanceType: "db.r6g.xlarge",
		Count:        10,
		ServiceDetails: &RDSDetails{
			Engine:   "aurora-postgresql",
			AZConfig: "multi-az",
		},
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = rec.GetDescription()
	}
}

func BenchmarkBasePurchaseClient_BatchPurchase(b *testing.B) {
	baseClient := &BasePurchaseClient{
		Region: "us-east-1",
	}
	mockClient := &MockPurchaseClient{}

	recommendations := make([]Recommendation, 10)
	for i := range recommendations {
		recommendations[i] = Recommendation{
			Service:      ServiceRDS,
			InstanceType: fmt.Sprintf("db.t3.%d", i),
			Count:        int32(i + 1),
		}
		mockClient.On("PurchaseRI", mock.Anything, mock.Anything).Return(PurchaseResult{
			Success: true,
		})
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = baseClient.BatchPurchase(context.Background(), mockClient, recommendations, 0)
	}
}