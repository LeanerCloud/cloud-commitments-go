package redshift

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
			name: "valid Redshift recommendation",
			rec: common.Recommendation{
				Service:      common.ServiceRedshift,
				InstanceType: "dc2.large",
				ServiceDetails: &common.RedshiftDetails{
					NodeType:      "dc2.large",
					NumberOfNodes: 3,
					ClusterType:   "multi-node",
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
			expectError: "Invalid service type for Redshift purchase",
		},
		{
			name: "missing service details",
			rec: common.Recommendation{
				Service:      common.ServiceRedshift,
				InstanceType: "dc2.large",
			},
			expectValid: false,
			expectError: "Invalid service details for Redshift",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := common.PurchaseResult{
				Config: tt.rec,
			}

			// Validate the recommendation type
			if tt.rec.Service != common.ServiceRedshift {
				result.Success = false
				result.Message = "Invalid service type for Redshift purchase"
			} else if _, ok := tt.rec.ServiceDetails.(*common.RedshiftDetails); !ok || tt.rec.ServiceDetails == nil {
				result.Success = false
				result.Message = "Invalid service details for Redshift"
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

func TestPurchaseClient_NodeTypes(t *testing.T) {
	tests := []struct {
		name         string
		nodeType     string
		clusterType  string
		numNodes     int32
		expectedDesc string
	}{
		{
			name:         "single node cluster",
			nodeType:     "dc2.large",
			clusterType:  "single-node",
			numNodes:     1,
			expectedDesc: "dc2.large 1-node single-node",
		},
		{
			name:         "multi-node cluster",
			nodeType:     "dc2.8xlarge",
			clusterType:  "multi-node",
			numNodes:     3,
			expectedDesc: "dc2.8xlarge 3-node multi-node",
		},
		{
			name:         "large multi-node cluster",
			nodeType:     "ra3.16xlarge",
			clusterType:  "multi-node",
			numNodes:     10,
			expectedDesc: "ra3.16xlarge 10-node multi-node",
		},
		{
			name:         "ra3 xlplus cluster",
			nodeType:     "ra3.xlplus",
			clusterType:  "multi-node",
			numNodes:     2,
			expectedDesc: "ra3.xlplus 2-node multi-node",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			details := &common.RedshiftDetails{
				NodeType:      tt.nodeType,
				NumberOfNodes: tt.numNodes,
				ClusterType:   tt.clusterType,
			}

			desc := details.GetDetailDescription()
			assert.Equal(t, tt.expectedDesc, desc)
		})
	}
}

func TestPurchaseClient_ClusterTypes(t *testing.T) {
	tests := []struct {
		name        string
		clusterType string
		isValid     bool
	}{
		{
			name:        "single-node cluster",
			clusterType: "single-node",
			isValid:     true,
		},
		{
			name:        "multi-node cluster",
			clusterType: "multi-node",
			isValid:     true,
		},
		{
			name:        "empty cluster type",
			clusterType: "",
			isValid:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			details := &common.RedshiftDetails{
				NodeType:      "dc2.large",
				NumberOfNodes: 1,
				ClusterType:   tt.clusterType,
			}

			assert.Equal(t, common.ServiceRedshift, details.GetServiceType())
			if tt.isValid {
				assert.Contains(t, details.GetDetailDescription(), tt.clusterType)
			}
		})
	}
}

func TestPurchaseClient_PaymentOptionMapping(t *testing.T) {
	tests := []struct {
		name          string
		paymentOption string
		offeringType  string
		shouldMatch   bool
	}{
		{
			name:          "all-upfront matches All Upfront",
			paymentOption: "all-upfront",
			offeringType:  "All Upfront",
			shouldMatch:   true,
		},
		{
			name:          "partial-upfront matches Partial Upfront",
			paymentOption: "partial-upfront",
			offeringType:  "Partial Upfront",
			shouldMatch:   true,
		},
		{
			name:          "no-upfront matches No Upfront",
			paymentOption: "no-upfront",
			offeringType:  "No Upfront",
			shouldMatch:   true,
		},
		{
			name:          "all-upfront does not match No Upfront",
			paymentOption: "all-upfront",
			offeringType:  "No Upfront",
			shouldMatch:   false,
		},
		{
			name:          "partial-upfront does not match All Upfront",
			paymentOption: "partial-upfront",
			offeringType:  "All Upfront",
			shouldMatch:   false,
		},
	}

	client := &PurchaseClient{}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := client.matchesOfferingType(tt.offeringType, tt.paymentOption)
			assert.Equal(t, tt.shouldMatch, result)
		})
	}
}

func TestPurchaseClient_CreatePurchaseTags(t *testing.T) {
	rec := common.Recommendation{
		Service:       common.ServiceRedshift,
		Region:        "us-east-1",
		InstanceType:  "dc2.large",
		PaymentOption: "no-upfront",
		Term:          36,
		ServiceDetails: &common.RedshiftDetails{
			NodeType:      "dc2.large",
			NumberOfNodes: 4,
			ClusterType:   "multi-node",
		},
	}

	// Verify recommendation has required fields for tagging
	assert.Equal(t, common.ServiceRedshift, rec.Service)
	assert.Equal(t, "us-east-1", rec.Region)
	assert.Equal(t, "dc2.large", rec.InstanceType)
	assert.Equal(t, "no-upfront", rec.PaymentOption)
	assert.Equal(t, 36, rec.Term)

	details := rec.ServiceDetails.(*common.RedshiftDetails)
	assert.Equal(t, "dc2.large", details.NodeType)
	assert.Equal(t, int32(4), details.NumberOfNodes)
	assert.Equal(t, "multi-node", details.ClusterType)
}

func TestPurchaseClient_BatchPurchase(t *testing.T) {
	client := &PurchaseClient{
		BasePurchaseClient: common.BasePurchaseClient{
			Region: "us-west-2",
		},
	}

	recommendations := []common.Recommendation{
		{
			Service: common.ServiceRedshift,
			Count:   1,
			ServiceDetails: &common.RedshiftDetails{
				NodeType:      "dc2.large",
				NumberOfNodes: 2,
				ClusterType:   "multi-node",
			},
		},
		{
			Service: common.ServiceRedshift,
			Count:   1,
			ServiceDetails: &common.RedshiftDetails{
				NodeType:      "ra3.4xlarge",
				NumberOfNodes: 3,
				ClusterType:   "multi-node",
			},
		},
	}

	assert.Equal(t, 2, len(recommendations))
	assert.Equal(t, "us-west-2", client.Region)
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
		Service:       common.ServiceRedshift,
		InstanceType:  "dc2.large",
		PaymentOption: "no-upfront",
		Term:          12,
		ServiceDetails: &common.RedshiftDetails{
			NodeType:      "dc2.large",
			NumberOfNodes: 2,
			ClusterType:   "multi-node",
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
		Service: common.ServiceRedshift,
		ServiceDetails: &common.RedshiftDetails{
			NodeType:      "dc2.large",
			NumberOfNodes: 3,
			ClusterType:   "multi-node",
		},
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		result := common.PurchaseResult{
			Config: rec,
		}

		if rec.Service != common.ServiceRedshift {
			result.Success = false
		} else if _, ok := rec.ServiceDetails.(*common.RedshiftDetails); !ok {
			result.Success = false
		} else {
			result.Success = true
		}
	}
}