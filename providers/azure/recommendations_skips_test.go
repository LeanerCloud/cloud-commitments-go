package azure

import (
	"context"
	"errors"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/consumption/armconsumption"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	azrecs "github.com/LeanerCloud/cloud-commitments-go/providers/azure/internal/recommendations"
	"github.com/LeanerCloud/cloud-commitments-go/providers/azure/mocks"
)

// consumptionFake runs the shared skip-aware extraction over fixed payloads,
// the way the real service converters do.
type consumptionFake struct {
	recs []armconsumption.ReservationRecommendationClassification
}

func (f *consumptionFake) GetRecommendations(ctx context.Context, _ *common.RecommendationParams) ([]common.Recommendation, error) {
	var out []common.Recommendation
	for _, r := range f.recs {
		if fields := azrecs.ExtractConsumptionOrSkip(ctx, "compute", r); fields != nil {
			out = append(out, common.Recommendation{Service: common.ServiceCompute, ResourceType: fields.ResourceType})
		}
	}
	return out, nil
}

// Fixture-based: modern payloads built from armconsumption types.
func TestGetRecommendations_NonUSDSkipIsSurfaced(t *testing.T) {
	eur := mocks.BuildModernReservationRecommendation(
		mocks.WithModernCosts(191, 116, 75), mocks.WithModernCurrency("EUR"))
	usd := mocks.BuildModernReservationRecommendation(
		mocks.WithModernCosts(191, 116, 75), mocks.WithModernSKUName("Standard_D2as_v4"))

	run := func(recs ...armconsumption.ReservationRecommendationClassification) ([]common.Recommendation, error) {
		overrideServiceClientFns(t, &consumptionFake{recs: recs},
			&fakeServiceClient{}, &fakeServiceClient{}, &fakeServiceClient{}, &fakeServiceClient{})
		a := &RecommendationsClientAdapter{cred: &mockAzureTokenCredential{}, subscriptionID: "sub", getAdvisorRecsFn: noopAdvisorFn}
		return a.GetRecommendations(context.Background(), &common.RecommendationParams{Service: common.ServiceCompute})
	}

	recs, err := run(eur)
	assert.Empty(t, recs)
	var partial *azrecs.PartialResultError
	require.True(t, errors.As(err, &partial), "an all-EUR account must not look like zero savings: %v", err)
	assert.Contains(t, partial.Skipped[0], "EUR")

	recs, err = run(eur, usd)
	assert.Len(t, recs, 1, "the priceable recommendation is still returned")
	require.True(t, errors.As(err, &partial))
	assert.Len(t, partial.Skipped, 1)

	var typed interface {
		Partial() bool
		SkippedCount() int
		SkipReasons() []string
	}
	require.True(t, errors.As(err, &typed))
	reasons := typed.SkipReasons()
	assert.Equal(t, 1, typed.SkippedCount())
	assert.Len(t, reasons, typed.SkippedCount())
	assert.Contains(t, reasons[0], "EUR")
	reasons[0] = "mutated"
	assert.Contains(t, typed.SkipReasons()[0], "EUR", "SkipReasons must return a copy")

	recs, err = run(usd)
	require.NoError(t, err)
	assert.Len(t, recs, 1)
}
