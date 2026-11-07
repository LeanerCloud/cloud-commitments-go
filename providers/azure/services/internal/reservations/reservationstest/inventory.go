package reservationstest

import (
	"context"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/reservations/armreservations"
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
	Rows []*armreservations.ReservationResponse
	Err  error
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
