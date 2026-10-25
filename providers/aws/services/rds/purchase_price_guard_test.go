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
)

// guardMock serves the exact-match page lookup, then the by-ID price read with
// the given FixedPrice and currency, and accepts the purchase.
func guardMock(fixed float64, currency string) *MockRDSClient {
	m := &MockRDSClient{}
	byID := func(in *rds.DescribeReservedDBInstancesOfferingsInput) bool {
		return aws.ToString(in.ReservedDBInstancesOfferingId) == "offering-1"
	}
	priced := exactOffering("offering-1")
	priced.FixedPrice = aws.Float64(fixed)
	if currency != "" {
		priced.CurrencyCode = aws.String(currency)
	}
	m.On("DescribeReservedDBInstancesOfferings", mock.Anything, mock.MatchedBy(byID)).
		Return(&rds.DescribeReservedDBInstancesOfferingsOutput{ReservedDBInstancesOfferings: []types.ReservedDBInstancesOffering{priced}}, nil)
	m.On("DescribeReservedDBInstancesOfferings", mock.Anything, mock.MatchedBy(func(in *rds.DescribeReservedDBInstancesOfferingsInput) bool { return !byID(in) })).
		Return(&rds.DescribeReservedDBInstancesOfferingsOutput{ReservedDBInstancesOfferings: []types.ReservedDBInstancesOffering{exactOffering("offering-1")}}, nil)
	m.On("PurchaseReservedDBInstancesOffering", mock.Anything, mock.Anything).
		Return(&rds.PurchaseReservedDBInstancesOfferingOutput{ReservedDBInstance: &types.ReservedDBInstance{ReservedDBInstanceId: aws.String("ri-1")}}, nil)
	return m
}

// guardRec is a 2-instance all-upfront rec approved at a $1000 total upfront.
func guardRec() common.Recommendation {
	r := idempotencyTestRec()
	r.Count = 2
	r.CommitmentCost = 1000
	return r
}

func buy(m *MockRDSClient, rec common.Recommendation) (common.PurchaseResult, error) {
	return (&Client{client: m, region: "us-east-1"}).PurchaseCommitment(context.Background(), rec, common.PurchaseOptions{})
}

func TestPurchaseCommitment_RefusesOfferingAboveApprovedUpfront(t *testing.T) {
	t.Parallel()
	m := guardMock(525.01, "USD") // 2 x 525.01 = 1050.02 > ceiling 1050.00
	res, err := buy(m, guardRec())
	require.ErrorContains(t, err, "exceeds the approved upfront")
	assert.ErrorContains(t, err, "1050.02")
	assert.ErrorContains(t, err, "1000.00")
	assert.False(t, res.Success)
	m.AssertNotCalled(t, "PurchaseReservedDBInstancesOffering", mock.Anything, mock.Anything)
}

func TestPurchaseCommitment_BuysAtExactlyTolerance(t *testing.T) {
	t.Parallel()
	m := guardMock(525, "USD") // 2 x 525 = 1050 == ceiling
	res, err := buy(m, guardRec())
	require.NoError(t, err)
	assert.True(t, res.Success)
	m.AssertCalled(t, "PurchaseReservedDBInstancesOffering", mock.Anything, mock.Anything)
}

// The MCP never sets CommitmentCost; zero must not block the buy.
func TestPurchaseCommitment_ZeroApprovedUpfrontIsNotChecked(t *testing.T) {
	t.Parallel()
	m := guardMock(9000, "USD")
	rec := guardRec()
	rec.CommitmentCost = 0
	_, err := buy(m, rec)
	require.NoError(t, err)
}

func TestPurchaseCommitment_RefusesNonUSD(t *testing.T) {
	t.Parallel()
	m := guardMock(400, "EUR")
	_, err := buy(m, guardRec())
	require.ErrorContains(t, err, "EUR")
	m.AssertNotCalled(t, "PurchaseReservedDBInstancesOffering", mock.Anything, mock.Anything)

	m = guardMock(400, "USD")
	rec := guardRec()
	rec.CommitmentCurrency = "eur"
	_, err = buy(m, rec)
	require.ErrorContains(t, err, "eur")
	m.AssertNotCalled(t, "PurchaseReservedDBInstancesOffering", mock.Anything, mock.Anything)
}

func TestPurchaseCommitment_EmptyCurrencyIsUSD(t *testing.T) {
	t.Parallel()
	_, err := buy(guardMock(500, ""), guardRec())
	require.NoError(t, err)
}

// No-upfront recs carry CommitmentCost 0, so the price is never read back.
func TestPurchaseCommitment_NoUpfrontUnaffected(t *testing.T) {
	t.Parallel()
	m := &MockRDSClient{}
	noUpfront := exactOffering("offering-1")
	noUpfront.OfferingType = aws.String("No Upfront")
	m.On("DescribeReservedDBInstancesOfferings", mock.Anything, mock.Anything).
		Return(&rds.DescribeReservedDBInstancesOfferingsOutput{ReservedDBInstancesOfferings: []types.ReservedDBInstancesOffering{noUpfront}}, nil)
	m.On("PurchaseReservedDBInstancesOffering", mock.Anything, mock.Anything).
		Return(&rds.PurchaseReservedDBInstancesOfferingOutput{ReservedDBInstance: &types.ReservedDBInstance{ReservedDBInstanceId: aws.String("ri-1")}}, nil)
	rec := idempotencyTestRec()
	rec.PaymentOption = "no-upfront"
	_, err := buy(m, rec)
	require.NoError(t, err)
	m.AssertNumberOfCalls(t, "DescribeReservedDBInstancesOfferings", 1)
}
