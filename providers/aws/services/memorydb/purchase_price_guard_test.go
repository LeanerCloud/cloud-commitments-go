package memorydb

import (
	"context"
	"errors"
	"math"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/memorydb"
	"github.com/aws/aws-sdk-go-v2/service/memorydb/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
)

func guardOffering(fixed float64) types.ReservedNodesOffering {
	return types.ReservedNodesOffering{
		ReservedNodesOfferingId: aws.String("offering-1"),
		NodeType:                aws.String("db.r6gd.large"),
		Duration:                31536000,
		OfferingType:            aws.String("All Upfront"),
		FixedPrice:              fixed,
	}
}

// byIDMock serves the page lookup, then answers the by-ID price read with the
// given offerings or error, and accepts the purchase.
func byIDMock(byID []types.ReservedNodesOffering, byIDErr error) *MockMemoryDBClient {
	m := &MockMemoryDBClient{}
	isByID := func(in *memorydb.DescribeReservedNodesOfferingsInput) bool {
		return aws.ToString(in.ReservedNodesOfferingId) == "offering-1"
	}
	m.On("DescribeReservedNodesOfferings", mock.Anything, mock.MatchedBy(isByID)).
		Return(&memorydb.DescribeReservedNodesOfferingsOutput{ReservedNodesOfferings: byID}, byIDErr)
	m.On("DescribeReservedNodesOfferings", mock.Anything, mock.MatchedBy(func(in *memorydb.DescribeReservedNodesOfferingsInput) bool { return !isByID(in) })).
		Return(&memorydb.DescribeReservedNodesOfferingsOutput{ReservedNodesOfferings: []types.ReservedNodesOffering{guardOffering(0)}}, nil)
	m.On("PurchaseReservedNodesOffering", mock.Anything, mock.Anything).
		Return(&memorydb.PurchaseReservedNodesOfferingOutput{ReservedNode: &types.ReservedNode{ReservationId: aws.String("rn-1")}}, nil)
	return m
}

func guardMock(fixed float64) *MockMemoryDBClient {
	return byIDMock([]types.ReservedNodesOffering{guardOffering(fixed)}, nil)
}

// guardRec is a 2-node all-upfront rec approved at a $1000 total upfront.
func guardRec() common.Recommendation {
	r := mdbIdemRec()
	r.Count = 2
	r.CommitmentCost = 1000
	return r
}

func buy(m *MockMemoryDBClient, rec common.Recommendation) (common.PurchaseResult, error) {
	return (&Client{client: m, region: "us-east-1"}).PurchaseCommitment(context.Background(), rec, common.PurchaseOptions{})
}

func TestPurchaseCommitment_RefusesOfferingAboveApprovedUpfront(t *testing.T) {
	t.Parallel()
	m := guardMock(525.01) // 2 x 525.01 = 1050.02 > ceiling 1050.00
	res, err := buy(m, guardRec())
	require.ErrorContains(t, err, "exceeds the approved upfront")
	assert.ErrorContains(t, err, "1050.02")
	assert.ErrorContains(t, err, "1000.00")
	assert.False(t, res.Success)
	m.AssertNotCalled(t, "PurchaseReservedNodesOffering", mock.Anything, mock.Anything)
}

func TestPurchaseCommitment_BuysAtExactlyTolerance(t *testing.T) {
	t.Parallel()
	m := guardMock(525) // 2 x 525 = 1050 == ceiling
	res, err := buy(m, guardRec())
	require.NoError(t, err)
	assert.True(t, res.Success)
	m.AssertCalled(t, "PurchaseReservedNodesOffering", mock.Anything, mock.Anything)
}

func TestPurchaseCommitment_ZeroApprovedUpfrontIsNotChecked(t *testing.T) {
	t.Parallel()
	m := guardMock(9000)
	rec := guardRec()
	rec.CommitmentCost = 0
	_, err := buy(m, rec)
	require.NoError(t, err)
	m.AssertNumberOfCalls(t, "DescribeReservedNodesOfferings", 1)
}

func TestPurchaseCommitment_RefusesZeroPricedUpfrontOffering(t *testing.T) {
	t.Parallel()
	m := guardMock(0)
	res, err := buy(m, guardRec())
	require.ErrorContains(t, err, "offering-1")
	assert.ErrorContains(t, err, "no FixedPrice")
	assert.False(t, res.Success)
	m.AssertNotCalled(t, "PurchaseReservedNodesOffering", mock.Anything, mock.Anything)
}

func TestPurchaseCommitment_RefusesNonUSDRec(t *testing.T) {
	t.Parallel()
	m := guardMock(400)
	rec := guardRec()
	rec.CommitmentCurrency = "eur"
	_, err := buy(m, rec)
	require.ErrorContains(t, err, "eur")
	m.AssertNotCalled(t, "PurchaseReservedNodesOffering", mock.Anything, mock.Anything)
}

func TestPurchaseCommitment_EmptyAndLowercaseUSDBuy(t *testing.T) {
	t.Parallel()
	for _, cur := range []string{"", "usd"} {
		rec := guardRec()
		rec.CommitmentCurrency = cur
		_, err := buy(guardMock(500), rec)
		require.NoError(t, err, cur)
	}
}

// No-upfront recs carry CommitmentCost 0, so the price is never read back.
func TestPurchaseCommitment_NoUpfrontUnaffected(t *testing.T) {
	t.Parallel()
	m := &MockMemoryDBClient{}
	noUpfront := guardOffering(0)
	noUpfront.OfferingType = aws.String("No Upfront")
	m.On("DescribeReservedNodesOfferings", mock.Anything, mock.Anything).
		Return(&memorydb.DescribeReservedNodesOfferingsOutput{ReservedNodesOfferings: []types.ReservedNodesOffering{noUpfront}}, nil)
	m.On("PurchaseReservedNodesOffering", mock.Anything, mock.Anything).
		Return(&memorydb.PurchaseReservedNodesOfferingOutput{ReservedNode: &types.ReservedNode{ReservationId: aws.String("rn-1")}}, nil)
	rec := mdbIdemRec()
	rec.PaymentOption = "no-upfront"
	_, err := buy(m, rec)
	require.NoError(t, err)
	m.AssertNumberOfCalls(t, "DescribeReservedNodesOfferings", 1)
}

// A re-drive whose node already exists must return it even after the offering
// was repriced above the approved upfront: nothing is bought, so no refusal.
func TestPurchaseCommitment_ExistingNodeWinsOverRepricedOffering(t *testing.T) {
	t.Parallel()
	m := guardMock(600) // 2 x 600 = 1200 against an approved 1000
	token := common.DeriveIdempotencyToken("exec-53", 0)
	derivedID := common.IdempotentReservationID("memorydb-id-", token)
	m.On("DescribeReservedNodes", mock.Anything, mock.Anything).
		Return(&memorydb.DescribeReservedNodesOutput{ReservedNodes: []types.ReservedNode{
			{ReservationId: aws.String(derivedID), State: aws.String("active")},
		}}, nil)
	res, err := (&Client{client: m, region: "us-east-1"}).PurchaseCommitment(context.Background(), guardRec(), common.PurchaseOptions{IdempotencyToken: token})
	require.NoError(t, err)
	assert.True(t, res.Success)
	assert.True(t, res.ExistingCommitment)
	assert.Equal(t, derivedID, res.CommitmentID)
	m.AssertNotCalled(t, "PurchaseReservedNodesOffering", mock.Anything, mock.Anything)
}

func TestPurchaseCommitment_RefusesWhenPriceReadFails(t *testing.T) {
	t.Parallel()
	m := byIDMock(nil, errors.New("throttled"))
	res, err := buy(m, guardRec())
	require.ErrorContains(t, err, "approved upfront check")
	assert.ErrorContains(t, err, "throttled")
	assert.False(t, res.Success)
	m.AssertNotCalled(t, "PurchaseReservedNodesOffering", mock.Anything, mock.Anything)
}

func TestPurchaseCommitment_RefusesWhenPriceReadIsNotOneOffering(t *testing.T) {
	t.Parallel()
	two := []types.ReservedNodesOffering{guardOffering(400), guardOffering(400)}
	for _, n := range [][]types.ReservedNodesOffering{two, nil} {
		m := byIDMock(n, nil)
		_, err := buy(m, guardRec())
		require.ErrorContains(t, err, "expected 1 offering")
		m.AssertNotCalled(t, "PurchaseReservedNodesOffering", mock.Anything, mock.Anything)
	}
}

func TestPurchaseCommitment_RefusesNonFiniteApprovedUpfront(t *testing.T) {
	t.Parallel()
	for _, approved := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		m := guardMock(400)
		rec := guardRec()
		rec.CommitmentCost = approved
		res, err := buy(m, rec)
		require.ErrorContains(t, err, "not a finite amount", "%v", approved)
		assert.False(t, res.Success)
		m.AssertNotCalled(t, "PurchaseReservedNodesOffering", mock.Anything, mock.Anything)
	}
}

func buyIn(region string, m *MockMemoryDBClient, rec common.Recommendation) (common.PurchaseResult, error) {
	return (&Client{client: m, region: region}).PurchaseCommitment(context.Background(), rec, common.PurchaseOptions{})
}

func TestPurchaseCommitment_ChinaRegionRefusesApprovedUpfront(t *testing.T) {
	t.Parallel()
	for _, region := range []string{"cn-north-1", "cn-northwest-1"} {
		m := guardMock(400)
		res, err := buyIn(region, m, guardRec())
		require.ErrorContains(t, err, "not billed in USD", region)
		assert.False(t, res.Success)
		m.AssertNotCalled(t, "PurchaseReservedNodesOffering", mock.Anything, mock.Anything)
	}
}

func TestPurchaseCommitment_ChinaRegionZeroApprovedStillBuys(t *testing.T) {
	t.Parallel()
	m := guardMock(400)
	rec := guardRec()
	rec.CommitmentCost = 0
	_, err := buyIn("cn-north-1", m, rec)
	require.NoError(t, err)
	m.AssertCalled(t, "PurchaseReservedNodesOffering", mock.Anything, mock.Anything)
}

// A No Upfront offering is exempt from the zero-FixedPrice refusal and the
// China-region refusal even when the rec carries a positive approved upfront.
func TestPurchaseCommitment_NoUpfrontOfferingWithApprovedUpfrontBuys(t *testing.T) {
	t.Parallel()
	for _, region := range []string{"us-east-1", "cn-north-1"} {
		off := guardOffering(0)
		off.OfferingType = aws.String("No Upfront")
		m := byIDMock([]types.ReservedNodesOffering{off}, nil)
		res, err := buyIn(region, m, guardRec())
		require.NoError(t, err, region)
		assert.True(t, res.Success)
		m.AssertCalled(t, "PurchaseReservedNodesOffering", mock.Anything, mock.Anything)
	}
}
