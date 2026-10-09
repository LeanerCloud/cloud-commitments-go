package reservations

import (
	"fmt"
	"strings"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/reservations/armreservations"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
)

// StrictLabels name the service and reserved resource in StrictCommitment
// errors, e.g. {Service: "compute", Resource: "VM"}.
type StrictLabels struct {
	Service  string
	Resource string
}

// StrictCommitment validates and converts one reservation, failing closed
// instead of dropping or zero-counting incomplete inventory. It returns a nil
// commitment for reservations of another type.
func StrictCommitment(r *armreservations.ReservationResponse, subscriptionID string, service common.ServiceType, wantType armreservations.ReservedResourceType, labels StrictLabels, now time.Time) (*common.Commitment, error) {
	// filterAppliedReservations passes nil and property-less rows through;
	// dropping them would hide a possibly just-bought reservation.
	if r == nil || r.Properties == nil || r.Properties.ReservedResourceType == nil {
		return nil, fmt.Errorf("%s: cannot classify reservation %s in subscription %q: missing properties or reservedResourceType",
			labels.Service, reservationLabel(r), subscriptionID)
	}
	commitment := CommitmentFromReservation(r, subscriptionID, service, wantType, now)
	if commitment == nil {
		return nil, nil
	}
	if err := validateReservationScope(r.Properties, commitment.CommitmentID, subscriptionID, labels, now); err != nil {
		return nil, err
	}
	if !isTerminalCommitmentState(commitment.State) {
		if err := validateLiveReservationFields(r, commitment.CommitmentID, subscriptionID, labels); err != nil {
			return nil, err
		}
	}
	return commitment, nil
}

func reservationLabel(r *armreservations.ReservationResponse) string {
	if r == nil || r.ID == nil {
		return "(no id)"
	}
	return fmt.Sprintf("%q", *r.ID)
}

// isTerminalCommitmentState reports lifecycle states that no longer count as
// coverage. It works on the converted state so a Succeeded reservation with a
// past expiry is terminal too.
func isTerminalCommitmentState(s common.CommitmentState) bool {
	switch s {
	case common.CommitmentStateCanceled, common.CommitmentStateExpired,
		common.CommitmentStateFailed, common.CommitmentStateRetired:
		return true
	}
	return false
}

// validateLiveReservationFields rejects a live reservation missing a field
// the duplicate guard keys or sizes on: a blank SKU or location never matches a
// recommendation, a missing quantity counts as zero capacity, and a missing
// purchase date makes a fresh purchase look old. Erroring (not skipping) is
// deliberate: the row may be the reservation bought an hour ago.
func validateLiveReservationFields(r *armreservations.ReservationResponse, reservationID, subscriptionID string, labels StrictLabels) error {
	var missing []string
	if r.SKU == nil || r.SKU.Name == nil || strings.TrimSpace(*r.SKU.Name) == "" {
		missing = append(missing, "sku.name")
	}
	if r.Location == nil || strings.TrimSpace(*r.Location) == "" {
		missing = append(missing, "location")
	}
	if r.Properties.Quantity == nil || *r.Properties.Quantity <= 0 {
		missing = append(missing, "quantity")
	}
	if r.Properties.PurchaseDate == nil {
		missing = append(missing, "purchaseDate")
	}
	if len(missing) > 0 {
		return fmt.Errorf("%s: incomplete live %s reservation %q in subscription %q: missing or invalid %s",
			labels.Service, labels.Resource, reservationID, subscriptionID, strings.Join(missing, ", "))
	}
	return nil
}

func validateReservationScope(props *armreservations.Properties, reservationID, subscriptionID string, labels StrictLabels, now time.Time) error {
	if props.AppliedScopeType == nil {
		return fmt.Errorf("%s: cannot attribute %s reservation %q to subscription %q: missing applied scope type", labels.Service, labels.Resource, reservationID, subscriptionID)
	}
	if *props.AppliedScopeType != armreservations.AppliedScopeTypeSingle {
		return fmt.Errorf("%s: cannot attribute %s reservation %q to subscription %q: applied scope type %q is not Single", labels.Service, labels.Resource, reservationID, subscriptionID, *props.AppliedScopeType)
	}
	if props.ProvisioningState != nil && *props.ProvisioningState == armreservations.ProvisioningStateSucceeded &&
		props.ExpiryDate != nil && props.ExpiryDate.UTC().Format(time.DateOnly) == now.UTC().Format(time.DateOnly) {
		return fmt.Errorf("%s: cannot determine lifecycle for %s reservation %q in subscription %q: reservations API date-only expiryDate %s falls on the current UTC date",
			labels.Service, labels.Resource, reservationID, subscriptionID, props.ExpiryDate.UTC().Format(time.DateOnly))
	}
	return nil
}
