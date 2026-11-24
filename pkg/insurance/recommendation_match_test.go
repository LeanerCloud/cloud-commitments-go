package insurance

import (
	"encoding/json"
	"math/big"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func matchFixture() (*RecommendationIdentity, []ComparisonIdentity, []LineItemIdentity) {
	r := &RecommendationIdentity{
		Provider: "aws", SourceAccountID: "source-account-a", Service: "savings-plans-ec2instance",
		PlanType: "EC2Instance", OfferingID: "provider-offer-a", Region: "us-east-1",
		InstanceFamily: "m5", Payment: "no_upfront", NativeDurationSeconds: 31536000,
		HourlyCommitment: big.NewRat(1, 2),
	}
	region := r.Region
	row := ComparisonIdentity{Region: &region, IsCurrent: true,
		LineItemID: matchItem, CurrentOfferID: matchOffer, Provider: "aws",
		CurrentType: "savings_plan", CurrentPayment: "no_upfront", CurrentSelectedAmount: big.NewRat(1, 2),
	}
	item := LineItemIdentity{
		ID: matchItem, PlanID: matchPlan, OfferID: matchOffer, Provider: "aws",
		SourceAccountID: r.SourceAccountID, ProviderOfferingID: r.OfferingID, Type: "savings_plan",
		PlanType: r.PlanType, Region: r.Region, InstanceFamily: r.InstanceFamily, Payment: r.Payment,
		DurationSeconds: big.NewRat(31536000, 1), SelectedCommitment: big.NewRat(1, 2),
	}
	return r, []ComparisonIdentity{row}, []LineItemIdentity{item}
}

func TestMatchRecommendationAssociatesOnlyProvenResourceIdentity(t *testing.T) {
	rec, rows, items := matchFixture()
	items[0].ID = strings.ToUpper(matchItem)
	items[0].PlanID = strings.ToUpper(matchPlan)
	items[0].OfferID = strings.ToUpper(matchOffer)
	before, marshalErr := json.Marshal([]any{rec, rows, items})
	require.NoError(t, marshalErr)
	result, err := MatchRecommendation(matchPlan, rec, rows, items)
	require.NoError(t, err)
	require.Equal(t, ResourceAssociated, result.Status)
	require.Equal(t, matchItem, result.LineItemID)
	require.Equal(t, ReasonStoredDecimalOnly, result.AmountProvenance)
	require.Equal(t, ReasonUnavailableSourcePrecision, result.EconomicStatus)
	require.Equal(t, "1/2", rec.HourlyCommitment.RatString())
	require.Equal(t, "1/2", items[0].SelectedCommitment.RatString())
	after, marshalErr := json.Marshal([]any{rec, rows, items})
	require.NoError(t, marshalErr)
	require.Equal(t, before, after)
	rec.NativeDurationSeconds = 94608000
	items[0].DurationSeconds = big.NewRat(94608000, 1)
	result, err = MatchRecommendation(matchPlan, rec, rows, items)
	require.NoError(t, err)
	require.Equal(t, ResourceAssociated, result.Status)
}

func TestMatchRecommendationRefusesUnprovenAssociation(t *testing.T) {
	tests := []struct {
		change func(*RecommendationIdentity, *ComparisonIdentity, *LineItemIdentity)
		name   string
	}{
		{name: "unusable_source", change: func(r *RecommendationIdentity, _ *ComparisonIdentity, i *LineItemIdentity) {
			r.SourceAccountID = "source\naccount"
			i.SourceAccountID = r.SourceAccountID
		}},
		{name: "unknown_source", change: func(r *RecommendationIdentity, _ *ComparisonIdentity, i *LineItemIdentity) {
			r.SourceAccountID = ""
			i.SourceAccountID = ""
		}},
		{name: "collector_is_not_source", change: func(_ *RecommendationIdentity, _ *ComparisonIdentity, i *LineItemIdentity) {
			i.SourceAccountID = "collector-account"
		}},
		{name: "source_case", change: func(_ *RecommendationIdentity, _ *ComparisonIdentity, i *LineItemIdentity) {
			i.SourceAccountID = "SOURCE-account-a"
		}},
		{name: "provider", change: func(_ *RecommendationIdentity, _ *ComparisonIdentity, i *LineItemIdentity) { i.Provider = "gcp" }},
		{name: "offering", change: func(_ *RecommendationIdentity, _ *ComparisonIdentity, i *LineItemIdentity) {
			i.ProviderOfferingID = "other-offer"
		}},
		{name: "missing_offering", change: func(r *RecommendationIdentity, _ *ComparisonIdentity, i *LineItemIdentity) {
			r.OfferingID = ""
			i.ProviderOfferingID = ""
		}},
		{name: "unsupported_service", change: func(r *RecommendationIdentity, _ *ComparisonIdentity, _ *LineItemIdentity) {
			r.Service = "savings-plans-compute"
		}},
		{name: "invented_type", change: func(_ *RecommendationIdentity, _ *ComparisonIdentity, i *LineItemIdentity) {
			i.Type = "aws/savingsplan/EC2Instance"
		}},
		{name: "plan_type", change: func(_ *RecommendationIdentity, _ *ComparisonIdentity, i *LineItemIdentity) { i.PlanType = "Compute" }},
		{name: "family", change: func(_ *RecommendationIdentity, _ *ComparisonIdentity, i *LineItemIdentity) { i.InstanceFamily = "m6" }},
		{name: "missing_details_region", change: func(r *RecommendationIdentity, _ *ComparisonIdentity, i *LineItemIdentity) {
			r.Region = ""
			i.Region = ""
		}},
		{name: "contradictory_top_region", change: func(r *RecommendationIdentity, _ *ComparisonIdentity, _ *LineItemIdentity) {
			r.TopLevelRegion = "us-west-2"
		}},
		{name: "native_duration", change: func(_ *RecommendationIdentity, _ *ComparisonIdentity, i *LineItemIdentity) {
			i.DurationSeconds = big.NewRat(94608000, 1)
		}},
		{name: "payment", change: func(_ *RecommendationIdentity, _ *ComparisonIdentity, i *LineItemIdentity) { i.Payment = "all_upfront" }},
		{name: "amount_mismatch", change: func(_ *RecommendationIdentity, _ *ComparisonIdentity, i *LineItemIdentity) {
			i.SelectedCommitment = big.NewRat(1, 1)
		}},
		{name: "missing_hourly", change: func(r *RecommendationIdentity, _ *ComparisonIdentity, _ *LineItemIdentity) { r.HourlyCommitment = nil }},
		{name: "zero_hourly", change: func(r *RecommendationIdentity, _ *ComparisonIdentity, _ *LineItemIdentity) {
			r.HourlyCommitment = new(big.Rat)
		}},
		{name: "current_amount", change: func(_ *RecommendationIdentity, c *ComparisonIdentity, _ *LineItemIdentity) {
			c.CurrentSelectedAmount = big.NewRat(2, 1)
		}},
		{name: "offering_case", change: func(_ *RecommendationIdentity, _ *ComparisonIdentity, i *LineItemIdentity) {
			i.ProviderOfferingID = "PROVIDER-offer-a"
		}},
		{name: "missing_family", change: func(r *RecommendationIdentity, _ *ComparisonIdentity, i *LineItemIdentity) {
			r.InstanceFamily = ""
			i.InstanceFamily = ""
		}},
		{name: "unsupported_native_term", change: func(r *RecommendationIdentity, _ *ComparisonIdentity, i *LineItemIdentity) {
			r.NativeDurationSeconds = 123
			i.DurationSeconds = big.NewRat(123, 1)
		}},
		{name: "current_provider", change: func(_ *RecommendationIdentity, c *ComparisonIdentity, _ *LineItemIdentity) { c.Provider = "gcp" }},
		{name: "current_unit_type", change: func(_ *RecommendationIdentity, c *ComparisonIdentity, _ *LineItemIdentity) { c.CurrentType = "ri" }},
		{name: "current_flag", change: func(_ *RecommendationIdentity, c *ComparisonIdentity, _ *LineItemIdentity) { c.IsCurrent = false }},
		{name: "current_region", change: func(_ *RecommendationIdentity, c *ComparisonIdentity, i *LineItemIdentity) {
			c.Region = &i.InstanceFamily
		}},
		{name: "missing_current_region", change: func(_ *RecommendationIdentity, c *ComparisonIdentity, _ *LineItemIdentity) { c.Region = nil }},
		{name: "line_item_region", change: func(_ *RecommendationIdentity, _ *ComparisonIdentity, i *LineItemIdentity) { i.Region = "eu-west-1" }},
		{name: "current_payment", change: func(_ *RecommendationIdentity, c *ComparisonIdentity, _ *LineItemIdentity) {
			c.CurrentPayment = "all_upfront"
		}},
		{name: "other_plan", change: func(_ *RecommendationIdentity, _ *ComparisonIdentity, i *LineItemIdentity) {
			i.PlanID = "faadf00d-ffff-4aba-8ada-abcdefabcdef"
		}},
		{name: "non_aws_recommendation", change: func(r *RecommendationIdentity, c *ComparisonIdentity, i *LineItemIdentity) {
			r.Provider, c.Provider, i.Provider = "gcp", "gcp", "gcp"
		}},
		{name: "invalid_payment", change: func(r *RecommendationIdentity, c *ComparisonIdentity, i *LineItemIdentity) {
			r.Payment, c.CurrentPayment, i.Payment = "monthly", "monthly", "monthly"
		}},
		{name: "current_offer", change: func(_ *RecommendationIdentity, c *ComparisonIdentity, _ *LineItemIdentity) {
			c.CurrentOfferID = "faadf00d-ffff-4aba-8ada-abcdefabcdef"
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, rows, items := matchFixture()
			tt.change(r, &rows[0], &items[0])
			got, err := MatchRecommendation(matchPlan, r, rows, items)
			require.NoError(t, err)
			require.Equal(t, MatchUnavailable, got.Status)
			require.Empty(t, got.LineItemID)
		})
	}
}
