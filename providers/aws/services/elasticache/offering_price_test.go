package elasticache

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/aws/aws-sdk-go-v2/service/elasticache"
	"github.com/aws/aws-sdk-go-v2/service/elasticache/types"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
)

// FIXTURE-BASED: SDK-typed response served by a fake client, not live AWS.
// The SDK offering type has no currency field, so USD is assumed outside cn- regions.
func TestGetOfferingDetails_PricesRecurringChargesAndCurrencyRule(t *testing.T) {
	rec := common.Recommendation{
		Service: common.ServiceCache, ResourceType: "cache.r6g.xlarge", PaymentOption: "partial-upfront", Term: "1yr",
		Details: &common.CacheDetails{Engine: "redis", NodeType: "cache.r6g.xlarge"},
	}
	for _, tc := range []struct{ region, err string }{{"us-east-2", ""}, {"cn-north-1", "not billed in USD"}} {
		t.Run(tc.region, func(t *testing.T) {
			m := &MockElastiCacheClient{}
			m.On("DescribeReservedCacheNodesOfferings", mock.Anything, mock.Anything).
				Return(&elasticache.DescribeReservedCacheNodesOfferingsOutput{ReservedCacheNodesOfferings: []types.ReservedCacheNodesOffering{{
					ReservedCacheNodesOfferingId: aws.String("off-1"), CacheNodeType: aws.String("cache.r6g.xlarge"),
					Duration: aws.Int32(31536000), OfferingType: aws.String("Partial Upfront"), ProductDescription: aws.String("redis"),
					FixedPrice: aws.Float64(1000), UsagePrice: aws.Float64(0),
					RecurringCharges: []types.RecurringCharge{{RecurringChargeAmount: aws.Float64(0.1), RecurringChargeFrequency: aws.String("Hourly")}},
				}}}, nil)
			d, err := (&Client{client: m, region: tc.region}).GetOfferingDetails(context.Background(), rec)
			if tc.err != "" {
				require.ErrorContains(t, err, tc.err)
				return
			}
			require.NoError(t, err)
			assert.InDelta(t, 0.1, d.RecurringCost, 1e-12)
			assert.InDelta(t, 1000+0.1*8760, d.TotalCost, 1e-9)
			assert.Equal(t, "USD", d.Currency)
		})
	}
}
