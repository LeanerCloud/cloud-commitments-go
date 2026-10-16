package azure

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/consumption/armconsumption"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// boundedPager reports More() forever but errors after ceiling NextPage calls,
// so a missing cap fails fast instead of hanging. onPage runs before each page.
type boundedPager struct {
	calls   int
	ceiling int
	onPage  func(call int)
}

func (p *boundedPager) More() bool { return true }

func (p *boundedPager) NextPage(context.Context) (armconsumption.ReservationsSummariesClientListResponse, error) {
	p.calls++
	if p.calls > p.ceiling {
		return armconsumption.ReservationsSummariesClientListResponse{}, fmt.Errorf("mock ceiling: more than %d calls, pagination is unbounded", p.ceiling)
	}
	if p.onPage != nil {
		p.onPage(p.calls)
	}
	id := "res-1"
	return armconsumption.ReservationsSummariesClientListResponse{
		ReservationSummariesListResult: armconsumption.ReservationSummariesListResult{
			Value: []*armconsumption.ReservationSummary{{Properties: &armconsumption.ReservationSummaryProperties{ReservationID: &id}}},
		},
	}, nil
}

func TestGetRIUtilization_EndlessPagerHitsCap(t *testing.T) {
	pager := &boundedPager{ceiling: 500}
	adapter := &RecommendationsClientAdapter{subscriptionID: "sub-cap"}

	res, err := adapter.getRIUtilizationViaAPI(context.Background(), &mockReservationsSummariesAPI{pager: pager}, "2026-01-01", "2026-02-01")
	require.Error(t, err)
	assert.Nil(t, res, "a capped walk must not return partial results")
	assert.Contains(t, err.Error(), "pagination cap (50 pages)")
	assert.Equal(t, 50, pager.calls)
}

func TestGetRIUtilization_CanceledMidWalkReturnsCtxErr(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pager := &boundedPager{ceiling: 500, onPage: func(call int) {
		if call == 2 {
			cancel()
		}
	}}
	adapter := &RecommendationsClientAdapter{subscriptionID: "sub-cancel"}

	res, err := adapter.getRIUtilizationViaAPI(ctx, &mockReservationsSummariesAPI{pager: pager}, "2026-01-01", "2026-02-01")
	require.Error(t, err)
	assert.Nil(t, res)
	assert.True(t, errors.Is(err, context.Canceled), "got %v", err)
	assert.Equal(t, 2, pager.calls, "no page may be fetched after cancellation")
}
