package managedredis

import (
	"context"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/redis/armredis/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeCachesPager reports More() until limit pages were requested, so a
// missing cap shows up as calls == limit instead of a hung test. onCall runs
// before the n-th page is returned (1-based).
type fakeCachesPager struct {
	calls, limit int
	onCall       func(n int)
	page         func(n int) armredis.ClientListBySubscriptionResponse
}

func (p *fakeCachesPager) More() bool { return p.calls < p.limit }

func (p *fakeCachesPager) NextPage(_ context.Context) (armredis.ClientListBySubscriptionResponse, error) {
	p.calls++
	if p.onCall != nil {
		p.onCall(p.calls)
	}
	if p.page == nil {
		return armredis.ClientListBySubscriptionResponse{}, nil
	}
	return p.page(p.calls), nil
}

// capacityPage returns one cache whose capacity is the page number, so each
// page contributes a distinct SKU.
func capacityPage(n int) armredis.ClientListBySubscriptionResponse {
	name := armredis.SKUNamePremium
	family := armredis.SKUFamilyP
	capacity := int32(n)
	return armredis.ClientListBySubscriptionResponse{ListResult: armredis.ListResult{
		Value: []*armredis.ResourceInfo{{Properties: &armredis.Properties{
			SKU: &armredis.SKU{Name: &name, Family: &family, Capacity: &capacity},
		}}},
	}}
}

func TestCollectSKUsFromPager_PageCap(t *testing.T) {
	t.Run("endless pager errors at the cap", func(t *testing.T) {
		pager := &fakeCachesPager{limit: 10000}
		skus, err := collectSKUsFromPager(context.Background(), pager)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "pagination cap")
		assert.Nil(t, skus)
		assert.Equal(t, maxCachesPages, pager.calls)
	})

	t.Run("exactly cap pages succeed", func(t *testing.T) {
		pager := &fakeCachesPager{limit: maxCachesPages}
		_, err := collectSKUsFromPager(context.Background(), pager)
		require.NoError(t, err)
		assert.Equal(t, maxCachesPages, pager.calls)
	})

	t.Run("cancel mid-walk returns the context error", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		pager := &fakeCachesPager{limit: 10000, onCall: func(n int) {
			if n == 2 {
				cancel()
			}
		}}
		skus, err := collectSKUsFromPager(ctx, pager)
		require.ErrorIs(t, err, context.Canceled)
		assert.Nil(t, skus)
		assert.Equal(t, 2, pager.calls)
	})

	t.Run("multi-page walk collects every page", func(t *testing.T) {
		pager := &fakeCachesPager{limit: 3, page: capacityPage}
		skus, err := collectSKUsFromPager(context.Background(), pager)
		require.NoError(t, err)
		assert.Equal(t, map[string]bool{"Premium_P1": true, "Premium_P2": true, "Premium_P3": true}, skus)
	})
}
