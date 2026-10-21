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

func TestPurchaseCommitment_BuysAtApprovedUpfront(t *testing.T) {
	t.Parallel()
	m := offeringsMock(pricedOffering(500)) // 2 x 500 == approved 1000
	res, err := (&Client{client: m, region: "us-east-1"}).PurchaseCommitment(context.Background(), pricedRec(), common.PurchaseOptions{})
	require.NoError(t, err)
	assert.True(t, res.Success)
	m.AssertCalled(t, "PurchaseReservedInstancesOffering", mock.Anything, mock.MatchedBy(func(in *ec2.PurchaseReservedInstancesOfferingInput) bool {
		return in.LimitPrice == nil
	}), mock.Anything)
}

// The MCP EC2 tool never sets CommitmentCost; zero must not block the buy.
func TestPurchaseCommitment_NoApprovedUpfrontIsNotChecked(t *testing.T) {
	t.Parallel()
	m := offeringsMock(pricedOffering(9000))
	rec := pricedRec()
	rec.CommitmentCost = 0
	_, err := (&Client{client: m, region: "us-east-1"}).PurchaseCommitment(context.Background(), rec, common.PurchaseOptions{})
	require.NoError(t, err)
}

func TestCheckApprovedUpfront_Currency(t *testing.T) {
	t.Parallel()
	off := pricedOffering(500)
	for _, cur := range []string{"", "USD", "usd"} {
		rec := pricedRec()
		rec.CommitmentCurrency = cur
		require.NoError(t, checkApprovedUpfront(rec, &off), "currency %q", cur)
	}
	rec := pricedRec()
	rec.CommitmentCurrency = "EUR"
	require.ErrorContains(t, checkApprovedUpfront(rec, &off), "EUR")
}

func TestCheckApprovedUpfront_RefusesMissingPriceAndNonUSDOffering(t *testing.T) {
	t.Parallel()
	noPrice := fullOffering("p", "")
	require.ErrorContains(t, checkApprovedUpfront(pricedRec(), &noPrice), "no FixedPrice")

	eur := pricedOffering(500)
	eur.CurrencyCode = types.CurrencyCodeValues("EUR")
	require.ErrorContains(t, checkApprovedUpfront(pricedRec(), &eur), "USD only")
}
