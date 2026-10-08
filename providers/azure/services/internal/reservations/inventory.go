package reservations

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/reservations/armreservations"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
)

// AppliedReservationsLister wraps AzureReservationAPIClient.GetAppliedReservationList,
// which returns the reservation ORDER IDs whose benefits apply to a given
// subscription (Single-scope reservations scoped to it plus Shared-scope
// reservations in its billing scope). That benefit-scope semantics is exactly
// what reservation inventory needs; the tenant-wide ReservationClient.NewListAllPager
// and the per-reservation BillingScopeID field do not answer this question.
type AppliedReservationsLister interface {
	GetAppliedReservationList(ctx context.Context, subscriptionID string, options *armreservations.AzureReservationAPIClientGetAppliedReservationListOptions) (armreservations.AzureReservationAPIClientGetAppliedReservationListResponse, error)
}

// OrderReservationsPager pages through the child reservations of one
// reservation order (ReservationClient.NewListPager).
type OrderReservationsPager interface {
	More() bool
	NextPage(ctx context.Context) (armreservations.ReservationClientListResponse, error)
}

// InventoryFactories constructs the SDK-facing pieces of the reservation
// inventory path. Tests inject their own factories to run hermetically
// without Azure credentials.
type InventoryFactories struct {
	NewAppliedLister func() (AppliedReservationsLister, error)
	NewOrderPager    func(reservationOrderID string) OrderReservationsPager
}

// failedOrderPager surfaces a ReservationClient construction error on first
// use instead of swallowing it at factory time: More reports a pending page
// so the caller calls NextPage once and gets the wrapped constructor error.
type failedOrderPager struct {
	err error
}

// More always reports a pending page so NextPage is attempted.
func (p failedOrderPager) More() bool { return true }

// NextPage returns the deferred constructor error.
func (p failedOrderPager) NextPage(context.Context) (armreservations.ReservationClientListResponse, error) {
	return armreservations.ReservationClientListResponse{}, p.err
}

// DefaultInventoryFactories returns factories backed by the real
// armreservations SDK clients.
func DefaultInventoryFactories(cred azcore.TokenCredential) InventoryFactories {
	return InventoryFactories{
		NewAppliedLister: func() (AppliedReservationsLister, error) {
			client, err := armreservations.NewAzureReservationAPIClient(cred, nil)
			if err != nil {
				return nil, fmt.Errorf("create applied reservations client: %w", err)
			}
			return client, nil
		},
		NewOrderPager: func(reservationOrderID string) OrderReservationsPager {
			client, err := armreservations.NewReservationClient(cred, nil)
			if err != nil {
				return failedOrderPager{err: fmt.Errorf("create reservations client: %w", err)}
			}
			return client.NewListPager(reservationOrderID, nil)
		},
	}
}

// ListAppliedReservations returns every child reservation whose benefits apply
// to subscriptionID, by listing the applied reservation orders and paging each
// order's reservations. maxPagesPerOrder caps per-order pagination.
//
// Any failure (lister construction, applied-list call, truncated order list,
// page fetch, cap hit) is returned as an error: a partial inventory is unsafe
// for the duplicate-purchase guard, which would treat reservations it failed
// to load as nonexistent and could buy duplicates.
func ListAppliedReservations(ctx context.Context, subscriptionID string, f InventoryFactories, maxPagesPerOrder int) ([]*armreservations.ReservationResponse, error) {
	lister, err := f.NewAppliedLister()
	if err != nil {
		return nil, fmt.Errorf("create applied reservations lister: %w", err)
	}
	applied, err := lister.GetAppliedReservationList(ctx, subscriptionID, nil)
	if err != nil {
		return nil, fmt.Errorf("list applied reservations for subscription %s: %w", subscriptionID, err)
	}

	orderIDs, err := appliedOrderIDs(applied)
	if err != nil {
		return nil, err
	}

	reservations := make([]*armreservations.ReservationResponse, 0)
	for _, orderID := range orderIDs {
		pager := f.NewOrderPager(orderID)
		for pageIdx := 0; pager.More(); pageIdx++ {
			if err := ctx.Err(); err != nil {
				return nil, fmt.Errorf("context canceled during reservation listing for order %s: %w", orderID, err)
			}
			if pageIdx >= maxPagesPerOrder {
				return nil, fmt.Errorf("reservation listing for order %s exceeded pagination cap (%d pages)", orderID, maxPagesPerOrder)
			}
			page, err := pager.NextPage(ctx)
			if err != nil {
				return nil, fmt.Errorf("list reservations for order %s: %w", orderID, err)
			}
			eligible, err := filterAppliedReservations(page.Value, subscriptionID)
			if err != nil {
				return nil, fmt.Errorf("reservation in order %s: %w", orderID, err)
			}
			reservations = append(reservations, eligible...)
		}
	}
	return reservations, nil
}

func filterAppliedReservations(page []*armreservations.ReservationResponse, subscriptionID string) ([]*armreservations.ReservationResponse, error) {
	eligible := make([]*armreservations.ReservationResponse, 0, len(page))
	for _, reservation := range page {
		if reservation == nil || reservation.Properties == nil {
			eligible = append(eligible, reservation)
			continue
		}
		if reservation.Properties.AppliedScopeType == nil || *reservation.Properties.AppliedScopeType != armreservations.AppliedScopeTypeSingle {
			eligible = append(eligible, reservation)
			continue
		}
		matches, err := singleScopeMatchesSubscription(reservation.Properties.AppliedScopes, subscriptionID)
		if err != nil {
			return nil, err
		}
		if matches {
			eligible = append(eligible, reservation)
		}
	}
	return eligible, nil
}

func singleScopeMatchesSubscription(scopes []*string, subscriptionID string) (bool, error) {
	if len(scopes) != 1 {
		return false, fmt.Errorf("single scope requires exactly one applied scope, got %d", len(scopes))
	}
	raw := scopes[0]
	if raw == nil || strings.TrimSpace(*raw) == "" {
		return false, fmt.Errorf("single scope has an empty applied scope")
	}
	scopeSubscription, err := subscriptionFromAppliedScope(*raw)
	if err != nil {
		return false, err
	}
	if strings.IndexFunc(scopeSubscription, unicode.IsSpace) >= 0 {
		return false, fmt.Errorf("invalid applied scope %q", *raw)
	}
	return strings.EqualFold(scopeSubscription, subscriptionID), nil
}

func subscriptionFromAppliedScope(scope string) (string, error) {
	if !strings.HasPrefix(scope, "/") {
		return "", fmt.Errorf("invalid applied scope %q", scope)
	}
	segments := strings.Split(scope, "/")
	if len(segments) != 3 && len(segments) != 5 {
		return "", fmt.Errorf("invalid applied scope %q", scope)
	}
	if !strings.EqualFold(segments[1], "subscriptions") || segments[2] == "" {
		return "", fmt.Errorf("invalid applied scope %q", scope)
	}
	if len(segments) == 5 && (!strings.EqualFold(segments[3], "resourceGroups") || segments[4] == "") {
		return "", fmt.Errorf("invalid applied scope %q", scope)
	}
	return segments[2], nil
}

// appliedOrderIDs extracts the reservation order IDs from an applied-list
// response. Order IDs arrive as full resource paths
// (/providers/Microsoft.Capacity/reservationOrders/{guid}); only the last
// segment is kept. Empty and duplicate IDs are skipped. A non-empty NextLink
// means the order list itself is truncated, which is an unsafe partial
// inventory for duplicate-purchase protection, so it is an error.
func appliedOrderIDs(resp armreservations.AzureReservationAPIClientGetAppliedReservationListResponse) ([]string, error) {
	if resp.Properties == nil || resp.Properties.ReservationOrderIDs == nil {
		return nil, nil
	}
	list := resp.Properties.ReservationOrderIDs
	if list.NextLink != nil && *list.NextLink != "" {
		return nil, fmt.Errorf("applied reservation order list is truncated (nextLink present); refusing to build a partial inventory")
	}
	seen := make(map[string]bool)
	orderIDs := make([]string, 0, len(list.Value))
	for _, raw := range list.Value {
		if raw == nil {
			continue
		}
		segments := strings.Split(strings.TrimSuffix(*raw, "/"), "/")
		id := segments[len(segments)-1]
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		orderIDs = append(orderIDs, id)
	}
	return orderIDs, nil
}

// CommitmentFromReservation converts one armreservations.ReservationResponse
// into a common.Commitment for the given service. It returns nil when r or
// its Properties are nil, or when the reservation's ReservedResourceType is
// not wantType (e.g. compute keeps only VirtualMachines reservations).
//
// StartDate is pinned to Properties.PurchaseDate, NEVER EffectiveDateTime:
// EffectiveDateTime is the start of the current reservation revision and
// moves forward on exchanges and splits, while the duplicate-purchase guard's
// recent-purchase cutoff (pkg/recfilter) needs the original purchase date.
// The SDK exposes PurchaseDate at day precision, so this cannot establish
// whether a purchase yesterday occurred within the last 24 hours.
//
// Cost stays at zero. The reservations API carries no per-reservation price,
// so zero here means "unknown", not "free". The shared common.Commitment.Cost
// scalar has no absent-value representation (tracked by issue #62); the only
// in-repo consumer of GetExistingCommitments (the pkg/recfilter
// DuplicateChecker) never reads Cost, so the zero value is harmless there.
func CommitmentFromReservation(r *armreservations.ReservationResponse, account string, service common.ServiceType, wantType armreservations.ReservedResourceType, now time.Time) *common.Commitment {
	if r == nil || r.Properties == nil {
		return nil
	}
	props := r.Properties
	if props.ReservedResourceType == nil || *props.ReservedResourceType != wantType {
		return nil
	}

	commitment := &common.Commitment{
		Provider:       common.ProviderAzure,
		Account:        account,
		CommitmentType: common.CommitmentReservedInstance,
		Service:        service,
		State:          commitmentStateFromProvisioning(props.ProvisioningState, props.ExpiryDate, now),
	}
	populateCommitmentFields(commitment, r)
	return commitment
}

// populateCommitmentFields copies the authoritative reservation fields into
// the commitment. StartDate is pinned to Properties.PurchaseDate, never
// EffectiveDateTime.
func populateCommitmentFields(commitment *common.Commitment, r *armreservations.ReservationResponse) {
	props := r.Properties
	if r.ID != nil {
		commitment.CommitmentID = *r.ID
	}
	if r.Location != nil {
		commitment.Region = *r.Location
	}
	if r.SKU != nil && r.SKU.Name != nil {
		commitment.ResourceType = *r.SKU.Name
	}
	if props.Quantity != nil {
		commitment.Count = int(*props.Quantity)
	}
	if props.PurchaseDate != nil {
		commitment.StartDate = *props.PurchaseDate
	}
	if props.ExpiryDate != nil {
		commitment.EndDate = *props.ExpiryDate
	}
}

// commitmentStateFromProvisioning maps armreservations.ProvisioningState onto
// the provider-neutral commitment lifecycle. Succeeded maps to active, with a
// defensive terminal check: a Succeeded reservation whose ExpiryDate is in
// the past maps to expired.
//
// A nil or unrecognized state maps to "" (empty), never to a fabricated
// "active": pkg/recfilter's isRecentActiveCommitment treats an unrecognized
// state as owned, which is the conservative direction: wrongly skipping a
// purchase is recoverable, a duplicate commitment is not.
func commitmentStateFromProvisioning(state *armreservations.ProvisioningState, expiry *time.Time, now time.Time) common.CommitmentState {
	if state == nil {
		return ""
	}
	switch *state {
	case armreservations.ProvisioningStateSucceeded:
		if expiry != nil && !expiry.After(now) {
			return common.CommitmentStateExpired
		}
		return common.CommitmentStateActive
	case armreservations.ProvisioningStateCreating,
		armreservations.ProvisioningStateCreated,
		armreservations.ProvisioningStatePendingBilling,
		armreservations.ProvisioningStateConfirmedBilling,
		armreservations.ProvisioningStatePendingResourceHold,
		armreservations.ProvisioningStateConfirmedResourceHold:
		return common.CommitmentStatePaymentPending
	case armreservations.ProvisioningStateCancelled:
		return common.CommitmentStateCanceled
	case armreservations.ProvisioningStateExpired:
		return common.CommitmentStateExpired
	case armreservations.ProvisioningStateFailed,
		armreservations.ProvisioningStateBillingFailed:
		return common.CommitmentStateFailed
	case armreservations.ProvisioningStateSplit,
		armreservations.ProvisioningStateMerged:
		return common.CommitmentStateRetired
	default:
		return ""
	}
}
