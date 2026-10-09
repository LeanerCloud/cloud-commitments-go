package redshift

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/aws/aws-sdk-go-v2/service/redshift"
	"github.com/aws/aws-sdk-go-v2/service/redshift/types"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
)

// FIXTURE-BASED: SDK-typed response served by a fake client, not live AWS.
func TestGetOfferingDetails_PricesRecurringChargesAndCurrency(t *testing.T) {
	rec := common.Recommendation{
		Service: common.ServiceDataWarehouse, ResourceType: "dc2.large", PaymentOption: "partial-upfront", Term: "1yr",
		Details: common.DataWarehouseDetails{NodeType: "dc2.large", NumberOfNodes: 2},
	}
	for _, tc := range []struct{ name, currency, err string }{{"ok", "USD", ""}, {"empty currency", "", "no currency"}} {
		t.Run(tc.name, func(t *testing.T) {
			m := &MockRedshiftClient{}
			m.On("DescribeReservedNodeOfferings", mock.Anything, mock.Anything).
				Return(&redshift.DescribeReservedNodeOfferingsOutput{ReservedNodeOfferings: []types.ReservedNodeOffering{{
					ReservedNodeOfferingId: aws.String("off-1"), NodeType: aws.String("dc2.large"), Duration: aws.Int32(31536000),
					ReservedNodeOfferingType: types.ReservedNodeOfferingType("Regular"), FixedPrice: aws.Float64(500),
					UsagePrice: aws.Float64(0), CurrencyCode: aws.String(tc.currency),
					RecurringCharges: []types.RecurringCharge{{RecurringChargeAmount: aws.Float64(0.15), RecurringChargeFrequency: aws.String("Hourly")}},
				}}}, nil)
			d, err := (&Client{client: m, region: "us-east-1"}).GetOfferingDetails(context.Background(), rec)
			if tc.err != "" {
				require.ErrorContains(t, err, tc.err)
				return
			}
			require.NoError(t, err)
			assert.InDelta(t, 0.15, d.RecurringCost, 1e-12)
			assert.InDelta(t, 500+0.15*8760, d.TotalCost, 1e-9)
		})
	}
}

// Purchase-path regression: matchesPaymentOption still reads the Hourly charge
// via offeringRecurringRate, which this change must not alter (the pricing path
// uses the stricter offeringprice package).
func TestOfferingRecurringRate_PurchasePathUnchanged(t *testing.T) {
	o := types.ReservedNodeOffering{
		FixedPrice: aws.Float64(100), UsagePrice: aws.Float64(0.10),
		RecurringCharges: []types.RecurringCharge{{RecurringChargeAmount: aws.Float64(0.15), RecurringChargeFrequency: aws.String("Hourly")}},
	}
	assert.Equal(t, 0.15, offeringRecurringRate(o), "Hourly charge wins over UsagePrice, even when both are nonzero")
	assert.True(t, matchesPaymentOption(o, "partial-upfront"))
	o.RecurringCharges = nil
	assert.Equal(t, 0.10, offeringRecurringRate(o), "falls back to UsagePrice")
}
