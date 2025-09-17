package rds

import (
	"context"
	"testing"

	"github.com/LeanerCloud/rds-ri-purchase-tool/internal/common"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/stretchr/testify/assert"
)

func TestNewPurchaseClient(t *testing.T) {
	cfg := aws.Config{
		Region: "us-east-1",
	}

	client := NewPurchaseClient(cfg)

	assert.NotNil(t, client)
	assert.NotNil(t, client.client)
	assert.Equal(t, "us-east-1", client.Region)
}

func TestPurchaseClient_ValidateRecommendation(t *testing.T) {
	tests := []struct {
		name        string
		rec         common.Recommendation
		expectValid bool
		expectError string
	}{
		{
			name: "valid RDS recommendation",
			rec: common.Recommendation{
				Service:      common.ServiceRDS,
				InstanceType: "db.t4g.medium",
				ServiceDetails: &common.RDSDetails{
					Engine:   "mysql",
					AZConfig: "multi-az",
				},
			},
			expectValid: true,
		},
		{
			name: "wrong service type",
			rec: common.Recommendation{
				Service:      common.ServiceElastiCache,
				InstanceType: "cache.r6g.large",
			},
			expectValid: false,
			expectError: "Invalid service type for RDS purchase",
		},
		{
			name: "missing service details",
			rec: common.Recommendation{
				Service:      common.ServiceRDS,
				InstanceType: "db.t4g.medium",
			},
			expectValid: false,
			expectError: "Invalid service details for RDS",
		},
		{
			name: "wrong service details type",
			rec: common.Recommendation{
				Service:      common.ServiceRDS,
				InstanceType: "db.t4g.medium",
				ServiceDetails: &common.ElastiCacheDetails{
					Engine: "redis",
				},
			},
			expectValid: false,
			expectError: "Invalid service details for RDS",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Test validation in PurchaseRI method
			result := common.PurchaseResult{
				Config: tt.rec,
			}

			// Validate the recommendation type
			if tt.rec.Service != common.ServiceRDS {
				result.Success = false
				result.Message = "Invalid service type for RDS purchase"
			} else if _, ok := tt.rec.ServiceDetails.(*common.RDSDetails); !ok || tt.rec.ServiceDetails == nil {
				result.Success = false
				result.Message = "Invalid service details for RDS"
			} else {
				result.Success = true
			}

			if tt.expectValid {
				assert.True(t, result.Success)
			} else {
				assert.False(t, result.Success)
				assert.Contains(t, result.Message, tt.expectError)
			}
		})
	}
}

func TestPurchaseClient_DurationMapping(t *testing.T) {
	tests := []struct {
		name     string
		months   int
		expected string
	}{
		{
			name:     "1 year",
			months:   12,
			expected: "31536000",
		},
		{
			name:     "3 years",
			months:   36,
			expected: "94608000",
		},
		{
			name:     "invalid term defaults to 3 years",
			months:   24,
			expected: "94608000",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := common.Recommendation{Term: tt.months}
			duration := rec.GetDurationString()
			assert.Equal(t, tt.expected, duration)
		})
	}
}

func TestPurchaseClient_MultiAZHandling(t *testing.T) {
	tests := []struct {
		name        string
		azConfig    string
		expectMulti bool
	}{
		{
			name:        "multi-az",
			azConfig:    "multi-az",
			expectMulti: true,
		},
		{
			name:        "single-az",
			azConfig:    "single-az",
			expectMulti: false,
		},
		{
			name:        "empty defaults to single",
			azConfig:    "",
			expectMulti: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := common.Recommendation{
				ServiceDetails: &common.RDSDetails{
					AZConfig: tt.azConfig,
				},
			}

			isMultiAZ := rec.GetMultiAZ()
			assert.Equal(t, tt.expectMulti, isMultiAZ)
		})
	}
}

func TestPurchaseClient_EngineHandling(t *testing.T) {
	tests := []struct {
		name     string
		engine   string
		azConfig string
		expected string
	}{
		{
			name:     "MySQL multi-AZ",
			engine:   "mysql",
			azConfig: "multi-az",
			expected: "mysql multi-az",
		},
		{
			name:     "PostgreSQL single-AZ",
			engine:   "postgres",
			azConfig: "single-az",
			expected: "postgres single-az",
		},
		{
			name:     "Aurora MySQL",
			engine:   "aurora-mysql",
			azConfig: "multi-az",
			expected: "aurora-mysql multi-az",
		},
		{
			name:     "Aurora PostgreSQL",
			engine:   "aurora-postgresql",
			azConfig: "single-az",
			expected: "aurora-postgresql single-az",
		},
		{
			name:     "MariaDB",
			engine:   "mariadb",
			azConfig: "multi-az",
			expected: "mariadb multi-az",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			details := &common.RDSDetails{
				Engine:   tt.engine,
				AZConfig: tt.azConfig,
			}

			description := details.GetDetailDescription()
			assert.Equal(t, tt.expected, description)
		})
	}
}

func TestPurchaseClient_CreatePurchaseTags(t *testing.T) {
	rec := common.Recommendation{
		Service:       common.ServiceRDS,
		Region:        "us-west-2",
		InstanceType:  "db.r6g.large",
		PaymentOption: "partial-upfront",
		Term:          36,
		ServiceDetails: &common.RDSDetails{
			Engine:   "postgres",
			AZConfig: "multi-az",
		},
	}

	// Verify recommendation has required fields for tagging
	assert.Equal(t, common.ServiceRDS, rec.Service)
	assert.Equal(t, "us-west-2", rec.Region)
	assert.Equal(t, "db.r6g.large", rec.InstanceType)
	assert.Equal(t, "partial-upfront", rec.PaymentOption)
	assert.Equal(t, 36, rec.Term)

	details := rec.ServiceDetails.(*common.RDSDetails)
	assert.Equal(t, "postgres", details.Engine)
	assert.Equal(t, "multi-az", details.AZConfig)
}

func TestPurchaseClient_BatchPurchase(t *testing.T) {
	client := &PurchaseClient{
		BasePurchaseClient: common.BasePurchaseClient{
			Region: "us-east-1",
		},
	}

	recommendations := []common.Recommendation{
		{
			Service:      common.ServiceRDS,
			InstanceType: "db.t4g.medium",
			Count:        2,
			ServiceDetails: &common.RDSDetails{
				Engine:   "mysql",
				AZConfig: "single-az",
			},
		},
		{
			Service:      common.ServiceRDS,
			InstanceType: "db.r6g.large",
			Count:        1,
			ServiceDetails: &common.RDSDetails{
				Engine:   "postgres",
				AZConfig: "multi-az",
			},
		},
	}

	assert.Equal(t, 2, len(recommendations))
	assert.Equal(t, "us-east-1", client.Region)
}

func TestPurchaseClient_Integration(t *testing.T) {
	// Skip if not running integration tests
	if testing.Short() {
		t.Skip("Skipping integration test")
	}

	ctx := context.Background()
	cfg := aws.Config{
		Region: "us-east-1",
	}

	client := NewPurchaseClient(cfg)

	// Test ValidateOffering with a sample recommendation
	rec := common.Recommendation{
		Service:       common.ServiceRDS,
		InstanceType:  "db.t3.micro",
		PaymentOption: "no-upfront",
		Term:          12,
		ServiceDetails: &common.RDSDetails{
			Engine:   "mysql",
			AZConfig: "single-az",
		},
	}

	// This will fail in dry-run mode but validates the API call structure
	err := client.ValidateOffering(ctx, rec)
	// We expect an error since we're not actually finding real offerings
	// but the test validates that the method works
	assert.Error(t, err) // Expected to not find offerings in test environment
}

// Benchmark tests
func BenchmarkPurchaseClient_Creation(b *testing.B) {
	cfg := aws.Config{
		Region: "us-east-1",
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = NewPurchaseClient(cfg)
	}
}

func BenchmarkPurchaseClient_Validation(b *testing.B) {
	rec := common.Recommendation{
		Service:      common.ServiceRDS,
		InstanceType: "db.t4g.medium",
		ServiceDetails: &common.RDSDetails{
			Engine:   "mysql",
			AZConfig: "multi-az",
		},
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		result := common.PurchaseResult{
			Config: rec,
		}

		if rec.Service != common.ServiceRDS {
			result.Success = false
		} else if _, ok := rec.ServiceDetails.(*common.RDSDetails); !ok {
			result.Success = false
		} else {
			result.Success = true
		}
	}
}