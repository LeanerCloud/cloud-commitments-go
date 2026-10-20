package savingsplans

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/billingbenefits/armbillingbenefits"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/providers/azure/internal/slot/slottest"
)

type checkingHTTP struct{ check func() }

func (h checkingHTTP) Do(*http.Request) (*http.Response, error) {
	h.check()
	body := `{"value":[],"Items":[],"NextPageLink":"","Count":0}`
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
}

type checkingListAllPager struct {
	check func()
	done  bool
}

func (p *checkingListAllPager) More() bool { return !p.done }

func (p *checkingListAllPager) NextPage(context.Context) (armbillingbenefits.SavingsPlanClientListAllResponse, error) {
	p.check()
	p.done = true
	return armbillingbenefits.SavingsPlanClientListAllResponse{}, nil
}

func TestSlotWiring_HoldsSlot(t *testing.T) {
	cases := map[string]func(c *Client, p *slottest.Probe, ctx context.Context){
		"GetExistingCommitments": func(c *Client, p *slottest.Probe, ctx context.Context) {
			c.SetListAllPager(&checkingListAllPager{check: p.Check})
			_, _ = c.GetExistingCommitments(ctx)
		},
		"benefit recommendations": func(c *Client, _ *slottest.Probe, ctx context.Context) {
			_, _ = c.GetRecommendations(ctx, &common.RecommendationParams{Term: "1yr"})
		},
		"fetchOnDemandRate": func(c *Client, _ *slottest.Probe, ctx context.Context) {
			_, _ = c.fetchOnDemandRate(ctx, "Compute_Savings_Plan")
		},
	}
	for name, drive := range cases {
		t.Run(name, func(t *testing.T) {
			p, ctx := slottest.New(t)
			c := NewClientWithHTTP(fakeCred{}, "sub-1", "", checkingHTTP{p.Check})
			drive(c, p, ctx)
			p.RequireCalled()
			p.Free()
		})
	}
}
