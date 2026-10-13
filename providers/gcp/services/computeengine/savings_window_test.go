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
			var rec common.Recommendation
			err := extractCostImpactFromRecommendation(costImpact(&money.Money{Units: -120, CurrencyCode: "USD"}, durationpb.New(tc.duration)), &rec)
			require.NoError(t, err)
			assert.InDelta(t, tc.want, rec.EstimatedSavings, 1e-9)
		})
	}
}

func TestExtractCostImpact_UnknownWindowIsAnError(t *testing.T) {
	cost := &money.Money{Units: -120, CurrencyCode: "USD"}
	for name, duration := range map[string]*durationpb.Duration{"missing": nil, "zero": durationpb.New(0), "negative": durationpb.New(-time.Hour)} {
		t.Run(name, func(t *testing.T) {
			var rec common.Recommendation
			err := extractCostImpactFromRecommendation(costImpact(cost, duration), &rec)
			require.ErrorIs(t, err, errBadAmount)
			assert.Zero(t, rec.EstimatedSavings)
		})
	}
}

func TestExtractCostImpact_ZeroCostNeedsNoDuration(t *testing.T) {
	var rec common.Recommendation
	require.NoError(t, extractCostImpactFromRecommendation(costImpact(&money.Money{CurrencyCode: "USD"}, nil), &rec))
}

// A recommendation without a duration is skipped, not reported as monthly.
func TestConvertGCPRecommendation_MissingDurationSkipsRecommendation(t *testing.T) {
	ctx := context.Background()
	client, _ := NewClient(ctx, "test-project", "us-central1")
	gcpRec := realisticCUDRecommendation()
	gcpRec.PrimaryImpact.GetCostProjection().Duration = nil

	converted, err := client.convertOrSkipBadAmount(ctx, gcpRec, common.RecommendationParams{})
	require.NoError(t, err)
	assert.Nil(t, converted)
}
