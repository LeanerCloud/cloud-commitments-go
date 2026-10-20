package savingsplans

import (
	"context"
	"net/http"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/billingbenefits/armbillingbenefits"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
)

// Issue #387: the SavingsPlanOrderAlias and validatePurchase bodies must carry
// the ARM SKU name "Compute_Savings_Plan" (the armSkuName the Benefit
// Recommendations API returns and the SDK's SavingsPlanOrderAliasCreate example
// sends), not the CUDly PlanType "Compute". Before the fix both bodies sent
// sku.name "Compute".

func skuRec(planType string) common.Recommendation {
	rec := makeRec("1yr")
	rec.Details = &common.SavingsPlanDetails{PlanType: planType, HourlyCommitment: 1.5, Currency: "USD"}
	return rec
}

func okAlias() *mockOrderAliasClient {
	orderID := "/providers/Microsoft.BillingBenefits/savingsPlanOrders/order1"
	return &mockOrderAliasClient{createPoller: &mockPoller{resp: armbillingbenefits.SavingsPlanOrderAliasClientCreateResponse{
		SavingsPlanOrderAliasModel: armbillingbenefits.SavingsPlanOrderAliasModel{ID: &orderID},
	}}}
}

func capturedSKUs(t *testing.T, rec common.Recommendation) (purchase, validate string) {
	t.Helper()
	alias := okAlias()
	rp := &mockRPValidate{}
	c := NewClient(nil, "sub", "eastus")
	c.SetOrderAliasClient(alias)
	c.SetRPValidateClient(rp)

	_, err := c.PurchaseCommitment(context.Background(), rec, common.PurchaseOptions{})
	require.NoError(t, err)
	require.NoError(t, c.ValidateOffering(context.Background(), rec))

	require.NotNil(t, alias.capturedBody.SKU)
	require.NotNil(t, alias.capturedBody.SKU.Name)
	require.Len(t, rp.captured.Benefits, 1)
	require.NotNil(t, rp.captured.Benefits[0].SKU)
	require.NotNil(t, rp.captured.Benefits[0].SKU.Name)
	return *alias.capturedBody.SKU.Name, *rp.captured.Benefits[0].SKU.Name
}

func TestSKUName_ComputePlanTypeSendsARMSKU(t *testing.T) {
	t.Parallel()
	for _, planType := range []string{"Compute", "Compute_Savings_Plan"} {
		t.Run(planType, func(t *testing.T) {
			purchase, validate := capturedSKUs(t, skuRec(planType))
			assert.Equal(t, "Compute_Savings_Plan", purchase)
			assert.Equal(t, "Compute_Savings_Plan", validate)
		})
	}
}

// A recommendation produced by GetRecommendations from a fixture row must
// purchase under the same SKU the row named.
func TestSKUName_RecommendationRoundTrip(t *testing.T) {
	t.Parallel()
	h := &routeHTTP{reply: func(*http.Request) (int, string) {
		return 200, `{"value":[` + benefitRow("P1Y", nil) + `]}`
	}}
	recs, err := newTestClient(h).GetRecommendations(context.Background(), &common.RecommendationParams{Term: "1yr", LookbackPeriod: "30d"})
	require.NoError(t, err)
	require.Len(t, recs, 1)

	purchase, validate := capturedSKUs(t, recs[0])
	assert.Equal(t, armSkuComputeSavingsPlan, purchase)
	assert.Equal(t, armSkuComputeSavingsPlan, validate)
}

// A PlanType with no known ARM SKU fails before any Azure call instead of
// sending a sku.name the API would reject.
func TestSKUName_UnknownPlanTypeFailsClosed(t *testing.T) {
	t.Parallel()
	for _, planType := range []string{"", "MachineLearning", "compute", " Compute", "Compute_Savings_Plan "} {
		t.Run(planType, func(t *testing.T) {
			alias := okAlias()
			rp := &mockRPValidate{}
			c := NewClient(nil, "sub", "eastus")
			c.SetOrderAliasClient(alias)
			c.SetRPValidateClient(rp)
			rec := skuRec(planType)

			result, err := c.PurchaseCommitment(context.Background(), rec, common.PurchaseOptions{})
			require.Error(t, err)
			assert.Contains(t, err.Error(), "no known Azure savings plan SKU")
			assert.False(t, result.Success)
			assert.Equal(t, err, result.Error)
			assert.Zero(t, alias.calls)

			err = c.ValidateOffering(context.Background(), rec)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "no known Azure savings plan SKU")
			assert.Zero(t, rp.calls)
		})
	}
}
