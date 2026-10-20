package reservations

import (
	"context"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/reservations/armreservations"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-go/providers/azure/internal/slot/slottest"
)

type checkingLister struct{ check func() }

func (l checkingLister) GetAppliedReservationList(context.Context, string, *armreservations.AzureReservationAPIClientGetAppliedReservationListOptions) (armreservations.AzureReservationAPIClientGetAppliedReservationListResponse, error) {
	l.check()
	return appliedListResponse([]*string{strPtr("order-a")}, nil), nil
}

type checkingOrderPager struct {
	check func()
	done  bool
}

func (p *checkingOrderPager) More() bool { return !p.done }

func (p *checkingOrderPager) NextPage(context.Context) (armreservations.ReservationClientListResponse, error) {
	p.check()
	p.done = true
	return armreservations.ReservationClientListResponse{}, nil
}

func TestListAppliedReservations_HoldsSlot(t *testing.T) {
	var lister, order int
	p, ctx := slottest.New(t)
	f := InventoryFactories{
		NewAppliedLister: func() (AppliedReservationsLister, error) {
			return checkingLister{func() { lister++; p.Check() }}, nil
		},
		NewOrderPager: func(string) OrderReservationsPager {
			return &checkingOrderPager{check: func() { order++; p.Check() }}
		},
	}
	_, err := ListAppliedReservations(ctx, "sub", f, 5)
	require.NoError(t, err)
	require.Equal(t, 1, lister)
	require.Equal(t, 1, order)
	p.Free()
}
