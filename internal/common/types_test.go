package common

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestServiceDetails(t *testing.T) {
	tests := []struct {
		name           string
		details        ServiceDetails
		expectedType   ServiceType
		expectedDesc   string
		checkSpecifics func(t *testing.T, details ServiceDetails)
	}{
		{
			name: "RDS details",
			details: &RDSDetails{
				Engine:   "mysql",
				AZConfig: "multi-az",
			},
			expectedType: ServiceRDS,
			expectedDesc: "mysql multi-az",
		},
		{
			name: "ElastiCache details",
			details: &ElastiCacheDetails{
				Engine:   "redis",
				NodeType: "cache.r6g.large",
			},
			expectedType: ServiceElastiCache,
			expectedDesc: "redis",
		},
		{
			name: "EC2 details",
			details: &EC2Details{
				Platform: "Linux/UNIX",
				Tenancy:  "shared",
				Scope:    "region",
			},
			expectedType: ServiceEC2,
			expectedDesc: "Linux/UNIX shared region",
		},
		{
			name: "OpenSearch details with master",
			details: &OpenSearchDetails{
				InstanceType:    "r5.large.search",
				InstanceCount:   3,
				MasterEnabled:   true,
				MasterType:      "c5.large.search",
				MasterCount:     3,
				DataNodeStorage: 100,
			},
			expectedType: ServiceOpenSearch,
			expectedDesc: "r5.large.search x3 (Master: c5.large.search x3)",
		},
		{
			name: "OpenSearch details without master",
			details: &OpenSearchDetails{
				InstanceType:    "r5.large.search",
				InstanceCount:   2,
				MasterEnabled:   false,
				DataNodeStorage: 50,
			},
			expectedType: ServiceOpenSearch,
			expectedDesc: "r5.large.search x2",
		},
		{
			name: "Redshift details",
			details: &RedshiftDetails{
				NodeType:      "dc2.large",
				NumberOfNodes: 3,
				ClusterType:   "multi-node",
			},
			expectedType: ServiceRedshift,
			expectedDesc: "dc2.large 3-node multi-node",
		},
		{
			name: "MemoryDB details",
			details: &MemoryDBDetails{
				NodeType:      "db.r6g.large",
				NumberOfNodes: 3,
				ShardCount:    2,
			},
			expectedType: ServiceMemoryDB,
			expectedDesc: "db.r6g.large 3-node 2-shard",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expectedType, tt.details.GetServiceType())
			assert.Equal(t, tt.expectedDesc, tt.details.GetDetailDescription())
		})
	}
}

func TestRecommendation_GetDescription(t *testing.T) {
	tests := []struct {
		name     string
		rec      Recommendation
		expected string
	}{
		{
			name: "RDS recommendation",
			rec: Recommendation{
				Service:      ServiceRDS,
				InstanceType: "db.t4g.medium",
				Count:        2,
				ServiceDetails: &RDSDetails{
					Engine:   "postgres",
					AZConfig: "multi-az",
				},
			},
			expected: "postgres db.t4g.medium multi-az 2x",
		},
		{
			name: "ElastiCache recommendation",
			rec: Recommendation{
				Service:      ServiceElastiCache,
				InstanceType: "cache.r6g.large",
				Count:        3,
				ServiceDetails: &ElastiCacheDetails{
					Engine:   "redis",
					NodeType: "cache.r6g.large",
				},
			},
			expected: "redis cache.r6g.large 3x",
		},
		{
			name: "EC2 recommendation",
			rec: Recommendation{
				Service:      ServiceEC2,
				InstanceType: "m5.large",
				Count:        4,
				ServiceDetails: &EC2Details{
					Platform: "Windows",
					Tenancy:  "dedicated",
					Scope:    "availability-zone",
				},
			},
			expected: "Windows m5.large dedicated 4x",
		},
		{
			name: "OpenSearch recommendation with master",
			rec: Recommendation{
				Service:      ServiceOpenSearch,
				InstanceType: "r5.large.search",
				ServiceDetails: &OpenSearchDetails{
					InstanceType:  "r5.large.search",
					InstanceCount: 3,
					MasterEnabled: true,
					MasterType:    "c5.large.search",
					MasterCount:   3,
				},
			},
			expected: "OpenSearch r5.large.search 3x (Master: c5.large.search 3x)",
		},
		{
			name: "Redshift recommendation",
			rec: Recommendation{
				Service: ServiceRedshift,
				ServiceDetails: &RedshiftDetails{
					NodeType:      "dc2.large",
					NumberOfNodes: 4,
					ClusterType:   "multi-node",
				},
			},
			expected: "Redshift dc2.large 4-node multi-node",
		},
		{
			name: "MemoryDB recommendation",
			rec: Recommendation{
				Service: ServiceMemoryDB,
				ServiceDetails: &MemoryDBDetails{
					NodeType:      "db.r6g.large",
					NumberOfNodes: 2,
					ShardCount:    1,
				},
			},
			expected: "MemoryDB db.r6g.large 2-node 1-shard",
		},
		{
			name: "Unknown service recommendation",
			rec: Recommendation{
				Service:      "Unknown",
				InstanceType: "unknown.large",
				Count:        1,
			},
			expected: "unknown.large 1x",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, tt.rec.GetDescription())
		})
	}
}

func TestRecommendation_GetServiceName(t *testing.T) {
	tests := []struct {
		service  ServiceType
		expected string
	}{
		{ServiceRDS, "RDS"},
		{ServiceElastiCache, "ElastiCache"},
		{ServiceEC2, "EC2"},
		{ServiceOpenSearch, "OpenSearch"},
		{ServiceElasticsearch, "OpenSearch"},
		{ServiceRedshift, "Redshift"},
		{ServiceMemoryDB, "MemoryDB"},
		{ServiceType("Unknown"), "Unknown"},
	}

	for _, tt := range tests {
		t.Run(string(tt.service), func(t *testing.T) {
			rec := Recommendation{Service: tt.service}
			assert.Equal(t, tt.expected, rec.GetServiceName())
		})
	}
}

func TestRecommendation_GetMultiAZ(t *testing.T) {
	tests := []struct {
		name     string
		rec      Recommendation
		expected bool
	}{
		{
			name: "RDS multi-AZ",
			rec: Recommendation{
				ServiceDetails: &RDSDetails{
					AZConfig: "multi-az",
				},
			},
			expected: true,
		},
		{
			name: "RDS single-AZ",
			rec: Recommendation{
				ServiceDetails: &RDSDetails{
					AZConfig: "single-az",
				},
			},
			expected: false,
		},
		{
			name: "Non-RDS service",
			rec: Recommendation{
				ServiceDetails: &ElastiCacheDetails{
					Engine: "redis",
				},
			},
			expected: false,
		},
		{
			name:     "Nil service details",
			rec:      Recommendation{},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, tt.rec.GetMultiAZ())
		})
	}
}

func TestRecommendation_GetDurationString(t *testing.T) {
	tests := []struct {
		term     int
		expected string
	}{
		{12, "31536000"},  // 1 year (valid RI term)
		{36, "94608000"},  // 3 years (valid RI term)
		{24, "94608000"},  // Invalid term - defaults to 3 years
		{6, "94608000"},   // Invalid term - defaults to 3 years
	}

	for _, tt := range tests {
		t.Run(fmt.Sprintf("%d months", tt.term), func(t *testing.T) {
			rec := Recommendation{Term: tt.term}
			assert.Equal(t, tt.expected, rec.GetDurationString())
		})
	}
}

func TestPurchaseResult(t *testing.T) {
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

func TestRegionProcessingStats(t *testing.T) {
	stats := RegionProcessingStats{
		Region:                  "us-east-1",
		Service:                 ServiceElastiCache,
		Success:                 true,
		RecommendationsFound:    10,
		RecommendationsSelected: 5,
		InstancesProcessed:      15,
		SuccessfulPurchases:     4,
		FailedPurchases:         1,
	}

	assert.Equal(t, "us-east-1", stats.Region)
	assert.Equal(t, ServiceElastiCache, stats.Service)
	assert.True(t, stats.Success)
	assert.Equal(t, 10, stats.RecommendationsFound)
	assert.Equal(t, 5, stats.RecommendationsSelected)
	assert.Equal(t, int32(15), stats.InstancesProcessed)
	assert.Equal(t, 4, stats.SuccessfulPurchases)
	assert.Equal(t, 1, stats.FailedPurchases)
}

func TestCostEstimate(t *testing.T) {
	estimate := CostEstimate{
		Recommendation: Recommendation{
			Service:      ServiceRDS,
			InstanceType: "db.r6g.large",
			Count:        2,
		},
		TotalFixedCost:   3000.00,
		MonthlyUsageCost: 100.00,
		TotalTermCost:    6600.00,
		Error:            "",
	}

	assert.Equal(t, 3000.00, estimate.TotalFixedCost)
	assert.Equal(t, 100.00, estimate.MonthlyUsageCost)
	assert.Equal(t, 6600.00, estimate.TotalTermCost)
	assert.Empty(t, estimate.Error)
}

func TestOfferingDetails(t *testing.T) {
	offering := OfferingDetails{
		OfferingID:    "offering-123",
		InstanceType:  "db.t4g.medium",
		Engine:        "postgres",
		Platform:      "",
		NodeType:      "",
		Duration:      "31536000",
		PaymentOption: "partial-upfront",
		MultiAZ:       true,
		FixedPrice:    1500.00,
		UsagePrice:    0.05,
		CurrencyCode:  "USD",
		OfferingType:  "Heavy Utilization",
	}

	assert.Equal(t, "offering-123", offering.OfferingID)
	assert.Equal(t, "postgres", offering.Engine)
	assert.True(t, offering.MultiAZ)
	assert.Equal(t, 1500.00, offering.FixedPrice)
	assert.Equal(t, 0.05, offering.UsagePrice)
}

func TestRecommendationParams(t *testing.T) {
	params := RecommendationParams{
		Service:            ServiceEC2,
		Region:             "eu-west-1",
		AccountID:          "123456789012",
		PaymentOption:      "no-upfront",
		TermInYears:        3,
		LookbackPeriodDays: 30,
	}

	assert.Equal(t, ServiceEC2, params.Service)
	assert.Equal(t, "eu-west-1", params.Region)
	assert.Equal(t, "123456789012", params.AccountID)
	assert.Equal(t, "no-upfront", params.PaymentOption)
	assert.Equal(t, 3, params.TermInYears)
	assert.Equal(t, 30, params.LookbackPeriodDays)
}

func TestServiceTypeConstants(t *testing.T) {
	// Ensure all service type constants are defined
	require.NotEmpty(t, ServiceRDS)
	require.NotEmpty(t, ServiceElastiCache)
	require.NotEmpty(t, ServiceEC2)
	require.NotEmpty(t, ServiceOpenSearch)
	require.NotEmpty(t, ServiceElasticsearch)
	require.NotEmpty(t, ServiceRedshift)
	require.NotEmpty(t, ServiceMemoryDB)
}

// Benchmark tests
func BenchmarkRecommendationGetDescription(b *testing.B) {
	rec := Recommendation{
		Service:      ServiceRDS,
		InstanceType: "db.t4g.medium",
		Count:        2,
		ServiceDetails: &RDSDetails{
			Engine:   "mysql",
			AZConfig: "multi-az",
		},
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = rec.GetDescription()
	}
}

func BenchmarkServiceDetailsGetType(b *testing.B) {
	details := &RDSDetails{
		Engine:   "mysql",
		AZConfig: "multi-az",
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = details.GetServiceType()
	}
}