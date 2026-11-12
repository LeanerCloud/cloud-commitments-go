package rds

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	"github.com/aws/aws-sdk-go-v2/service/rds/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/provider"
)

func TestClient_ImplementsOfferingPricer(t *testing.T) {
	t.Parallel()
	mockRDS := &MockRDSClient{}
	client := &Client{client: mockRDS, region: "us-east-2"}
	rec := common.Recommendation{
		Service: common.ServiceRelationalDB, ResourceType: "db.m6g.large",
		PaymentOption: "all-upfront", Term: "1yr",
		Details: &common.DatabaseDetails{Engine: "postgres", AZConfig: "multi-az"},
	}
	mockRDS.On("DescribeReservedDBInstancesOfferings", mock.Anything, mock.Anything).
		Return(&rds.DescribeReservedDBInstancesOfferingsOutput{
			ReservedDBInstancesOfferings: []types.ReservedDBInstancesOffering{{
				ReservedDBInstancesOfferingId: aws.String("offering-999"),
				DBInstanceClass:               aws.String("db.m6g.large"),
				Duration:                      aws.Int32(31536000),
				OfferingType:                  aws.String("All Upfront"),
				MultiAZ:                       aws.Bool(true),
				ProductDescription:            aws.String("postgresql"),
				FixedPrice:                    aws.Float64(3500.0),
				UsagePrice:                    aws.Float64(0.0),
				CurrencyCode:                  aws.String("USD"),
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
	assert.Equal(t, "offering-999", got.OfferingID)
}
