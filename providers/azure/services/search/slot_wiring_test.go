package search

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/consumption/armconsumption"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/search/armsearch"

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
	resv := func(p *slottest.Probe) *slotPager[armconsumption.ReservationsDetailsClientListResponse] {
		return &slotPager[armconsumption.ReservationsDetailsClientListResponse]{p: p}
	}
	cases := []struct {
		name string
		call func(ctx context.Context, p *slottest.Probe)
	}{
		{"reservations", func(ctx context.Context, p *slottest.Probe) {
			_, _ = (&Client{reservationsPager: resv(p)}).GetExistingCommitments(ctx)
		}},
		{"valid resource types", func(ctx context.Context, p *slottest.Probe) {
			_, _ = (&Client{searchServicesPager: &slotPager[armsearch.ServicesClientListBySubscriptionResponse]{p: p}}).GetValidResourceTypes(ctx)
		}},
		{"retail pricing", func(ctx context.Context, p *slottest.Probe) {
			_, _ = (&Client{httpClient: slotHTTP{p}}).getSearchPricing(ctx, "basic", "eastus", 1)
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
