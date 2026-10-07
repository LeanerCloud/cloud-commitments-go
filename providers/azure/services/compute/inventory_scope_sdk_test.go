package compute_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/arm"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/reservations/armreservations"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/recfilter"
	"github.com/LeanerCloud/cloud-commitments-go/providers/azure/services/compute"
)

type inventorySDKCredential struct{}

func (inventorySDKCredential) GetToken(context.Context, policy.TokenRequestOptions) (azcore.AccessToken, error) {
	return azcore.AccessToken{Token: "test", ExpiresOn: time.Now().Add(time.Hour)}, nil
}

type inventorySDKTransport struct {
	child             string
	reservationStatus int
}

func (t inventorySDKTransport) Do(req *http.Request) (*http.Response, error) {
	body := `{"properties":{"reservationOrderIds":{"value":["/providers/Microsoft.Capacity/reservationOrders/order-one"]}}}`
	status := http.StatusOK
	if strings.HasSuffix(req.URL.Path, "/reservations") {
		body = `{"value":[` + t.child + `]}`
		if t.reservationStatus != 0 {
			status = t.reservationStatus
		}
	}
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    req,
	}, nil
}

func sdkInventoryClient(t *testing.T, subscriptionID string, transport inventorySDKTransport) *compute.Client {
	t.Helper()
	opts := &arm.ClientOptions{ClientOptions: azcore.ClientOptions{
		Transport: transport,
		Retry:     policy.RetryOptions{MaxRetries: -1},
	}}
	applied, err := armreservations.NewAzureReservationAPIClient(inventorySDKCredential{}, opts)
	require.NoError(t, err)
	orders, err := armreservations.NewReservationClient(inventorySDKCredential{}, opts)
	require.NoError(t, err)
	client := compute.NewClient(inventorySDKCredential{}, subscriptionID, "eastus")
	client.SetInventoryFactories(&compute.InventoryFactories{
		NewAppliedLister: func() (compute.AppliedReservationsLister, error) { return applied, nil },
		NewOrderPager: func(orderID string) compute.OrderReservationsPager {
			return orders.NewListPager(orderID, nil)
		},
	})
	return client
}

type sdkChild struct {
	id, resourceType, scopeType, scopes, region, state string
	quantity                                           int
}

func sdkReservationChild(child sdkChild, now time.Time) string {
	return fmt.Sprintf(`{"id":%q,"location":%q,"sku":{"name":"Standard_D2s_v3"},"properties":{"reservedResourceType":%q,"provisioningState":%q,"appliedScopeType":%s,"appliedScopes":%s,"quantity":%d,"purchaseDate":%q,"expiryDate":%q}}`,
		"/providers/Microsoft.Capacity/reservationOrders/order-one/reservations/"+child.id,
		child.region, child.resourceType, child.state, child.scopeType, child.scopes, child.quantity,
		now.UTC().Format("2006-01-02"), now.UTC().AddDate(1, 0, 0).Format("2006-01-02"))
}

func TestGetExistingCommitmentsSDKSharedVMFailsClosed(t *testing.T) {
	child := sdkReservationChild(sdkChild{
		id: "child-one", resourceType: "VirtualMachines", scopeType: `"Shared"`, scopes: `null`,
		region: "eastus", state: "Succeeded", quantity: 1,
	}, time.Now())
	for _, subscriptionID := range []string{"sub-a", "sub-b"} {
		t.Run(subscriptionID, func(t *testing.T) {
			client := sdkInventoryClient(t, subscriptionID, inventorySDKTransport{child: child})
			inventory, err := client.GetExistingCommitments(context.Background())
			require.ErrorContains(t, err, "cannot attribute")
			require.Nil(t, inventory)

			recommendation := common.Recommendation{
				Provider: common.ProviderAzure, Service: common.ServiceCompute,
				Account: subscriptionID, Region: "eastus", ResourceType: "Standard_D2s_v3", Count: 1,
			}
			passed, filtered, err := recfilter.NewDuplicateChecker(24).AdjustRecommendationsForExisting(
				context.Background(), []common.Recommendation{recommendation}, client)
			require.ErrorContains(t, err, "cannot attribute")
			require.Len(t, passed, 1)
			require.Empty(t, filtered)
		})
	}
}

func TestGetExistingCommitmentsSDKScopeBoundary(t *testing.T) {
	now := time.Now().UTC()
	cases := []struct {
		name         string
		resourceType string
		scopeType    string
		scopes       string
		wantError    bool
		wantCount    int
	}{
		{name: "shared SQL is ignored", resourceType: "SQLDatabases", scopeType: `"Shared"`, scopes: `null`},
		{name: "missing VM scope fails", resourceType: "VirtualMachines", scopeType: `null`, scopes: `null`, wantError: true},
		{name: "unknown VM scope fails", resourceType: "VirtualMachines", scopeType: `"Mystery"`, scopes: `null`, wantError: true},
		{name: "single VM remains attributed", resourceType: "VirtualMachines", scopeType: `"Single"`, scopes: `["/subscriptions/sub-a"]`, wantCount: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := sdkInventoryClient(t, "sub-a", inventorySDKTransport{
				child: sdkReservationChild(sdkChild{
					id: "child-one", resourceType: tc.resourceType, scopeType: tc.scopeType, scopes: tc.scopes,
					region: "westus2", state: "Succeeded", quantity: 2,
				}, now),
			})
			inventory, err := client.GetExistingCommitments(context.Background())
			if tc.wantError {
				require.ErrorContains(t, err, "cannot attribute")
				require.Nil(t, inventory)
				return
			}
			require.NoError(t, err)
			require.Len(t, inventory, tc.wantCount)
			if tc.wantCount == 1 {
				row := inventory[0]
				require.Equal(t, "sub-a", row.Account)
				require.Equal(t, "westus2", row.Region)
				require.Equal(t, "Standard_D2s_v3", row.ResourceType)
				require.Equal(t, common.CommitmentStateActive, row.State)
				require.Equal(t, 2, row.Count)
				require.Equal(t, now.Format("2006-01-02"), row.StartDate.UTC().Format("2006-01-02"))
				require.Equal(t, now.AddDate(1, 0, 0).Format("2006-01-02"), row.EndDate.UTC().Format("2006-01-02"))
			}
		})
	}
}

func TestGetExistingCommitmentsSDKSplitSingleAndTerminal(t *testing.T) {
	now := time.Now()
	first := sdkReservationChild(sdkChild{
		id: "child-a", resourceType: "VirtualMachines", scopeType: `"Single"`, scopes: `["/subscriptions/sub-a"]`,
		region: "westus2", state: "Succeeded", quantity: 2,
	}, now)
	other := sdkReservationChild(sdkChild{
		id: "child-b", resourceType: "VirtualMachines", scopeType: `"Single"`, scopes: `["/subscriptions/sub-b"]`,
		region: "eastus", state: "Succeeded", quantity: 3,
	}, now)
	client := sdkInventoryClient(t, "sub-a", inventorySDKTransport{child: first + "," + other})
	inventory, err := client.GetExistingCommitments(context.Background())
	require.NoError(t, err)
	require.Len(t, inventory, 1)
	require.Equal(t, "/providers/Microsoft.Capacity/reservationOrders/order-one/reservations/child-a", inventory[0].CommitmentID)
	require.Equal(t, 2, inventory[0].Count)
	rec := common.Recommendation{Provider: common.ProviderAzure, Service: common.ServiceCompute,
		Account: "sub-a", Region: "westus2", ResourceType: "Standard_D2s_v3", Count: 2}
	passed, filtered, err := recfilter.NewDuplicateChecker(24).AdjustRecommendationsForExisting(
		context.Background(), []common.Recommendation{rec}, client)
	require.NoError(t, err)
	require.Empty(t, passed)
	require.Len(t, filtered, 1)

	terminal := sdkReservationChild(sdkChild{
		id: "child-c", resourceType: "VirtualMachines", scopeType: `"Single"`, scopes: `["/subscriptions/sub-a"]`,
		region: "westus2", state: string(armreservations.ProvisioningStateCancelled), quantity: 2,
	}, now)
	client = sdkInventoryClient(t, "sub-a", inventorySDKTransport{child: terminal})
	passed, filtered, err = recfilter.NewDuplicateChecker(24).AdjustRecommendationsForExisting(
		context.Background(), []common.Recommendation{rec}, client)
	require.NoError(t, err)
	require.Len(t, passed, 1)
	require.Empty(t, filtered)
}

func TestGetExistingCommitmentsSDKPageFailureFailsClosed(t *testing.T) {
	client := sdkInventoryClient(t, "sub-a", inventorySDKTransport{reservationStatus: http.StatusInternalServerError})
	inventory, err := client.GetExistingCommitments(context.Background())
	require.ErrorContains(t, err, "list reservations")
	require.Nil(t, inventory)
	rec := common.Recommendation{Provider: common.ProviderAzure, Service: common.ServiceCompute,
		Account: "sub-a", Region: "eastus", ResourceType: "Standard_D2s_v3", Count: 1}
	passed, filtered, err := recfilter.NewDuplicateChecker(24).AdjustRecommendationsForExisting(
		context.Background(), []common.Recommendation{rec}, client)
	require.ErrorContains(t, err, "list reservations")
	require.Len(t, passed, 1)
	require.Empty(t, filtered)
}
