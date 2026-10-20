package cosmosdb

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/consumption/armconsumption"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/cosmos/armcosmos/v2"

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
			c.SetCosmosAccountsPager(&onePager[armcosmos.DatabaseAccountsClientListResponse]{probe: p})
			_, _ = c.GetValidResourceTypes(ctx)
		},
		"fetchDominantAPIType": func(ctx context.Context, p *slottest.Probe, c *Client) {
			c.SetCosmosAccountsPager(&onePager[armcosmos.DatabaseAccountsClientListResponse]{probe: p})
			c.fetchDominantAPIType(ctx)
		},
		"fetchAzurePricing": func(ctx context.Context, p *slottest.Probe, c *Client) {
			c.httpClient = probeHTTP{p}
			_, _ = c.fetchAzurePricing(ctx, "serviceName eq 'Azure Cosmos DB'")
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
