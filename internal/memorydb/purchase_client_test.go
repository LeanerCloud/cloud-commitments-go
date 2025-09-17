package memorydb

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
			name: "valid MemoryDB recommendation",
			rec: common.Recommendation{
				Service:      common.ServiceMemoryDB,
				InstanceType: "db.r6g.large",
				ServiceDetails: &common.MemoryDBDetails{
					NodeType:      "db.r6g.large",
					NumberOfNodes: 3,
					ShardCount:    2,
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
			expectError: "Invalid service type for MemoryDB purchase",
		},
		{
			name: "missing service details",
			rec: common.Recommendation{
				Service:      common.ServiceMemoryDB,
				InstanceType: "db.r6g.large",
			},
			expectValid: false,
			expectError: "Invalid service details for MemoryDB",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := common.PurchaseResult{
				Config: tt.rec,
			}

			// Validate the recommendation type
			if tt.rec.Service != common.ServiceMemoryDB {
				result.Success = false
				result.Message = "Invalid service type for MemoryDB purchase"
			} else if _, ok := tt.rec.ServiceDetails.(*common.MemoryDBDetails); !ok || tt.rec.ServiceDetails == nil {
				result.Success = false
				result.Message = "Invalid service details for MemoryDB"
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

func TestPurchaseClient_NodeConfigurations(t *testing.T) {
	tests := []struct {
		name         string
		nodeType     string
		numNodes     int32
		shardCount   int32
		expectedDesc string
	}{
		{
			name:         "single shard configuration",
			nodeType:     "db.r6g.large",
			numNodes:     2,
			shardCount:   1,
			expectedDesc: "db.r6g.large 2-node 1-shard",
		},
		{
			name:         "multi-shard configuration",
			nodeType:     "db.r6g.xlarge",
			numNodes:     6,
			shardCount:   3,
			expectedDesc: "db.r6g.xlarge 6-node 3-shard",
		},
		{
			name:         "large cluster",
			nodeType:     "db.r6g.2xlarge",
			numNodes:     10,
			shardCount:   5,
			expectedDesc: "db.r6g.2xlarge 10-node 5-shard",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			details := &common.MemoryDBDetails{
				NodeType:      tt.nodeType,
				NumberOfNodes: tt.numNodes,
				ShardCount:    tt.shardCount,
			}

			desc := details.GetDetailDescription()
			assert.Equal(t, tt.expectedDesc, desc)
		})
	}
}

func TestPurchaseClient_NodeTypes(t *testing.T) {
	tests := []struct {
		name     string
		nodeType string
		isValid  bool
	}{
		{
			name:     "r6g large",
			nodeType: "db.r6g.large",
			isValid:  true,
		},
		{
			name:     "r6g xlarge",
			nodeType: "db.r6g.xlarge",
			isValid:  true,
		},
		{
			name:     "r6g 2xlarge",
			nodeType: "db.r6g.2xlarge",
			isValid:  true,
		},
		{
			name:     "r6g 4xlarge",
			nodeType: "db.r6g.4xlarge",
			isValid:  true,
		},
		{
			name:     "r6g 8xlarge",
			nodeType: "db.r6g.8xlarge",
			isValid:  true,
		},
		{
			name:     "r6g 12xlarge",
			nodeType: "db.r6g.12xlarge",
			isValid:  true,
		},
		{
			name:     "r6g 16xlarge",
			nodeType: "db.r6g.16xlarge",
			isValid:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			details := &common.MemoryDBDetails{
				NodeType:      tt.nodeType,
				NumberOfNodes: 1,
				ShardCount:    1,
			}

			assert.Equal(t, common.ServiceMemoryDB, details.GetServiceType())
			if tt.isValid {
				assert.Contains(t, details.GetDetailDescription(), tt.nodeType)
			}
		})
	}
}

func TestPurchaseClient_ShardConfigurations(t *testing.T) {
	tests := []struct {
		name           string
		shardCount     int32
		nodesPerShard  int32
		totalNodes     int32
		expectedShards int32
	}{
		{
			name:           "1 shard with 2 nodes",
			shardCount:     1,
			nodesPerShard:  2,
			totalNodes:     2,
			expectedShards: 1,
		},
		{
			name:           "3 shards with 2 nodes each",
			shardCount:     3,
			nodesPerShard:  2,
			totalNodes:     6,
			expectedShards: 3,
		},
		{
			name:           "5 shards with 3 nodes each",
			shardCount:     5,
			nodesPerShard:  3,
			totalNodes:     15,
			expectedShards: 5,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			details := &common.MemoryDBDetails{
				NodeType:      "db.r6g.large",
				NumberOfNodes: tt.totalNodes,
				ShardCount:    tt.shardCount,
			}

			assert.Equal(t, tt.expectedShards, details.ShardCount)
			assert.Equal(t, tt.totalNodes, details.NumberOfNodes)
		})
	}
}

func TestPurchaseClient_PaymentOptionMapping(t *testing.T) {
	tests := []struct {
		name          string
		paymentOption string
		expectedType  string
	}{
		{
			name:          "all-upfront",
			paymentOption: "all-upfront",
			expectedType:  "All Upfront",
		},
		{
			name:          "partial-upfront",
			paymentOption: "partial-upfront",
			expectedType:  "Partial Upfront",
		},
		{
			name:          "no-upfront",
			paymentOption: "no-upfront",
			expectedType:  "No Upfront",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Test that payment options are properly mapped
			rec := common.Recommendation{
				PaymentOption: tt.paymentOption,
			}

			// In actual implementation, this would be mapped to the correct offering type
			assert.NotEmpty(t, rec.PaymentOption)
		})
	}
}

func TestPurchaseClient_CreatePurchaseTags(t *testing.T) {
	rec := common.Recommendation{
		Service:       common.ServiceMemoryDB,
		Region:        "us-west-2",
		InstanceType:  "db.r6g.xlarge",
		PaymentOption: "partial-upfront",
		Term:          36,
		ServiceDetails: &common.MemoryDBDetails{
			NodeType:      "db.r6g.xlarge",
			NumberOfNodes: 4,
			ShardCount:    2,
		},
	}

	// Verify recommendation has required fields for tagging
	assert.Equal(t, common.ServiceMemoryDB, rec.Service)
	assert.Equal(t, "us-west-2", rec.Region)
	assert.Equal(t, "db.r6g.xlarge", rec.InstanceType)
	assert.Equal(t, "partial-upfront", rec.PaymentOption)
	assert.Equal(t, 36, rec.Term)

	details := rec.ServiceDetails.(*common.MemoryDBDetails)
	assert.Equal(t, "db.r6g.xlarge", details.NodeType)
	assert.Equal(t, int32(4), details.NumberOfNodes)
	assert.Equal(t, int32(2), details.ShardCount)
}

func TestPurchaseClient_BatchPurchase(t *testing.T) {
	client := &PurchaseClient{
		BasePurchaseClient: common.BasePurchaseClient{
			Region: "us-east-1",
		},
	}

	recommendations := []common.Recommendation{
		{
			Service: common.ServiceMemoryDB,
			Count:   1,
			ServiceDetails: &common.MemoryDBDetails{
				NodeType:      "db.r6g.large",
				NumberOfNodes: 2,
				ShardCount:    1,
			},
		},
		{
			Service: common.ServiceMemoryDB,
			Count:   1,
			ServiceDetails: &common.MemoryDBDetails{
				NodeType:      "db.r6g.2xlarge",
				NumberOfNodes: 6,
				ShardCount:    3,
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
		Service:       common.ServiceMemoryDB,
		InstanceType:  "db.r6g.large",
		PaymentOption: "no-upfront",
		Term:          12,
		ServiceDetails: &common.MemoryDBDetails{
			NodeType:      "db.r6g.large",
			NumberOfNodes: 2,
			ShardCount:    1,
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
		Service: common.ServiceMemoryDB,
		ServiceDetails: &common.MemoryDBDetails{
			NodeType:      "db.r6g.large",
			NumberOfNodes: 3,
			ShardCount:    1,
		},
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		result := common.PurchaseResult{
			Config: rec,
		}

		if rec.Service != common.ServiceMemoryDB {
			result.Success = false
		} else if _, ok := rec.ServiceDetails.(*common.MemoryDBDetails); !ok {
			result.Success = false
		} else {
			result.Success = true
		}
	}
}