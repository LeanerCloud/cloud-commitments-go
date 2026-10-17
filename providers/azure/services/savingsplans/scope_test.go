package savingsplans

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/billingbenefits/armbillingbenefits"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/recfilter"
)

const (
	eaScope  = "/providers/Microsoft.Billing/billingAccounts/ea-SECRET"
	mcaScope = "/providers/Microsoft.Billing/billingAccounts/mca-SECRET:1_x/billingProfiles/BP:1_x"
)

func accountPlan(scope string, typ *armbillingbenefits.AppliedScopeType, props *armbillingbenefits.AppliedScopeProperties) *armbillingbenefits.SavingsPlanModel {
	return &armbillingbenefits.SavingsPlanModel{ID: toPtr("plan-id"), SKU: &armbillingbenefits.SKU{Name: toPtr("Compute_Savings_Plan")}, Properties: &armbillingbenefits.SavingsPlanModelProperties{BillingScopeID: toPtr(scope), AppliedScopeType: typ, AppliedScopeProperties: props}}
}

// scoped builds applied-scope properties; "" leaves the field nil.
func scoped(subscription, resourceGroup string) *armbillingbenefits.AppliedScopeProperties {
	p := &armbillingbenefits.AppliedScopeProperties{}
	if subscription != "" {
		p.SubscriptionID = toPtr(subscription)
	}
	if resourceGroup != "" {
		p.ResourceGroupID = toPtr(resourceGroup)
	}
	return p
}

func TestPlanOwnedBy_BillingAccountScopes(t *testing.T) {
	single := toPtr(armbillingbenefits.AppliedScopeTypeSingle)
	sub := func(s string) *armbillingbenefits.AppliedScopeProperties { return scoped(s, "") }
	rg := func(s string) *armbillingbenefits.AppliedScopeProperties { return scoped("", s) }
	both := scoped
	cases := []struct {
		name  string
		scope string
		typ   *armbillingbenefits.AppliedScopeType
		props *armbillingbenefits.AppliedScopeProperties
		want  planDisposition // planNotOurs means an error is expected
	}{
		{"ea-sub-ours", eaScope, single, sub("/subscriptions/sub-a"), planOwned},
		{"mca-sub-ours", mcaScope, single, sub("/Subscriptions/SUB-A"), planOwned},
		{"mca-sub-other", mcaScope, single, sub("/subscriptions/sub-b"), planSkippedSingle},
		{"mixed-case-profile-scope", "/providers/MICROSOFT.billing/BILLINGACCOUNTS/x/BillingProfiles/y", single, sub("/subscriptions/sub-a"), planOwned},
		{"mixed-case-rg-literal", eaScope, single, rg("/Subscriptions/sub-a/RESOURCEGROUPS/rg"), planOwned},
		{"rg-ours", eaScope, single, rg("/subscriptions/sub-a/resourceGroups/rg1"), planOwned},
		{"rg-other", eaScope, single, rg("/subscriptions/sub-b/resourceGroups/rg1"), planSkippedSingle},
		{"sub-rg-agree", eaScope, single, both("/subscriptions/sub-a", "/subscriptions/sub-a/resourceGroups/rg1"), planOwned},
		{"sub-rg-disagree", eaScope, single, both("/subscriptions/sub-a", "/subscriptions/sub-b/resourceGroups/rg1"), planNotOurs},
		{"blank-sub-valid-rg", eaScope, single, both("  ", "/subscriptions/sub-a/resourceGroups/rg1"), planOwned},
		{"blank-sub-no-rg", eaScope, single, sub(" "), planNotOurs},
		{"nil-properties", eaScope, single, nil, planNotOurs},
		{"nil-type", eaScope, nil, sub("/subscriptions/sub-a"), planNotOurs},
		{"unknown-type", eaScope, toPtr(armbillingbenefits.AppliedScopeType("Bogus")), sub("/subscriptions/sub-a"), planNotOurs},
		{"shared-with-sub", eaScope, toPtr(armbillingbenefits.AppliedScopeTypeShared), sub("/subscriptions/sub-a"), planNotOurs},
		{"mg-id-set", eaScope, single, &armbillingbenefits.AppliedScopeProperties{SubscriptionID: toPtr("/subscriptions/sub-a"), ManagementGroupID: toPtr("/providers/Microsoft.Management/managementGroups/mg")}, planNotOurs},
		{"mg-id-blank-ignored", eaScope, single, &armbillingbenefits.AppliedScopeProperties{SubscriptionID: toPtr("/subscriptions/sub-a"), ManagementGroupID: toPtr(" ")}, planOwned},
		{"tenant-and-name-ignored", eaScope, single, &armbillingbenefits.AppliedScopeProperties{SubscriptionID: toPtr("/subscriptions/sub-a"), TenantID: toPtr("t"), DisplayName: toPtr("n")}, planOwned},
		{"sub-with-suffix", eaScope, single, sub("/subscriptions/sub-a/resourceGroups/rg"), planNotOurs},
		{"sub-trailing-slash", eaScope, single, sub("/subscriptions/sub-a/"), planNotOurs},
		{"rg-providers-tail", eaScope, single, rg("/subscriptions/sub-a/resourceGroups/rg/providers/x/y"), planNotOurs},
		{"rg-trailing-slash", eaScope, single, rg("/subscriptions/sub-a/resourceGroups/rg/"), planNotOurs},
		{"rg-empty-name", eaScope, single, rg("/subscriptions/sub-a/resourceGroups/"), planNotOurs},
		{"rg-whitespace", eaScope, single, rg("/subscriptions/sub-a/resourceGroups/r g"), planNotOurs},
		{"rg-control", eaScope, single, rg("/subscriptions/sub-a/resourceGroups/r\x00g"), planNotOurs},
		{"rg-blank-non-nil", eaScope, single, rg(" "), planNotOurs},
		{"account-foo-suffix", "/providers/Microsoft.Billing/billingAccounts/x/foo", single, sub("/subscriptions/sub-a"), planNotOurs},
		{"account-trailing-slash", eaScope + "/", single, sub("/subscriptions/sub-a"), planNotOurs},
		{"account-empty-id", "/providers/Microsoft.Billing/billingAccounts/", single, sub("/subscriptions/sub-a"), planNotOurs},
		{"account-empty-profile", "/providers/Microsoft.Billing/billingAccounts/x/billingProfiles/", single, sub("/subscriptions/sub-a"), planNotOurs},
		{"account-whitespace", "/providers/Microsoft.Billing/billingAccounts/x y", single, sub("/subscriptions/sub-a"), planNotOurs},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			got, err := planOwnedBy(accountPlan(tt.scope, tt.typ, tt.props), "sub-a")
			if tt.want == planNotOurs {
				require.Error(t, err)
				for _, leak := range []string{"SECRET", "billingAccounts", "sub-b"} {
					assert.NotContains(t, err.Error(), leak)
				}
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}

	for name, want := range map[string]planDisposition{"ours": planNotOurs, "other": planSkippedSingle} {
		t.Run(name+"-without-id", func(t *testing.T) {
			s := "/subscriptions/sub-a"
			if name == "other" {
				s = "/subscriptions/sub-b"
			}
			sp := accountPlan(eaScope, single, sub(s))
			sp.ID = nil
			got, err := planOwnedBy(sp, "sub-a")
			assert.Equal(t, want, got)
			assert.Equal(t, want == planNotOurs, err != nil)
		})
	}
}

// Not parallel: it replaces the global log writer.
func TestGetExistingCommitments_AggregateSkipLog(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)

	single := toPtr(armbillingbenefits.AppliedScopeTypeSingle)
	other, ours := scoped("/subscriptions/sub-b", ""), scoped("/subscriptions/sub-a", "")
	run := func(rows ...*armbillingbenefits.SavingsPlanModel) {
		buf.Reset()
		c := NewClient(nil, "sub-a", "eastus")
		c.SetListAllPager(&mockListAllPager{results: rows})
		_, err := c.GetExistingCommitments(context.Background())
		require.NoError(t, err)
	}

	run(accountPlan(eaScope, single, other), accountPlan(mcaScope, single, other), accountPlan(eaScope, single, ours))
	assert.Equal(t, 1, bytes.Count(buf.Bytes(), []byte("skipped 2 single-scope savings plans applied to other subscriptions")))
	assert.NotContains(t, buf.String(), "SECRET")
	assert.NotContains(t, buf.String(), "plan-id")

	run(accountPlan(eaScope, single, ours))
	assert.NotContains(t, buf.String(), "skipped")
}

func TestGetExistingCommitments_SDKBillingAccountSingleScope(t *testing.T) {
	row := func(id, scope, applied, props string) string {
		return fmt.Sprintf(`{"id":%q,"name":"Compute","sku":{"name":"Compute_Savings_Plan"},"properties":{"billingScopeId":%q,"appliedScopeType":%q,"appliedScopeProperties":%s,"provisioningState":"Succeeded","effectiveDateTime":%q}}`,
			id, scope, applied, props, time.Now().UTC().Format(time.RFC3339))
	}
	cases := []struct {
		name      string
		row       string
		wantOwned bool
		wantErr   bool
	}{
		{"ea-ours", row("p1", eaScope, "Single", `{"subscriptionId":"/subscriptions/sub-a","tenantId":"t","displayName":"d"}`), true, false},
		{"mca-rg-ours", row("p1", mcaScope, "Single", `{"resourceGroupId":"/subscriptions/sub-a/resourceGroups/rg"}`), true, false},
		{"ea-other", row("p1", eaScope, "Single", `{"subscriptionId":"/subscriptions/sub-b"}`), false, false},
		{"single-no-subscription", row("p1", eaScope, "Single", `{}`), false, true},
		{"shared", row("p1", eaScope, "Shared", `{}`), false, true},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, err := fmt.Fprintf(w, `{"value":[%s]}`, tt.row)
				assert.NoError(t, err)
			}))
			t.Cleanup(server.Close)
			client := NewClient(nil, "sub-a", "eastus")
			client.SetListAllPager(newFixtureSavingsPlanClient(t, server).NewListAllPager(nil))
			recs := []common.Recommendation{{Provider: common.ProviderAzure, Service: common.ServiceSavingsPlansAll, ResourceType: "Compute", Count: 1}}
			passed, filtered, err := recfilter.NewDuplicateChecker(24).AdjustRecommendationsForExisting(context.Background(), recs, client)
			if tt.wantErr {
				require.ErrorContains(t, err, "incomplete savings plan inventory")
				assert.Equal(t, recs, passed)
				assert.Empty(t, filtered)
				return
			}
			require.NoError(t, err)
			if tt.wantOwned {
				assert.Empty(t, passed)
				assert.Len(t, filtered, 1)
			} else {
				assert.Equal(t, recs, passed)
				assert.Empty(t, filtered)
			}
		})
	}
}
