package compute_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/recfilter"
)

// Issue #190: an incomplete live VM reservation must fail closed through the
// public GetExistingCommitments and the real DuplicateChecker. Fixtures are
// SDK HTTP responses, not live Azure observations.

func completenessChild(state string, quantity int, region string) string {
	now := time.Now().UTC()
	return sdkReservationChild(sdkChild{
		id: "inc", resourceType: "VirtualMachines", scopeType: `"Single"`, scopes: `["/subscriptions/sub-a"]`,
		region: region, state: state, quantity: quantity,
	}, now.AddDate(0, 0, -30), now.AddDate(1, 0, 0))
}

func mustReplace(t *testing.T, s, old, repl string) string {
	t.Helper()
	require.Contains(t, s, old)
	return strings.Replace(s, old, repl, 1)
}

func requireFailsClosed(t *testing.T, child, wantErr string) {
	t.Helper()
	client := sdkInventoryClient(t, "sub-a", inventorySDKTransport{child: child})
	inventory, err := client.GetExistingCommitments(context.Background())
	require.ErrorContains(t, err, wantErr)
	require.Nil(t, inventory)
	rec := common.Recommendation{Provider: common.ProviderAzure, Service: common.ServiceCompute,
		Account: "sub-a", Region: "eastus", ResourceType: "Standard_D2s_v3", Count: 1}
	passed, filtered, err := recfilter.NewDuplicateChecker(24).AdjustRecommendationsForExisting(
		context.Background(), []common.Recommendation{rec}, client)
	require.ErrorContains(t, err, wantErr)
	require.Len(t, passed, 1)
	require.Empty(t, filtered)
}

func TestGetExistingCommitmentsSDKIncompleteFailsClosed(t *testing.T) {
	live := completenessChild("Succeeded", 1, "eastus")
	cases := []struct {
		name    string
		child   string
		wantErr string
	}{
		{"no properties", `{"id":"/providers/Microsoft.Capacity/reservationOrders/order-one/reservations/inc","location":"eastus"}`, "missing properties or reservedResourceType"},
		{"no reservedResourceType", mustReplace(t, live, `"reservedResourceType":"VirtualMachines",`, ``), "missing properties or reservedResourceType"},
		{"quantity nil", mustReplace(t, live, `"quantity":1,`, ``), "quantity"},
		{"quantity zero", completenessChild("Succeeded", 0, "eastus"), "quantity"},
		{"purchaseDate nil", mustReplace(t, live, `"purchaseDate":"`+time.Now().UTC().AddDate(0, 0, -30).Format(time.DateOnly)+`",`, ``), "purchaseDate"},
		{"sku nil", mustReplace(t, live, `"sku":{"name":"Standard_D2s_v3"},`, ``), "sku.name"},
		{"sku blank", mustReplace(t, live, `"name":"Standard_D2s_v3"`, `"name":" "`), "sku.name"},
		{"location nil", mustReplace(t, live, `"location":"eastus",`, ``), "location"},
		{"location blank", completenessChild("Succeeded", 1, " "), "location"},
		{"payment pending is live", completenessChild("PendingBilling", 0, "eastus"), "quantity"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) { requireFailsClosed(t, tc.child, tc.wantErr) })
	}
}

func TestGetExistingCommitmentsSDKIncompleteControls(t *testing.T) {
	t.Run("cancelled with missing fields is not an error", func(t *testing.T) {
		child := mustReplace(t, completenessChild("Cancelled", 0, ""), `"sku":{"name":"Standard_D2s_v3"},`, ``)
		client := sdkInventoryClient(t, "sub-a", inventorySDKTransport{child: child})
		inventory, err := client.GetExistingCommitments(context.Background())
		require.NoError(t, err)
		require.Len(t, inventory, 1)
		require.Equal(t, common.CommitmentStateCanceled, inventory[0].State)
	})
	t.Run("succeeded with past expiry is terminal", func(t *testing.T) {
		now := time.Now().UTC()
		child := sdkReservationChild(sdkChild{
			id: "old", resourceType: "VirtualMachines", scopeType: `"Single"`, scopes: `["/subscriptions/sub-a"]`,
			region: "eastus", state: "Succeeded", quantity: 0,
		}, now.AddDate(-1, 0, 0), now.AddDate(0, 0, -2))
		client := sdkInventoryClient(t, "sub-a", inventorySDKTransport{child: child})
		_, err := client.GetExistingCommitments(context.Background())
		require.NoError(t, err)
	})
	t.Run("non-VM with nil quantity is ignored", func(t *testing.T) {
		now := time.Now().UTC()
		child := sdkReservationChild(sdkChild{
			id: "sql", resourceType: "SQLDatabases", scopeType: `"Single"`, scopes: `["/subscriptions/sub-a"]`,
			region: "eastus", state: "Succeeded", quantity: 1,
		}, now.AddDate(0, 0, -30), now.AddDate(1, 0, 0))
		child = mustReplace(t, child, `"quantity":1,`, ``)
		client := sdkInventoryClient(t, "sub-a", inventorySDKTransport{child: child})
		inventory, err := client.GetExistingCommitments(context.Background())
		require.NoError(t, err)
		require.Empty(t, inventory)
	})
	t.Run("complete reservation keeps its real fields", func(t *testing.T) {
		client := sdkInventoryClient(t, "sub-a", inventorySDKTransport{child: completenessChild("Succeeded", 3, "westeurope")})
		inventory, err := client.GetExistingCommitments(context.Background())
		require.NoError(t, err)
		require.Len(t, inventory, 1)
		require.Equal(t, "westeurope", inventory[0].Region)
		require.Equal(t, "Standard_D2s_v3", inventory[0].ResourceType)
		require.Equal(t, 3, inventory[0].Count)
		require.Equal(t, common.CommitmentStateActive, inventory[0].State)
		require.False(t, inventory[0].StartDate.IsZero())
	})
}
