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
	return &armbillingbenefits.SavingsPlanModel{ID: toPtr("plan-id"), SKU: &armbillingbenefits.SKU{Name: toPtr("Compute_Savings_Plan")}, Properties: &armbillingbenefits.SavingsPlanModelProperties{BillingScopeID: toPtr(scope), EffectiveDateTime: toPtr(time.Now()), AppliedScopeType: typ, AppliedScopeProperties: props}}
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
	t.Parallel()
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
	t.Parallel()
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

// subPlan is a plan billed to subscription sub-b with the given applied scope.
func subPlan(id string, typ *armbillingbenefits.AppliedScopeType, props *armbillingbenefits.AppliedScopeProperties) *armbillingbenefits.SavingsPlanModel {
	sp := accountPlan("/subscriptions/sub-b", typ, props)
	sp.ID = toPtr(id)
	return sp
}

func TestGetCoveringCommitments_ForeignSubscriptionPlans(t *testing.T) {
	single := toPtr(armbillingbenefits.AppliedScopeTypeSingle)
	own := accountPlan("/subscriptions/sub-a", toPtr(armbillingbenefits.AppliedScopeTypeShared), nil)
	own.ID = toPtr("owned")
	coveringNoID := subPlan("", single, scoped("/subscriptions/sub-a", ""))
	coveringNoID.ID = nil
	otherNoID := subPlan("", single, scoped("/subscriptions/sub-c", ""))
	otherNoID.ID = nil
	cases := []struct {
		name      string
		row       *armbillingbenefits.SavingsPlanModel
		wantCover bool
		wantErr   bool
	}{
		{"single-ours", subPlan("f", single, scoped("/subscriptions/sub-a", "")), true, false},
		{"single-ours-mixed-case", subPlan("f", single, scoped("/Subscriptions/SUB-A", "")), true, false},
		{"single-rg-ours", subPlan("f", single, scoped("", "/subscriptions/sub-a/resourceGroups/rg")), true, false},
		{"single-other", subPlan("f", single, scoped("/subscriptions/sub-c", "")), false, false},
		{"single-rg-other", subPlan("f", single, scoped("", "/subscriptions/sub-c/resourceGroups/rg")), false, false},
		{"single-unresolvable", subPlan("f", single, nil), true, false},
		{"single-disagreeing", subPlan("f", single, scoped("/subscriptions/sub-c", "/subscriptions/sub-a/resourceGroups/rg")), true, false},
		{"shared", subPlan("f", toPtr(armbillingbenefits.AppliedScopeTypeShared), nil), true, false},
		{"management-group", subPlan("f", toPtr(armbillingbenefits.AppliedScopeTypeManagementGroup), nil), true, false},
		{"nil-type", subPlan("f", nil, nil), true, false},
		{"unknown-type", subPlan("f", toPtr(armbillingbenefits.AppliedScopeType("Bogus")), scoped("/subscriptions/sub-c", "")), true, false},
		{"covering-no-id", coveringNoID, false, true},
		{"covering-blank-id", subPlan(" ", single, scoped("/subscriptions/sub-a", "")), false, true},
		{"other-no-id", otherNoID, false, false},
		{"account-shared", accountPlan(eaScope, toPtr(armbillingbenefits.AppliedScopeTypeShared), nil), false, true},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			c := NewClient(nil, "sub-a", "eastus")
			c.SetListAllPager(&mockListAllPager{results: []*armbillingbenefits.SavingsPlanModel{own, tt.row}})
			got, err := c.GetCoveringCommitments(context.Background())
			if tt.wantErr {
				require.Error(t, err)
				assert.Nil(t, got)
				return
			}
			require.NoError(t, err)
			want := []common.Commitment{mustConvertSavingsPlan(t, own, "sub-a")}
			if tt.wantCover {
				want = append(want, mustConvertSavingsPlan(t, tt.row, "sub-b"))
			}
			assert.Equal(t, want, got)

			// Ownership is unchanged: the payer sub-b owns the plan, not sub-a.
			c.SetListAllPager(&mockListAllPager{results: []*armbillingbenefits.SavingsPlanModel{own, tt.row}})
			owned, err := c.GetExistingCommitments(context.Background())
			require.NoError(t, err)
			assert.Equal(t, want[:1], owned)
		})
	}
}

// coveringLister mirrors the optional interface recfilter.DuplicateChecker
// consults before GetExistingCommitments (pkg/recfilter/dedupe.go).
type coveringLister interface {
	GetCoveringCommitments(ctx context.Context) ([]common.Commitment, error)
}

var _ coveringLister = (*Client)(nil)

// coveringView hands DuplicateChecker the covering list as its inventory, so
// this test exercises the recfilter matching against the pinned pkg as well as
// the workspace one, which picks GetCoveringCommitments up on its own.
type coveringView struct{ *Client }

func (v coveringView) GetExistingCommitments(ctx context.Context) ([]common.Commitment, error) {
	return v.GetCoveringCommitments(ctx)
}

// A plan billed to sub-b and bought an hour ago, decoded by the real SDK pager
// (#466): only sub-b owns it, but it suppresses sub-a's matching recommendation
// unless its Single scope names another subscription.
func TestDuplicateChecker_PlanBilledToAnotherSubscription(t *testing.T) {
	row := func(applied, props string) string {
		return fmt.Sprintf(`{"id":"p1","name":"Compute","sku":{"name":"Compute_Savings_Plan"},"properties":{"billingScopeId":"/subscriptions/sub-b","appliedScopeType":%q,"appliedScopeProperties":%s,"provisioningState":"Succeeded","effectiveDateTime":%q}}`,
			applied, props, time.Now().UTC().Add(-time.Hour).Format(time.RFC3339))
	}
	cases := []struct {
		name         string
		row          string
		wantSuppress bool
	}{
		{"single-ours", row("Single", `{"subscriptionId":"/subscriptions/sub-a"}`), true},
		{"single-rg-ours", row("Single", `{"resourceGroupId":"/subscriptions/sub-a/resourceGroups/rg"}`), true},
		{"shared", row("Shared", `{}`), true},
		{"management-group", row("ManagementGroup", `{"managementGroupId":"/providers/Microsoft.Management/managementGroups/mg"}`), true},
		{"single-other", row("Single", `{"subscriptionId":"/subscriptions/sub-c"}`), false},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, err := fmt.Fprintf(w, `{"value":[%s]}`, tt.row)
				assert.NoError(t, err)
			}))
			t.Cleanup(server.Close)
			sdk := newFixtureSavingsPlanClient(t, server)
			client := func(sub string) *Client {
				c := NewClient(nil, sub, "eastus")
				c.SetListAllPager(sdk.NewListAllPager(nil))
				return c
			}
			ownedA, err := client("sub-a").GetExistingCommitments(context.Background())
			require.NoError(t, err)
			assert.Empty(t, ownedA)
			ownedB, err := client("sub-b").GetExistingCommitments(context.Background())
			require.NoError(t, err)
			require.Len(t, ownedB, 1)
			assert.Equal(t, "sub-b", ownedB[0].Account)

			recs := []common.Recommendation{{Provider: common.ProviderAzure, Service: common.ServiceSavingsPlansAll, ResourceType: "Compute", Count: 1}}
			passed, filtered, err := recfilter.NewDuplicateChecker(24).AdjustRecommendationsForExisting(context.Background(), recs, coveringView{client("sub-a")})
			require.NoError(t, err)
			if tt.wantSuppress {
				assert.Empty(t, passed)
				assert.Equal(t, recs, filtered)
			} else {
				assert.Equal(t, recs, passed)
				assert.Empty(t, filtered)
			}
		})
	}
}

func mustConvertSavingsPlan(t *testing.T, sp *armbillingbenefits.SavingsPlanModel, sub string) common.Commitment {
	t.Helper()
	c, err := convertSavingsPlan(sp, sub)
	require.NoError(t, err)
	return *c
}
