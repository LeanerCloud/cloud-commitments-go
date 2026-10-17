package savingsplans

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/recfilter"
)

// Fixtures follow the shape of the armbillingbenefits SDK examples (GUID name,
// Compute_Savings_Plan SKU); they are not a captured live response.
func resourceTypeFixture(t *testing.T, sku string) *Client {
	t.Helper()
	row := fmt.Sprintf(`{"id":"/subscriptions/sub-a/providers/Microsoft.BillingBenefits/savingsPlanOrders/o1/savingsPlans/6f1f3c1a-1b2c-4d5e-8f90-123456789abc","name":"6f1f3c1a-1b2c-4d5e-8f90-123456789abc"%s,"properties":{"billingScopeId":"/subscriptions/sub-a","provisioningState":"Succeeded","effectiveDateTime":%q}}`,
		sku, time.Now().UTC().Format(time.RFC3339))
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, err := fmt.Fprintf(w, `{"value":[%s]}`, row)
		assert.NoError(t, err)
	}))
	t.Cleanup(server.Close)
	client := NewClient(nil, "sub-a", "eastus")
	client.SetListAllPager(newFixtureSavingsPlanClient(t, server).NewListAllPager(nil))
	return client
}

func TestOwnedPlanSuppressesComputeRecommendation(t *testing.T) {
	client := resourceTypeFixture(t, `,"sku":{"name":"Compute_Savings_Plan"}`)
	hours, cost, saved, commit := int32(168), 100.0, 20.0, 1.5
	row := &benefitRecommendation{ID: "rec-1", Kind: kindSavingsPlan, Properties: &benefitRecommendationProps{
		ArmSkuName: armSkuComputeSavingsPlan, Term: "P1Y", Scope: scopeShared, CommitmentGranularity: grainHourly,
		CurrencyCode: "USD", TotalHours: &hours, CostWithoutBenefit: &cost,
		RecommendationDetails: &benefitRecommendDetls{CommitmentAmount: &commit, SavingsAmount: &saved},
	}}
	rec, err := convertBenefitRecommendation(row, "sub-a")
	require.NoError(t, err)
	recs := []common.Recommendation{*rec}
	passed, filtered, err := recfilter.NewDuplicateChecker(24).AdjustRecommendationsForExisting(context.Background(), recs, client)
	require.NoError(t, err)
	assert.Empty(t, passed)
	assert.Len(t, filtered, 1)
}

func TestGetExistingCommitments_FailsClosedOnBadSKU(t *testing.T) {
	for name, sku := range map[string]string{
		"missing": ``,
		"empty":   `,"sku":{"name":""}`,
		"no-name": `,"sku":{}`,
		"unknown": `,"sku":{"name":"Mystery_Plan"}`,
	} {
		t.Run(name, func(t *testing.T) {
			got, err := resourceTypeFixture(t, sku).GetExistingCommitments(context.Background())
			require.ErrorContains(t, err, "incomplete savings plan inventory")
			require.ErrorContains(t, err, "6f1f3c1a-1b2c-4d5e-8f90-123456789abc")
			require.ErrorContains(t, err, "extend the SKU mapping in convertSavingsPlan")
			assert.Nil(t, got)
		})
	}
}

func TestGetExistingCommitments_SKUMatchIsCaseInsensitive(t *testing.T) {
	got, err := resourceTypeFixture(t, `,"sku":{"name":"compute_savings_plan"}`).GetExistingCommitments(context.Background())
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "Compute", got[0].ResourceType)
}
