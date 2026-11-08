package reservationstest

import (
	"context"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/reservations/armreservations"
	"github.com/stretchr/testify/assert"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
)

// OrderPath is the reservation order ID the fakes below serve.
const OrderPath = "/providers/Microsoft.Capacity/reservationOrders/order-1"

// LiveRow is a complete live reservation of resourceType: Shared scope billed
// to subscriptionID, Succeeded, purchased today and expiring next year.
func LiveRow(resourceType armreservations.ReservedResourceType, subscriptionID, id, sku, location string, qty int32) *armreservations.ReservationResponse {
	shared := armreservations.AppliedScopeTypeShared
	succeeded := armreservations.ProvisioningStateSucceeded
	purchased := time.Now().UTC().Truncate(24 * time.Hour)
	expiry := purchased.AddDate(1, 0, 0)
	billing := "/subscriptions/" + subscriptionID
	path := OrderPath + "/reservations/" + id
	return &armreservations.ReservationResponse{
		ID:       &path,
		Location: &location,
		SKU:      &armreservations.SKUName{Name: &sku},
		Properties: &armreservations.Properties{
			ReservedResourceType: &resourceType,
			AppliedScopeType:     &shared,
			BillingScopeID:       &billing,
			ProvisioningState:    &succeeded,
			Quantity:             &qty,
			PurchaseDate:         &purchased,
			ExpiryDate:           &expiry,
		},
	}
}

// AppliedLister serves OrderPath as the only applied order, or Err.
type AppliedLister struct {
	Err error
}

func (l AppliedLister) GetAppliedReservationList(context.Context, string, *armreservations.AzureReservationAPIClientGetAppliedReservationListOptions) (armreservations.AzureReservationAPIClientGetAppliedReservationListResponse, error) {
	if l.Err != nil {
		return armreservations.AzureReservationAPIClientGetAppliedReservationListResponse{}, l.Err
	}
	order := OrderPath
	return armreservations.AzureReservationAPIClientGetAppliedReservationListResponse{
		AppliedReservations: armreservations.AppliedReservations{
			Properties: &armreservations.AppliedReservationsProperties{
				ReservationOrderIDs: &armreservations.AppliedReservationList{Value: []*string{&order}},
			},
		},
	}, nil
}

// OrderPager is a single-page pager over Rows, or Err on the page fetch.
type OrderPager struct {
	Err  error
	Rows []*armreservations.ReservationResponse
	done bool
}

func (p *OrderPager) More() bool { return !p.done }

func (p *OrderPager) NextPage(context.Context) (armreservations.ReservationClientListResponse, error) {
	p.done = true
	if p.Err != nil {
		return armreservations.ReservationClientListResponse{}, p.Err
	}
	return armreservations.ReservationClientListResponse{ReservationList: armreservations.ReservationList{Value: p.Rows}}, nil
}

// CountingPager reports More until Limit pages were requested, so a missing
// pagination cap shows up as Calls == Limit instead of a hung test.
type CountingPager struct {
	Calls, Limit int
}

func (p *CountingPager) More() bool { return p.Calls < p.Limit }

func (p *CountingPager) NextPage(context.Context) (armreservations.ReservationClientListResponse, error) {
	p.Calls++
	return armreservations.ReservationClientListResponse{}, nil
}

// CancelOnSecondCallPager never ends and calls Cancel while serving its
// second page.
type CancelOnSecondCallPager struct {
	Cancel context.CancelFunc
	Calls  int
}

func (p *CancelOnSecondCallPager) More() bool { return true }

func (p *CancelOnSecondCallPager) NextPage(context.Context) (armreservations.ReservationClientListResponse, error) {
	p.Calls++
	if p.Calls == 2 {
		p.Cancel()
	}
	return armreservations.ReservationClientListResponse{}, nil
}

// AssertUnknownInventory asserts that c carries only what the consumption
// ReservationDetail API reports: no region, state, price, quantity or start
// date. Producers built on that API must not invent these from the client.
func AssertUnknownInventory(t *testing.T, c common.Commitment) {
	t.Helper()
	assert.Empty(t, c.Region, "region is not in ReservationDetail")
	assert.Empty(t, c.State, "state is not in ReservationDetail")
	assert.False(t, c.CostKnown)
	assert.Zero(t, c.Cost)
	assert.Zero(t, c.Count)
	assert.True(t, c.StartDate.IsZero())
}
