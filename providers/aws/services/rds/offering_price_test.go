package rds

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/aws/aws-sdk-go-v2/service/rds"
	"github.com/aws/aws-sdk-go-v2/service/rds/types"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
)

// FIXTURE-BASED: SDK-typed response served by a fake client, not live AWS.
func TestGetOfferingDetails_PricesRecurringChargesAndCurrency(t *testing.T) {
	rec := common.Recommendation{
		Service: common.ServiceRelationalDB, ResourceType: "db.m6g.large", PaymentOption: "no-upfront", Term: "1yr",
		Details: &common.DatabaseDetails{Engine: "postgres", AZConfig: "multi-az"},
	}
	for _, tc := range []struct {
		name     string
		usage    float64
		currency string
		err      string
		total    float64
	}{
		{"no-upfront hourly charge, usage 0", 0, "USD", "", 0.05 * 8760},
		{"empty currency", 0, "", "no currency", 0},
		{"usage and charge both nonzero", 0.05, "USD", "refusing to guess", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := &MockRDSClient{}
			m.On("DescribeReservedDBInstancesOfferings", mock.Anything, mock.Anything).
				Return(&rds.DescribeReservedDBInstancesOfferingsOutput{ReservedDBInstancesOfferings: []types.ReservedDBInstancesOffering{{
					ReservedDBInstancesOfferingId: aws.String("off-1"), DBInstanceClass: aws.String("db.m6g.large"),
					Duration: aws.Int32(31536000), OfferingType: aws.String("No Upfront"), MultiAZ: aws.Bool(true),
					ProductDescription: aws.String("postgresql"), FixedPrice: aws.Float64(0), UsagePrice: aws.Float64(tc.usage),
					CurrencyCode:     aws.String(tc.currency),
					RecurringCharges: []types.RecurringCharge{{RecurringChargeAmount: aws.Float64(0.05), RecurringChargeFrequency: aws.String("Hourly")}},
				}}}, nil)
			d, err := (&Client{client: m, region: "us-east-2"}).GetOfferingDetails(context.Background(), rec)
			if tc.err != "" {
				require.ErrorContains(t, err, tc.err)
				return
			}
			require.NoError(t, err)
			assert.InDelta(t, 0.05, d.RecurringCost, 1e-12)
			assert.InDelta(t, tc.total, d.TotalCost, 1e-9)
		})
	}
}
