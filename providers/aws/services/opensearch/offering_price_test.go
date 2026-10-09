package opensearch

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/aws/aws-sdk-go-v2/service/opensearch"
	"github.com/aws/aws-sdk-go-v2/service/opensearch/types"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
)

// FIXTURE-BASED: SDK-typed response served by a fake client, not live AWS.
func TestGetOfferingDetails_PricesRecurringChargesAndCurrency(t *testing.T) {
	rec := common.Recommendation{
		Service: common.ServiceSearch, ResourceType: "m5.large.search", PaymentOption: "partial-upfront", Term: "1yr",
		Details: common.SearchDetails{InstanceType: "m5.large.search"},
	}
	for _, tc := range []struct {
		name, currency, err string
	}{{"ok", "USD", ""}, {"empty currency", "", "no currency"}} {
		t.Run(tc.name, func(t *testing.T) {
			m := &MockOpenSearchClient{}
			m.On("DescribeReservedInstanceOfferings", mock.Anything, mock.Anything).
				Return(&opensearch.DescribeReservedInstanceOfferingsOutput{ReservedInstanceOfferings: []types.ReservedInstanceOffering{{
					ReservedInstanceOfferingId: aws.String("off-1"), InstanceType: types.OpenSearchPartitionInstanceTypeM5LargeSearch,
					Duration: 31536000, PaymentOption: types.ReservedInstancePaymentOptionPartialUpfront,
					FixedPrice: aws.Float64(3000), UsagePrice: aws.Float64(0), CurrencyCode: aws.String(tc.currency),
					RecurringCharges: []types.RecurringCharge{{RecurringChargeAmount: aws.Float64(0.15), RecurringChargeFrequency: aws.String("Hourly")}},
				}}}, nil)
			d, err := (&Client{client: m, region: "us-east-1"}).GetOfferingDetails(context.Background(), rec)
			if tc.err != "" {
				require.ErrorContains(t, err, tc.err)
				return
			}
			require.NoError(t, err)
			assert.InDelta(t, 0.15, d.RecurringCost, 1e-12)
			assert.InDelta(t, 3000+0.15*8760, d.TotalCost, 1e-9)
		})
	}
}
