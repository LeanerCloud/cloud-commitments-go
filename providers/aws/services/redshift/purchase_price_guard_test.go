package redshift

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/redshift"
	"github.com/aws/aws-sdk-go-v2/service/redshift/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
)

func guardOffering(fixed *float64, currency string) types.ReservedNodeOffering {
	o := types.ReservedNodeOffering{
		ReservedNodeOfferingId:   aws.String("offering-1"),
		NodeType:                 aws.String("ra3.xlplus"),
		Duration:                 aws.Int32(31536000),
		ReservedNodeOfferingType: types.ReservedNodeOfferingType("Regular"),
		FixedPrice:               fixed,
	}
	if currency != "" {
		o.CurrencyCode = aws.String(currency)
	}
	return o
}

// byIDMock serves the page lookup (an all-upfront priced offering-1), then
// answers the by-ID price read with the given offerings or error, and accepts
// the purchase.
func byIDMock(byID []types.ReservedNodeOffering, byIDErr error) *MockRedshiftClient {
	m := &MockRedshiftClient{}
	isByID := func(in *redshift.DescribeReservedNodeOfferingsInput) bool {
		return aws.ToString(in.ReservedNodeOfferingId) == "offering-1"
	}
	m.On("DescribeReservedNodeOfferings", mock.Anything, mock.MatchedBy(isByID)).
		Return(&redshift.DescribeReservedNodeOfferingsOutput{ReservedNodeOfferings: byID}, byIDErr)
	m.On("DescribeReservedNodeOfferings", mock.Anything, mock.MatchedBy(func(in *redshift.DescribeReservedNodeOfferingsInput) bool { return !isByID(in) })).
		Return(&redshift.DescribeReservedNodeOfferingsOutput{ReservedNodeOfferings: []types.ReservedNodeOffering{guardOffering(aws.Float64(1000), "")}}, nil)
	expectBuy(m)
	return m
}

func guardMock(fixed float64, currency string) *MockRedshiftClient {
	return byIDMock([]types.ReservedNodeOffering{guardOffering(aws.Float64(fixed), currency)}, nil)
}

// guardRec is a 2-node all-upfront rec approved at a $1000 total upfront.
func guardRec() common.Recommendation {
	r := rsIdemRec()
	r.Count = 2
	r.CommitmentCost = 1000
	return r
}

func buy(m *MockRedshiftClient, rec common.Recommendation) (common.PurchaseResult, error) {
	return (&Client{client: m, region: "us-east-1"}).PurchaseCommitment(context.Background(), rec, common.PurchaseOptions{})
}

func notBought(t *testing.T, m *MockRedshiftClient) {
	t.Helper()
	m.AssertNotCalled(t, "PurchaseReservedNodeOffering", mock.Anything, mock.Anything)
}

func TestPurchaseCommitment_RefusesOfferingAboveApprovedUpfront(t *testing.T) {
	t.Parallel()
	m := guardMock(525.01, "USD") // 2 x 525.01 = 1050.02 > ceiling 1050.00
	res, err := buy(m, guardRec())
	require.ErrorContains(t, err, "exceeds the approved upfront")
	assert.ErrorContains(t, err, "1050.02")
	assert.ErrorContains(t, err, "1000.00")
	assert.False(t, res.Success)
	notBought(t, m)
}

func TestPurchaseCommitment_BuysAtExactlyTolerance(t *testing.T) {
	t.Parallel()
	m := guardMock(525, "USD") // 2 x 525 = 1050 == ceiling
	res, err := buy(m, guardRec())
	require.NoError(t, err)
	assert.True(t, res.Success)
	m.AssertCalled(t, "PurchaseReservedNodeOffering", mock.Anything, mock.Anything)
}

func TestPurchaseCommitment_ZeroApprovedUpfrontIsNotChecked(t *testing.T) {
	t.Parallel()
	m := guardMock(9000, "USD")
	rec := guardRec()
	rec.CommitmentCost = 0
	_, err := buy(m, rec)
	require.NoError(t, err)
	m.AssertNumberOfCalls(t, "DescribeReservedNodeOfferings", 1)
}

func TestPurchaseCommitment_RefusesNonUSD(t *testing.T) {
	t.Parallel()
	m := guardMock(400, "EUR")
	_, err := buy(m, guardRec())
	require.ErrorContains(t, err, "EUR")
	notBought(t, m)

	m = guardMock(400, "USD")
	rec := guardRec()
	rec.CommitmentCurrency = "eur"
	_, err = buy(m, rec)
	require.ErrorContains(t, err, "eur")
	notBought(t, m)
}

func TestPurchaseCommitment_EmptyAndLowercaseUSDBuy(t *testing.T) {
	t.Parallel()
	_, err := buy(guardMock(500, ""), guardRec())
	require.NoError(t, err)
	_, err = buy(guardMock(500, "usd"), guardRec())
	require.NoError(t, err)
	rec := guardRec()
	rec.CommitmentCurrency = "usd"
	_, err = buy(guardMock(500, "USD"), rec)
	require.NoError(t, err)
}

// No-upfront recs carry CommitmentCost 0, so the price is never read back.
func TestPurchaseCommitment_NoUpfrontUnaffected(t *testing.T) {
	t.Parallel()
	m := &MockRedshiftClient{}
	noUpfront := guardOffering(nil, "")
	noUpfront.RecurringCharges = []types.RecurringCharge{{RecurringChargeAmount: aws.Float64(0.5), RecurringChargeFrequency: aws.String("Hourly")}}
	m.On("DescribeReservedNodeOfferings", mock.Anything, mock.Anything).
		Return(&redshift.DescribeReservedNodeOfferingsOutput{ReservedNodeOfferings: []types.ReservedNodeOffering{noUpfront}}, nil)
	expectBuy(m)
	rec := rsIdemRec()
	rec.PaymentOption = "no-upfront"
	_, err := buy(m, rec)
	require.NoError(t, err)
	m.AssertNumberOfCalls(t, "DescribeReservedNodeOfferings", 1)
}

// A re-drive whose node already exists must return it even after the offering
// was repriced above the approved upfront: nothing is bought, so no refusal.
func TestPurchaseCommitment_ExistingNodeWinsOverRepricedOffering(t *testing.T) {
	t.Parallel()
	m := guardMock(600, "USD") // 2 x 600 = 1200 against an approved 1000
	expectNodePages(m, []types.ReservedNode{
		dnode("rn-existing", "active", dedupeReqAt.Add(time.Minute), func(n *types.ReservedNode) { n.NodeCount = aws.Int32(2) }),
	})
	res, err := (&Client{client: m, region: "us-east-1"}).PurchaseCommitment(context.Background(), guardRec(), dedupeOpts())
	require.NoError(t, err)
	assert.True(t, res.Success)
	assert.True(t, res.ExistingCommitment)
	assert.Equal(t, "rn-existing", res.CommitmentID)
	notBought(t, m)
}

func TestPurchaseCommitment_RefusesWhenPriceReadFails(t *testing.T) {
	t.Parallel()
	m := byIDMock(nil, errors.New("throttled"))
	res, err := buy(m, guardRec())
	require.ErrorContains(t, err, "approved upfront check")
	assert.ErrorContains(t, err, "throttled")
	assert.False(t, res.Success)
	notBought(t, m)
}

func TestPurchaseCommitment_RefusesWhenPriceReadIsNotOneOffering(t *testing.T) {
	t.Parallel()
	two := []types.ReservedNodeOffering{guardOffering(aws.Float64(400), "USD"), guardOffering(aws.Float64(400), "USD")}
	for _, n := range [][]types.ReservedNodeOffering{two, nil} {
		m := byIDMock(n, nil)
		_, err := buy(m, guardRec())
		require.ErrorContains(t, err, "expected 1 offering")
		notBought(t, m)
	}
}

func TestPurchaseCommitment_RefusesOfferingWithoutFixedPrice(t *testing.T) {
	t.Parallel()
	m := byIDMock([]types.ReservedNodeOffering{guardOffering(nil, "")}, nil)
	_, err := buy(m, guardRec())
	require.ErrorContains(t, err, "no FixedPrice")
	notBought(t, m)
}
