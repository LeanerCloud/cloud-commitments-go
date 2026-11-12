package redshift

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/redshift"
	"github.com/aws/aws-sdk-go-v2/service/redshift/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/provider"
)

func TestClient_ImplementsOfferingPricer(t *testing.T) {
	t.Parallel()
	mockRS := &MockRedshiftClient{}
	client := &Client{client: mockRS, region: "us-east-1"}
	rec := common.Recommendation{
		Service: common.ServiceDataWarehouse, ResourceType: "dc2.large",
		PaymentOption: "all-upfront", Term: "1yr",
		Details: common.DataWarehouseDetails{NodeType: "dc2.large", NumberOfNodes: 2},
	}
	// Lookup by node type (findOfferingID), then by the resolved offering ID.
	mockRS.On("DescribeReservedNodeOfferings", mock.Anything, mock.MatchedBy(func(input *redshift.DescribeReservedNodeOfferingsInput) bool {
		return input.ReservedNodeOfferingId == nil
	})).Return(&redshift.DescribeReservedNodeOfferingsOutput{
		ReservedNodeOfferings: []types.ReservedNodeOffering{{
			ReservedNodeOfferingId:   aws.String("offering-123"),
			NodeType:                 aws.String("dc2.large"),
			Duration:                 aws.Int32(31536000),
			ReservedNodeOfferingType: types.ReservedNodeOfferingType("Regular"),
			FixedPrice:               aws.Float64(500.0),
		}},
	}, nil)
	mockRS.On("DescribeReservedNodeOfferings", mock.Anything, mock.MatchedBy(func(input *redshift.DescribeReservedNodeOfferingsInput) bool {
		return input.ReservedNodeOfferingId != nil && *input.ReservedNodeOfferingId == "offering-123"
	})).Return(&redshift.DescribeReservedNodeOfferingsOutput{
		ReservedNodeOfferings: []types.ReservedNodeOffering{{
			ReservedNodeOfferingId:   aws.String("offering-123"),
			NodeType:                 aws.String("dc2.large"),
			Duration:                 aws.Int32(31536000),
			ReservedNodeOfferingType: types.ReservedNodeOfferingType("Regular"),
			FixedPrice:               aws.Float64(500.0),
			UsagePrice:               aws.Float64(0),
			CurrencyCode:             aws.String("USD"),
			RecurringCharges: []types.RecurringCharge{{
				RecurringChargeAmount:    aws.Float64(0.15),
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
