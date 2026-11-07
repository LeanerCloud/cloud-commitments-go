package cache

import (
	"context"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/redis/armredis/v3"
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
