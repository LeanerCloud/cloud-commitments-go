package reservations

import (
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/reservations/armreservations"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
)

var testLabels = StrictLabels{Service: "svc", Resource: "X"}

func strictFixture(rt string) *armreservations.ReservationResponse {
	now := time.Now()
	single := armreservations.AppliedScopeTypeSingle
	succeeded := armreservations.ProvisioningStateSucceeded
	reserved := armreservations.ReservedResourceType(rt)
	expiry := now.AddDate(1, 0, 0)
	return &armreservations.ReservationResponse{
		ID:       strPtr("res-1"),
		Location: strPtr("eastus"),
		SKU:      &armreservations.SKUName{Name: strPtr("Sku1")},
		Properties: &armreservations.Properties{
			ReservedResourceType: &reserved,
			AppliedScopeType:     &single,
			AppliedScopes:        []*string{strPtr("/subscriptions/sub-a")},
			ProvisioningState:    &succeeded,
			Quantity:             int32Ptr(2),
			PurchaseDate:         &now,
			ExpiryDate:           &expiry,
		},
	}
}

// The reservedResourceType match is case-insensitive: the API enum is
// documented in PascalCase but the match must not hinge on casing.
func TestCommitmentFromReservation_TypeMatchIgnoresCase(t *testing.T) {
	r := strictFixture("virtualmachines")
	c := CommitmentFromReservation(r, "sub-a", common.ServiceCompute, armreservations.ReservedResourceTypeVirtualMachines, time.Now())
	require.NotNil(t, c)
	assert.Equal(t, "Sku1", c.ResourceType)
	assert.Nil(t, CommitmentFromReservation(strictFixture("SqlDatabases"), "sub-a", common.ServiceCompute, armreservations.ReservedResourceTypeVirtualMachines, time.Now()))
}

func TestStrictCommitment_ErrorTextUsesLabels(t *testing.T) {
	now := time.Now()
	_, err := StrictCommitment(nil, "sub-a", common.ServiceCompute, armreservations.ReservedResourceTypeVirtualMachines, testLabels, now)
	require.EqualError(t, err, `svc: cannot classify reservation (no id) in subscription "sub-a": missing properties or reservedResourceType`)

	r := strictFixture("VirtualMachines")
	r.Properties.Quantity = nil
	_, err = StrictCommitment(r, "sub-a", common.ServiceCompute, armreservations.ReservedResourceTypeVirtualMachines, testLabels, now)
	require.EqualError(t, err, `svc: incomplete live X reservation "res-1" in subscription "sub-a": missing or invalid quantity`)

	shared := armreservations.AppliedScopeTypeShared
	r = strictFixture("VirtualMachines")
	r.Properties.AppliedScopeType = &shared
	_, err = StrictCommitment(r, "sub-a", common.ServiceCompute, armreservations.ReservedResourceTypeVirtualMachines, testLabels, now)
	require.EqualError(t, err, `svc: cannot attribute X reservation "res-1" to subscription "sub-a": applied scope type "Shared" is not Single`)
}
