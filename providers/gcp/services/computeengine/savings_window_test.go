package computeengine

import (
	"context"
	"testing"
	"time"

	"cloud.google.com/go/recommender/apiv1/recommenderpb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/genproto/googleapis/type/money"
	"google.golang.org/protobuf/types/known/durationpb"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
)

// Fixture-based: recommenderpb-shaped, not a captured Recommender response.
func costImpact(cost *money.Money, duration *durationpb.Duration) *recommenderpb.Recommendation {
	return &recommenderpb.Recommendation{
		PrimaryImpact: &recommenderpb.Impact{
			Category: recommenderpb.Impact_COST,
			Projection: &recommenderpb.Impact_CostProjection{CostProjection: &recommenderpb.CostProjection{
				Cost: cost, Duration: duration,
			}},
		},
	}
}

func TestExtractCostImpact_NormalizesToMonthly(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		duration time.Duration
		want     float64
	}{
		"a 730h month is already monthly": {730 * time.Hour, 120},
		"a 365-day projection":            {365 * 24 * time.Hour, 120 * 730.0 / 8760},
		"a 15-day projection":             {15 * 24 * time.Hour, 120 * 730.0 / 360},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var rec common.Recommendation
			err := extractCostImpactFromRecommendation(costImpact(&money.Money{Units: -120, CurrencyCode: "USD"}, durationpb.New(tc.duration)), &rec)
			require.NoError(t, err)
			assert.InDelta(t, tc.want, rec.EstimatedSavings, 1e-9)
		})
	}
}

func TestExtractCostImpact_UnknownWindowIsAnError(t *testing.T) {
	t.Parallel()
	cost := &money.Money{Units: -120, CurrencyCode: "USD"}
	for name, duration := range map[string]*durationpb.Duration{"missing": nil, "zero": durationpb.New(0), "negative": durationpb.New(-time.Hour)} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var rec common.Recommendation
			err := extractCostImpactFromRecommendation(costImpact(cost, duration), &rec)
			require.ErrorIs(t, err, errBadAmount)
			assert.Zero(t, rec.EstimatedSavings)
		})
	}
}

func TestExtractCostImpact_ZeroCostNeedsNoDuration(t *testing.T) {
	t.Parallel()
	var rec common.Recommendation
	require.NoError(t, extractCostImpactFromRecommendation(costImpact(&money.Money{CurrencyCode: "USD"}, nil), &rec))
}

// A recommendation without a duration is skipped, not reported as monthly.
func TestConvertGCPRecommendation_MissingDurationSkipsRecommendation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	client, _ := NewClient(ctx, "test-project", "us-central1")
	gcpRec := realisticCUDRecommendation()
	gcpRec.PrimaryImpact.GetCostProjection().Duration = nil

	converted, err := client.convertOrSkipBadAmount(ctx, gcpRec, common.RecommendationParams{})
	require.NoError(t, err)
	assert.Nil(t, converted)
}

func TestExtractCostImpact_RequiresUSD(t *testing.T) {
	t.Parallel()
	month := durationpb.New(savingsWindow)
	cases := map[string]struct {
		cost    *money.Money
		wantErr bool
		want    float64
	}{
		"USD non-zero":                {&money.Money{Units: -120, CurrencyCode: "USD"}, false, 120},
		"EUR non-zero":                {&money.Money{Units: -120, CurrencyCode: "EUR"}, true, 0},
		"empty currency non-zero":     {&money.Money{Units: -120}, true, 0},
		"EUR nanos only":              {&money.Money{Nanos: -5, CurrencyCode: "EUR"}, true, 0},
		"zero cost, empty currency":   {&money.Money{}, false, 0},
		"zero cost, non-USD currency": {&money.Money{CurrencyCode: "EUR"}, false, 0},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var rec common.Recommendation
			err := extractCostImpactFromRecommendation(costImpact(tc.cost, month), &rec)
			if tc.wantErr {
				require.ErrorIs(t, err, errBadAmount)
				assert.Contains(t, err.Error(), tc.cost.CurrencyCode)
				assert.Zero(t, rec.EstimatedSavings)
				return
			}
			require.NoError(t, err)
			assert.InDelta(t, tc.want, rec.EstimatedSavings, 1e-9)
		})
	}
}

// A non-USD recommendation is skipped, not reported as USD savings.
func TestConvertGCPRecommendation_NonUSDCostSkipsRecommendation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	client, _ := NewClient(ctx, "test-project", "us-central1")
	gcpRec := realisticCUDRecommendation()
	gcpRec.PrimaryImpact.GetCostProjection().Cost.CurrencyCode = "EUR"

	converted, err := client.convertOrSkipBadAmount(ctx, gcpRec, common.RecommendationParams{})
	require.NoError(t, err)
	assert.Nil(t, converted)
}
