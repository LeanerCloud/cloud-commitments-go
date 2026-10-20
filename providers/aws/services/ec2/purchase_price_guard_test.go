package ec2

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
)

// pricedRec is a 2-instance partial-upfront rec approved at a $1000 total upfront.
func pricedRec() common.Recommendation {
	r := matchRec()
	r.Count = 2
	r.PaymentOption = "partial-upfront"
	r.CommitmentCost = 1000
	return r
}

func pricedOffering(fixed float32) types.ReservedInstancesOffering {
	return fullOffering("priced", "", func(o *types.ReservedInstancesOffering) {
		o.OfferingType = types.OfferingTypeValuesPartialUpfront
		o.FixedPrice = aws.Float32(fixed)
		o.CurrencyCode = types.CurrencyCodeValuesUsd
	})
}

func TestPurchaseCommitment_RefusesOfferingAboveApprovedUpfront(t *testing.T) {
	t.Parallel()
	// 2 x 600 = 1200 against an approved 1000 (ceiling 1050).
	m := offeringsMock(pricedOffering(600))
	res, err := (&Client{client: m, region: "us-east-1"}).PurchaseCommitment(context.Background(), pricedRec(), common.PurchaseOptions{})
	require.ErrorContains(t, err, "exceeds the approved upfront")
	assert.False(t, res.Success)
	m.AssertNotCalled(t, "PurchaseReservedInstancesOffering", mock.Anything, mock.Anything)
}

func TestPurchaseCommitment_SetsLimitPriceAtApprovedCeiling(t *testing.T) {
	t.Parallel()
	var got *ec2.PurchaseReservedInstancesOfferingInput
	m := offeringsMock(pricedOffering(500)) // 2 x 500 = 1000 == approved
	m.ExpectedCalls = m.ExpectedCalls[:1]   // drop the permissive Purchase stub
	m.On("PurchaseReservedInstancesOffering", mock.Anything, mock.MatchedBy(func(in *ec2.PurchaseReservedInstancesOfferingInput) bool {
		got = in
		return true
	})).Return(&ec2.PurchaseReservedInstancesOfferingOutput{ReservedInstancesId: aws.String("ri-x")}, nil)
	m.On("CreateTags", mock.Anything, mock.Anything).Return(&ec2.CreateTagsOutput{}, nil).Maybe()
	_, err := (&Client{client: m, region: "us-east-1"}).PurchaseCommitment(context.Background(), pricedRec(), common.PurchaseOptions{})
	require.NoError(t, err)
	require.NotNil(t, got.LimitPrice)
	assert.InDelta(t, 1050.0, aws.ToFloat64(got.LimitPrice.Amount), 0.001)
	assert.Equal(t, types.CurrencyCodeValuesUsd, got.LimitPrice.CurrencyCode)
}

func TestPurchaseCommitment_NoLimitPriceWithoutApprovedUpfront(t *testing.T) {
	t.Parallel()
	var got *ec2.PurchaseReservedInstancesOfferingInput
	m := &MockEC2Client{}
	m.On("DescribeReservedInstancesOfferings", mock.Anything, mock.Anything).
		Return(&ec2.DescribeReservedInstancesOfferingsOutput{ReservedInstancesOfferings: []types.ReservedInstancesOffering{fullOffering("n", "")}}, nil)
	m.On("PurchaseReservedInstancesOffering", mock.Anything, mock.MatchedBy(func(in *ec2.PurchaseReservedInstancesOfferingInput) bool {
		got = in
		return true
	})).Return(&ec2.PurchaseReservedInstancesOfferingOutput{ReservedInstancesId: aws.String("ri-x")}, nil)
	m.On("CreateTags", mock.Anything, mock.Anything).Return(&ec2.CreateTagsOutput{}, nil).Maybe()
	_, err := (&Client{client: m, region: "us-east-1"}).PurchaseCommitment(context.Background(), matchRec(), common.PurchaseOptions{})
	require.NoError(t, err)
	assert.Nil(t, got.LimitPrice)
}

func TestUpfrontLimit_RefusesMissingPriceAndNonUSD(t *testing.T) {
	t.Parallel()
	noPrice := fullOffering("p", "")
	_, err := upfrontLimit(pricedRec(), &noPrice)
	require.ErrorContains(t, err, "no FixedPrice")

	eur := pricedOffering(500)
	eur.CurrencyCode = types.CurrencyCodeValues("EUR")
	_, err = upfrontLimit(pricedRec(), &eur)
	require.ErrorContains(t, err, "USD only")
}
