package memorydb

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/memorydb"
	"github.com/aws/aws-sdk-go-v2/service/memorydb/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/provider"
)

func TestClient_ImplementsOfferingPricer(t *testing.T) {
	t.Parallel()
	mockMDB := &MockMemoryDBClient{}
	client := &Client{client: mockMDB, region: "us-east-1"}
	rec := common.Recommendation{
		Service: common.ServiceCache, ResourceType: "db.r6gd.xlarge",
		PaymentOption: "partial-upfront", Term: "1yr",
		Details: &common.CacheDetails{Engine: "redis", NodeType: "db.r6gd.xlarge"},
	}
	mockMDB.On("DescribeReservedNodesOfferings", mock.Anything, mock.Anything).
		Return(&memorydb.DescribeReservedNodesOfferingsOutput{
			ReservedNodesOfferings: []types.ReservedNodesOffering{{
				ReservedNodesOfferingId: aws.String("offering-123"),
				NodeType:                aws.String("db.r6gd.xlarge"),
				Duration:                31536000,
				OfferingType:            aws.String("Partial Upfront"),
				FixedPrice:              5000.0,
				RecurringCharges: []types.RecurringCharge{{
					RecurringChargeAmount:    0.25,
					RecurringChargeFrequency: aws.String("Hourly"),
				}},
			}},
		}, nil)

	var sc provider.ServiceClient = client
	pricer, ok := sc.(provider.OfferingPricer)
	require.True(t, ok, "client must implement provider.OfferingPricer")

	got, err := pricer.GetOfferingDetails(context.Background(), rec)
	require.NoError(t, err)
	want, err := client.GetOfferingDetails(context.Background(), rec)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, want, got)
	assert.Equal(t, "offering-123", got.OfferingID)
}
