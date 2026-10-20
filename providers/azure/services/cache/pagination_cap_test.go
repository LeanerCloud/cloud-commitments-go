package cache

import (
	"context"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/redis/armredis/v3"
	"github.com/stretchr/testify/assert"
)

// countingRedisPager reports More() until limit pages were requested, so a
// missing cap shows up as calls == limit instead of a hung test.
type countingRedisPager struct {
	calls, limit int
}

func (p *countingRedisPager) More() bool { return p.calls < p.limit }
func (p *countingRedisPager) NextPage(_ context.Context) (armredis.ClientListBySubscriptionResponse, error) {
	p.calls++
	return armredis.ClientListBySubscriptionResponse{}, nil
}

func TestFetchSKUCatalogue_PageCapStopsEndlessPager(t *testing.T) {
	t.Parallel()
	pager := &countingRedisPager{limit: 1000}
	client := NewClient(nil, "test-subscription", "eastus")
	client.SetRedisCachesPager(pager)

	assert.Nil(t, client.fetchSKUCatalogue(context.Background()))
	assert.Equal(t, maxCachesPages, pager.calls)
}

func TestFetchSKUCatalogue_CancelledContextStopsWalk(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	pager := &countingRedisPager{limit: 1000}
	client := NewClient(nil, "test-subscription", "eastus")
	client.SetRedisCachesPager(pager)

	assert.Nil(t, client.fetchSKUCatalogue(ctx))
	assert.Zero(t, pager.calls)
}

// finalPartialRedisPager serves exactly pages pages: each full page holds
// perPage distinct SKUs, the last holds a single one with no next link.
type finalPartialRedisPager struct {
	calls, pages, perPage int
}

func (p *finalPartialRedisPager) More() bool { return p.calls < p.pages }
func (p *finalPartialRedisPager) NextPage(_ context.Context) (armredis.ClientListBySubscriptionResponse, error) {
	n := p.perPage
	if p.calls == p.pages-1 {
		n = 1
	}
	var caches []*armredis.ResourceInfo
	for i := 0; i < n; i++ {
		name, family, capacity := armredis.SKUNamePremium, armredis.SKUFamilyP, int32(p.calls*p.perPage+i)
		caches = append(caches, &armredis.ResourceInfo{Properties: &armredis.Properties{
			SKU: &armredis.SKU{Name: &name, Family: &family, Capacity: &capacity},
		}})
	}
	p.calls++
	return armredis.ClientListBySubscriptionResponse{ListResult: armredis.ListResult{Value: caches}}, nil
}

func TestFetchSKUCatalogue_ExactlyCapPagesWithPartialLastPageSucceeds(t *testing.T) {
	t.Parallel()
	pager := &finalPartialRedisPager{pages: maxCachesPages, perPage: 2}
	client := NewClient(nil, "test-subscription", "eastus")
	client.SetRedisCachesPager(pager)

	got := client.fetchSKUCatalogue(context.Background())
	assert.Len(t, got, 2*(maxCachesPages-1)+1)
	assert.Equal(t, maxCachesPages, pager.calls)
}
