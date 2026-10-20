package azure

import (
	"context"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/advisor/armadvisor"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/providers/azure/internal/slot/slottest"
)

type advisorProbePager struct {
	probe *slottest.Probe
	done  bool
}

func (p *advisorProbePager) More() bool { return !p.done }
func (p *advisorProbePager) NextPage(context.Context) (armadvisor.RecommendationsClientListResponse, error) {
	p.done = true
	p.probe.Check()
	return armadvisor.RecommendationsClientListResponse{}, nil
}

func TestCollectAdvisorRecommendationsHoldsSharedSlot(t *testing.T) {
	t.Parallel()
	p, ctx := slottest.New(t)
	a := &RecommendationsClientAdapter{subscriptionID: "sub"}
	_, _ = a.collectAdvisorRecommendations(ctx, common.RecommendationParams{}, &advisorProbePager{probe: p})
	p.RequireCalled()
	p.Free()
}
