package azure

import (
	"context"
	"errors"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/advisor/armadvisor"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	azrecs "github.com/LeanerCloud/cloud-commitments-go/providers/azure/internal/recommendations"
)

// Fixture-based: payloads are shaped from armadvisor.ResourceRecommendationBase
// with the extendedProperties keys documented in advisorKey* (no live Azure
// Advisor data was available).

func advisorRec(impacted string, ext map[string]*string) *armadvisor.ResourceRecommendationBase {
	return &armadvisor.ResourceRecommendationBase{
		Properties: &armadvisor.RecommendationProperties{ImpactedField: &impacted, ExtendedProperties: ext},
	}
}

func reservationExt(over map[string]*string) map[string]*string {
	ext := map[string]*string{
		"reservedResourceType": strPtr("VirtualMachines"),
		"annualSavingsAmount":  strPtr("1200"),
		"savingsCurrency":      strPtr("USD"),
		"term":                 strPtr("P3Y"),
		"sku":                  strPtr("Standard_D2s_v3"),
		"qty":                  strPtr("2"),
		"region":               strPtr("eastus"),
	}
	for k, v := range over {
		if v == nil {
			delete(ext, k)
		} else {
			ext[k] = v
		}
	}
	return ext
}

const subscriptionScope = "Microsoft.Subscriptions/subscriptions"

func TestConvertAdvisor_DerivesFieldsFromPayload(t *testing.T) {
	t.Parallel()
	a := &RecommendationsClientAdapter{subscriptionID: "sub"}

	ri := a.convertAdvisorRecommendation(context.Background(), advisorRec(subscriptionScope, reservationExt(nil)))
	require.NotNil(t, ri)
	assert.Equal(t, common.CommitmentReservedInstance, ri.CommitmentType)
	assert.Equal(t, "3yr", ri.Term, "term comes from the payload, not a 1yr default")
	assert.Equal(t, "Standard_D2s_v3", ri.ResourceType)
	assert.Equal(t, 2, ri.Count)
	assert.Equal(t, 100.0, ri.EstimatedSavings)
	assert.Empty(t, ri.PaymentOption, "the payload carries no payment plan; variants are fanned out later")

	sp := a.convertAdvisorRecommendation(context.Background(), advisorRec(subscriptionScope, map[string]*string{
		"recommendationSubCategory": strPtr("SavingsPlan"),
		"annualSavingsAmount":       strPtr("2400"),
		"savingsCurrency":           strPtr("USD"),
		"term":                      strPtr("P1Y"),
	}))
	require.NotNil(t, sp)
	assert.Equal(t, common.CommitmentSavingsPlan, sp.CommitmentType)
	assert.Equal(t, common.ServiceSavingsPlansAll, sp.Service)
	assert.Equal(t, "1yr", sp.Term)
}

func TestConvertAdvisor_NonPurchaseRecommendationsAreIgnored(t *testing.T) {
	t.Parallel()
	a := &RecommendationsClientAdapter{subscriptionID: "sub"}
	// Resize/shutdown: resource-scoped, no reservedResourceType.
	ctx, skips := azrecs.WithSkipLog(context.Background())
	rightsize := advisorRec("Microsoft.Compute/virtualMachines", map[string]*string{
		"annualSavingsAmount": strPtr("1000"), "savingsCurrency": strPtr("USD"),
	})
	assert.Nil(t, a.convertAdvisorRecommendation(ctx, rightsize))
	assert.Empty(t, skips.Entries(), "a non-purchase recommendation is not a skip")
}

func TestConvertAdvisor_UnknownOrMissingValuesAreSkippedLoudly(t *testing.T) {
	t.Parallel()
	a := &RecommendationsClientAdapter{subscriptionID: "sub"}
	for name, over := range map[string]map[string]*string{
		"unknown term":     {"term": strPtr("P5Y")},
		"missing term":     {"term": nil},
		"non-USD":          {"savingsCurrency": strPtr("EUR")},
		"missing currency": {"savingsCurrency": nil},
		"missing sku":      {"sku": nil},
		"missing qty":      {"qty": nil},
		"zero qty":         {"qty": strPtr("0")},
		"junk qty":         {"qty": strPtr("two")},
		"missing savings":  {"annualSavingsAmount": nil},
	} {
		t.Run(name, func(t *testing.T) {
			ctx, skips := azrecs.WithSkipLog(context.Background())
			assert.Nil(t, a.convertAdvisorRecommendation(ctx, advisorRec(subscriptionScope, reservationExt(over))))
			assert.Len(t, skips.Entries(), 1)
		})
	}
}

func TestAppendAdvisorPageRecs_ReservationRowIsBaseSavingsPlanIsExpanded(t *testing.T) {
	t.Parallel()
	a := &RecommendationsClientAdapter{subscriptionID: "sub"}
	page := armadvisor.RecommendationsClientListResponse{ResourceRecommendationBaseListResult: armadvisor.ResourceRecommendationBaseListResult{
		Value: []*armadvisor.ResourceRecommendationBase{advisorRec(subscriptionScope, reservationExt(nil)),
			advisorRec(subscriptionScope, reservationExt(map[string]*string{"recommendationSubCategory": strPtr("SavingsPlan")}))},
	}}
	recs := a.appendAdvisorPageRecs(context.Background(), common.RecommendationParams{}, page, nil)
	require.Len(t, recs, 3)
	assert.Empty(t, recs[0].PaymentOption, "reservation rows stay base rows until priced")
	assert.Equal(t, 100.0, recs[0].EstimatedSavings)
	assert.Nil(t, recs[0].RecurringMonthlyCost, "no price in the payload: unknown, not zero")
	assert.Equal(t, []string{"upfront", "monthly"}, []string{recs[1].PaymentOption, recs[2].PaymentOption})
}

func TestGetRecommendations_AdvisorTruncationIsSurfaced(t *testing.T) {
	overrideServiceClientFns(t, &pricedFake{}, &fakeServiceClient{}, &fakeServiceClient{}, &fakeServiceClient{}, &fakeServiceClient{})
	a := &RecommendationsClientAdapter{cred: &mockAzureTokenCredential{}, subscriptionID: "sub"}
	a.getAdvisorRecsFn = func(ctx context.Context, p common.RecommendationParams) ([]common.Recommendation, error) {
		pager := &fakePager[armadvisor.RecommendationsClientListResponse]{limit: 10000, page: advisorPage}
		return a.collectAdvisorRecommendations(ctx, p, pager)
	}
	recs, err := a.GetRecommendations(context.Background(), &common.RecommendationParams{})
	assert.NotEmpty(t, recs, "what was collected is kept")
	var partial *azrecs.PartialResultError
	require.True(t, errors.As(err, &partial), "a capped Advisor walk must not look complete: %v", err)
	assert.Contains(t, partial.Skipped[0], "pagination cap")
	// #670: a truncated walk is an incomplete source, not an unpriceable row
	// or a pricing outage, though its reason text also starts "advisor: azure advisor:".
	assert.Equal(t, []SkipKind{SkipSourceIncomplete}, partial.SkipKinds())
}

func TestGetRecommendations_AdvisorSavingsPlanDedupedAgainstBenefitRecs(t *testing.T) {
	sp := func(account, term string, from string) common.Recommendation {
		return common.Recommendation{Provider: common.ProviderAzure, Service: common.ServiceCompute,
			CommitmentType: common.CommitmentSavingsPlan, Account: account, Term: term, Region: from}
	}
	benefit := sp("sub", "1yr", "benefit")
	run := func(spRecs []common.Recommendation, advisor []common.Recommendation) []common.Recommendation {
		overrideServiceClientFns(t, &pricedFake{}, &fakeServiceClient{}, &fakeServiceClient{}, &fakeServiceClient{}, &fakeServiceClient{recs: spRecs})
		a := &RecommendationsClientAdapter{cred: &mockAzureTokenCredential{}, subscriptionID: "sub",
			getAdvisorRecsFn: func(context.Context, common.RecommendationParams) ([]common.Recommendation, error) {
				return advisor, nil
			}}
		recs, err := a.GetRecommendations(context.Background(), &common.RecommendationParams{})
		require.NoError(t, err)
		return recs
	}
	advisorRI := riRow(common.ServiceCompute, "advisor-ri", "1yr", "Standard_D2s_v3", 1, "")
	advisorRI.Account = "sub"

	got := run([]common.Recommendation{benefit}, []common.Recommendation{sp("sub", "1yr", "advisor"), sp("sub", "3yr", "advisor"), advisorRI})
	assert.Len(t, got, 4, "the covered (account, term) Advisor SP row is dropped; other terms stay; the RI row becomes two priced legs")
	assert.Contains(t, got, benefit)
	assert.Contains(t, got, sp("sub", "3yr", "advisor"))

	got = run(nil, []common.Recommendation{sp("sub", "1yr", "advisor")})
	assert.Len(t, got, 1, "Advisor SP advice is kept when the benefit source returns nothing")
}

func TestGetRecommendations_CollectsSynapseForDataWarehouse(t *testing.T) {
	overrideServiceClientFns(t, &fakeServiceClient{}, &fakeServiceClient{}, &fakeServiceClient{}, &fakeServiceClient{}, &fakeServiceClient{})
	want := common.Recommendation{Provider: common.ProviderAzure, Service: common.ServiceDataWarehouse}
	newSynapseClientFn = newFakeFn(&fakeServiceClient{recs: []common.Recommendation{want}})
	a := &RecommendationsClientAdapter{cred: &mockAzureTokenCredential{}, subscriptionID: "sub", getAdvisorRecsFn: noopAdvisorFn}

	recs, err := a.GetRecommendations(context.Background(), &common.RecommendationParams{})
	require.NoError(t, err)
	assert.Equal(t, []common.Recommendation{want}, recs)

	recs, err = a.GetRecommendations(context.Background(), &common.RecommendationParams{Service: common.ServiceCompute})
	require.NoError(t, err)
	assert.Empty(t, recs, "synapse is skipped when another service is requested")
}

func TestConvertAdvisor_UnmappedReservedResourceTypeIsIgnoredNotSkipped(t *testing.T) {
	t.Parallel()
	a := &RecommendationsClientAdapter{subscriptionID: "sub"}
	ctx, skips := azrecs.WithSkipLog(context.Background())
	got := a.convertAdvisorRecommendation(ctx, advisorRec(subscriptionScope, reservationExt(map[string]*string{"reservedResourceType": strPtr("CosmosDb")})))
	assert.Nil(t, got)
	assert.Empty(t, skips.Entries(), "a service we do not price must not make every subscription look partial")
}

func TestConvertAdvisor_RejectsBadSavingsAmounts(t *testing.T) {
	t.Parallel()
	a := &RecommendationsClientAdapter{subscriptionID: "sub"}
	for name, v := range map[string]*string{
		"nil": nil, "thousands separator": strPtr("1,234.00"), "empty": strPtr(""),
		"NaN": strPtr("NaN"), "Inf": strPtr("Inf"), "-Inf": strPtr("-Inf"), "negative": strPtr("-1200"),
	} {
		t.Run(name, func(t *testing.T) {
			ctx, skips := azrecs.WithSkipLog(context.Background())
			assert.Nil(t, a.convertAdvisorRecommendation(ctx, advisorRec(subscriptionScope, reservationExt(map[string]*string{"annualSavingsAmount": v}))))
			assert.Len(t, skips.Entries(), 1)
		})
	}
	zero := a.convertAdvisorRecommendation(context.Background(), advisorRec(subscriptionScope, reservationExt(map[string]*string{"annualSavingsAmount": strPtr("0")})))
	require.NotNil(t, zero, "a real zero saving is kept")
	assert.Equal(t, 0.0, zero.EstimatedSavings)
}

// The Advisor payload carries only a USD-checked savings figure, no commitment
// amounts, so no payment leg is proven USD and the currency stays unknown.
func TestConvertAdvisor_LeavesCommitmentCurrencyEmpty(t *testing.T) {
	t.Parallel()
	a := &RecommendationsClientAdapter{subscriptionID: "sub"}
	rec := a.convertAdvisorRecommendation(context.Background(), advisorRec(subscriptionScope, reservationExt(nil)))
	require.NotNil(t, rec)
	variants := azrecs.ExpandPaymentVariants(*rec)
	require.NotEmpty(t, variants)
	for _, v := range variants {
		assert.Empty(t, v.CommitmentCurrency, v.PaymentOption)
	}
}
