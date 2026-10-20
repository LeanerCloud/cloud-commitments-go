package cache

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/consumption/armconsumption"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/redis/armredis/v3"

	"github.com/LeanerCloud/cloud-commitments-go/providers/azure/internal/slot/slottest"
)

// slotPager is a one-page fake pager whose NextPage reports whether the shared
// slot is held.
type slotPager[R any] struct {
	p    *slottest.Probe
	done bool
}

func (s *slotPager[R]) More() bool { return !s.done }
func (s *slotPager[R]) NextPage(context.Context) (R, error) {
	s.p.Check()
	s.done = true
	var z R
	return z, nil
}

type slotHTTP struct{ p *slottest.Probe }

func (h slotHTTP) Do(*http.Request) (*http.Response, error) {
	h.p.Check()
	return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"Items":[]}`))}, nil
}

func TestSlotHeldDuringCalls(t *testing.T) {
	t.Parallel()
	recs := func(p *slottest.Probe) *slotPager[armconsumption.ReservationRecommendationsClientListResponse] {
		return &slotPager[armconsumption.ReservationRecommendationsClientListResponse]{p: p}
	}
	resv := func(p *slottest.Probe) *slotPager[armconsumption.ReservationsDetailsClientListResponse] {
		return &slotPager[armconsumption.ReservationsDetailsClientListResponse]{p: p}
	}
	cases := []struct {
		name string
		call func(ctx context.Context, p *slottest.Probe)
	}{
		{"recommendations", func(ctx context.Context, p *slottest.Probe) {
			_, _ = (&Client{recommendationsPager: recs(p)}).GetRecommendations(ctx, nil)
		}},
		{"reservations", func(ctx context.Context, p *slottest.Probe) {
			_, _ = (&Client{reservationsPager: resv(p)}).GetExistingCommitments(ctx)
		}},
		{"valid resource types", func(ctx context.Context, p *slottest.Probe) {
			_, _ = (&Client{redisCachesPager: &slotPager[armredis.ClientListBySubscriptionResponse]{p: p}}).GetValidResourceTypes(ctx)
		}},
		{"sku catalog", func(ctx context.Context, p *slottest.Probe) {
			(&Client{region: "eastus", redisCachesPager: &slotPager[armredis.ClientListBySubscriptionResponse]{p: p}}).fetchSKUCatalogue(ctx)
		}},
		{"retail pricing", func(ctx context.Context, p *slottest.Probe) {
			_, _ = (&Client{httpClient: slotHTTP{p}}).getRedisPricing(ctx, "Premium_P1", "eastus", 1)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, ctx := slottest.New(t)
			tc.call(ctx, p)
			p.RequireCalled()
			p.Free()
		})
	}
}
