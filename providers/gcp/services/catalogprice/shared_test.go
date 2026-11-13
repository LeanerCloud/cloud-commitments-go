package catalogprice

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/cloudbilling/v1"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/providers/gcp/internal/skulist"
)

type stubBilling struct{}

func (stubBilling) ListSKUs(string) (*cloudbilling.ListSkusResponse, error) { return nil, nil }

func priceInput(list func(skulist.BillingService) (*cloudbilling.ListSkusResponse, error), onDemand, commit float64) PriceInput {
	return PriceInput{
		Name:    "test",
		Billing: stubBilling{},
		List:    list,
		Extract: func([]*cloudbilling.Sku) (float64, float64, string, error) {
			return onDemand, commit, "USD", nil
		},
		Label:     "thing",
		Region:    "us-central1",
		TermYears: 3,
	}
}

func okList(skulist.BillingService) (*cloudbilling.ListSkusResponse, error) {
	return &cloudbilling.ListSkusResponse{}, nil
}

func TestPrice_ScalesToTerm(t *testing.T) {
	t.Parallel()
	got, err := Price(context.Background(), priceInput(okList, 0.10, 0.06))
	require.NoError(t, err)
	assert.Equal(t, "USD", got.Currency)
	assert.InDelta(t, 0.06, got.HourlyRate, 1e-9)
	assert.InDelta(t, 0.06*3*8760, got.CommitmentPrice, 1e-6)
	assert.InDelta(t, 0.10*3*8760, got.OnDemandPrice, 1e-6)
	assert.InDelta(t, 40, got.SavingsPercentage, 1e-6)
}

func TestPrice_WrapsListError(t *testing.T) {
	t.Parallel()
	boom := errors.New("boom")
	_, err := Price(context.Background(), priceInput(func(skulist.BillingService) (*cloudbilling.ListSkusResponse, error) {
		return nil, boom
	}, 0.10, 0.06))
	require.ErrorIs(t, err, boom)
	assert.Contains(t, err.Error(), "failed to list SKUs")
}

func TestPrice_RejectsMissingCommitmentPrice(t *testing.T) {
	t.Parallel()
	_, err := Price(context.Background(), priceInput(okList, 0.10, 0))
	require.Error(t, err)
}

func TestOfferingDetails_BuildsIDAndWrapsPricingError(t *testing.T) {
	t.Parallel()
	rec := common.Recommendation{ResourceType: "r1", Term: "3yr", PaymentOption: "all-upfront"}
	var gotYears int
	d, err := OfferingDetails(context.Background(), rec, "gcp-x", "us-east1", func(_ context.Context, res, region string, years int) (*Term, error) {
		gotYears = years
		return &Term{Currency: "USD", HourlyRate: 0.05, CommitmentPrice: 1314}, nil
	})
	require.NoError(t, err)
	assert.Equal(t, "gcp-x-r1-us-east1-3yr", d.OfferingID)
	assert.Equal(t, 3, gotYears)
	assert.InDelta(t, 1314, d.TotalCost, 1e-9)

	_, err = OfferingDetails(context.Background(), rec, "gcp-x", "us-east1", func(context.Context, string, string, int) (*Term, error) {
		return nil, errors.New("nope")
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to get pricing: nope")
}
