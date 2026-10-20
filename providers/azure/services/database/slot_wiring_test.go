package database

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/consumption/armconsumption"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/sql/armsql"

	"github.com/LeanerCloud/cloud-commitments-go/providers/azure/internal/slot/slottest"
)

// onePager yields a single empty page and asserts the slot is held while
// that page is fetched.
type onePager[T any] struct {
	probe *slottest.Probe
	done  bool
}

func (p *onePager[T]) More() bool { return !p.done }
func (p *onePager[T]) NextPage(context.Context) (T, error) {
	p.done = true
	p.probe.Check()
	var zero T
	return zero, nil
}

type probeCaps struct{ probe *slottest.Probe }

func (c probeCaps) ListByLocation(context.Context, string, *armsql.CapabilitiesClientListByLocationOptions) (armsql.CapabilitiesClientListByLocationResponse, error) {
	c.probe.Check()
	return armsql.CapabilitiesClientListByLocationResponse{}, nil
}

type probeHTTP struct{ probe *slottest.Probe }

func (h probeHTTP) Do(*http.Request) (*http.Response, error) {
	h.probe.Check()
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"Items":[]}`))}, nil
}

func TestClientCallsHoldSharedSlot(t *testing.T) {
	cases := map[string]func(ctx context.Context, p *slottest.Probe, c *Client){
		"GetRecommendations": func(ctx context.Context, p *slottest.Probe, c *Client) {
			c.SetRecommendationsPager(&onePager[armconsumption.ReservationRecommendationsClientListResponse]{probe: p})
			_, _ = c.GetRecommendations(ctx, nil)
		},
		"GetExistingCommitments": func(ctx context.Context, p *slottest.Probe, c *Client) {
			c.SetReservationsPager(&onePager[armconsumption.ReservationsDetailsClientListResponse]{probe: p})
			_, _ = c.GetExistingCommitments(ctx)
		},
		"GetValidResourceTypes": func(ctx context.Context, p *slottest.Probe, c *Client) {
			c.SetCapabilitiesClient(probeCaps{p})
			_, _ = c.GetValidResourceTypes(ctx)
		},
		"fetchSKUCatalogue": func(ctx context.Context, p *slottest.Probe, c *Client) {
			c.SetCapabilitiesClient(probeCaps{p})
			c.fetchSKUCatalogue(ctx)
		},
		"walkManagedInstances": func(ctx context.Context, p *slottest.Probe, c *Client) {
			c.SetManagedInstancesPager(&onePager[armsql.ManagedInstancesClientListResponse]{probe: p})
			c.walkManagedInstances(ctx)
		},
		"hasRegularServers": func(ctx context.Context, p *slottest.Probe, c *Client) {
			c.SetServersPager(&onePager[armsql.ServersClientListResponse]{probe: p})
			c.hasRegularServers(ctx)
		},
		"fetchAzurePricing": func(ctx context.Context, p *slottest.Probe, c *Client) {
			c.httpClient = probeHTTP{p}
			_, _ = c.fetchAzurePricing(ctx, "serviceName eq 'SQL Database'")
		},
	}
	for name, drive := range cases {
		t.Run(name, func(t *testing.T) {
			p, ctx := slottest.New(t)
			drive(ctx, p, NewClient(nil, "sub", "eastus"))
			p.RequireCalled()
			p.Free()
		})
	}
}
