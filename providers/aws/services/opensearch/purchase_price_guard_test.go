package opensearch

import (
	"context"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/opensearch"
	"github.com/aws/aws-sdk-go-v2/service/opensearch/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
)

func guardOffering(fixed *float64, currency string) types.ReservedInstanceOffering {
	o := types.ReservedInstanceOffering{
		ReservedInstanceOfferingId: aws.String("offering-1"),
		InstanceType:               types.OpenSearchPartitionInstanceTypeM5XlargeSearch,
		Duration:                   94608000,
		PaymentOption:              types.ReservedInstancePaymentOptionAllUpfront,
		FixedPrice:                 fixed,
	}
	if currency != "" {
		o.CurrencyCode = aws.String(currency)
	}
	return o
}

// byIDMock serves the page lookup, then answers the by-ID price read with the
// given offerings or error, and accepts the purchase.
func byIDMock(byID []types.ReservedInstanceOffering, byIDErr error) *MockOpenSearchClient {
	m := &MockOpenSearchClient{}
	isByID := func(in *opensearch.DescribeReservedInstanceOfferingsInput) bool {
		return aws.ToString(in.ReservedInstanceOfferingId) == "offering-1"
	}
	m.On("DescribeReservedInstanceOfferings", mock.Anything, mock.MatchedBy(isByID)).
		Return(&opensearch.DescribeReservedInstanceOfferingsOutput{ReservedInstanceOfferings: byID}, byIDErr)
	m.On("DescribeReservedInstanceOfferings", mock.Anything, mock.MatchedBy(func(in *opensearch.DescribeReservedInstanceOfferingsInput) bool { return !isByID(in) })).
		Return(&opensearch.DescribeReservedInstanceOfferingsOutput{ReservedInstanceOfferings: []types.ReservedInstanceOffering{guardOffering(nil, "")}}, nil)
	m.On("PurchaseReservedInstanceOffering", mock.Anything, mock.Anything).
		Return(&opensearch.PurchaseReservedInstanceOfferingOutput{ReservedInstanceId: aws.String("ri-1")}, nil)
	return m
}

func guardMock(fixed float64, currency string) *MockOpenSearchClient {
	return byIDMock([]types.ReservedInstanceOffering{guardOffering(aws.Float64(fixed), currency)}, nil)
}

// guardRec is a 2-instance all-upfront rec approved at a $1000 total upfront.
func guardRec() common.Recommendation {
	r := osIdemRec()
	r.Count = 2
	r.CommitmentCost = 1000
	return r
}

func buy(m *MockOpenSearchClient, rec common.Recommendation) (common.PurchaseResult, error) {
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
	m.AssertNotCalled(t, "PurchaseReservedInstanceOffering", mock.Anything, mock.Anything)
}

func TestPurchaseCommitment_BuysAtExactlyTolerance(t *testing.T) {
	t.Parallel()
	m := guardMock(525, "USD") // 2 x 525 = 1050 == ceiling
	res, err := buy(m, guardRec())
	require.NoError(t, err)
	assert.True(t, res.Success)
	m.AssertCalled(t, "PurchaseReservedInstanceOffering", mock.Anything, mock.Anything)
}

func TestPurchaseCommitment_ZeroApprovedUpfrontIsNotChecked(t *testing.T) {
	t.Parallel()
	m := guardMock(9000, "USD")
	rec := guardRec()
	rec.CommitmentCost = 0
	_, err := buy(m, rec)
	require.NoError(t, err)
	m.AssertNumberOfCalls(t, "DescribeReservedInstanceOfferings", 1)
}

func TestPurchaseCommitment_RefusesNonUSD(t *testing.T) {
	t.Parallel()
	m := guardMock(400, "EUR")
	_, err := buy(m, guardRec())
	require.ErrorContains(t, err, "EUR")
	m.AssertNotCalled(t, "PurchaseReservedInstanceOffering", mock.Anything, mock.Anything)

	m = guardMock(400, "USD")
	rec := guardRec()
	rec.CommitmentCurrency = "eur"
	_, err = buy(m, rec)
	require.ErrorContains(t, err, "eur")
	m.AssertNotCalled(t, "PurchaseReservedInstanceOffering", mock.Anything, mock.Anything)
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
	m := &MockOpenSearchClient{}
	noUpfront := guardOffering(nil, "")
	noUpfront.PaymentOption = types.ReservedInstancePaymentOptionNoUpfront
	m.On("DescribeReservedInstanceOfferings", mock.Anything, mock.Anything).
		Return(&opensearch.DescribeReservedInstanceOfferingsOutput{ReservedInstanceOfferings: []types.ReservedInstanceOffering{noUpfront}}, nil)
	m.On("PurchaseReservedInstanceOffering", mock.Anything, mock.Anything).
		Return(&opensearch.PurchaseReservedInstanceOfferingOutput{ReservedInstanceId: aws.String("ri-1")}, nil)
	rec := osIdemRec()
	rec.PaymentOption = "no-upfront"
	_, err := buy(m, rec)
	require.NoError(t, err)
	m.AssertNumberOfCalls(t, "DescribeReservedInstanceOfferings", 1)
}

// A re-drive whose RI already exists must return it even after the offering
// was repriced above the approved upfront: nothing is bought, so no refusal.
func TestPurchaseCommitment_ExistingRIWinsOverRepricedOffering(t *testing.T) {
	t.Parallel()
	m := guardMock(600, "USD") // 2 x 600 = 1200 against an approved 1000
	token := common.DeriveIdempotencyToken("exec-53", 0)
	derivedName := common.IdempotentReservationID("opensearch-id-", token)
	m.On("DescribeReservedInstances", mock.Anything, mock.Anything).
		Return(&opensearch.DescribeReservedInstancesOutput{ReservedInstances: []types.ReservedInstance{
			{ReservedInstanceId: aws.String("os-existing"), ReservationName: aws.String(derivedName), State: aws.String("active")},
		}}, nil)
	res, err := (&Client{client: m, region: "us-east-1"}).PurchaseCommitment(context.Background(), guardRec(), common.PurchaseOptions{IdempotencyToken: token})
	require.NoError(t, err)
	assert.True(t, res.Success)
	assert.True(t, res.ExistingCommitment)
	assert.Equal(t, "os-existing", res.CommitmentID)
	m.AssertNotCalled(t, "PurchaseReservedInstanceOffering", mock.Anything, mock.Anything)
}

func TestPurchaseCommitment_RefusesWhenPriceReadFails(t *testing.T) {
	t.Parallel()
	m := byIDMock(nil, errors.New("throttled"))
	res, err := buy(m, guardRec())
	require.ErrorContains(t, err, "approved upfront check")
	assert.ErrorContains(t, err, "throttled")
	assert.False(t, res.Success)
	m.AssertNotCalled(t, "PurchaseReservedInstanceOffering", mock.Anything, mock.Anything)
}

func TestPurchaseCommitment_RefusesWhenPriceReadIsNotOneOffering(t *testing.T) {
	t.Parallel()
	two := []types.ReservedInstanceOffering{guardOffering(aws.Float64(400), "USD"), guardOffering(aws.Float64(400), "USD")}
	for _, n := range [][]types.ReservedInstanceOffering{two, nil} {
		m := byIDMock(n, nil)
		_, err := buy(m, guardRec())
		require.ErrorContains(t, err, "expected 1 offering")
		m.AssertNotCalled(t, "PurchaseReservedInstanceOffering", mock.Anything, mock.Anything)
	}
}

func TestPurchaseCommitment_RefusesOfferingWithoutFixedPrice(t *testing.T) {
	t.Parallel()
	m := byIDMock([]types.ReservedInstanceOffering{guardOffering(nil, "")}, nil)
	_, err := buy(m, guardRec())
	require.ErrorContains(t, err, "no FixedPrice")
	m.AssertNotCalled(t, "PurchaseReservedInstanceOffering", mock.Anything, mock.Anything)
}
