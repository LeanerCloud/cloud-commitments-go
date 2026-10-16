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
	return &armbillingbenefits.SavingsPlanModel{
		ID: toPtr("plan-id"),
		Properties: &armbillingbenefits.SavingsPlanModelProperties{
			BillingScopeID: toPtr(scope), AppliedScopeType: typ, AppliedScopeProperties: props,
		},
	}
}

func TestPlanOwnedBy_BillingAccountScopes(t *testing.T) {
	single := toPtr(armbillingbenefits.AppliedScopeTypeSingle)
	sub := func(s string) *armbillingbenefits.AppliedScopeProperties {
		return &armbillingbenefits.AppliedScopeProperties{SubscriptionID: toPtr(s)}
	}
	rg := func(s string) *armbillingbenefits.AppliedScopeProperties {
		return &armbillingbenefits.AppliedScopeProperties{ResourceGroupID: toPtr(s)}
	}
	both := func(s, r string) *armbillingbenefits.AppliedScopeProperties {
		return &armbillingbenefits.AppliedScopeProperties{SubscriptionID: toPtr(s), ResourceGroupID: toPtr(r)}
	}
	cases := []struct {
		name  string
		scope string
		typ   *armbillingbenefits.AppliedScopeType
		props *armbillingbenefits.AppliedScopeProperties
		want  planDisposition
		fail  bool
	}{
		{"ea-sub-ours", eaScope, single, sub("/subscriptions/sub-a"), planOwned, false},
		{"mca-sub-ours", mcaScope, single, sub("/Subscriptions/SUB-A"), planOwned, false},
		{"mca-sub-other", mcaScope, single, sub("/subscriptions/sub-b"), planSkippedSingle, false},
		{"rg-ours", eaScope, single, rg("/subscriptions/sub-a/resourceGroups/rg1"), planOwned, false},
		{"rg-other", eaScope, single, rg("/subscriptions/sub-b/resourceGroups/rg1"), planSkippedSingle, false},
		{"sub-rg-agree", eaScope, single, both("/subscriptions/sub-a", "/subscriptions/sub-a/resourceGroups/rg1"), planOwned, false},
		{"sub-rg-disagree", eaScope, single, both("/subscriptions/sub-a", "/subscriptions/sub-b/resourceGroups/rg1"), planNotOurs, true},
		{"blank-sub-valid-rg", eaScope, single, both("  ", "/subscriptions/sub-a/resourceGroups/rg1"), planOwned, false},
		{"blank-sub-no-rg", eaScope, single, sub(" "), planNotOurs, true},
		{"nil-properties", eaScope, single, nil, planNotOurs, true},
		{"nil-type", eaScope, nil, sub("/subscriptions/sub-a"), planNotOurs, true},
		{"unknown-type", eaScope, toPtr(armbillingbenefits.AppliedScopeType("Bogus")), sub("/subscriptions/sub-a"), planNotOurs, true},
		{"shared-with-sub", eaScope, toPtr(armbillingbenefits.AppliedScopeTypeShared), sub("/subscriptions/sub-a"), planNotOurs, true},
		{"mg-id-set", eaScope, single, &armbillingbenefits.AppliedScopeProperties{SubscriptionID: toPtr("/subscriptions/sub-a"), ManagementGroupID: toPtr("/providers/Microsoft.Management/managementGroups/mg")}, planNotOurs, true},
		{"mg-id-blank-ignored", eaScope, single, &armbillingbenefits.AppliedScopeProperties{SubscriptionID: toPtr("/subscriptions/sub-a"), ManagementGroupID: toPtr(" ")}, planOwned, false},
		{"tenant-and-name-ignored", eaScope, single, &armbillingbenefits.AppliedScopeProperties{SubscriptionID: toPtr("/subscriptions/sub-a"), TenantID: toPtr("t"), DisplayName: toPtr("n")}, planOwned, false},
		{"sub-with-suffix", eaScope, single, sub("/subscriptions/sub-a/resourceGroups/rg"), planNotOurs, true},
		{"sub-trailing-slash", eaScope, single, sub("/subscriptions/sub-a/"), planNotOurs, true},
		{"rg-providers-tail", eaScope, single, rg("/subscriptions/sub-a/resourceGroups/rg/providers/x/y"), planNotOurs, true},
		{"rg-trailing-slash", eaScope, single, rg("/subscriptions/sub-a/resourceGroups/rg/"), planNotOurs, true},
		{"rg-empty-name", eaScope, single, rg("/subscriptions/sub-a/resourceGroups/"), planNotOurs, true},
		{"rg-whitespace", eaScope, single, rg("/subscriptions/sub-a/resourceGroups/r g"), planNotOurs, true},
		{"rg-control", eaScope, single, rg("/subscriptions/sub-a/resourceGroups/r\x00g"), planNotOurs, true},
		{"rg-blank-non-nil", eaScope, single, rg(" "), planNotOurs, true},
		{"account-foo-suffix", "/providers/Microsoft.Billing/billingAccounts/x/foo", single, sub("/subscriptions/sub-a"), planNotOurs, true},
		{"account-trailing-slash", eaScope + "/", single, sub("/subscriptions/sub-a"), planNotOurs, true},
		{"account-empty-id", "/providers/Microsoft.Billing/billingAccounts/", single, sub("/subscriptions/sub-a"), planNotOurs, true},
		{"account-empty-profile", "/providers/Microsoft.Billing/billingAccounts/x/billingProfiles/", single, sub("/subscriptions/sub-a"), planNotOurs, true},
		{"account-whitespace", "/providers/Microsoft.Billing/billingAccounts/x y", single, sub("/subscriptions/sub-a"), planNotOurs, true},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			got, err := planOwnedBy(accountPlan(tt.scope, tt.typ, tt.props), "sub-a")
			if tt.fail {
				require.Error(t, err)
				assert.NotContains(t, err.Error(), "SECRET")
				assert.NotContains(t, err.Error(), "billingAccounts")
				assert.NotContains(t, err.Error(), "sub-b")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}

	t.Run("ours-without-id", func(t *testing.T) {
		sp := accountPlan(eaScope, single, sub("/subscriptions/sub-a"))
		sp.ID = nil
		_, err := planOwnedBy(sp, "sub-a")
		require.ErrorContains(t, err, "no ID")
	})
	t.Run("other-without-id", func(t *testing.T) {
		sp := accountPlan(eaScope, single, sub("/subscriptions/sub-b"))
		sp.ID = nil
		got, err := planOwnedBy(sp, "sub-a")
		require.NoError(t, err)
		assert.Equal(t, planSkippedSingle, got)
	})
}

// Not parallel: it replaces the global log writer.
func TestGetExistingCommitments_AggregateSkipLog(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)

	single := toPtr(armbillingbenefits.AppliedScopeTypeSingle)
	other := &armbillingbenefits.AppliedScopeProperties{SubscriptionID: toPtr("/subscriptions/sub-b")}
	ours := &armbillingbenefits.AppliedScopeProperties{SubscriptionID: toPtr("/subscriptions/sub-a")}
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
		return fmt.Sprintf(`{"id":%q,"name":"Compute","properties":{"billingScopeId":%q,"appliedScopeType":%q,"appliedScopeProperties":%s,"provisioningState":"Succeeded","effectiveDateTime":%q}}`,
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
