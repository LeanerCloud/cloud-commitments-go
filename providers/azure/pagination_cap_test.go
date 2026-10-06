package azure

import (
	"context"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/advisor/armadvisor"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/resources/armsubscriptions"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
)

// fakePager reports More() until limit pages were requested, so a missing cap
// shows up as calls == limit instead of a hung test. onCall runs before the
// n-th page is returned (1-based).
type fakePager[R any] struct {
	calls, limit int
	onCall       func(n int)
	page         func(n int) R
}

func (p *fakePager[R]) More() bool { return p.calls < p.limit }

func (p *fakePager[R]) NextPage(_ context.Context) (R, error) {
	p.calls++
	if p.onCall != nil {
		p.onCall(p.calls)
	}
	var zero R
	if p.page == nil {
		return zero, nil
	}
	return p.page(p.calls), nil
}

func cancelAtCall(cancel context.CancelFunc, at int) func(int) {
	return func(n int) {
		if n == at {
			cancel()
		}
	}
}

func subscriptionPage(n int) armsubscriptions.ClientListResponse {
	id, name := "sub-"+string(rune('a'+n)), "Sub"
	return armsubscriptions.ClientListResponse{SubscriptionListResult: armsubscriptions.SubscriptionListResult{
		Value: []*armsubscriptions.Subscription{{SubscriptionID: &id, DisplayName: &name}},
	}}
}

func locationPage(n int) armsubscriptions.ClientListLocationsResponse {
	name := "loc" + string(rune('a'+n))
	return armsubscriptions.ClientListLocationsResponse{LocationListResult: armsubscriptions.LocationListResult{
		Value: []*armsubscriptions.Location{{Name: &name}},
	}}
}

func providerWithSubscriptionPager(pager SubscriptionsPager) *Provider {
	p := &Provider{cred: &mockTokenCredential{}}
	p.SetSubscriptionsClient(&mockSubscriptionsClient{
		listPagerFunc: func(*armsubscriptions.ClientListOptions) SubscriptionsPager { return pager },
	})
	return p
}

func TestFetchAccounts_PageCap(t *testing.T) {
	clearAzureSubscriptionEnv(t)

	t.Run("endless pager errors at the cap", func(t *testing.T) {
		pager := &fakePager[armsubscriptions.ClientListResponse]{limit: 10000}
		_, err := providerWithSubscriptionPager(pager).fetchAccounts(context.Background())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "pagination cap")
		assert.Equal(t, maxSubscriptionPages, pager.calls)
	})

	t.Run("exactly cap pages succeed", func(t *testing.T) {
		pager := &fakePager[armsubscriptions.ClientListResponse]{limit: maxSubscriptionPages}
		_, err := providerWithSubscriptionPager(pager).fetchAccounts(context.Background())
		require.NoError(t, err)
		assert.Equal(t, maxSubscriptionPages, pager.calls)
	})

	t.Run("cancel mid-walk returns the context error", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		pager := &fakePager[armsubscriptions.ClientListResponse]{limit: 10000, onCall: cancelAtCall(cancel, 2)}
		_, err := providerWithSubscriptionPager(pager).fetchAccounts(ctx)
		require.ErrorIs(t, err, context.Canceled)
		assert.Equal(t, 2, pager.calls)
	})

	t.Run("multi-page walk collects every page", func(t *testing.T) {
		pager := &fakePager[armsubscriptions.ClientListResponse]{limit: 3, page: subscriptionPage}
		accounts, err := providerWithSubscriptionPager(pager).fetchAccounts(context.Background())
		require.NoError(t, err)
		assert.Len(t, accounts, 3)
	})
}

func providerWithLocationsPager(pager LocationsPager) *Provider {
	subID, subName := "test-subscription", "Test Sub"
	p := &Provider{cred: &mockTokenCredential{}}
	p.SetSubscriptionsClient(&mockSubscriptionsClient{
		listPagerFunc: func(*armsubscriptions.ClientListOptions) SubscriptionsPager {
			return &mockSubscriptionsPager{pages: []armsubscriptions.ClientListResponse{{
				SubscriptionListResult: armsubscriptions.SubscriptionListResult{
					Value: []*armsubscriptions.Subscription{{SubscriptionID: &subID, DisplayName: &subName}},
				},
			}}}
		},
		listLocationsPagerFunc: func(string, *armsubscriptions.ClientListLocationsOptions) LocationsPager { return pager },
	})
	return p
}

func TestGetRegions_PageCap(t *testing.T) {
	clearAzureSubscriptionEnv(t)

	t.Run("endless pager errors at the cap", func(t *testing.T) {
		pager := &fakePager[armsubscriptions.ClientListLocationsResponse]{limit: 10000}
		_, err := providerWithLocationsPager(pager).GetRegions(context.Background())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "pagination cap")
		assert.Equal(t, maxLocationsPages, pager.calls)
	})

	t.Run("exactly cap pages succeed", func(t *testing.T) {
		pager := &fakePager[armsubscriptions.ClientListLocationsResponse]{limit: maxLocationsPages}
		_, err := providerWithLocationsPager(pager).GetRegions(context.Background())
		require.NoError(t, err)
		assert.Equal(t, maxLocationsPages, pager.calls)
	})

	t.Run("cancel mid-walk returns the context error", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		pager := &fakePager[armsubscriptions.ClientListLocationsResponse]{limit: 10000, onCall: cancelAtCall(cancel, 2)}
		_, err := providerWithLocationsPager(pager).GetRegions(ctx)
		require.ErrorIs(t, err, context.Canceled)
		assert.Equal(t, 2, pager.calls)
	})

	t.Run("multi-page walk collects every page", func(t *testing.T) {
		pager := &fakePager[armsubscriptions.ClientListLocationsResponse]{limit: 3, page: locationPage}
		regions, err := providerWithLocationsPager(pager).GetRegions(context.Background())
		require.NoError(t, err)
		assert.Len(t, regions, 3)
	})
}

func advisorPage(n int) armadvisor.RecommendationsClientListResponse {
	field := "Microsoft.Compute/virtualMachines"
	id := "/subscriptions/123/resourceGroups/rg1/providers/Microsoft.Compute/virtualMachines/vm" + string(rune('a'+n))
	savings, currency := "1000.00", "USD"
	return armadvisor.RecommendationsClientListResponse{ResourceRecommendationBaseListResult: armadvisor.ResourceRecommendationBaseListResult{
		Value: []*armadvisor.ResourceRecommendationBase{{
			ID: &id,
			Properties: &armadvisor.RecommendationProperties{
				ImpactedField: &field,
				ExtendedProperties: map[string]*string{
					"annualSavingsAmount": &savings,
					"savingsCurrency":     &currency,
				},
			},
		}},
	}}
}

func TestCollectAdvisorRecommendations_PageCap(t *testing.T) {
	adapter := &RecommendationsClientAdapter{subscriptionID: "test-subscription"}
	params := common.RecommendationParams{}

	t.Run("endless pager stops at the cap keeping partial results", func(t *testing.T) {
		pager := &fakePager[armadvisor.RecommendationsClientListResponse]{limit: 10000, page: advisorPage}
		recs, err := adapter.collectAdvisorRecommendations(context.Background(), params, pager)
		require.NoError(t, err)
		assert.Equal(t, maxAdvisorPages, pager.calls)
		assert.NotEmpty(t, recs)
	})

	t.Run("exactly cap pages succeed", func(t *testing.T) {
		pager := &fakePager[armadvisor.RecommendationsClientListResponse]{limit: maxAdvisorPages}
		_, err := adapter.collectAdvisorRecommendations(context.Background(), params, pager)
		require.NoError(t, err)
		assert.Equal(t, maxAdvisorPages, pager.calls)
	})

	t.Run("cancel mid-walk returns the context error", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		pager := &fakePager[armadvisor.RecommendationsClientListResponse]{limit: 10000, onCall: cancelAtCall(cancel, 2)}
		recs, err := adapter.collectAdvisorRecommendations(ctx, params, pager)
		require.ErrorIs(t, err, context.Canceled)
		assert.Nil(t, recs)
		assert.Equal(t, 2, pager.calls)
	})

	t.Run("multi-page walk collects every page", func(t *testing.T) {
		one := &fakePager[armadvisor.RecommendationsClientListResponse]{limit: 1, page: advisorPage}
		oneRecs, err := adapter.collectAdvisorRecommendations(context.Background(), params, one)
		require.NoError(t, err)
		require.NotEmpty(t, oneRecs)

		three := &fakePager[armadvisor.RecommendationsClientListResponse]{limit: 3, page: advisorPage}
		threeRecs, err := adapter.collectAdvisorRecommendations(context.Background(), params, three)
		require.NoError(t, err)
		assert.Len(t, threeRecs, 3*len(oneRecs))
	})
}
