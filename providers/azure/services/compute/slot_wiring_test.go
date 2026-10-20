package compute

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/compute/armcompute/v5"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/consumption/armconsumption"

	"github.com/LeanerCloud/cloud-commitments-go/providers/azure/internal/slot/slottest"
)

type checkingRecsPager struct {
	check func()
	done  bool
}

func (p *checkingRecsPager) More() bool { return !p.done }

func (p *checkingRecsPager) NextPage(context.Context) (armconsumption.ReservationRecommendationsClientListResponse, error) {
	p.check()
	p.done = true
	return armconsumption.ReservationRecommendationsClientListResponse{}, nil
}

type checkingSKUsPager struct {
	check func()
	done  bool
}

func (p *checkingSKUsPager) More() bool { return !p.done }

func (p *checkingSKUsPager) NextPage(context.Context) (armcompute.ResourceSKUsClientListResponse, error) {
	p.check()
	p.done = true
	return armcompute.ResourceSKUsClientListResponse{}, nil
}

type checkingHTTP struct{ check func() }

func (h checkingHTTP) Do(*http.Request) (*http.Response, error) {
	h.check()
	body := `{"Items":[],"NextPageLink":"","Count":0}`
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
}

func TestSlotWiring_HoldsSlot(t *testing.T) {
	cases := map[string]func(c *Client, p *slottest.Probe, ctx context.Context){
		"GetRecommendations pager": func(c *Client, p *slottest.Probe, ctx context.Context) {
			c.SetRecommendationsPager(&checkingRecsPager{check: p.Check})
			_, _ = c.GetRecommendations(ctx, nil)
		},
		"GetValidResourceTypes pager": func(c *Client, p *slottest.Probe, ctx context.Context) {
			c.SetResourceSKUsPager(&checkingSKUsPager{check: p.Check})
			_, _ = c.GetValidResourceTypes(ctx)
		},
		"fetchSKUCatalogue pager": func(c *Client, p *slottest.Probe, ctx context.Context) {
			c.SetResourceSKUsPager(&checkingSKUsPager{check: p.Check})
			c.cachedSKULookup(ctx, "Standard_D2s_v3")
		},
		"fetchAzurePricing http": func(c *Client, _ *slottest.Probe, ctx context.Context) {
			_, _ = c.fetchAzurePricing(ctx, "serviceName eq 'Virtual Machines'")
		},
		"getVMPricing http": func(c *Client, _ *slottest.Probe, ctx context.Context) {
			_, _ = c.getVMPricing(ctx, "Standard_D2s_v3", "eastus", 1)
		},
	}
	for name, drive := range cases {
		t.Run(name, func(t *testing.T) {
			p, ctx := slottest.New(t)
			c := NewClientWithHTTP(nil, "sub-1", "eastus", checkingHTTP{p.Check})
			drive(c, p, ctx)
			p.RequireCalled()
			p.Free()
		})
	}
}
