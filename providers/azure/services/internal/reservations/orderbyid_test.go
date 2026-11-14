package reservations

import (
	"context"
	"net/http"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/reservations/armreservations"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func orderByIDClient(t *testing.T, status int, body string, gets *int) clientFunc {
	return func(r *http.Request) (*http.Response, error) {
		require.Equal(t, http.MethodGet, r.Method)
		require.Equal(t, ReservationOrderURL("order-1"), r.URL.String())
		*gets++
		return fakeResp(status, body), nil
	}
}

func TestAdoptCompletedOrder_ByState(t *testing.T) {
	t.Parallel()
	for _, state := range []string{"Succeeded", "Creating", "Merged", "Split", ""} {
		gets := 0
		id, err := adoptCompletedOrder(context.Background(), orderByIDClient(t, 200, `{"properties":{"provisioningState":"`+state+`"}}`, &gets), "tok", "order-1")
		require.NoError(t, err, state)
		assert.Equal(t, "order-1", id)
		assert.Equal(t, 1, gets, "a GET by ID, never the list")
	}
	for _, state := range []string{"Failed", string(armreservations.ProvisioningStateCancelled), "Expired"} {
		gets := 0
		id, err := adoptCompletedOrder(context.Background(), orderByIDClient(t, 200, `{"properties":{"provisioningState":"`+state+`"}}`, &gets), "tok", "order-1")
		require.Error(t, err, state)
		assert.Empty(t, id)
		assert.ErrorContains(t, err, "order-1")
		assert.ErrorContains(t, err, state)
		assert.NotErrorIs(t, err, ErrReservationOrderBlocked)
	}
	for _, state := range []string{"BillingFailed", "SomeFutureState"} {
		gets := 0
		id, err := adoptCompletedOrder(context.Background(), orderByIDClient(t, 200, `{"properties":{"provisioningState":"`+state+`"}}`, &gets), "tok", "order-1")
		assert.ErrorIs(t, err, ErrReservationOrderBlocked, state)
		assert.Empty(t, id)
	}
}

func TestAdoptCompletedOrder_ReadFailuresAreNotAdopted(t *testing.T) {
	t.Parallel()
	gets := 0
	id, err := adoptCompletedOrder(context.Background(), orderByIDClient(t, 404, `{"error":"gone"}`, &gets), "tok", "order-1")
	require.ErrorContains(t, err, "status 404")
	assert.Empty(t, id)
	_, err = adoptCompletedOrder(context.Background(), orderByIDClient(t, 200, `not json`, &gets), "tok", "order-1")
	require.Error(t, err)
	_, err = adoptCompletedOrder(context.Background(), orderByIDClient(t, 200, `{}`, &gets), "tok", "")
	require.Error(t, err, "an empty recorded order ID cannot be adopted")
}
