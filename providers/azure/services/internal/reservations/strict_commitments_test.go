package reservations_test

import (
	"context"
	"errors"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/reservations/armreservations"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/providers/azure/services/internal/reservations"
	"github.com/LeanerCloud/cloud-commitments-go/providers/azure/services/internal/reservations/reservationstest"
)

const sub = "sub-a"

var labels = reservations.StrictLabels{Service: "svc", Resource: "Redis"}

func factories(listErr error, rows ...*armreservations.ReservationResponse) reservations.InventoryFactories {
	return reservations.InventoryFactories{
		NewAppliedLister: func() (reservations.AppliedReservationsLister, error) {
			return reservationstest.AppliedLister{Err: listErr}, nil
		},
		NewOrderPager: func(string) reservations.OrderReservationsPager {
			return &reservationstest.OrderPager{Rows: rows}
		},
	}
}

func list(f reservations.InventoryFactories) ([]common.Commitment, error) {
	return reservations.StrictCommitments(context.Background(), sub, f, 5, common.ServiceMemoryDB,
		armreservations.ReservedResourceTypeRedisCache, labels)
}

func TestStrictCommitments_ListErrorKeepsPrefixAndCause(t *testing.T) {
	t.Parallel()
	cause := errors.New("boom")
	_, err := list(factories(cause))
	require.ErrorIs(t, err, cause)
	assert.Contains(t, err.Error(), "svc: list applied reservations: ")
}

func TestStrictCommitments_IgnoresOtherTypes(t *testing.T) {
	t.Parallel()
	got, err := list(factories(nil,
		reservationstest.LiveRow(armreservations.ReservedResourceTypeSQLDatabases, sub, "sql", "S0", "eastus", 1),
		reservationstest.LiveRow(armreservations.ReservedResourceTypeRedisCache, sub, "redis", "P1", "eastus", 1)))
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, reservationstest.OrderPath+"/reservations/redis", got[0].CommitmentID)
}

func TestStrictCommitments_IncompleteLiveRowFailsClosed(t *testing.T) {
	t.Parallel()
	row := reservationstest.LiveRow(armreservations.ReservedResourceTypeRedisCache, sub, "redis", "P1", "eastus", 1)
	row.Properties.Quantity = nil
	_, err := list(factories(nil, row))
	require.ErrorContains(t, err, "quantity")
}

func TestStrictCommitments_ReturnsConvertedFields(t *testing.T) {
	t.Parallel()
	got, err := list(factories(nil,
		reservationstest.LiveRow(armreservations.ReservedResourceTypeRedisCache, sub, "a", "P1", "westus2", 3),
		reservationstest.LiveRow(armreservations.ReservedResourceTypeRedisCache, sub, "b", "P2", "eastus", 1)))
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, "P1", got[0].ResourceType)
	assert.Equal(t, "westus2", got[0].Region)
	assert.Equal(t, 3, got[0].Count)
	assert.Equal(t, common.ServiceMemoryDB, got[0].Service)
	assert.Equal(t, common.CommitmentStateActive, got[0].State)
	assert.Equal(t, "P2", got[1].ResourceType)
}
