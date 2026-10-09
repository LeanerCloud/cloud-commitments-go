package memorydb

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/aws/aws-sdk-go-v2/service/memorydb"
	"github.com/aws/aws-sdk-go-v2/service/memorydb/types"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
)

// FIXTURE-BASED: SDK-typed response served by a fake client, not live AWS.
// The SDK offering type has no currency field, so USD is assumed outside cn- regions.
func TestGetOfferingDetails_PricesRecurringChargesAndCurrencyRule(t *testing.T) {
	rec := common.Recommendation{
		Service: common.ServiceCache, ResourceType: "db.r6gd.xlarge", PaymentOption: "partial-upfront", Term: "1yr",
		Details: &common.CacheDetails{Engine: "redis", NodeType: "db.r6gd.xlarge"},
	}
	for _, tc := range []struct{ region, err string }{{"us-east-1", ""}, {"cn-northwest-1", "not billed in USD"}} {
		t.Run(tc.region, func(t *testing.T) {
			m := &MockMemoryDBClient{}
			m.On("DescribeReservedNodesOfferings", mock.Anything, mock.Anything).
				Return(&memorydb.DescribeReservedNodesOfferingsOutput{ReservedNodesOfferings: []types.ReservedNodesOffering{{
					ReservedNodesOfferingId: aws.String("off-1"), NodeType: aws.String("db.r6gd.xlarge"), Duration: 31536000,
					OfferingType: aws.String("Partial Upfront"), FixedPrice: 5000,
					RecurringCharges: []types.RecurringCharge{{RecurringChargeAmount: 0.25, RecurringChargeFrequency: aws.String("Hourly")}},
				}}}, nil)
			d, err := (&Client{client: m, region: tc.region}).GetOfferingDetails(context.Background(), rec)
			if tc.err != "" {
				require.ErrorContains(t, err, tc.err)
				return
			}
			require.NoError(t, err)
			assert.InDelta(t, 0.25, d.RecurringCost, 1e-12)
			assert.InDelta(t, 5000+0.25*8760, d.TotalCost, 1e-9)
			assert.Equal(t, "USD", d.Currency)
		})
	}
}
