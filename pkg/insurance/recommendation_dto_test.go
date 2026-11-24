package insurance

import (
	"encoding/json"
	"math/big"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func buildFixture(t *testing.T, c *Comparison, items []LineItem) (*RecommendationComparisonDTO, error) {
	t.Helper()
	rec := contractRecommendation()
	return BuildRecommendationComparison("requested-row", matchPlan, rec, c, items)
}

func mustMarshal(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return b
}

func TestRecommendationDTOMapsEveryFieldFromDistinctValues(t *testing.T) {
	c, items := contractMatchFixture(t)
	cur := &c.Rows[0].Current
	cur.SelectedAmount = rat(t, "0.50000000000000000001")
	cur.Monthly = Financials{CommitmentCostTotal: big.NewRat(101, 1), CloudProviderCost: big.NewRat(102, 1),
		Premium: big.NewRat(103, 1), GrossSavings: big.NewRat(104, 1), NetSavings: big.NewRat(105, 1), CoveredOnDemandCost: big.NewRat(106, 1)}
	cur.UpfrontCost = big.NewRat(107, 1)
	cand := &c.Rows[0].Candidates[0]
	cand.Monthly = Financials{CommitmentCostTotal: big.NewRat(201, 1), CloudProviderCost: big.NewRat(202, 1),
		Premium: big.NewRat(203, 1), GrossSavings: big.NewRat(204, 1), NetSavings: big.NewRat(205, 1), CoveredOnDemandCost: big.NewRat(206, 1)}
	cand.UpfrontCost = big.NewRat(207, 1)
	lease := "synthetic-lease"
	cand.LeaseMenuItemID = &lease
	c.Current.UpfrontCost = big.NewRat(987654321, 1)
	beforeC, beforeItems := mustMarshal(t, c), mustMarshal(t, items)

	got, err := buildFixture(t, c, items)
	require.NoError(t, err)
	require.Equal(t, "requested-row", got.RecommendationID)
	require.Equal(t, ResourceAssociated, got.Status)
	require.Empty(t, got.Reason)
	require.Equal(t, ReasonStoredDecimalOnly, got.AmountProvenance)
	require.Equal(t, ReasonUnavailableSourcePrecision, got.EconomicStatus)
	require.Equal(t, "1970-01-01T00:00:01Z", got.FetchedAt)
	require.Nil(t, got.Currency)
	require.Nil(t, got.ComparableDelta)
	require.Equal(t, currencyNote, got.CurrencyNote)
	require.True(t, got.PremiumIncluded)
	require.NotEmpty(t, got.NonGatingDisclosure)
	require.NotEmpty(t, got.SponsorshipDisclosure)

	want := RecommendationOfferDTO{OfferID: matchOffer, Provider: "aws", CommitmentType: "savings_plan",
		HourlySelectedCommitment: ptr("0.50000000000000000001"), NativeDurationSeconds: ptr("31536000"),
		NativePlanType: ptr("EC2Instance"), NativeInstanceFamily: ptr("m5"), EffectiveContractTerm: ptr("one_year_gris"),
		PaymentOption: ptr("no_upfront"), LeaseAttached: false,
		Monthly: FinancialsDTO{CommitmentCostTotal: ptr("101"), CloudProviderCost: ptr("102"), Premium: ptr("103"),
			GrossSavings: ptr("104"), NetSavings: ptr("105"), CoveredOnDemandCost: ptr("106")},
		UpfrontCost: ptr("107")}
	require.Equal(t, want, *got.Current)

	require.Len(t, got.Candidates, 2)
	alt := got.Candidates[0]
	require.Equal(t, CandidateVendorAlternative, alt.Status)
	require.Equal(t, CandidateUnavailable, alt.NativeIdentityStatus)
	wantAlt := RecommendationOfferDTO{OfferID: "faadf00d-ffff-4aba-8ada-abcdefabcdef", Provider: "aws", CommitmentType: "savings_plan",
		HourlySelectedCommitment: ptr("0.50000000000000000001"), EffectiveContractTerm: ptr("one_year_gris"),
		PaymentOption: ptr("all_upfront"), LeaseAttached: true,
		Monthly: FinancialsDTO{CommitmentCostTotal: ptr("201"), CloudProviderCost: ptr("202"), Premium: ptr("203"),
			GrossSavings: ptr("204"), NetSavings: ptr("205"), CoveredOnDemandCost: ptr("206")},
		UpfrontCost: ptr("207")}
	require.Equal(t, wantAlt, *alt.Figures)
	require.Nil(t, alt.Figures.NativeDurationSeconds)
	require.Nil(t, alt.Figures.NativePlanType)
	require.Nil(t, alt.Figures.NativeInstanceFamily)

	wire := string(mustMarshal(t, got))
	for _, leak := range []string{"Source-account-a", "fixture-org", "987654321", "hypotheticals", "delta_vs_current", "provider-offer-a", "guarantee"} {
		require.NotContains(t, wire, leak)
	}
	require.Equal(t, beforeC, mustMarshal(t, c))
	require.Equal(t, beforeItems, mustMarshal(t, items))
}

func TestRecommendationDTOKeepsZeroDistinctFromNull(t *testing.T) {
	c, items := contractMatchFixture(t)
	c.Rows[0].Current.UpfrontCost = new(big.Rat)
	c.Rows[0].Current.Monthly = Financials{Premium: new(big.Rat)}
	got, err := buildFixture(t, c, items)
	require.NoError(t, err)
	require.Equal(t, "0", *got.Current.UpfrontCost)
	require.Equal(t, "0", *got.Current.Monthly.Premium)
	require.Nil(t, got.Current.Monthly.NetSavings)
}

func TestRecommendationDTOKeepsVeryPreciseDecimalsExact(t *testing.T) {
	c, items := contractMatchFixture(t)
	precise := rat(t, "123456789012345678.123456789012345678901234567890")
	c.Rows[0].Current.UpfrontCost = precise
	c.Rows[0].Current.Monthly.NetSavings = rat(t, "-0.000000000000000000000001")
	got, err := buildFixture(t, c, items)
	require.NoError(t, err)
	require.Equal(t, "123456789012345678.12345678901234567890123456789", *got.Current.UpfrontCost)
	require.Equal(t, "-0.000000000000000000000001", *got.Current.Monthly.NetSavings)
}

func ptr(s string) *string { return &s }

func TestPaymentAndCleanPtrSanitizeAndKeepNil(t *testing.T) {
	assert.Nil(t, payment(nil))
	assert.Nil(t, cleanPtr(nil))
	p := PaymentOption("no‮_upfront\n")
	assert.Equal(t, "no_upfront", *payment(&p))
	s := "a\x00b"
	assert.Equal(t, "ab", *cleanPtr(&s))
}
