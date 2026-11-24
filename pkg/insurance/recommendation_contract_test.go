package insurance

import (
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const (
	matchPlan  = "baadf00d-ffff-4aba-8ada-abcdefabcdef"
	matchItem  = "caadf00d-ffff-4aba-8ada-abcdefabcdef"
	matchOffer = "daadf00d-ffff-4aba-8ada-abcdefabcdef"
)

func contractMatchFixture(t *testing.T) (*Comparison, []LineItem) {
	t.Helper()
	item := `{"id":"` + matchItem + `","plan_id":"` + matchPlan + `","offer_id":"` + matchOffer + `","account_id":"Source-account-a","selected_quantity":7,"selected_commitment":0.50000000000000000001,"is_selected":true,"contract_term":"one_year_gris","payment_option":"No Upfront","contract_spec":{"commitment_type":"opaque-contract-type"},"offer":{"id":"` + matchOffer + `","type":"savings_plan","provider":"aws","provider_offering_id":"provider-offer-a","offering_id":"distinct-catalog-offer","duration_seconds":31536000,"region":"us-east-1","az":null,"instance_type":null,"instance_family":"m5","product_description":null,"tenancy":null,"plan_type":"EC2Instance","is_multi_az":null,"is_flexible":null,"payment_option":"No Upfront"}}`
	items, err := DecodeLineItems(strings.NewReader("["+item+"]"), matchPlan)
	require.NoError(t, err)
	current := `{"is_current":true,"offer_id":"` + matchOffer + `","selected_amount":0.50000000000000000001,"commitment_type":"savings_plan","contract_term":"one_year_gris","payment_option":"no_upfront","offer":{"provider":"aws","region":"us-east-1"},"discount_rate":0,"commitment_upfront_cost":0,"commitment_financials_monthly_rate":{},"delta_vs_current":{"monthly_net_savings":0,"upfront_cost":0,"discount_rate":0}}`
	candidate := strings.ReplaceAll(strings.Replace(current, `"is_current":true`, `"is_current":false`, 1), matchOffer, "faadf00d-ffff-4aba-8ada-abcdefabcdef")
	candidate = strings.Replace(candidate, "no_upfront", "all_upfront", 1)
	body := `{"current_totals":{"commitment_financials_monthly_rate":{},"commitment_upfront_cost":0},"hypothetical_totals":[],"data":[{"line_item_id":"` + matchItem + `","current":` + current + `,"candidates":[` + candidate + "," + candidate + `]}]}`
	comparison, err := DecodeComparison(strings.NewReader(body), "fixture-org", matchPlan, time.Unix(1, 0))
	require.NoError(t, err)
	return comparison, items
}

// contractRecommendation is the identity the stored recommendation projects to for contractMatchFixture.
func contractRecommendation() *RecommendationIdentity {
	hourly, _ := new(big.Rat).SetString("0.50000000000000000001")
	return &RecommendationIdentity{Provider: "aws", SourceAccountID: "Source-account-a", CloudAccountID: "cloud-account-a",
		Service: "savings-plans-ec2instance", PlanType: "EC2Instance", OfferingID: "provider-offer-a",
		Region: "us-east-1", TopLevelRegion: "us-east-1", InstanceFamily: "m5", Payment: "no_upfront",
		NativeDurationSeconds: 31536000, HourlyCommitment: hourly}
}

func TestProjectContractMapsEveryIdentityField(t *testing.T) {
	comparison, items := contractMatchFixture(t)
	rows, identities, err := ProjectContract(comparison, items)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Len(t, identities, 1)
	require.Equal(t, "provider-offer-a", identities[0].ProviderOfferingID)
	require.Equal(t, "opaque-contract-type", items[0].ContractSpec.CommitmentType)
	require.Equal(t, "one_year_gris", items[0].ContractTerm)
	wantItem := LineItemIdentity{
		ID: matchItem, PlanID: matchPlan, OfferID: matchOffer, Provider: "aws",
		SourceAccountID: "Source-account-a", ProviderOfferingID: "provider-offer-a", Type: "savings_plan",
		PlanType: "EC2Instance", Region: "us-east-1", InstanceFamily: "m5", Payment: "no_upfront",
		DurationSeconds: big.NewRat(31536000, 1), SelectedCommitment: items[0].SelectedCommitment,
	}
	require.Equal(t, wantItem, identities[0])
	region := "us-east-1"
	candidates := rows[0].Candidates
	require.Equal(t, ComparisonIdentity{Region: &region, IsCurrent: true, LineItemID: matchItem, CurrentOfferID: matchOffer,
		Provider: "aws", CurrentType: "savings_plan", CurrentPayment: "no_upfront",
		CurrentSelectedAmount: comparison.Rows[0].Current.SelectedAmount, Candidates: candidates}, rows[0])
	require.Len(t, rows[0].Candidates, 2)
	require.Equal(t, "all_upfront", rows[0].Candidates[0].Payment)
	require.Equal(t, rows[0].Candidates[0].OfferID, rows[0].Candidates[1].OfferID)
	require.Zero(t, identities[0].SelectedCommitment.Cmp(comparison.Rows[0].Current.SelectedAmount))
}

func TestProjectContractJoinsActualDecodedIdentity(t *testing.T) {
	comparison, items := contractMatchFixture(t)
	rows, identities, err := ProjectContract(comparison, items)
	require.NoError(t, err)
	got, err := MatchRecommendation(matchPlan, contractRecommendation(), rows, identities)
	require.NoError(t, err)
	require.Equal(t, ResourceAssociated, got.Status)
	require.Equal(t, matchItem, got.LineItemID)
	require.Len(t, got.Candidates, 2)
	require.Equal(t, CandidateVendorAlternative, got.Candidates[0].Status)
	require.Equal(t, ReasonStoredDecimalOnly, got.AmountProvenance)
	require.Equal(t, ReasonUnavailableSourcePrecision, got.EconomicStatus)
}

func TestProjectContractRejectsMissingComparison(t *testing.T) {
	_, items := contractMatchFixture(t)
	_, _, err := ProjectContract(nil, items)
	require.Error(t, err)
}

func TestProjectContractPreservesMissingOrContradictoryEvidence(t *testing.T) {
	tests := []struct {
		change func(*Comparison, *LineItem)
		check  func(*testing.T, ComparisonIdentity, LineItemIdentity)
		name   string
	}{
		{name: "account", change: func(_ *Comparison, i *LineItem) { i.AccountID = nil },
			check: func(t *testing.T, _ ComparisonIdentity, i LineItemIdentity) { require.Empty(t, i.SourceAccountID) }},
		{name: "catalog_is_not_provider_offering", change: func(_ *Comparison, i *LineItem) { i.Offer.ProviderOfferingID = "" },
			check: func(t *testing.T, _ ComparisonIdentity, i LineItemIdentity) { require.Empty(t, i.ProviderOfferingID) }},
		{name: "duration", change: func(_ *Comparison, i *LineItem) { i.Offer.DurationSeconds = nil },
			check: func(t *testing.T, _ ComparisonIdentity, i LineItemIdentity) { require.Nil(t, i.DurationSeconds) }},
		{name: "region", change: func(_ *Comparison, i *LineItem) { i.Offer.Region = nil },
			check: func(t *testing.T, _ ComparisonIdentity, i LineItemIdentity) { require.Empty(t, i.Region) }},
		{name: "family", change: func(_ *Comparison, i *LineItem) { i.Offer.InstanceFamily = nil },
			check: func(t *testing.T, _ ComparisonIdentity, i LineItemIdentity) { require.Empty(t, i.InstanceFamily) }},
		{name: "plan_type", change: func(_ *Comparison, i *LineItem) { i.Offer.PlanType = nil },
			check: func(t *testing.T, _ ComparisonIdentity, i LineItemIdentity) { require.Empty(t, i.PlanType) }},
		{name: "unselected", change: func(_ *Comparison, i *LineItem) { i.IsSelected = false },
			check: func(t *testing.T, _ ComparisonIdentity, i LineItemIdentity) { require.Empty(t, i.Type) }},
		{name: "payment_contradicts_offer", change: func(_ *Comparison, i *LineItem) {
			p := PaymentAllUpfront
			i.Offer.PaymentOption = &p
		}, check: func(t *testing.T, _ ComparisonIdentity, i LineItemIdentity) { require.Empty(t, i.Type) }},
		{name: "current_flag", change: func(c *Comparison, _ *LineItem) { c.Rows[0].Current.IsCurrent = false },
			check: func(t *testing.T, r ComparisonIdentity, _ LineItemIdentity) { require.False(t, r.IsCurrent) }},
		{name: "current_region", change: func(c *Comparison, _ *LineItem) { c.Rows[0].Current.Region = nil },
			check: func(t *testing.T, r ComparisonIdentity, _ LineItemIdentity) { require.Nil(t, r.Region) }},
		{name: "current_type", change: func(c *Comparison, _ *LineItem) { c.Rows[0].Current.CommitmentType = "ri" },
			check: func(t *testing.T, r ComparisonIdentity, _ LineItemIdentity) { require.Equal(t, "ri", r.CurrentType) }},
		{name: "amount", change: func(c *Comparison, _ *LineItem) { c.Rows[0].Current.SelectedAmount = big.NewRat(1, 2) },
			check: func(t *testing.T, r ComparisonIdentity, _ LineItemIdentity) {
				require.Equal(t, "1/2", r.CurrentSelectedAmount.RatString())
			}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, items := contractMatchFixture(t)
			tt.change(c, &items[0])
			rows, identities, err := ProjectContract(c, items)
			require.NoError(t, err)
			tt.check(t, rows[0], identities[0])
			got, err := MatchRecommendation(matchPlan, contractRecommendation(), rows, identities)
			require.NoError(t, err)
			require.Equal(t, MatchUnavailable, got.Status)
			require.Empty(t, got.LineItemID)
		})
	}
}

func TestVendorAlternativesMapsEveryField(t *testing.T) {
	payment := PaymentPartialUpfront
	entries := []OfferEntry{
		{IsCurrent: true, OfferID: "current"},
		{OfferID: "offer-a", Provider: "aws", CommitmentType: "savings_plan", PaymentOption: &payment, SelectedAmount: big.NewRat(3, 4)},
		{OfferID: "offer-b", Provider: "azure", CommitmentType: "ri"},
	}
	got := vendorAlternatives(entries)
	require.Equal(t, []CandidateIdentity{
		{OfferID: "offer-a", Provider: "aws", Type: "savings_plan", Payment: "partial_upfront", SelectedAmount: big.NewRat(3, 4)},
		{OfferID: "offer-b", Provider: "azure", Type: "ri"},
	}, got)
	require.Empty(t, vendorAlternatives(nil))
}

func TestProjectContractCarriesPaymentOptions(t *testing.T) {
	comparison, items := contractMatchFixture(t)
	upfront, partial := PaymentAllUpfront, PaymentPartialUpfront
	items[0].PaymentOption, items[0].Offer.PaymentOption = upfront, &upfront
	comparison.Rows[0].Current.PaymentOption = &partial
	rows, identities, err := ProjectContract(comparison, items)
	require.NoError(t, err)
	require.Equal(t, "all_upfront", identities[0].Payment)
	require.Equal(t, "partial_upfront", rows[0].CurrentPayment)
}

func TestProjectContractExcludesTheCurrentOfferFromAlternatives(t *testing.T) {
	comparison, items := contractMatchFixture(t)
	row := &comparison.Rows[0]
	current := row.Current
	current.IsCurrent = true
	row.Candidates = []OfferEntry{row.Candidates[0], current, row.Candidates[1]}
	rows, _, err := ProjectContract(comparison, items)
	require.NoError(t, err)
	require.Len(t, rows[0].Candidates, 2)
	for _, candidate := range rows[0].Candidates {
		require.NotEqual(t, current.OfferID, candidate.OfferID)
	}
	_, identities, err := ProjectContract(comparison, items)
	require.NoError(t, err)
	got, err := MatchRecommendation(matchPlan, contractRecommendation(), rows, identities)
	require.NoError(t, err)
	require.Len(t, got.Candidates, 2)
}

func TestProjectContractUnselectedTwinDoesNotCreateAmbiguity(t *testing.T) {
	comparison, items := contractMatchFixture(t)
	twin := items[0]
	twin.ID = "eaadf00d-ffff-4aba-8ada-abcdefabcdef"
	twin.IsSelected = false
	items = append(items, twin)
	rec := contractRecommendation()
	_, identities, err := ProjectContract(comparison, items)
	require.NoError(t, err)
	require.Empty(t, identities[1].Type)
	id, status := SelectLineItem(rec, identities)
	require.Equal(t, SelectOne, status)
	require.Equal(t, matchItem, id)
	_, status = SelectLineItem(rec, identities[1:])
	require.Equal(t, SelectNone, status)
}

func TestProjectContractCarriesProviders(t *testing.T) {
	comparison, items := contractMatchFixture(t)
	items[0].Offer.Provider = "gcp"
	comparison.Rows[0].Current.Provider = "azure"
	rows, identities, err := ProjectContract(comparison, items)
	require.NoError(t, err)
	require.Equal(t, "gcp", identities[0].Provider)
	require.Equal(t, "azure", rows[0].Provider)
}
