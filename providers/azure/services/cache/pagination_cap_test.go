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
	pager := &countingRedisPager{limit: 1000}
	client := NewClient(nil, "test-subscription", "eastus")
	client.SetRedisCachesPager(pager)

	assert.Nil(t, client.fetchSKUCatalogue(context.Background()))
	assert.Equal(t, maxCachesPages, pager.calls)
}

func TestFetchSKUCatalogue_CancelledContextStopsWalk(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	pager := &countingRedisPager{limit: 1000}
	client := NewClient(nil, "test-subscription", "eastus")
	client.SetRedisCachesPager(pager)

	assert.Nil(t, client.fetchSKUCatalogue(ctx))
	assert.Zero(t, pager.calls)
}
