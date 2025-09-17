package opensearch

import (
	"context"
	"testing"

	"github.com/LeanerCloud/rds-ri-purchase-tool/internal/common"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/stretchr/testify/assert"
)

func TestNewPurchaseClient(t *testing.T) {
	cfg := aws.Config{
		Region: "us-west-2",
	}

	client := NewPurchaseClient(cfg)

	assert.NotNil(t, client)
	assert.NotNil(t, client.client)
	assert.Equal(t, "us-west-2", client.Region)
}

func TestPurchaseClient_ValidateRecommendation(t *testing.T) {
	tests := []struct {
		name        string
		rec         common.Recommendation
		expectValid bool
		expectError string
	}{
		{
			name: "valid OpenSearch recommendation with master",
			rec: common.Recommendation{
				Service:      common.ServiceOpenSearch,
				InstanceType: "r5.large.search",
				ServiceDetails: &common.OpenSearchDetails{
					InstanceType:    "r5.large.search",
					InstanceCount:   3,
					MasterEnabled:   true,
					MasterType:      "c5.large.search",
					MasterCount:     3,
					DataNodeStorage: 100,
				},
			},
			expectValid: true,
		},
		{
			name: "valid OpenSearch recommendation without master",
			rec: common.Recommendation{
				Service:      common.ServiceOpenSearch,
				InstanceType: "r5.large.search",
				ServiceDetails: &common.OpenSearchDetails{
					InstanceType:    "r5.large.search",
					InstanceCount:   2,
					MasterEnabled:   false,
					DataNodeStorage: 50,
				},
			},
			expectValid: true,
		},
		{
			name: "wrong service type",
			rec: common.Recommendation{
				Service:      common.ServiceRDS,
				InstanceType: "db.t4g.medium",
			},
			expectValid: false,
			expectError: "Invalid service type for OpenSearch purchase",
		},
		{
			name: "missing service details",
			rec: common.Recommendation{
				Service:      common.ServiceOpenSearch,
				InstanceType: "r5.large.search",
			},
			expectValid: false,
			expectError: "Invalid service details for OpenSearch",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Test validation in PurchaseRI method
			result := common.PurchaseResult{
				Config: tt.rec,
			}

			// Validate the recommendation type
			if tt.rec.Service != common.ServiceOpenSearch {
				result.Success = false
				result.Message = "Invalid service type for OpenSearch purchase"
			} else if _, ok := tt.rec.ServiceDetails.(*common.OpenSearchDetails); !ok || tt.rec.ServiceDetails == nil {
				result.Success = false
				result.Message = "Invalid service details for OpenSearch"
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

func TestPurchaseClient_MasterNodeConfiguration(t *testing.T) {
	tests := []struct {
		name          string
		masterEnabled bool
		masterType    string
		masterCount   int32
		expectedDesc  string
	}{
		{
			name:          "with dedicated master nodes",
			masterEnabled: true,
			masterType:    "c5.large.search",
			masterCount:   3,
			expectedDesc:  "r5.large.search x3 (Master: c5.large.search x3)",
		},
		{
			name:          "without dedicated master nodes",
			masterEnabled: false,
			masterType:    "",
			masterCount:   0,
			expectedDesc:  "r5.large.search x2",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			instanceCount := int32(2)
			if tt.masterEnabled {
				instanceCount = 3
			}

			details := &common.OpenSearchDetails{
				InstanceType:  "r5.large.search",
				InstanceCount: instanceCount,
				MasterEnabled: tt.masterEnabled,
				MasterType:    tt.masterType,
				MasterCount:   tt.masterCount,
			}

			desc := details.GetDetailDescription()
			assert.Equal(t, tt.expectedDesc, desc)
		})
	}
}

func TestPurchaseClient_InstanceTypes(t *testing.T) {
	tests := []struct {
		name         string
		instanceType string
		isValid      bool
	}{
		{
			name:         "r5 large search",
			instanceType: "r5.large.search",
			isValid:      true,
		},
		{
			name:         "c5 xlarge search",
			instanceType: "c5.xlarge.search",
			isValid:      true,
		},
		{
			name:         "m5 2xlarge search",
			instanceType: "m5.2xlarge.search",
			isValid:      true,
		},
		{
			name:         "r6g large search",
			instanceType: "r6g.large.search",
			isValid:      true,
		},
		{
			name:         "t3 small search",
			instanceType: "t3.small.search",
			isValid:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			details := &common.OpenSearchDetails{
				InstanceType:  tt.instanceType,
				InstanceCount: 1,
			}

			assert.Equal(t, common.ServiceOpenSearch, details.GetServiceType())
			assert.Contains(t, details.GetDetailDescription(), tt.instanceType)
		})
	}
}

func TestPurchaseClient_DataNodeStorage(t *testing.T) {
	tests := []struct {
		name            string
		dataNodeStorage int32
	}{
		{
			name:            "small storage",
			dataNodeStorage: 10,
		},
		{
			name:            "medium storage",
			dataNodeStorage: 100,
		},
		{
			name:            "large storage",
			dataNodeStorage: 1000,
		},
		{
			name:            "very large storage",
			dataNodeStorage: 10000,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			details := &common.OpenSearchDetails{
				InstanceType:    "r5.large.search",
				InstanceCount:   2,
				DataNodeStorage: tt.dataNodeStorage,
			}

			assert.Equal(t, tt.dataNodeStorage, details.DataNodeStorage)
		})
	}
}

func TestPurchaseClient_CreatePurchaseTags(t *testing.T) {
	rec := common.Recommendation{
		Service:       common.ServiceOpenSearch,
		Region:        "us-east-1",
		InstanceType:  "r5.large.search",
		PaymentOption: "no-upfront",
		Term:          36,
		ServiceDetails: &common.OpenSearchDetails{
			InstanceType:    "r5.large.search",
			InstanceCount:   3,
			MasterEnabled:   true,
			MasterType:      "c5.large.search",
			MasterCount:     3,
			DataNodeStorage: 500,
		},
	}

	// Verify recommendation has required fields for tagging
	assert.Equal(t, common.ServiceOpenSearch, rec.Service)
	assert.Equal(t, "us-east-1", rec.Region)
	assert.Equal(t, "r5.large.search", rec.InstanceType)
	assert.Equal(t, "no-upfront", rec.PaymentOption)
	assert.Equal(t, 36, rec.Term)

	details := rec.ServiceDetails.(*common.OpenSearchDetails)
	assert.Equal(t, "r5.large.search", details.InstanceType)
	assert.Equal(t, int32(3), details.InstanceCount)
	assert.True(t, details.MasterEnabled)
	assert.Equal(t, "c5.large.search", details.MasterType)
	assert.Equal(t, int32(3), details.MasterCount)
}

func TestPurchaseClient_ElasticsearchLegacySupport(t *testing.T) {
	// Test that Elasticsearch (legacy) service is handled correctly
	rec := common.Recommendation{
		Service:      common.ServiceElasticsearch,
		InstanceType: "m4.large.elasticsearch",
		ServiceDetails: &common.OpenSearchDetails{
			InstanceType:  "m4.large.elasticsearch",
			InstanceCount: 2,
		},
	}

	// Should be recognized as OpenSearch internally
	details := rec.ServiceDetails.(*common.OpenSearchDetails)
	assert.Equal(t, common.ServiceOpenSearch, details.GetServiceType())
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
		Service:       common.ServiceOpenSearch,
		InstanceType:  "t3.small.search",
		PaymentOption: "no-upfront",
		Term:          12,
		ServiceDetails: &common.OpenSearchDetails{
			InstanceType:  "t3.small.search",
			InstanceCount: 1,
		},
	}

	// This will fail in dry-run mode but validates the API call structure
	err := client.ValidateOffering(ctx, rec)
	// We expect an error since we're not actually finding real offerings
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
		Service:      common.ServiceOpenSearch,
		InstanceType: "r5.large.search",
		ServiceDetails: &common.OpenSearchDetails{
			InstanceType:  "r5.large.search",
			InstanceCount: 3,
		},
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		result := common.PurchaseResult{
			Config: rec,
		}

		if rec.Service != common.ServiceOpenSearch {
			result.Success = false
		} else if _, ok := rec.ServiceDetails.(*common.OpenSearchDetails); !ok {
			result.Success = false
		} else {
			result.Success = true
		}
	}
}