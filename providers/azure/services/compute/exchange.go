// Package compute provides Azure VM Reserved Instances client.
// This file implements the "list exchangeable reservations" half of Azure
// Convertible RI exchange parity with AWS EC2 (refs #473).
//
// Azure VM reservations are exchangeable when ALL of the following hold:
//  1. ReservedResourceType == VirtualMachines
//  2. ProvisioningState == Succeeded
//  3. InstanceFlexibility == On  (nil or Off means the reservation is NOT
//     eligible for the cross-SKU/cross-region exchange path)
//
// Listings use armreservations.ReservationClient.NewListAllPager which
// enumerates all reservations across the tenant (not scoped to a single
// subscription), matching what the Azure portal shows on the
// "Reservations" blade.
package compute

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/reservations/armreservations"
)

// ExchangeableReservation represents an Azure VM reservation that is
// eligible for exchange.
type ExchangeableReservation struct {
	// ReservationOrderID is the GUID of the parent reservation order,
	// parsed from the ARM resource ID. Required by CalculateExchange.
	ReservationOrderID string `json:"reservation_order_id"`

	// ReservationID is the full ARM resource ID of the reservation item,
	// e.g. /providers/Microsoft.Capacity/reservationOrders/{orderID}/reservations/{resID}.
	// Used as the source identifier in CalculateExchange.ReservationsToExchange.
	ReservationID string `json:"reservation_id"`

	// BillingScopeID is the ARM scope that paid for this reservation, e.g.
	// "/subscriptions/{subscriptionID}". Azure documents the underlying
	// field as "Subscription that will be charged for purchasing
	// Reservation", so it identifies the owning subscription even for a
	// reservation whose AppliedScopeType is Shared (Shared controls which
	// subscriptions receive the DISCOUNT; exactly one scope is CHARGED).
	//
	// This is the only ownership signal available on a tenant-wide listing,
	// and an exchange refunds each source reservation to its own billing
	// scope. Callers authorizing an exchange MUST require this to match the
	// subscription they authorized, or a caller scoped to one subscription
	// can hand back another's commitments (issue #1527).
	//
	// Empty when Azure did not report one. Callers must treat that as
	// "ownership unknown" and refuse, never as "no restriction".
	BillingScopeID string `json:"billing_scope_id,omitempty"`

	// AppliedScopeType is who receives this reservation's discount. An
	// exchange purchases its replacement with the same scope. Empty when
	// Azure did not report one, which CalculateExchange refuses.
	AppliedScopeType armreservations.AppliedScopeType `json:"applied_scope_type,omitempty"`

	// AppliedScopes are the ARM scopes (e.g. "/subscriptions/{id}") a Single
	// scoped reservation applies to. Empty for Shared.
	AppliedScopes []string `json:"applied_scopes,omitempty"`

	// SKU is the VM size (e.g. "Standard_D2s_v3").
	SKU string `json:"sku"`

	// Quantity is the number of VM instances covered.
	Quantity int32 `json:"quantity"`

	// Region is the ARM region name (e.g. "eastus"). May be empty for
	// reservations with AppliedScopeType == Shared.
	Region string `json:"region,omitempty"`

	// Term is the reservation term in ISO 8601 duration format, stringified
	// from armreservations.PossibleReservationTermValues() ("P1Y", "P3Y" or
	// "P5Y"). Consumers must not treat a term outside the one/three-year
	// pair as unsupported.
	Term string `json:"term,omitempty"`

	// ExpiryDate is when the reservation expires. Zero if not set by Azure.
	ExpiryDate time.Time `json:"expiry_date,omitempty"`

	// InstanceFlexibility reports the instance size flexibility setting.
	// Always "On" for reservations returned by this function.
	InstanceFlexibility string `json:"instance_flexibility"`

	// DisplayName is the human-readable reservation name set at purchase time.
	DisplayName string `json:"display_name,omitempty"`
}

// ExchangeableReservationPager defines the paging contract for listing
// reservations. Satisfied by the pager returned from
// armreservations.ReservationClient.NewListAllPager; a stub can be
// injected for tests via SetExchangeablePager.
//
// Each page carries a ListResult whose Value slice holds
// *armreservations.ReservationResponse items.
type ExchangeableReservationPager interface {
	More() bool
	NextPage(ctx context.Context) (armreservations.ReservationClientListAllResponse, error)
}

// SetExchangeablePager injects a mock pager for unit tests. Tests call
// this instead of providing real Azure credentials.
func (c *Client) SetExchangeablePager(p ExchangeableReservationPager) {
	c.exchangeablePager = p
}

// ListExchangeableReservations returns all active VM reservations in the
// tenant that are eligible for exchange.
//
// Eligibility requires ProvisioningState == Succeeded AND
// InstanceFlexibility == On. Reservations with nil InstanceFlexibility
// or InstanceFlexibility == Off are excluded.
//
// The listing is tenant-wide (not scoped to c.subscriptionID) because
// the Azure Capacity exchange API operates on reservation order IDs
// which span subscriptions.
//
// Returns an empty non-nil slice when no eligible reservations are found.
func (c *Client) ListExchangeableReservations(ctx context.Context) ([]ExchangeableReservation, error) {
	pager, err := c.createExchangeablePager()
	if err != nil {
		return nil, fmt.Errorf("compute: list exchangeable reservations: create pager: %w", err)
	}
	return c.collectExchangeableReservations(ctx, pager)
}

// createExchangeablePager returns an injected mock pager when one has
// been set via SetExchangeablePager, or constructs a real
// armreservations.ReservationClient pager otherwise.
func (c *Client) createExchangeablePager() (ExchangeableReservationPager, error) {
	if c.exchangeablePager != nil {
		return c.exchangeablePager, nil
	}
	client, err := armreservations.NewReservationClient(c.cred, nil)
	if err != nil {
		return nil, fmt.Errorf("create armreservations client: %w", err)
	}
	return client.NewListAllPager(nil), nil
}

// collectExchangeableReservations iterates the pager and applies the
// eligibility filter. Any pagination error is returned immediately
// (partial results are unsafe -- a missing reservation could lead to a
// duplicate exchange attempt upstream).
func (c *Client) collectExchangeableReservations(ctx context.Context, pager ExchangeableReservationPager) ([]ExchangeableReservation, error) {
	result := make([]ExchangeableReservation, 0)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("compute: list exchangeable reservations: page: %w", err)
		}
		for _, item := range page.Value {
			r := convertToExchangeableReservation(item)
			if r != nil {
				result = append(result, *r)
			}
		}
	}
	return result, nil
}

// isExchangeEligible reports whether the reservation passes the Azure
// exchange eligibility criteria:
//
//   - ReservedResourceType == VirtualMachines
//   - ProvisioningState == Succeeded
//   - InstanceFlexibility == On  (nil is treated as Off)
func isExchangeEligible(item *armreservations.ReservationResponse) bool {
	if item == nil || item.Properties == nil {
		return false
	}
	props := item.Properties
	if props.ReservedResourceType == nil || *props.ReservedResourceType != armreservations.ReservedResourceTypeVirtualMachines {
		return false
	}
	if props.ProvisioningState == nil || *props.ProvisioningState != armreservations.ProvisioningStateSucceeded {
		return false
	}
	return props.InstanceFlexibility != nil && *props.InstanceFlexibility == armreservations.InstanceFlexibilityOn
}

// reservationFields holds the optional pointer fields read off a
// ReservationResponse and its Properties, each defaulted to its zero value
// when the source pointer is nil. Grouped into a struct (rather than 7
// positional returns) per gocritic's tooManyResultsChecker.
type reservationFields struct {
	id          string
	sku         string
	region      string
	term        string
	displayName string
	quantity    int32
	expiryDate  time.Time
}

// extractReservationFields reads the optional pointer fields from item and its
// Properties, returning safe zero values for any nil pointers.
func extractReservationFields(item *armreservations.ReservationResponse) reservationFields {
	props := item.Properties // guaranteed non-nil by isExchangeEligible
	var f reservationFields
	if item.ID != nil {
		f.id = *item.ID
	}
	if item.SKU != nil && item.SKU.Name != nil {
		f.sku = *item.SKU.Name
	}
	if item.Location != nil {
		f.region = *item.Location
	}
	if props.Quantity != nil {
		f.quantity = *props.Quantity
	}
	if props.Term != nil {
		f.term = string(*props.Term)
	}
	if props.ExpiryDate != nil {
		f.expiryDate = *props.ExpiryDate
	}
	if props.DisplayName != nil {
		f.displayName = *props.DisplayName
	}
	return f
}

// convertToExchangeableReservation converts a single armreservations item
// to the CUDly type, returning nil when the item fails the eligibility
// criteria.
func convertToExchangeableReservation(item *armreservations.ReservationResponse) *ExchangeableReservation {
	if !isExchangeEligible(item) {
		return nil
	}
	f := extractReservationFields(item)
	orderID := parseReservationOrderID(f.id)
	// parseReservationOrderID returns "" for IDs that do not contain the expected
	// "/reservationOrders/" segment (malformed or unexpected format). Reservations
	// with an empty order ID are still returned here so the caller can include them
	// in the inventory view, but callers MUST filter out empty-order-ID entries
	// before initiating an exchange operation -- the Azure exchange API requires a
	// non-empty reservationOrderId.
	var billingScopeID string
	if item.Properties.BillingScopeID != nil {
		billingScopeID = *item.Properties.BillingScopeID
	}
	var appliedScopeType armreservations.AppliedScopeType
	if item.Properties.AppliedScopeType != nil {
		appliedScopeType = *item.Properties.AppliedScopeType
	}
	var appliedScopes []string
	for _, s := range item.Properties.AppliedScopes {
		if s != nil {
			appliedScopes = append(appliedScopes, *s)
		}
	}
	return &ExchangeableReservation{
		ReservationOrderID:  orderID,
		ReservationID:       f.id,
		BillingScopeID:      billingScopeID,
		AppliedScopeType:    appliedScopeType,
		AppliedScopes:       appliedScopes,
		SKU:                 f.sku,
		Quantity:            f.quantity,
		Region:              f.region,
		Term:                f.term,
		ExpiryDate:          f.expiryDate,
		InstanceFlexibility: string(armreservations.InstanceFlexibilityOn),
		DisplayName:         f.displayName,
	}
}

// ErrUnsupportedAppliedScope marks a source whose applied scope cannot be
// carried onto the exchange's purchased reservations. Callers should treat it
// as a client error rather than an Azure failure.
var ErrUnsupportedAppliedScope = errors.New("unsupported applied scope")

// validateAppliedScope refuses a scope the exchange cannot reproduce on the
// purchased reservation instead of letting Azure fall back to Shared.
func (r *ExchangeableReservation) validateAppliedScope() error {
	switch r.AppliedScopeType {
	case armreservations.AppliedScopeTypeShared:
		return nil
	case armreservations.AppliedScopeTypeSingle:
		if len(r.AppliedScopes) != 1 {
			return fmt.Errorf("%w: applied_scopes must hold exactly one scope when applied_scope_type is %s, got %d", ErrUnsupportedAppliedScope, r.AppliedScopeType, len(r.AppliedScopes))
		}
		return nil
	case "":
		return fmt.Errorf("%w: applied_scope_type is required; pass the reservation as returned by ListExchangeableReservations", ErrUnsupportedAppliedScope)
	default:
		return fmt.Errorf("%w: applied_scope_type %q cannot be carried through an exchange", ErrUnsupportedAppliedScope, r.AppliedScopeType)
	}
}

// sameAppliedScope compares the scope type, and the scope set only for
// Single, case-insensitively and order-free since ARM resource IDs are
// case-insensitive. Azure ignores appliedScopes for Shared.
func (r *ExchangeableReservation) sameAppliedScope(o *ExchangeableReservation) bool {
	if r.AppliedScopeType != o.AppliedScopeType {
		return false
	}
	return r.AppliedScopeType != armreservations.AppliedScopeTypeSingle ||
		slices.Equal(normalizedScopes(r.AppliedScopes), normalizedScopes(o.AppliedScopes))
}

func normalizedScopes(scopes []string) []string {
	out := make([]string, len(scopes))
	for i, s := range scopes {
		out[i] = strings.ToLower(s)
	}
	slices.Sort(out)
	return out
}

// parseReservationOrderID extracts the reservation order GUID from a
// full ARM resource ID of the form:
//
//	/providers/Microsoft.Capacity/reservationOrders/{orderID}/reservations/{resID}
//
// Returns an empty string when the ID is blank or does not match the
// expected format, rather than returning an error (graceful degradation --
// the reservation is still returned with an empty order ID and the caller
// can skip it for exchange operations that require the order ID).
func parseReservationOrderID(resourceID string) string {
	if resourceID == "" {
		return ""
	}
	// Normalise to lower-case for case-insensitive segment matching.
	lower := strings.ToLower(resourceID)
	const marker = "/reservationorders/"
	idx := strings.Index(lower, marker)
	if idx < 0 {
		return ""
	}
	rest := resourceID[idx+len(marker):]
	// The next path segment is the order GUID; subsequent segments follow
	// a "/" separator.
	end := strings.IndexByte(rest, '/')
	if end < 0 {
		// Trailing segment -- the whole rest is the order ID.
		return rest
	}
	return rest[:end]
}
