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
	t.Parallel()
	r := strictFixture("virtualmachines")
	c := CommitmentFromReservation(r, "sub-a", common.ServiceCompute, armreservations.ReservedResourceTypeVirtualMachines, time.Now())
	require.NotNil(t, c)
	assert.Equal(t, "Sku1", c.ResourceType)
	assert.Nil(t, CommitmentFromReservation(strictFixture("SqlDatabases"), "sub-a", common.ServiceCompute, armreservations.ReservedResourceTypeVirtualMachines, time.Now()))
}

func TestStrictCommitment_ErrorTextUsesLabels(t *testing.T) {
	t.Parallel()
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
	require.EqualError(t, err, `svc: cannot attribute X reservation "res-1" to subscription "sub-a": Shared scope without a subscription billingScopeId`)

	r = strictFixture("VirtualMachines")
	unknown := armreservations.AppliedScopeType("ManagementGroup")
	r.Properties.AppliedScopeType = &unknown
	_, err = StrictCommitment(r, "sub-a", common.ServiceCompute, armreservations.ReservedResourceTypeVirtualMachines, testLabels, now)
	require.EqualError(t, err, `svc: cannot attribute X reservation "res-1" to subscription "sub-a": unsupported applied scope type "ManagementGroup"`)
}

// Issue #293: a Shared reservation is owned by the subscription named in its
// billingScopeId, the subscription CUDly purchases under.
func TestStrictCommitment_SharedScopeAttributedToBillingSubscription(t *testing.T) {
	t.Parallel()
	now := time.Now()
	shared := armreservations.AppliedScopeTypeShared
	r := strictFixture("VirtualMachines")
	r.Properties.AppliedScopeType = &shared
	r.Properties.AppliedScopes = nil
	r.Properties.BillingScopeID = strPtr("/subscriptions/SUB-A")

	c, err := StrictCommitment(r, "sub-a", common.ServiceCompute, armreservations.ReservedResourceTypeVirtualMachines, testLabels, now)
	require.NoError(t, err)
	require.NotNil(t, c)
	assert.Equal(t, "sub-a", c.Account)
	assert.Equal(t, 2, c.Count)

	c, err = StrictCommitment(r, "sub-b", common.ServiceCompute, armreservations.ReservedResourceTypeVirtualMachines, testLabels, now)
	require.NoError(t, err)
	assert.Nil(t, c, "a Shared reservation bought by another subscription is not this subscription's commitment")

	r.Properties.BillingScopeID = strPtr("/subscriptions/")
	_, err = StrictCommitment(r, "sub-a", common.ServiceCompute, armreservations.ReservedResourceTypeVirtualMachines, testLabels, now)
	require.ErrorContains(t, err, "Shared scope without a subscription billingScopeId")
}
